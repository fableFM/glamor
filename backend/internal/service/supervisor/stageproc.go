package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/harness"
	runsmachine "github.com/fableFM/glamor/internal/service/runsmachine"
)

// stageResult — итог завершения процесса этапа (канал классификации).
type stageResult struct {
	stageID        int64
	runID          string
	exitCode       int
	usage          *harness.Usage // последний usage из стрима (nullable, D-51)
	stall          bool           // watchdog: нет событий N сек
	timeout        bool           // общий лимит времени этапа
	retriableBurst bool           // K подряд retriable-ошибок стрима
	reason         string
}

// stageProc — живой процесс этапа с watchdog'ом.
type stageProc struct {
	stageID int64
	cmd     *exec.Cmd

	sup        *Supervisor
	logFile    *os.File
	batcher    batchWriter
	adapter    harness.Harness
	stage      *dtorep.Stage
	workDir    string
	events     []harness.Event // для ExtractSessionID (ограниченный буфер)
	lastEvent  atomic.Int64    // unixnano последнего события стрима
	retriable  atomic.Int64    // подряд идущие retriable-ошибки
	tokensIn   atomic.Int64    // накопление usage-СОБЫТИЙ (T-24: дельты)
	tokensOut  atomic.Int64
	result     chan stageResult
	killOnce   atomic.Bool
	completion chan stageResult
	scannersWg sync.WaitGroup // scanStream-горутины (join перед чтением events)
}

type batchWriter interface {
	Append(kind string, payload []byte)
	Flush(ctx context.Context) error
	Close(ctx context.Context) error
}

// spawnStage — подготовка и запуск процесса этапа (D-13: сырой выхлоп
// дублируется в runs/<id>/stage-<key>-<iter>.log — receipts).
func (s *Supervisor) spawnStage(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage) (*stageProc, error) {
	adapter, err := s.registry.Get(stage.Harness)
	if err != nil {
		return nil, err
	}

	project, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil {
		return nil, err
	}

	stageSpec, err := s.stageSpecFor(ctx, stage)
	if err != nil {
		return nil, err
	}

	// pre-stage хук (T-10: branch_mismatch)
	if s.preStage != nil {
		if err := s.preStage(ctx, run, project.Path); err != nil {
			_ = s.appendRunEvent(ctx, run.ID, &stage.ID, "run.branch_mismatch",
				fmt.Sprintf(`{"error":%q}`, err.Error()))
			return nil, fmt.Errorf("pre-stage hook: %w", err)
		}
	}

	// session id предыдущей попытки (resume, D-16)
	lc := LaunchContext{
		Run: run, Stage: stage, StageSpec: stageSpec, IsResume: stage.ResumeCount > 0,
		RunDir: s.runDir(run.ID), ProjectPath: project.Path,
	}
	if fullSpec, err := s.specFor(ctx, run.ID); err == nil && fullSpec.Loop != nil {
		lc.MaxIterations = fullSpec.Loop.MaxIters
	}
	// queue notes + steer-сообщения — в промпт ближайшей попытки (D-22)
	messages, err := s.consumeNotes(ctx, run.ID)
	if err != nil {
		logError(ctx, "failed to consume notes", err)
	}
	lc.SteerMessages = messages
	if stage.Iteration > 1 {
		prev, err := s.stages.GetStageByIteration(ctx, run.ID, stage.StageKey, stage.Iteration-1)
		if err == nil && prev.SessionID != nil {
			lc.PrevSessionID = *prev.SessionID
		}
	}

	// T-30: вход distill-этапа — трейс поведения рана (чинит P0: LessonSignals
	// нигде не заполнялся). Мастер-выключатель lessons:off — трейс не собираем.
	if runsmachine.IsDistillStage(stageSpec) {
		if spec, err := s.specFor(ctx, run.ID); err == nil && spec.LessonsOn() {
			s.attachBehaviorTrace(ctx, run, stage, &lc)
		}
	}

	prompt, err := s.prompt(ctx, lc)
	if err != nil {
		return nil, fmt.Errorf("failed to build prompt: %w", err)
	}

	// T-17: промпт попытки сохраняется артефактом рана (prompt-<stage>-<iter>.md)
	if err := s.savePromptArtifact(ctx, run, stage, lc.RunDir, prompt); err != nil {
		logError(ctx, "failed to save prompt artifact", err)
	}

	launchSpec := harness.LaunchSpec{
		Prompt:    prompt,
		WorkDir:   project.Path,
		Model:     stageSpec.Model,
		Effort:    stageSpec.Effort,
		SessionID: lc.PrevSessionID,
	}
	if stageSpec.Artifact != nil {
		launchSpec.ArtifactContract = harness.ArtifactSpec{
			Path:     stageSpec.Artifact.Path,
			Required: stageSpec.Artifact.Required,
		}
	}

	cmdSpec, err := adapter.BuildCommand(launchSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to build command: %w", err)
	}
	if len(cmdSpec.Argv) == 0 {
		return nil, fmt.Errorf("adapter %s returned empty argv", adapter.Name())
	}

	// лог попытки (receipts, D-13)
	logPath := filepath.Join(s.GetConfig().RunsDir, run.ID,
		fmt.Sprintf("stage-%s-%d.log", stage.StageKey, stage.Iteration))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create run log dir: %w", err)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create stage log: %w", err)
	}

	cmd := exec.Command(cmdSpec.Argv[0], cmdSpec.Argv[1:]...)
	cmd.Dir = project.Path
	cmd.Env = append(os.Environ(), cmdSpec.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // убивать с детьми
	if cmdSpec.Stdin != "" {
		cmd.Stdin = strings.NewReader(cmdSpec.Stdin)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("failed to open stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("failed to open stderr pipe: %w", err)
	}

	now := time.Now().UTC()
	if err := s.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateRunning,
		dtorep.StageTransitionFields{StartedAt: &now}); err != nil {
		_ = logFile.Close()
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("failed to start process: %w", err)
	}

	pid := int64(cmd.Process.Pid)
	if err := s.machine.UpdateRunningStage(ctx, stage.ID, dtorep.StageTransitionFields{PID: &pid}); err != nil {
		logError(ctx, fmt.Sprintf("failed to set pid for stage %d", stage.ID), err)
	}

	proc := &stageProc{
		stageID:    stage.ID,
		cmd:        cmd,
		sup:        s,
		logFile:    logFile,
		batcher:    newStageBatcher(s.journal, run.ID, &stage.ID),
		adapter:    adapter,
		stage:      stage,
		workDir:    project.Path,
		result:     make(chan stageResult, 1),
		completion: make(chan stageResult, 1),
	}
	proc.lastEvent.Store(time.Now().UnixNano())

	proc.scannersWg.Add(2)
	go func() { defer proc.scannersWg.Done(); proc.scanStream(stdout, false) }()
	go func() { defer proc.scannersWg.Done(); proc.scanStream(stderr, true) }()
	go proc.watchdog()

	return proc, nil
}

// scanStream читает поток построчно: сырые строки — в лог (receipts),
// stdout — через адаптер в нормализованные события (батчами, T-04).
func (p *stageProc) scanStream(r io.Reader, isStderr bool) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		_, _ = p.logFile.Write(append(line, '\n'))

		if isStderr {
			continue // stderr — только в лог (kimi туда пишет thinking, qwen — heartbeat)
		}

		p.lastEvent.Store(time.Now().UnixNano())
		for _, ev := range p.adapter.ParseStream(line) {
			p.trackEvent(ev)
		}
	}
}

// trackEvent ведёт учёт события: батч в журнал, session id, usage,
// retriable-счётчик для watchdog'а.
func (p *stageProc) trackEvent(ev harness.Event) {
	// буфер для ExtractSessionID — ограничиваем
	const maxEvents = 1000
	if len(p.events) < maxEvents {
		p.events = append(p.events, ev)
	}

	if ev.Kind == harness.EventSessionInit || ev.SessionID != "" {
		if sid, err := p.adapter.ExtractSessionID([]harness.Event{ev}, p.workDir); err == nil && sid != "" {
			if err := p.sup.machine.UpdateRunningStage(context.Background(), p.stageID,
				dtorep.StageTransitionFields{SessionID: &sid}); err != nil {
				logError(context.Background(), "failed to save session id", err)
			}
		}
	}

	if ev.Err != nil && ev.Err.Retriable {
		p.retriable.Add(1)
	} else if ev.Kind != harness.EventRaw {
		p.retriable.Store(0)
	}

	// D-51: накопление usage-событий (попытка = один процесс; opencode
	// шлёт step_finish на шаг, у остальных — один result на попытку)
	if ev.Usage != nil {
		p.tokensIn.Add(ev.Usage.Input)
		p.tokensOut.Add(ev.Usage.Output)
	}

	p.batcher.Append(harness.JournalKind(ev), []byte(streamPayload(ev)))
}

// watchdog: stall (нет событий N сек), общий таймаут этапа, retriable-шторм.
func (p *stageProc) watchdog() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	started := time.Now()

	for range ticker.C {

		// процесс завершён?
		select {
		case <-p.completion:
			return
		default:
		}

		var res *stageResult
		switch {
		case time.Since(started) > p.sup.cfg.StageTimeout:
			res = &stageResult{timeout: true, reason: fmt.Sprintf("stage timeout %s exceeded", p.sup.cfg.StageTimeout)}
		case time.Since(time.Unix(0, p.lastEvent.Load())) > p.sup.cfg.StallTimeout:
			res = &stageResult{stall: true, reason: fmt.Sprintf("no stream events for %s", p.sup.cfg.StallTimeout)}
		case p.retriable.Load() >= int64(p.sup.cfg.RetriableThreshold):
			res = &stageResult{
				retriableBurst: true,
				reason:         fmt.Sprintf("%d consecutive retriable stream errors", p.retriable.Load()),
			}
		}
		if res != nil {
			p.kill(p.sup.cfg.KillGrace)
			return
		}
	}
}

// kill — SIGTERM группе → grace → SIGKILL (D-14/15).
func (p *stageProc) kill(grace time.Duration) {
	if p.killOnce.CompareAndSwap(false, true) {
		if p.cmd.Process != nil {
			pgid, err := syscall.Getpgid(p.cmd.Process.Pid)
			if err == nil {
				_ = syscall.Kill(-pgid, syscall.SIGTERM)
			} else {
				_ = p.cmd.Process.Signal(syscall.SIGTERM)
			}

			time.AfterFunc(grace, func() {
				if p.cmd.ProcessState == nil {
					if pgid, err := syscall.Getpgid(p.cmd.Process.Pid); err == nil {
						_ = syscall.Kill(-pgid, syscall.SIGKILL)
					} else {
						_ = p.cmd.Process.Kill()
					}
				}
			})
		}
	}
}

// wait ждёт завершения процесса и формирует итог для классификации.
func (p *stageProc) wait() stageResult {
	// Сначала сканеры дочитывают потоки до EOF (процесс завершился —
	// записывающий конец pipe закрыт), потом Wait: os/exec после выхода
	// команды закрывает pipes и может оборвать чтение хвоста потока
	// («incorrect to call Wait before all reads have completed»).
	p.scannersWg.Wait()
	err := p.cmd.Wait()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	// агрегированный usage попытки (T-24): сумма usage-событий стрима;
	// 0/0 и отсутствие событий → nil (метрики «—», не выдумываем)
	var usage *harness.Usage
	if in, out := p.tokensIn.Load(), p.tokensOut.Load(); in > 0 || out > 0 {
		usage = &harness.Usage{Input: in, Output: out}
	}

	_ = p.batcher.Close(context.Background())
	_ = p.logFile.Close()

	res := stageResult{exitCode: exitCode, usage: usage}
	p.completion <- res // сигнал watchdog'у завершиться
	return res
}

// savePromptArtifact пишет промпт попытки в RunDir и регистрирует артефакт
// в БД (виден на вкладке «Промпт»/«Артефакты» UI, T-14).
func (s *Supervisor) savePromptArtifact(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage, runDir, prompt string) error {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("failed to create run dir: %w", err)
	}
	name := fmt.Sprintf("prompt-%s-%d.md", stage.StageKey, stage.Iteration)
	path := filepath.Join(runDir, name)
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		return fmt.Errorf("failed to write prompt file: %w", err)
	}
	if _, err := s.artifacts.CreateArtifact(ctx, dtorep.CreateArtifactRequest{
		RunID:   run.ID,
		StageID: &stage.ID,
		Path:    path,
		Kind:    "prompt",
	}); err != nil {
		return fmt.Errorf("failed to register prompt artifact: %w", err)
	}
	return nil
}

package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/service/runsmachine"
)

// janitorCommandTimeout — дефолтный таймаут одной janitor-команды.
const janitorCommandTimeout = 10 * time.Minute

// janitorLogName — фиксированное имя лога janitor-этапа в runDir
// (плейсхолдер {{artifact.janitor.log}} в промптах следующих этапов, T-22).
const janitorLogName = "janitor.log"

// runJanitor выполняет janitor-этап (T-22): детерминированные команды из
// конфигурации пайплайна (НЕ вывод LLM — безопасность зафиксирована в
// спеке), без harness. Вывод → stream.text события + лог janitor.log;
// exit code != 0 → по политике on_fail (fail_stage | warn).
func (s *Supervisor) runJanitor(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage, spec runsmachine.StageSpec, projectPath string) error {
	now := time.Now().UTC()
	if err := s.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateRunning,
		dtorep.StageTransitionFields{StartedAt: &now}); err != nil {
		return err
	}

	logPath := filepath.Join(s.runDir(run.ID), janitorLogName)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("failed to create run dir: %w", err)
	}
	// лог перезаписывается на каждую попытку — свежий всегда по фикс. имени
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("failed to create janitor log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	batcher := newStageBatcher(s.journal, run.ID, &stage.ID)
	defer func() { _ = batcher.Close(context.Background()) }()

	timeout := janitorCommandTimeout
	if spec.CommandTimeoutSec > 0 {
		timeout = time.Duration(spec.CommandTimeoutSec) * time.Second
	}
	onFail := spec.OnFail
	if onFail == "" {
		onFail = "fail_stage"
	}

	finished := time.Now().UTC()
	var failedCmd string
	var failedExit int

	for _, cmdLine := range spec.Commands {
		header := fmt.Sprintf("$ %s", cmdLine)
		_, _ = fmt.Fprintln(logFile, header)
		batcher.Append("stream.text", []byte(marshalEventPayload(map[string]any{
			"normalized": "janitor.command", "text": header,
		})))

		exitCode, cmdErr := s.runJanitorCommand(ctx, projectPath, cmdLine, timeout, logFile, batcher)
		_, _ = fmt.Fprintf(logFile, "[exit %d]\n\n", exitCode)

		if cmdErr != nil || exitCode != 0 {
			failedCmd = cmdLine
			failedExit = exitCode
			if onFail == "warn" {
				batcher.Append("stream.error", []byte(marshalEventPayload(map[string]any{
					"message": fmt.Sprintf("janitor command failed (warn): %s (exit %d)", cmdLine, exitCode),
				})))
				continue
			}
			break // fail_stage
		}
	}

	// артефакт — лог janitor-этапа (T-22)
	if _, err := s.artifacts.CreateArtifact(ctx, dtorep.CreateArtifactRequest{
		RunID:   run.ID,
		StageID: &stage.ID,
		Path:    logPath,
		Kind:    "janitor_log",
	}); err != nil {
		logError(ctx, "failed to register janitor log", err)
	}

	finished = time.Now().UTC()
	if failedCmd != "" && onFail != "warn" {
		errMsg := fmt.Sprintf("janitor command failed: %q (exit %d)", failedCmd, failedExit)
		return s.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateFailed,
			dtorep.StageTransitionFields{FinishedAt: &finished, ExitCode: ptrInt64(int64(failedExit)), Error: &errMsg})
	}
	return s.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateSucceeded,
		dtorep.StageTransitionFields{FinishedAt: &finished, ExitCode: ptrInt64(0)})
}

// runJanitorCommand выполняет одну команду: вывод построчно в лог и
// стрим-события; process group + таймаут (kill всей группы).
func (s *Supervisor) runJanitorCommand(ctx context.Context, workDir, cmdLine string, timeout time.Duration, logFile *os.File, batcher batchWriter) (int, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "sh", "-c", cmdLine)
	cmd.Dir = workDir
	setProcessGroup(cmd)

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("failed to start janitor command: %w", err)
	}

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = fmt.Fprintln(logFile, line)
			batcher.Append("stream.text", []byte(marshalEventPayload(map[string]any{
				"normalized": "janitor.output", "text": line,
			})))
		}
	}()

	waitErr := cmd.Wait()
	_ = pw.Close()
	<-scanDone

	if cmdCtx.Err() == context.DeadlineExceeded {
		killProcessGroupCmd(cmd)
		return -1, fmt.Errorf("janitor command timed out after %s", timeout)
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, fmt.Errorf("janitor command failed: %w", waitErr)
	}
	return 0, nil
}

func ptrInt64(v int64) *int64 { return &v }

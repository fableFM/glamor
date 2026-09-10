package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/service/lessons"
	runsmachine "github.com/fableFM/glamor/internal/service/runsmachine"
	"github.com/fableFM/glamor/pkg/uuid"
)

// Контур уроков в supervisor'е (T-30, D-81): сбор BehaviorTrace перед
// distill-этапом (чинит P0 — вход distill не был подключён), терминальный
// distill на провальных исходах, версионная деградация vendor-уроков при
// старте рана, relapse-эвристика после reviewer, архивация verdict-<n>.json.

// LessonsHooks — операции над уроками, нужные контуру (интерфейс у
// потребителя, D-80; реализация — internal/service/lessons.Service).
type LessonsHooks interface {
	// CheckVendorVersions — сверка vendor_version vendor-уроков с go.mod
	// проекта (outdated при расхождении, восстановление при совпадении).
	CheckVendorVersions(ctx context.Context, projectPath string) (outdated, restored []dtorep.Lesson, err error)
	// LoadRunInjections — уроки, реально инжектированные в промпты рана
	// (из артефакта run_dir/injected-lessons.jsonl).
	LoadRunInjections(ctx context.Context, runDir string) ([]lessons.InjectedLesson, error)
	// RelapseCheck — рецидив: blocking/major finding совпал с инжектированным
	// уроком (счётчики + хиты для событий журнала).
	RelapseCheck(ctx context.Context, injected []lessons.InjectedLesson, findings []lessons.Finding) ([]lessons.RelapseHit, error)
}

// SetLessonsHooks подключает контур уроков (ручной DI из main, D-80).
// nil — трейс и терминальный distill работают (ядро детерминированно),
// версионная сверка и relapse пропускаются.
func (s *Supervisor) SetLessonsHooks(h LessonsHooks) {
	s.lessonsHooks = h
}

// terminalDistillGrace — пауза между переходом рана в failed и запуском
// терминального distill: окно для ручного resume (failed→running) и
// отсечка гонок сразу после перехода.
const terminalDistillGrace = 5 * time.Second

// --- трейс поведения (вход distill) ----------------------------------------

// attachBehaviorTrace собирает трейс перед distill-этапом и заполняет
// LaunchContext: BehaviorTrace ({{behavior_trace}}) + run_facts.json в
// run_dir; LessonSignals ({{gate_answers}}) — fallback для пользовательских
// пайплайнов. Best-effort: сбой записи артефакта не отменяет этап.
func (s *Supervisor) attachBehaviorTrace(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage, lc *LaunchContext) {
	trace := s.collectBehaviorTrace(ctx, run, s.currentOutcome(ctx, run))
	if path, err := lessons.WriteRunFacts(lc.RunDir, trace); err != nil {
		logError(ctx, "failed to write run facts", err)
	} else {
		s.registerRunFactsArtifact(ctx, run, stage, path)
	}
	lc.BehaviorTrace = trace.PromptText(lessons.TracePromptBudget)
	lc.LessonSignals = s.lessonSignals(ctx, run.ID)
}

// registerRunFactsArtifact регистрирует run_facts.json артефактом рана
// (m15: дедуп по пути — рестарт/повторная попытка distill не плодит
// дубликаты в БД). Best-effort.
func (s *Supervisor) registerRunFactsArtifact(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage, path string) {
	if existing, err := s.artifacts.ListArtifactsByRun(ctx, run.ID); err == nil {
		for _, a := range existing {
			if a.Path == path {
				return // уже зарегистрирован
			}
		}
	}
	if _, err := s.artifacts.CreateArtifact(ctx, dtorep.CreateArtifactRequest{
		RunID: run.ID, StageID: &stage.ID, Path: path, Kind: "run_facts",
	}); err != nil {
		logError(ctx, "failed to register run facts artifact", err)
	}
}

// collectBehaviorTrace — маппинг данных supervisor'а (гейты, заметки,
// попытки этапов) во вход сборщика lessons.CollectTrace.
func (s *Supervisor) collectBehaviorTrace(ctx context.Context, run *dtorep.Run, outcome lessons.RunOutcome) *lessons.BehaviorTrace {
	in := lessons.TraceInput{
		RunID:    run.ID,
		TaskText: run.TaskText,
		RunDir:   s.runDir(run.ID),
		Outcome:  outcome,
	}

	if gates, err := s.gates.ListGatesByRun(ctx, run.ID); err == nil {
		for _, g := range gates {
			if g.Answer != nil && *g.Answer != "" {
				in.Gates = append(in.Gates, lessons.GateSignal{
					Kind: string(g.Kind), Question: g.Question, Answer: *g.Answer,
				})
			}
		}
	}
	if notes, err := s.notes.ListNotesByRun(ctx, run.ID); err == nil {
		for _, n := range notes {
			in.Notes = append(in.Notes, lessons.RunNote{Kind: string(n.Kind), Text: n.Text})
		}
	}
	if stages, err := s.stages.ListStagesByRun(ctx, run.ID); err == nil {
		for _, st := range stages {
			fact := lessons.StageFact{
				Key: st.StageKey, Iteration: st.Iteration,
				State: string(st.State), ExitCode: st.ExitCode,
			}
			if st.Error != nil {
				fact.Error = *st.Error
			}
			in.Stages = append(in.Stages, fact)
		}
	}
	return lessons.CollectTrace(in)
}

// currentOutcome — исход рана на момент сбора трейса: состояние, финальный
// verdict (если reviewer уже отработал), глубина fix-петли, статистика
// гейтов. Для терминального distill run.State уже терминальный.
func (s *Supervisor) currentOutcome(ctx context.Context, run *dtorep.Run) lessons.RunOutcome {
	out := lessons.RunOutcome{State: string(run.State)}

	if gates, err := s.gates.ListGatesByRun(ctx, run.ID); err == nil {
		for _, g := range gates {
			switch g.State {
			case dtorep.GateStateApproved:
				out.GatesApproved++
			case dtorep.GateStateRejected:
				out.GatesRejected++
			}
		}
	}

	if data, err := os.ReadFile(filepath.Join(s.runDir(run.ID), "verdict.json")); err == nil {
		var v lessons.Verdict
		if err := json.Unmarshal(data, &v); err == nil {
			out.FinalVerdict = v.Verdict
		}
	}

	if spec, err := s.specFor(ctx, run.ID); err == nil && spec.Loop != nil {
		if fixer, err := s.stages.GetLatestStage(ctx, run.ID, spec.Loop.To); err == nil {
			out.Iterations = int(fixer.Iteration)
		}
	}
	return out
}

// --- терминальный distill (distill на провальных исходах) -------------------

// processTerminalDistills — хук на терминальные неуспешные исходы (T-30):
// failed-ран при lessons:on тоже проходит distill — провальные раны дают
// самые ценные уроки. Реализация — out-of-band этап: ран уже в терминальном
// состоянии, distill запускается с тем же run_dir, гейт lesson_review
// открывается напрямую (openTerminalLessonGate), без перевода рана в
// waiting_gate. Пометка «обработан» — строка этапа distill в БД +
// in-memory кеш (пересобирается при подъёме; история до подъёма отсекается
// по startedAt, чтобы не дистиллировать древние раны).
func (s *Supervisor) processTerminalDistills(ctx context.Context) error {
	failed, err := s.runs.ListRuns(ctx, dtorep.ListRunsRequest{
		States: []dtorep.RunState{dtorep.RunStateFailed},
	})
	if err != nil {
		return fmt.Errorf("failed to list failed runs: %w", err)
	}

	for i := range failed {
		run := &failed[i]
		if run.FinishedAt == nil {
			continue
		}
		s.mu.Lock()
		handled := s.terminalDistillHandled[run.ID]
		s.mu.Unlock()
		if handled {
			continue
		}
		done, err := s.maybeStartTerminalDistill(ctx, run)
		if err != nil {
			logError(ctx, fmt.Sprintf("terminal distill for run %s failed", run.ID), err)
			continue // не помечаем — повторим на следующем тике
		}
		if done {
			s.mu.Lock()
			s.terminalDistillHandled[run.ID] = true
			s.mu.Unlock()
		}
	}
	return nil
}

// maybeStartTerminalDistill запускает (или доводит до конца) distill для
// failed-рана. done=true — ран обработан окончательно: distill-этап в
// терминальном состоянии (succeeded/skipped/failed) ИЛИ гейт lesson_review
// уже открыт. pending/interrupted стадии — перезапускаем (M2: запуск,
// оборванный рестартом демона, не должен терять distill).
func (s *Supervisor) maybeStartTerminalDistill(ctx context.Context, run *dtorep.Run) (bool, error) {
	spec, err := s.specFor(ctx, run.ID) // аугментированная: builtin distill есть при lessons:on
	if err != nil {
		return false, err
	}
	if !spec.LessonsOn() {
		return true, nil // мастер-выключатель — обрабатывать нечего
	}
	key := spec.DistillStageKey()
	if key == "" {
		return true, nil // аугментер не подключён (например тесты) — гарантии нет
	}

	// гейт уже открыт — distill отработал (идемпотентность по ключу гейта)
	if _, err := s.gates.GetGateByIdempotencyKey(ctx, terminalLessonGateIdem(run.ID)); err == nil {
		return true, nil
	}

	latest, err := s.stages.GetLatestStage(ctx, run.ID, key)
	switch {
	case err == nil:
		switch latest.State {
		case dtorep.StageStateSucceeded, dtorep.StageStateSkipped, dtorep.StageStateFailed:
			return true, nil
		case dtorep.StageStateRunning:
			return false, nil // в работе (procs трекает)
		case dtorep.StageStatePending:
			// создан, но не запущен (сбой launchStage / рестарт между
			// StartStage и запуском) — просто запускаем
			return false, s.launchStage(ctx, run, latest)
		case dtorep.StageStateInterrupted:
			// оборван рестартом демона — диалоговый ре-вход (без
			// resume_count: это не auto-resume сессии)
			newStage, err := s.machine.ReenterStage(ctx, latest.ID, "")
			if err != nil {
				return false, fmt.Errorf("failed to re-enter terminal distill: %w", err)
			}
			return false, s.launchStage(ctx, run, newStage)
		}
		return true, nil
	case !errors.Is(err, cstmerrors.ErrNotFound):
		return false, fmt.Errorf("failed to check distill stage: %w", err)
	}

	// Свежий терминальный distill. История до подъёма демона пропускается
	// (cutoff по startedAt) — но только для СВЕЖЕГО запуска: recovery
	// (строка этапа/гейт уже есть) обрабатывается выше независимо от
	// давности (M2).
	if run.FinishedAt.Before(s.startedAt) {
		return true, nil // завершился до подъёма — не дистиллируем древние раны
	}
	if time.Since(*run.FinishedAt) < terminalDistillGrace {
		return false, nil // окно ручного resume — повторим на следующем тике
	}

	// distill ещё не было — полный трейс с исходом → run_facts.json (виден в UI)
	trace := s.collectBehaviorTrace(ctx, run, s.currentOutcome(ctx, run))
	if _, err := lessons.WriteRunFacts(s.runDir(run.ID), trace); err != nil {
		logError(ctx, "failed to write run facts (terminal)", err)
	}

	harnessName := ""
	origin := ""
	for _, st := range spec.Stages {
		if st.Key == key {
			harnessName = st.Harness
			origin = st.Origin
		}
	}
	stage, err := s.machine.StartStage(ctx, run.ID, key, harnessName)
	if err != nil {
		return false, fmt.Errorf("failed to start terminal distill stage: %w", err)
	}
	if err := s.appendRunEvent(ctx, run.ID, &stage.ID, "run.terminal_distill",
		marshalEventPayload(map[string]any{
			"stage_key": key,
			"origin":    origin,
			"outcome":   string(run.State),
		})); err != nil {
		return false, err
	}
	return false, s.launchStage(ctx, run, stage)
}

// terminalLessonGateIdem — идемпотентность out-of-band гейта терминального
// distill: один гейт на ран (повторный distill не плодит гейты).
func terminalLessonGateIdem(runID string) string {
	return "terminal-lesson-review-" + runID
}

// openTerminalLessonGate — гейт lesson_review для завершившегося рана
// (терминальный distill, T-30): ран уже failed, waiting_gate из терминала
// невозможен — гейт создаётся out-of-band, без смены состояния рана.
// Резолвится штатно (ResolveGateAPI → LessonsFinalizer). Идемпотентно по
// ключу (повторный distill того же рана не плодит гейты).
func (s *Supervisor) openTerminalLessonGate(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage, question, contextJSON string) error {
	idem := terminalLessonGateIdem(run.ID)
	if _, err := s.gates.GetGateByIdempotencyKey(ctx, idem); err == nil {
		return nil // уже открыт
	} else if !errors.Is(err, cstmerrors.ErrNotFound) {
		return err
	}

	gateID := uuid.New()
	if err := s.gates.CreateGate(ctx, dtorep.CreateGateRequest{
		ID:             gateID,
		RunID:          run.ID,
		StageID:        &stage.ID,
		Kind:           dtorep.GateKindLessonReview,
		Question:       question,
		ContextJSON:    contextJSON,
		IdempotencyKey: idem,
	}); err != nil {
		return err
	}

	// payload — форма machine.OpenGate (фронт вставляет гейт в стор по событию)
	payload := marshalEventPayload(map[string]any{
		"gate_id":      gateID,
		"kind":         dtorep.GateKindLessonReview,
		"question":     question,
		"context_json": contextJSON,
		"stage_id":     &stage.ID,
	})
	return s.appendRunEvent(ctx, run.ID, &stage.ID, runsmachine.EventKindGateOpened, payload)
}

// --- петля качества ----------------------------------------------------------

// checkVendorVersions — версионная деградация vendor-уроков при старте рана
// (T-30): расхождение vendor_version с go.mod → урок outdated (ядро) +
// событие в журнал. Best-effort: старт рана не блокируется.
func (s *Supervisor) checkVendorVersions(ctx context.Context, run *dtorep.Run, projectPath string) {
	if s.lessonsHooks == nil {
		return
	}
	outdated, restored, err := s.lessonsHooks.CheckVendorVersions(ctx, projectPath)
	if err != nil {
		logError(ctx, "vendor version check failed", err)
		return
	}
	toItems := func(list []dtorep.Lesson) []map[string]any {
		items := make([]map[string]any, 0, len(list))
		for _, l := range list {
			item := map[string]any{"lesson_id": l.ID, "title": l.Title}
			if l.Vendor != nil {
				item["vendor"] = *l.Vendor
			}
			if l.VendorVersion != nil {
				item["vendor_version"] = *l.VendorVersion
			}
			items = append(items, item)
		}
		return items
	}
	if len(outdated) > 0 {
		if err := s.appendRunEvent(ctx, run.ID, nil, "lessons.vendor_outdated",
			marshalEventPayload(map[string]any{"lessons": toItems(outdated)})); err != nil {
			logError(ctx, "failed to append vendor_outdated event", err)
		}
	}
	if len(restored) > 0 {
		if err := s.appendRunEvent(ctx, run.ID, nil, "lessons.vendor_restored",
			marshalEventPayload(map[string]any{"lessons": toItems(restored)})); err != nil {
			logError(ctx, "failed to append vendor_restored event", err)
		}
	}
}

// archiveVerdictSnapshot — архивация verdict-<iter>.json на каждой итерации
// reviewer (T-30: полная fix-петля в трейсе; голый verdict.json перезаписы-
// вается каждой итерацией). Best-effort.
func (s *Supervisor) archiveVerdictSnapshot(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage) {
	spec, err := s.specFor(ctx, run.ID)
	if err != nil || spec.Loop == nil || stage.StageKey != spec.Loop.From {
		return
	}
	src := filepath.Join(s.runDir(run.ID), "verdict.json")
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	dst := filepath.Join(s.runDir(run.ID), fmt.Sprintf("verdict-%d.json", stage.Iteration))
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		logError(ctx, "failed to archive verdict snapshot", err)
	}
}

// runRelapseCheck — relapse-эвристика после этапа reviewer (T-30): finding
// blocking/major совпал по FTS с уроком, инжектированным в этот ран →
// счётчик relapse++ (ядро) + событие в журнал. Best-effort: эвристика не
// блокирует пайплайн, статусы уроков не меняет.
func (s *Supervisor) runRelapseCheck(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage) {
	if s.lessonsHooks == nil {
		return
	}
	spec, err := s.specFor(ctx, run.ID)
	if err != nil || spec.Loop == nil || stage.StageKey != spec.Loop.From {
		return
	}
	data, err := os.ReadFile(filepath.Join(s.runDir(run.ID), "verdict.json"))
	if err != nil {
		return
	}
	var verdict lessons.Verdict
	if err := json.Unmarshal(data, &verdict); err != nil || len(verdict.Findings) == 0 {
		return
	}
	// M4: дедуп по (run, finding id) — finding, живущий несколько итераций
	// reviewer, штрафует урок один раз за ран
	runDir := s.runDir(run.ID)
	counted := map[string]bool{}
	for _, h := range lessons.ReadRelapseHits(runDir) {
		counted[h.FindingID] = true
	}
	findings := verdict.Findings[:0]
	for _, f := range verdict.Findings {
		if f.ID == "" || !counted[f.ID] {
			findings = append(findings, f)
		}
	}
	if len(findings) == 0 {
		return
	}
	injected, err := s.lessonsHooks.LoadRunInjections(ctx, runDir)
	if err != nil {
		logError(ctx, "failed to load injected lessons", err)
		return
	}
	if len(injected) == 0 {
		return
	}
	hits, err := s.lessonsHooks.RelapseCheck(ctx, injected, findings)
	if err != nil {
		logError(ctx, "relapse check failed", err)
	}
	if err := lessons.AppendRelapseHits(runDir, hits); err != nil {
		logError(ctx, "failed to persist relapse hits", err)
	}
	for _, h := range hits {
		if err := s.appendRunEvent(ctx, run.ID, &stage.ID, "lessons.relapse",
			marshalEventPayload(map[string]any{
				"lesson_id":    h.LessonID,
				"lesson_title": h.LessonTitle,
				"finding_id":   h.FindingID,
				"severity":     h.Severity,
			})); err != nil {
			logError(ctx, "failed to append relapse event", err)
		}
	}
}

// skipDistillIfDisabled — мастер-выключатель lessons:off (T-30): distill-
// этап пропускается (строка skipped + событие в журнале), даже если нода
// осталась в YAML пайплайна. true — этап запускать НЕ нужно.
func (s *Supervisor) skipDistillIfDisabled(ctx context.Context, run *dtorep.Run, stageKey string) bool {
	spec, err := s.specFor(ctx, run.ID)
	if err != nil || spec.LessonsOn() {
		return false
	}
	var isDistill bool
	for _, st := range spec.Stages {
		if st.Key == stageKey {
			isDistill = runsmachine.IsDistillStage(st)
		}
	}
	if !isDistill {
		return false
	}

	if _, err := s.stages.GetLatestStage(ctx, run.ID, stageKey); err == nil {
		return true // попытка уже есть (skipped или запущенная до выключения)
	}
	if err := s.machine.SkipStage(ctx, run.ID, stageKey); err != nil {
		logError(ctx, fmt.Sprintf("failed to skip distill stage %q", stageKey), err)
		return true
	}
	if err := s.appendRunEvent(ctx, run.ID, nil, "stage.skipped",
		marshalEventPayload(map[string]any{"stage_key": stageKey, "reason": "lessons_off"})); err != nil {
		logError(ctx, "failed to append distill skip event", err)
	}
	return true
}

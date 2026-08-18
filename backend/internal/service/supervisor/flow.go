package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	runsmachine "github.com/fableFM/glamor/internal/service/runsmachine"
)

// launchStage запускает попытку этапа в отдельной горутине через семафор
// пула процессов (D-33); ожидание семафора сопровождается stage.queued.
func (s *Supervisor) launchStage(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage) error {
	s.mu.Lock()
	if _, tracked := s.procs[stage.ID]; tracked {
		s.mu.Unlock()
		return nil // уже запущена (тик может увидеть pending дважды)
	}
	if _, ok := s.queued[stage.ID]; ok {
		s.mu.Unlock()
		return nil // уже ждёт семафор
	}
	s.queued[stage.ID] = true
	s.mu.Unlock()

	// stage.queued — до захвата семафора (может ждать долго)
	if err := s.appendRunEvent(ctx, run.ID, &stage.ID, "stage.queued",
		fmt.Sprintf(`{"stage_key":%q,"iteration":%d}`, stage.StageKey, stage.Iteration)); err != nil {
		s.mu.Lock()
		delete(s.queued, stage.ID)
		s.mu.Unlock()
		return err
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		s.sem <- struct{}{} // пул процессов
		defer func() { <-s.sem }()

		s.mu.Lock()
		delete(s.queued, stage.ID)
		if s.draining {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		// janitor-нода (T-22): детерминированные команды без harness
		if stageSpec, err := s.stageSpecFor(ctx, stage); err == nil && stageSpec.Kind == "janitor" {
			project, err := s.projects.GetProjectByID(ctx, run.ProjectID)
			if err != nil {
				logError(ctx, fmt.Sprintf("failed to load project for janitor stage %d", stage.ID), err)
				s.failStage(ctx, stage, fmt.Sprintf("load project failed: %v", err))
				return
			}
			if err := s.runJanitor(ctx, run, stage, stageSpec, project.Path); err != nil {
				logError(ctx, fmt.Sprintf("janitor stage %d failed", stage.ID), err)
			}
			return
		}

		proc, err := s.spawnStage(ctx, run, stage)
		if err != nil {
			logError(ctx, fmt.Sprintf("failed to spawn stage %d", stage.ID), err)
			s.failStage(ctx, stage, fmt.Sprintf("spawn failed: %v", err))
			return
		}

		s.mu.Lock()
		s.procs[stage.ID] = proc
		s.mu.Unlock()

		result := proc.wait()
		result.stageID = stage.ID
		result.runID = run.ID

		s.mu.Lock()
		delete(s.procs, stage.ID)
		s.mu.Unlock()

		s.completions <- result
	}()
	return nil
}

// drainCompletions забирает завершившиеся процессы и классифицирует их (D-13/14).
func (s *Supervisor) drainCompletions(ctx context.Context) {
	for {
		select {
		case result := <-s.completions:
			if err := s.classify(ctx, result); err != nil {
				logError(ctx, fmt.Sprintf("failed to classify stage %d", result.stageID), err)
			}
		default:
			return
		}
	}
}

// classify — источник истины о завершении этапа (D-13): exit code +
// наличие/валидация артефакта; контур прерываний — D-14.
func (s *Supervisor) classify(ctx context.Context, result stageResult) error {
	stage, err := s.stages.GetStageByID(ctx, result.stageID)
	if err != nil {
		return fmt.Errorf("failed to load stage: %w", err)
	}
	if stage.State != dtorep.StageStateRunning {
		return nil // состояние уже изменено (stop/steer пользователя)
	}

	now := time.Now().UTC()
	exitCode := int64(result.exitCode)

	// 1. Явная остановка (D-14): stop_requested_by записан ДО убийства.
	if stage.StopRequestedBy != nil && *stage.StopRequestedBy != "" {
		errMsg := fmt.Sprintf("stopped by %s", *stage.StopRequestedBy)
		if err := s.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateInterrupted,
			dtorep.StageTransitionFields{ExitCode: &exitCode, FinishedAt: &now, Error: &errMsg}); err != nil {
			return err
		}
		if *stage.StopRequestedBy == "user" {
			return nil // auto-resume НЕ срабатывает (D-14)
		}
		// "daemon" (graceful shutdown) — auto-resume при подъёме допустим
		return s.scheduleAutoResume(ctx, stage)
	}

	// 2. Watchdog-прерывания (stall / timeout / retriable-шторм) → interrupted → auto-resume
	if result.stall || result.timeout || result.retriableBurst {
		errMsg := result.reason
		if err := s.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateInterrupted,
			dtorep.StageTransitionFields{ExitCode: &exitCode, FinishedAt: &now, Error: &errMsg}); err != nil {
			return err
		}
		if err := s.appendRunEvent(ctx, stage.RunID, &stage.ID, "stage.interrupted",
			fmt.Sprintf(`{"reason":%q}`, result.reason)); err != nil {
			return err
		}
		return s.scheduleAutoResume(ctx, stage)
	}

	// 3. exit code + артефакт (D-13)
	if result.exitCode == 0 {
		artifactOK, err := s.checkArtifact(ctx, stage)
		if err != nil {
			return err
		}
		if artifactOK {
			fields := dtorep.StageTransitionFields{ExitCode: &exitCode, FinishedAt: &now}
			if result.usage != nil {
				fields.TokensIn = &result.usage.Input
				fields.TokensOut = &result.usage.Output
			}
			if err := s.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateSucceeded, fields); err != nil {
				return err
			}
			run, err := s.runs.GetRunByID(ctx, stage.RunID)
			if err != nil {
				return err
			}
			// регистрация артефакта этапа (D-13, виден в UI)
			s.registerStageArtifact(ctx, run, stage)
			// диалоговый протокол (D-20/21): артефакт-гейты до продвижения
			if err := s.handlePostStageGates(ctx, run, stage); err != nil {
				return err
			}
			if s.onSuccess != nil {
				spec, err := s.specFor(ctx, stage.RunID)
				if err != nil {
					return err
				}
				return s.onSuccess(ctx, run, stage, spec)
			}
			return nil
		}
		errMsg := "stage finished with exit 0 but required artifact is missing"
		return s.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateFailed,
			dtorep.StageTransitionFields{ExitCode: &exitCode, FinishedAt: &now, Error: &errMsg})
	}

	// 4. non-zero exit без артефакта → interrupted → auto-resume (D-14)
	errMsg := fmt.Sprintf("process exited with code %d", result.exitCode)
	if err := s.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateInterrupted,
		dtorep.StageTransitionFields{ExitCode: &exitCode, FinishedAt: &now, Error: &errMsg}); err != nil {
		return err
	}
	if err := s.appendRunEvent(ctx, stage.RunID, &stage.ID, "stage.interrupted",
		fmt.Sprintf(`{"reason":%q}`, errMsg)); err != nil {
		return err
	}
	return s.scheduleAutoResume(ctx, stage)
}

// scheduleAutoResume — политика auto-resume (D-14): backoff 30s→2m→5m,
// исчерпание → эскалация-гейт (NextAction → ActionEscalate на тике).
func (s *Supervisor) scheduleAutoResume(ctx context.Context, stage *dtorep.Stage) error {
	if stage.ResumeCount >= s.GetConfig().MaxAutoResumes {
		return nil // NextAction вернёт escalate на следующем тике
	}

	delay := s.GetConfig().Backoff[0]
	if int(stage.ResumeCount) < len(s.GetConfig().Backoff) {
		delay = s.GetConfig().Backoff[stage.ResumeCount]
	}

	s.mu.Lock()
	s.resumeAfter[stage.ID] = time.Now().Add(delay)
	s.mu.Unlock()

	slog.InfoContext(ctx, "auto-resume scheduled",
		slog.String("component", componentName),
		slog.Int64("stage_id", stage.ID),
		slog.String("delay", delay.String()),
	)
	return nil
}

// processDueResumes запускает отложенные resume, чьё время пришло.
func (s *Supervisor) processDueResumes(ctx context.Context) error {
	s.mu.Lock()
	var due []int64
	now := time.Now()
	for stageID, at := range s.resumeAfter {
		if !at.After(now) {
			due = append(due, stageID)
			delete(s.resumeAfter, stageID)
		}
	}
	s.mu.Unlock()

	for _, stageID := range due {
		stage, err := s.stages.GetStageByID(ctx, stageID)
		if err != nil {
			return err
		}
		if stage.State != dtorep.StageStateInterrupted {
			continue // пользователь успел что-то сделать (stop/steer)
		}
		// resume только от последней попытки цепочки (иначе двойной resume
		// с NextAction-путём)
		latest, err := s.stages.GetLatestStage(ctx, stage.RunID, stage.StageKey)
		if err != nil {
			return err
		}
		if latest.ID != stage.ID {
			continue
		}
		if err := s.resumeStage(ctx, stage); err != nil {
			return err
		}
	}
	return nil
}

// reconcileProcs: процесс жив, а стадия уже не running (stop/steer через
// API перевели её в interrupted) → убить процессную группу.
func (s *Supervisor) reconcileProcs(ctx context.Context) {
	s.mu.Lock()
	procs := make([]*stageProc, 0, len(s.procs))
	for _, p := range s.procs {
		procs = append(procs, p)
	}
	s.mu.Unlock()

	for _, p := range procs {
		stage, err := s.stages.GetStageByID(ctx, p.stageID)
		if err != nil {
			logError(ctx, fmt.Sprintf("failed to load stage %d", p.stageID), err)
			continue
		}
		if stage.State != dtorep.StageStateRunning {
			p.kill(s.GetConfig().KillGrace)
		}
	}
}

// failStage — ошибка спавна (не запустились вообще) → failed, не interrupted.
func (s *Supervisor) failStage(ctx context.Context, stage *dtorep.Stage, msg string) {
	now := time.Now().UTC()
	if err := s.machine.TransitionStage(ctx, stage.ID, dtorep.StageStateFailed,
		dtorep.StageTransitionFields{FinishedAt: &now, Error: &msg}); err != nil {
		logError(ctx, fmt.Sprintf("failed to mark stage %d failed", stage.ID), err)
	}
}

// checkArtifact — контракт артефакта из спеки этапа (D-13): обязательный
// файл обязан существовать и быть непустым.
func (s *Supervisor) checkArtifact(ctx context.Context, stage *dtorep.Stage) (bool, error) {
	spec, err := s.stageSpecFor(ctx, stage)
	if err != nil {
		return false, err
	}
	if spec.Artifact == nil || !spec.Artifact.Required {
		return true, nil
	}

	project, run, err := s.runAndProject(ctx, stage.RunID)
	if err != nil {
		return false, err
	}
	_ = run

	path := expandPath(spec.Artifact.Path, stage.RunID, s.runDir(stage.RunID))
	info, err := statFile(joinPath(project.Path, path))
	if err == nil && info.Size() > 0 {
		return true, nil
	}

	// D-20: «вопросы как артефакт» — этап с questions_path, записавший
	// непустые вопросы, завершился штатно и БЕЗ основного артефакта:
	// это валидный исход попытки (далее handlePostStageGates откроет
	// гейт question), а не провал. Поймано на живом ране 2026-08-17.
	if spec.QuestionsPath != "" {
		qpath := expandPath(spec.QuestionsPath, stage.RunID, s.runDir(stage.RunID))
		if qinfo, qerr := statFile(joinPath(project.Path, qpath)); qerr == nil && qinfo.Size() > 0 {
			return true, nil
		}
	}
	return false, nil
}

// runDir — каталог артефактов рана (~/.glamor/runs/<id>, T-17).
func (s *Supervisor) runDir(runID string) string {
	return joinPath(s.GetConfig().RunsDir, runID)
}

// specFor — спека пайплайна рана.
func (s *Supervisor) specFor(ctx context.Context, runID string) (runsmachine.Spec, error) {
	run, err := s.runs.GetRunByID(ctx, runID)
	if err != nil {
		return runsmachine.Spec{}, err
	}
	pipeline, err := s.pipelines.GetPipelineByID(ctx, run.PipelineVersionID)
	if err != nil {
		return runsmachine.Spec{}, err
	}
	spec, err := runsmachine.ParseSpec(pipeline.SpecJSON)
	if err != nil {
		return runsmachine.Spec{}, err
	}
	return spec, nil
}

// stageSpecFor — спека этапа из пайплайна рана (модель/effort/артефакт).
func (s *Supervisor) stageSpecFor(ctx context.Context, stage *dtorep.Stage) (runsmachine.StageSpec, error) {
	spec, err := s.specFor(ctx, stage.RunID)
	if err != nil {
		return runsmachine.StageSpec{}, err
	}
	for _, st := range spec.Stages {
		if st.Key == stage.StageKey {
			return st, nil
		}
	}
	return runsmachine.StageSpec{}, fmt.Errorf("stage %q not found in pipeline spec", stage.StageKey)
}

func (s *Supervisor) runAndProject(ctx context.Context, runID string) (*dtorep.Project, *dtorep.Run, error) {
	run, err := s.runs.GetRunByID(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	project, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	return project, run, nil
}

// appendRunEvent — событие рана в журнал (outbox в одной tx, D-11).
func (s *Supervisor) appendRunEvent(ctx context.Context, runID string, stageID *int64, kind, payload string) error {
	return s.journal.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return s.journal.Append(ctx, tx, dtorep.Event{
			RunID:       runID,
			StageID:     stageID,
			Kind:        kind,
			PayloadJSON: payload,
		})
	})
}

func logError(ctx context.Context, msg string, err error) {
	slog.ErrorContext(ctx, msg,
		slog.String("component", componentName),
		slog.String("error", err.Error()),
	)
}

// marshalEventPayload — payload нормализованного события стрима.
func marshalEventPayload(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return `{"error":"payload marshal failed"}`
	}
	return string(data)
}

// registerStageArtifact регистрирует файл-артефакт этапа в БД (best-effort).
func (s *Supervisor) registerStageArtifact(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage) {
	spec, err := s.stageSpecFor(ctx, stage)
	if err != nil || spec.Artifact == nil {
		return
	}
	path := expandPath(spec.Artifact.Path, run.ID, s.runDir(run.ID))
	if _, err := s.artifacts.CreateArtifact(ctx, dtorep.CreateArtifactRequest{
		RunID:   run.ID,
		StageID: &stage.ID,
		Path:    path,
		Kind:    "stage_output",
	}); err != nil {
		logError(ctx, "failed to register stage artifact", err)
	}
}

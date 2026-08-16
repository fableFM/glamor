package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	usecase "github.com/fableFM/glamor/internal/usecase/runs"
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
			// диалоговый протокол (D-20/21): артефакт-гейты до продвижения
			if err := s.handlePostStageGates(ctx, run, stage); err != nil {
				return err
			}
			if s.onSuccess != nil {
				return s.onSuccess(ctx, run, stage)
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
	if stage.ResumeCount >= s.cfg.MaxAutoResumes {
		return nil // NextAction вернёт escalate на следующем тике
	}

	delay := s.cfg.Backoff[0]
	if int(stage.ResumeCount) < len(s.cfg.Backoff) {
		delay = s.cfg.Backoff[stage.ResumeCount]
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
			p.kill(s.cfg.KillGrace)
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

	info, err := statFile(joinPath(project.Path, spec.Artifact.Path))
	if err != nil {
		return false, nil // файла нет
	}
	return info.Size() > 0, nil
}

// stageSpecFor — спека этапа из пайплайна рана (модель/effort/артефакт).
func (s *Supervisor) stageSpecFor(ctx context.Context, stage *dtorep.Stage) (usecase.StageSpec, error) {
	run, err := s.runs.GetRunByID(ctx, stage.RunID)
	if err != nil {
		return usecase.StageSpec{}, err
	}
	pipeline, err := s.pipelines.GetPipelineByID(ctx, run.PipelineVersionID)
	if err != nil {
		return usecase.StageSpec{}, err
	}
	spec, err := usecase.ParseSpec(pipeline.SpecJSON)
	if err != nil {
		return usecase.StageSpec{}, err
	}
	for _, st := range spec.Stages {
		if st.Key == stage.StageKey {
			return st, nil
		}
	}
	return usecase.StageSpec{}, fmt.Errorf("stage %q not found in pipeline spec", stage.StageKey)
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

var _ = eventsrep.RunIDAll

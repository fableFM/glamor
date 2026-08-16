package supervisor

import (
	"context"
	"fmt"
	"strings"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	runsmachine "github.com/fableFM/glamor/internal/service/runsmachine"
)

// handlePostStageGates — диалоговый протокол после успешного этапа
// (D-20/21, T-11): questions.md → гейт question; иначе gate_after из
// спеки → соответствующий гейт. Вызывается из classify (ветка succeeded),
// ДО продвижения пайплайна — открытый гейт держит ран в waiting_gate.
func (s *Supervisor) handlePostStageGates(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage) error {
	spec, err := s.stageSpecFor(ctx, stage)
	if err != nil {
		return err
	}

	// 1. «Вопросы как артефакт» (D-20): файл существует и непуст → question
	if spec.QuestionsPath != "" {
		has, err := s.hasQuestionsFile(ctx, run, spec.QuestionsPath)
		if err != nil {
			return err
		}
		if has {
			contextJSON := marshalEventPayload(map[string]any{
				"questions_path": expandRunID(spec.QuestionsPath, run.ID),
				"stage_key":      stage.StageKey,
				"iteration":      stage.Iteration,
			})
			_, err := s.machine.OpenGate(ctx, runsmachine.OpenGateRequest{
				RunID:       run.ID,
				StageID:     &stage.ID,
				Kind:        dtorep.GateKindQuestion,
				Question:    fmt.Sprintf("Этап %q задал вопросы (см. %s)", stage.StageKey, spec.QuestionsPath),
				ContextJSON: contextJSON,
			})
			return err
		}
	}

	// 2. gate_after из спеки (plan_approval/final_review/...)
	if spec.GateAfter != "" {
		contextJSON := marshalEventPayload(map[string]any{
			"stage_key": stage.StageKey,
			"iteration": stage.Iteration,
		})
		if spec.Artifact != nil {
			contextJSON = marshalEventPayload(map[string]any{
				"stage_key":      stage.StageKey,
				"iteration":      stage.Iteration,
				"artifact_paths": []string{spec.Artifact.Path},
			})
		}
		_, err := s.machine.OpenGate(ctx, runsmachine.OpenGateRequest{
			RunID:       run.ID,
			StageID:     &stage.ID,
			Kind:        dtorep.GateKind(spec.GateAfter),
			Question:    gateQuestion(dtorep.GateKind(spec.GateAfter), stage.StageKey),
			ContextJSON: contextJSON,
		})
		return err
	}

	return nil
}

// gateQuestion — текст гейта по виду (T-11 таблица).
func gateQuestion(kind dtorep.GateKind, stageKey string) string {
	switch kind {
	case dtorep.GateKindPlanApproval:
		return fmt.Sprintf("План готов (этап %q). Утвердить?", stageKey)
	case dtorep.GateKindFinalReview:
		return fmt.Sprintf("Пайплайн отработал (этап %q). Принять результат?", stageKey)
	case dtorep.GateKindEscalation:
		return fmt.Sprintf("Этап %q: петля/попытки исчерпаны. Что делать?", stageKey)
	default:
		return fmt.Sprintf("Гейт после этапа %q", stageKey)
	}
}

// hasQuestionsFile — questions.md существует и непуст (D-20).
func (s *Supervisor) hasQuestionsFile(ctx context.Context, run *dtorep.Run, questionsPath string) (bool, error) {
	project, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil {
		return false, err
	}
	info, err := statFile(joinPath(project.Path, expandRunID(questionsPath, run.ID)))
	if err != nil {
		return false, nil // файла нет — вопросов нет
	}
	return info.Size() > 0, nil
}

// processSteers — Interrupt & Steer (D-22): interrupted-стадия с
// stop_requested_by=user и НЕпотреблённой steer-заметкой → диалоговый
// ре-вход (без auto-resume счётчика), supervisor добил процесс reconcile'ом.
func (s *Supervisor) processSteers(ctx context.Context) error {
	running, err := s.runs.ListRuns(ctx, dtorep.ListRunsRequest{
		States: []dtorep.RunState{dtorep.RunStateRunning},
	})
	if err != nil {
		return err
	}

	for _, run := range running {
		stages, err := s.stages.ListStagesByRun(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, st := range stages {
			if st.State != dtorep.StageStateInterrupted ||
				st.StopRequestedBy == nil || *st.StopRequestedBy != "user" {
				continue
			}
			steers, err := s.notes.ListUnconsumedSteers(ctx, st.ID)
			if err != nil {
				return err
			}
			if len(steers) == 0 {
				continue // обычный user-stop — не резюмим (D-14)
			}
			if _, err := s.machine.ReenterStage(ctx, st.ID, ""); err != nil {
				logError(ctx, fmt.Sprintf("failed to steer-resume stage %d", st.ID), err)
			}
		}
	}
	return nil
}

// consumeNotes — заметки рана (queue note + steer) уходят в промпт
// ближайшей попытки (D-22: «к ближайшему событию») и помечаются consumed.
func (s *Supervisor) consumeNotes(ctx context.Context, runID string) ([]string, error) {
	unconsumed, err := s.notes.ListUnconsumedNotes(ctx, runID)
	if err != nil {
		return nil, err
	}
	// steers по всем стадиям рана
	stages, err := s.stages.ListStagesByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	var notes []dtorep.Note
	notes = append(notes, unconsumed...)
	for _, st := range stages {
		steers, err := s.notes.ListUnconsumedSteers(ctx, st.ID)
		if err != nil {
			return nil, err
		}
		notes = append(notes, steers...)
	}

	messages := make([]string, 0, len(notes))
	for _, n := range notes {
		messages = append(messages, n.Text)
		if err := s.notes.MarkNoteConsumed(ctx, n.ID); err != nil {
			logError(ctx, fmt.Sprintf("failed to consume note %d", n.ID), err)
		}
	}
	return messages, nil
}

func expandRunID(path, runID string) string {
	return strings.ReplaceAll(path, "{run_id}", runID)
}

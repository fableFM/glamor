package supervisor

import (
	"context"
	"fmt"
	"os"
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
			// содержимое questions.md — прямо в текст гейта (пользователь
			// отвечает в чате, не открывая файл; запрос 2026-08-17)
			questionText := s.readQuestionsText(ctx, run, spec.QuestionsPath)
			contextJSON := marshalEventPayload(map[string]any{
				"questions_path": expandPath(spec.QuestionsPath, run.ID, s.runDir(run.ID)),
				"stage_key":      stage.StageKey,
				"iteration":      stage.Iteration,
			})
			_, err := s.machine.OpenGate(ctx, runsmachine.OpenGateRequest{
				RunID:       run.ID,
				StageID:     &stage.ID,
				Kind:        dtorep.GateKindQuestion,
				Question:    questionText,
				ContextJSON: contextJSON,
			})
			return err
		}
	}

	// 2. gate_after из спеки (plan_approval/final_review/...)
	if spec.GateAfter != "" {
		// lesson_review (T-29): открываем только если distill оставил карточки
		if spec.GateAfter == "lesson_review" && spec.Artifact != nil {
			project, err := s.projects.GetProjectByID(ctx, run.ProjectID)
			if err != nil {
				return err
			}
			lessonsPath := joinPath(project.Path, expandPath(spec.Artifact.Path, run.ID, s.runDir(run.ID)))
			data, err := os.ReadFile(lessonsPath)
			if err != nil || !strings.Contains(string(data), "title:") ||
				strings.Contains(string(data), "NO_LESSONS") {
				return nil // уроков нет — гейт не открываем
			}
		}
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
		question := gateQuestion(dtorep.GateKind(spec.GateAfter), stage.StageKey)
		// саммари артефакта — прямо в текст гейта (plan_approval: начало
		// spec.md; чат-ответ без открытия файла, запрос 2026-08-17)
		if spec.Artifact != nil {
			if excerpt := s.readArtifactExcerpt(ctx, run, spec.Artifact.Path); excerpt != "" {
				question += "\n\n---\n" + excerpt
			}
		}

		_, err := s.machine.OpenGate(ctx, runsmachine.OpenGateRequest{
			RunID:       run.ID,
			StageID:     &stage.ID,
			Kind:        dtorep.GateKind(spec.GateAfter),
			Question:    question,
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
	info, err := statFile(joinPath(project.Path, expandPath(questionsPath, run.ID, s.runDir(run.ID))))
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

// expandPath раскрывает плейсхолдеры путей: {run_id} и {run_dir}
// (каталог артефактов рана ~/.glamor/runs/<id>, T-17).
func expandPath(path, runID, runDir string) string {
	path = strings.ReplaceAll(path, "{run_id}", runID)
	path = strings.ReplaceAll(path, "{run_dir}", runDir)
	return path
}

// lessonSignals — маркированные сигналы для distill-этапа (T-29):
// ответы пользователя на резолвнутых гейтах + заметки рана.
func (s *Supervisor) lessonSignals(ctx context.Context, runID string) string {
	var sb strings.Builder

	gates, err := s.gates.ListGatesByRun(ctx, runID)
	if err == nil {
		for _, g := range gates {
			if g.Answer != nil && *g.Answer != "" {
				fmt.Fprintf(&sb, "Гейт %s (%s) → ответ пользователя: %s\n\n", g.Kind, g.Question, *g.Answer)
			}
		}
	}

	notes, err := s.notes.ListNotesByRun(ctx, runID)
	if err == nil {
		for _, n := range notes {
			fmt.Fprintf(&sb, "Заметка (%s): %s\n\n", n.Kind, n.Text)
		}
	}

	if sb.Len() == 0 {
		return "(сигналов нет)"
	}
	return sb.String()
}

// questionsMaxLen — усечение текста вопросов в гейте (TG-лимиты, чат).
const questionsMaxLen = 3000

// readQuestionsText — содержимое questions.md для текста гейта (усечённое).
func (s *Supervisor) readQuestionsText(ctx context.Context, run *dtorep.Run, questionsPath string) string {
	project, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil {
		return "Этап задал вопросы (см. questions.md)"
	}
	path := joinPath(project.Path, expandPath(questionsPath, run.ID, s.runDir(run.ID)))
	data, err := os.ReadFile(path)
	if err != nil {
		return "Этап задал вопросы (см. questions.md)"
	}
	text := strings.TrimSpace(string(data))
	if len(text) > questionsMaxLen {
		text = text[:questionsMaxLen] + "\n…(усечено, полный текст — в questions.md)"
	}
	return text
}

// artifactExcerptMaxLen — усечение саммари артефакта в тексте гейта.
const artifactExcerptMaxLen = 1500

// readArtifactExcerpt — начало файла артефакта для текста гейта
// (plan_approval: саммари spec.md прямо в чат).
func (s *Supervisor) readArtifactExcerpt(ctx context.Context, run *dtorep.Run, artifactPath string) string {
	project, err := s.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil {
		return ""
	}
	path := joinPath(project.Path, expandPath(artifactPath, run.ID, s.runDir(run.ID)))
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if len(text) > artifactExcerptMaxLen {
		text = text[:artifactExcerptMaxLen] + "\n…(усечено, полный текст — в файле артефакта)"
	}
	return text
}

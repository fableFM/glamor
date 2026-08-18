package lessons

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
)

// GateFinalizer — резолв-эффекты гейта lesson_review (T-29, D-52):
// approve → сохранить в проектную память; comment → в глобальную;
// answer → в проектную с ответом в «Причину»; reject → отклонить
// (записывается rejected для dedup).
type GateFinalizer struct {
	svc      *Service
	runs     runsrep.RepositoryWithTX
	projects projectsrep.RepositoryWithTX
}

func NewGateFinalizer(svc *Service, runs runsrep.RepositoryWithTX, projects projectsrep.RepositoryWithTX) *GateFinalizer {
	return &GateFinalizer{svc: svc, runs: runs, projects: projects}
}

// FinalizeLessonGate применяет решение пользователя по черновикам уроков.
func (f *GateFinalizer) FinalizeLessonGate(ctx context.Context, gate *dtorep.Gate, action string, text *string) error {
	var ctxData struct {
		LessonsPath string `json:"lessons_path"`
		StageKey    string `json:"stage_key"`
	}
	if err := json.Unmarshal([]byte(gate.ContextJSON), &ctxData); err != nil {
		return fmt.Errorf("failed to parse lesson gate context: %w", err)
	}

	data, err := os.ReadFile(ctxData.LessonsPath)
	if err != nil {
		return fmt.Errorf("failed to read lessons draft: %w", err)
	}
	cards := ParseCards(string(data))
	if len(cards) == 0 {
		return nil // карточек нет — нечего сохранять
	}

	run, err := f.runs.GetRunByID(ctx, gate.RunID)
	if err != nil {
		return fmt.Errorf("failed to load run: %w", err)
	}
	project, err := f.projects.GetProjectByID(ctx, run.ProjectID)
	if err != nil {
		return fmt.Errorf("failed to load project: %w", err)
	}

	answer := ""
	if text != nil {
		answer = *text
	}

	for _, card := range cards {
		var scope, status string
		switch action {
		case "approve":
			scope, status = "project", StatusConfirmed
		case "comment":
			scope, status = "global", StatusConfirmed
		case "answer":
			scope, status = "project", StatusConfirmed
		case "reject":
			scope, status = "project", StatusRejected
		default:
			continue
		}

		if _, err := f.svc.SaveCard(ctx, card, scope, &project.ID, project.Path,
			&gate.RunID, ctxData.StageKey, status, answer); err != nil {
			if IsDuplicate(err) {
				continue // отклонённый ранее — не предлагаем повторно
			}
			return err
		}
	}
	return nil
}

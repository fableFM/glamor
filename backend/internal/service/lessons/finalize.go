package lessons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
)

// GateFinalizer — резолв-эффекты гейта lesson_review (T-29, D-52; операции
// distill v2 — T-30): approve → сохранить в проектную память; comment → в
// глобальную; answer → в проектную с ответом в «Причину»; reject →
// отклонить (записывается rejected для dedup). Per-card резолв (T-30):
// запрос может нести принятые/отклонённые операции по индексам.
type GateFinalizer struct {
	svc      *Service
	runs     runsrep.RepositoryWithTX
	projects projectsrep.RepositoryWithTX
}

func NewGateFinalizer(svc *Service, runs runsrep.RepositoryWithTX, projects projectsrep.RepositoryWithTX) *GateFinalizer {
	return &GateFinalizer{svc: svc, runs: runs, projects: projects}
}

// FinalizeLessonGate применяет решение пользователя по черновикам уроков.
// sel == nil → «всё или ничего» (действие применяется ко всем операциям);
// sel != nil → per-card: принимаются/отклоняются только перечисленные
// операции, неперечисленные пропускаются нейтрально (не сохраняются, dedup
// на них не влияет).
func (f *GateFinalizer) FinalizeLessonGate(ctx context.Context, gate *dtorep.Gate,
	action string, text *string, sel *dtorep.LessonOpSelection,
) error {
	var ctxData struct {
		LessonsPath string `json:"lessons_path"`
		StageKey    string `json:"stage_key"`
	}
	if err := json.Unmarshal([]byte(gate.ContextJSON), &ctxData); err != nil {
		return fmt.Errorf("failed to parse lesson gate context: %w", err)
	}
	if ctxData.LessonsPath == "" {
		return fmt.Errorf("lesson gate %s: empty lessons_path in context", gate.ID)
	}

	data, err := os.ReadFile(ctxData.LessonsPath)
	if err != nil {
		return fmt.Errorf("failed to read lessons draft: %w", err)
	}
	ops := ParseOperations(string(data))
	if len(ops) == 0 {
		return nil // операций нет (NO_LESSONS/мусор) — нечего применять
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
	base := ApplyParams{
		ProjectID: &project.ID, ProjectPath: project.Path,
		RunID: &gate.RunID, StageKey: ctxData.StageKey,
	}

	if sel != nil {
		return f.applyPerCard(ctx, ops, base, answer, sel)
	}
	return f.applyAll(ctx, ops, base, action, answer)
}

// applyAll — резолв «всё или ничего» (T-29): действие гейта задаёт
// scope/status/source для операций черновика. Reject обрабатывает только
// NEW/QUESTION (rejected для dedup): дельта-операции (REFINE/SUPERSEDE/
// LINK) при отклонении просто не применяются — полный reject не должен
// мутировать confirmed-уроки (D-52, C1).
func (f *GateFinalizer) applyAll(ctx context.Context, ops []Operation,
	base ApplyParams, action, answer string,
) error {
	params := base
	switch action {
	case "approve":
		params.Scope, params.Status, params.Source = "project", StatusConfirmed, SourceUser
	case "comment":
		params.Scope, params.Status, params.Source = "global", StatusConfirmed, SourceUser
	case "answer":
		params.Scope, params.Status, params.Source = "project", StatusConfirmed, SourceUser
		params.UserAnswer = answer
	case "reject":
		params.Scope, params.Status, params.Source = "project", StatusRejected, SourceAuto
		var filtered []Operation
		for _, op := range ops {
			if op.Op == OpNew || op.Op == OpQuestion {
				filtered = append(filtered, op)
			}
		}
		ops = filtered
	default:
		return nil
	}
	return joinOpErrors(f.svc.ApplyOperations(ctx, ops, params))
}

// applyPerCard — per-card резолв (T-30): accept → применить как confirmed
// (scope гейта — проектный); reject → NEW сохраняются rejected для dedup,
// дельта-операции (REFINE/SUPERSEDE/LINK) просто не применяются.
func (f *GateFinalizer) applyPerCard(ctx context.Context, ops []Operation,
	base ApplyParams, answer string, sel *dtorep.LessonOpSelection,
) error {
	mark := map[int]string{} // index → accept|reject
	for _, i := range sel.Accept {
		mark[i] = "accept"
	}
	for _, i := range sel.Reject {
		mark[i] = "reject"
	}

	var accepted, rejectedNew []Operation
	for i, op := range ops {
		switch mark[i] {
		case "accept":
			accepted = append(accepted, op)
		case "reject":
			// rejected сохраняем только новые карточки (dedup, T-29);
			// отклонённые дельты — просто не применены
			if op.Op == OpNew || op.Op == OpQuestion {
				rejectedNew = append(rejectedNew, op)
			}
		}
	}

	acceptParams := base
	acceptParams.Scope, acceptParams.Status, acceptParams.Source = "project", StatusConfirmed, SourceUser
	acceptParams.UserAnswer = answer
	errAccept := joinOpErrors(f.svc.ApplyOperations(ctx, accepted, acceptParams))

	rejectParams := base
	rejectParams.Scope, rejectParams.Status, rejectParams.Source = "project", StatusRejected, SourceAuto
	errReject := joinOpErrors(f.svc.ApplyOperations(ctx, rejectedNew, rejectParams))

	return errors.Join(errAccept, errReject)
}

// joinOpErrors — ошибки применения операций одной пачки (per-card
// независимость сохраняется в ApplyOperations; здесь — только агрегация).
func joinOpErrors(results []OpResult) error {
	var errs []error
	for _, r := range results {
		if r.Err != nil {
			errs = append(errs, r.Err)
		}
	}
	return errors.Join(errs...)
}

// Package runsapi — API-сценарии ранов (D-80: service): создание рана с
// идемпотентностью (D-12) и git-preflight'ом (T-10), stop/resume,
// заметки, резолв гейтов с API-семантикой. CAS-переходы состояний
// делегируются стейт-машине internal/service/runsmachine (ADR-001).
package runsapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/service/runsmachine"
	"github.com/fableFM/glamor/pkg/uuid"
)

// Service — API-сценарии ранов. Стейт-машина (runsmachine) — за полем
// machine; свои репозитории — для чтений и идемпотентности сценариев.
type Service struct {
	machine     *runsmachine.Machine
	txm         runsmachine.TxExecutor
	appender    runsmachine.EventAppender
	runs        runsrep.RepositoryWithTX
	stages      stagesrep.RepositoryWithTX
	notes       notesrep.RepositoryWithTX
	projects    projectsrep.RepositoryWithTX
	pipelines   pipelinesrep.RepositoryWithTX
	gates       gatesrep.RepositoryWithTX
	preflight   PreflightFunc
	branchNamer BranchNamerFunc
}

// New собирает сценарии из готовых зависимостей (ручной DI в main, D-80).
// txm/appender — те же экземпляры, что у стейт-машины (events.Journal),
// чтобы события tx попадали в Hub после коммита.
func New(
	machine *runsmachine.Machine,
	txm runsmachine.TxExecutor,
	appender runsmachine.EventAppender,
	runs runsrep.RepositoryWithTX,
	stages stagesrep.RepositoryWithTX,
	notes notesrep.RepositoryWithTX,
	projects projectsrep.RepositoryWithTX,
	pipelines pipelinesrep.RepositoryWithTX,
	gates gatesrep.RepositoryWithTX,
) *Service {
	return &Service{
		machine:   machine,
		txm:       txm,
		appender:  appender,
		runs:      runs,
		stages:    stages,
		notes:     notes,
		projects:  projects,
		pipelines: pipelines,
		gates:     gates,
	}
}

// CreateRunParams — параметры создания рана (POST /runs).
type CreateRunParams struct {
	ProjectID         int64
	PipelineVersionID int64
	TaskText          string
	BaseBranch        string // пусто → default_branch проекта
	Branch            string // пусто → glamor/<slug> (D-31)
	Depth             int64
	NotifyTG          bool
	Force             bool // override грязного чекаута в preflight (D-34, T-10)
	IdempotencyKey    string
}

// PreflightFunc — git-preflight перед созданием рана (T-10, internal/gitx).
// Подключается из main; nil — preflight пропускается.
type PreflightFunc func(ctx context.Context, projectPath, baseBranch, branch string, force bool) error

// SetPreflight подключает git-preflight (T-10).
func (u *Service) SetPreflight(fn PreflightFunc) {
	u.preflight = fn
}

// BranchNamerFunc — генератор имени рабочей ветки из slug'а (T-10:
// gitx.SuggestBranch с суффиксами -2/-3 при коллизии в git).
type BranchNamerFunc func(ctx context.Context, projectPath, slug string) (string, error)

// SetBranchNamer подключает генератор имени ветки (T-10).
func (u *Service) SetBranchNamer(fn BranchNamerFunc) {
	u.branchNamer = fn
}

// CreateRun создаёт ран в состоянии draft (старт этапов — supervisor, T-09).
// Идемпотентность (D-12): повтор ключа возвращает первый ран
// (alreadyExisted=true). Lock (D-33): активный ран на (project, branch) →
// ErrRunLocked (частичный UNIQUE-индекс, enforced в БД).
func (u *Service) CreateRun(ctx context.Context, params CreateRunParams) (run *dtorep.Run, alreadyExisted bool, err error) {
	if params.IdempotencyKey == "" {
		params.IdempotencyKey = uuid.New()
	}

	project, err := u.projects.GetProjectByID(ctx, params.ProjectID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load project: %w", err)
	}
	if _, err := u.pipelines.GetPipelineByID(ctx, params.PipelineVersionID); err != nil {
		return nil, false, fmt.Errorf("failed to load pipeline: %w", err)
	}

	if params.BaseBranch == "" {
		params.BaseBranch = project.DefaultBranch
	}
	if params.Branch == "" {
		slug := slugify(params.TaskText)
		if u.branchNamer != nil {
			branch, err := u.branchNamer(ctx, project.Path, slug)
			if err != nil {
				return nil, false, fmt.Errorf("failed to suggest branch: %w", err)
			}
			params.Branch = branch
		} else {
			params.Branch = "glamor/" + slug
		}
	}

	if u.preflight != nil {
		if err := u.preflight(ctx, project.Path, params.BaseBranch, params.Branch, params.Force); err != nil {
			return nil, false, err
		}
	}

	runID := uuid.New()
	err = u.runs.CreateRun(ctx, dtorep.CreateRunRequest{
		ID:                runID,
		ProjectID:         params.ProjectID,
		PipelineVersionID: params.PipelineVersionID,
		TaskText:          params.TaskText,
		BaseBranch:        params.BaseBranch,
		Branch:            params.Branch,
		State:             dtorep.RunStateDraft,
		Depth:             params.Depth,
		NotifyTG:          params.NotifyTG,
		IdempotencyKey:    params.IdempotencyKey,
	})
	switch {
	case err == nil:
	case errors.Is(err, cstmerrors.ErrDuplicate):
		// повтор ключа → первый ран; иначе — lock ветки (D-33)
		existing, getErr := u.runs.GetRunByIdempotencyKey(ctx, params.IdempotencyKey)
		if getErr == nil {
			return existing, true, nil
		}
		return nil, false, fmt.Errorf("project %d branch %s: %w",
			params.ProjectID, params.Branch, cstmerrors.ErrRunLocked)
	default:
		return nil, false, err
	}

	run, err = u.runs.GetRunByID(ctx, runID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load created run: %w", err)
	}
	return run, false, nil
}

// StopRun — остановка пользователем (D-14): run → stopped, running-стадии
// помечаются stop_requested_by=user (auto-resume не сработает) и уходят в
// interrupted; процессы добивает supervisor (T-09). Идемпотентно.
func (u *Service) StopRun(ctx context.Context, runID string) (*dtorep.Run, error) {
	err := u.txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		runsTx := runsrep.NewTx(tx)
		stagesTx := stagesrep.NewTx(tx)

		run, err := runsTx.GetRunByID(ctx, runID)
		if err != nil {
			return fmt.Errorf("failed to load run: %w", err)
		}
		if run.State == dtorep.RunStateStopped {
			return nil // идемпотентный повтор
		}
		if err := u.machine.TransitionRunInTx(ctx, tx, runID, dtorep.RunStateStopped); err != nil {
			return err
		}

		// D-14: stop_requested_by=user ДО убийства процесса (supervisor, T-09)
		stages, err := stagesTx.ListStagesByRun(ctx, runID)
		if err != nil {
			return err
		}
		for _, st := range stages {
			if st.State != dtorep.StageStateRunning {
				continue
			}
			by := "user"
			ok, err := stagesTx.TransitionStageState(ctx, st.ID,
				dtorep.StageStateRunning, dtorep.StageStateInterrupted,
				dtorep.StageTransitionFields{StopRequestedBy: &by})
			if err != nil {
				return err
			}
			if !ok {
				continue // стадия уже завершилась — не страшно
			}
			if err := runsmachine.AppendStateEvent(ctx, tx, u.appender, runID, &st.ID,
				runsmachine.EventKindStageStateChanged,
				string(dtorep.StageStateRunning), string(dtorep.StageStateInterrupted)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return u.runs.GetRunByID(ctx, runID)
}

// ResumeRun — ручной рестарт failed/interrupted рана (T-05):
// failed → running (ручной переход, ADR-001 доп. 2026-08-16); дальше
// supervisor тикает NextAction (resume interrupted-стадии и т.п.).
// Идемпотентно: running/waiting_gate → no-op.
func (u *Service) ResumeRun(ctx context.Context, runID string) (*dtorep.Run, error) {
	run, err := u.runs.GetRunByID(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to load run: %w", err)
	}

	switch run.State {
	case dtorep.RunStateRunning, dtorep.RunStateWaitingGate:
		return run, nil // идемпотентный повтор
	case dtorep.RunStateFailed:
		if err := u.machine.TransitionRun(ctx, runID, dtorep.RunStateRunning); err != nil {
			return nil, err
		}
	case dtorep.RunStateDraft:
		if err := u.machine.TransitionRun(ctx, runID, dtorep.RunStateRunning); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("run %s in state %s: %w",
			runID, run.State, cstmerrors.ErrInvalidTransition)
	}

	return u.runs.GetRunByID(ctx, runID)
}

// GateAction — действие резолва из API.
type GateAction string

const (
	GateActionApprove GateAction = "approve"
	GateActionReject  GateAction = "reject"
	GateActionAnswer  GateAction = "answer"
	GateActionComment GateAction = "comment"
)

func mapGateAction(action GateAction) (dtorep.GateState, error) {
	switch action {
	case GateActionApprove:
		return dtorep.GateStateApproved, nil
	case GateActionReject:
		return dtorep.GateStateRejected, nil
	case GateActionAnswer, GateActionComment:
		return dtorep.GateStateAnswered, nil
	}
	return "", fmt.Errorf("unknown gate action %q: %w", action, cstmerrors.ErrValidation)
}

// ResolveGateAPI — резолв гейта с API-семантикой идемпотентности:
// гейт уже резолвнут ТЕМ ЖЕ резолюшном → (gate, alreadyResolved=true, nil);
// другим → ErrGateAlreadyResolved (409). Возвращает актуальное состояние
// гейта (контроллеру не нужно перечитывать репозиторий).
func (u *Service) ResolveGateAPI(ctx context.Context, gateID string, action GateAction, text *string) (gate *dtorep.Gate, alreadyResolved bool, err error) {
	resolution, err := mapGateAction(action)
	if err != nil {
		return nil, false, err
	}

	gate, err = u.gates.GetGateByID(ctx, gateID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load gate: %w", err)
	}

	if gate.State != dtorep.GateStateOpen {
		if gate.State == resolution {
			return gate, true, nil
		}
		return nil, false, fmt.Errorf("gate %s resolved as %s, requested %s: %w",
			gateID, gate.State, resolution, cstmerrors.ErrGateAlreadyResolved)
	}

	if err := u.machine.ResolveGate(ctx, gateID, resolution, text); err != nil {
		return nil, false, err
	}

	// Диалоговый резолв (D-20/22): answer/comment с текстом по гейту этапа
	// → ре-вход этапа с сообщением (resume сессии с ответом).
	if (action == GateActionAnswer || action == GateActionComment) &&
		text != nil && *text != "" && gate.StageID != nil {
		if _, err := u.machine.ReenterStage(ctx, *gate.StageID, *text); err != nil {
			return nil, false, err
		}
	}

	gate, err = u.gates.GetGateByID(ctx, gateID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to reload gate: %w", err)
	}
	return gate, false, nil
}

// InterruptStageSteer — Interrupt & Steer (D-22): running-стадия →
// interrupted с stop_requested_by=user + steer-заметка; supervisor (T-09)
// добьёт процесс и резюмит сессию с сообщением (T-11).
func (u *Service) InterruptStageSteer(ctx context.Context, stageID int64, message string) (*dtorep.Stage, error) {
	if message == "" {
		return nil, fmt.Errorf("steer message is empty: %w", cstmerrors.ErrValidation)
	}

	err := u.txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		stagesTx := stagesrep.NewTx(tx)
		notesTx := notesrep.NewTx(tx)

		stage, err := stagesTx.GetStageByID(ctx, stageID)
		if err != nil {
			return fmt.Errorf("failed to load stage: %w", err)
		}
		if stage.State != dtorep.StageStateRunning {
			return fmt.Errorf("stage %d in state %s: %w",
				stageID, stage.State, cstmerrors.ErrInvalidTransition)
		}

		by := "user"
		ok, err := stagesTx.TransitionStageState(ctx, stageID,
			dtorep.StageStateRunning, dtorep.StageStateInterrupted,
			dtorep.StageTransitionFields{StopRequestedBy: &by})
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("stage %d: %w", stageID, cstmerrors.ErrConcurrentModification)
		}
		if err := runsmachine.AppendStateEvent(ctx, tx, u.appender, stage.RunID, &stageID,
			runsmachine.EventKindStageStateChanged,
			string(dtorep.StageStateRunning), string(dtorep.StageStateInterrupted)); err != nil {
			return err
		}

		_, err = notesTx.CreateNote(ctx, dtorep.CreateNoteRequest{
			RunID:          stage.RunID,
			StageID:        &stageID,
			Kind:           dtorep.NoteKindSteer,
			Text:           message,
			IdempotencyKey: uuid.New(),
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return u.stages.GetStageByID(ctx, stageID)
}

// CreateNote — queue note к ближайшему событию рана (D-22).
// Идемпотентно по ключу: повтор → существующая заметка.
func (u *Service) CreateNote(ctx context.Context, runID, text, idempotencyKey string) (*dtorep.Note, error) {
	if text == "" {
		return nil, fmt.Errorf("note text is empty: %w", cstmerrors.ErrValidation)
	}
	if idempotencyKey == "" {
		idempotencyKey = uuid.New()
	}

	if _, err := u.runs.GetRunByID(ctx, runID); err != nil {
		return nil, fmt.Errorf("failed to load run: %w", err)
	}

	noteID, err := u.notes.CreateNote(ctx, dtorep.CreateNoteRequest{
		RunID:          runID,
		Kind:           dtorep.NoteKindNote,
		Text:           text,
		IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, cstmerrors.ErrDuplicate) {
		return u.notes.GetNoteByIdempotencyKey(ctx, idempotencyKey)
	}
	if err != nil {
		return nil, err
	}

	notes, err := u.notes.ListNotesByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	for i := range notes {
		if notes[i].ID == noteID {
			return &notes[i], nil
		}
	}
	return nil, fmt.Errorf("note %d: %w", noteID, cstmerrors.ErrNotFound)
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify — slug для имени ветки из текста задачи (D-31).
func slugify(taskText string) string {
	s := strings.ToLower(strings.TrimSpace(taskText))
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	const maxLen = 40
	if len(s) > maxLen {
		s = strings.Trim(s[:maxLen], "-")
	}
	if s == "" {
		s = "run"
	}
	return s
}

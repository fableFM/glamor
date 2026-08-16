package runsmachine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/pkg/uuid"
)

// MaxResumeCount — лимит auto-resume одного этапа (D-14/16); при исчерпании —
// эскалация-гейт. Конфигурируемость — T-09 (supervisor).
const MaxResumeCount = 3

// TxExecutor выполняет функцию в транзакции (D-10). Реализации:
// repository.TxManager (по умолчанию) и events.Journal (T-04 — добавляет
// рассылку событий в Hub после коммита). ctx, переданный в fn, может быть
// обогащён исполнителем (collector отложенных событий) — используйте его.
type TxExecutor interface {
	WithTx(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error
}

// EventAppender пишет событие журнала внутри транзакции перехода (D-11).
// Реализация по умолчанию — репозиторий events; в T-04 подменяется на
// events.Journal с рассылкой в WS hub ПОСЛЕ коммита.
type EventAppender interface {
	Append(ctx context.Context, tx *sql.Tx, ev dtorep.Event) error
}

// Machine — стейт-машина ранов/стадий/гейтов. Состояния в памяти нет:
// все решения принимаются чтением БД (event-driven tick, ADR-001).
type Machine struct {
	txm       TxExecutor
	runs      runsrep.RepositoryWithTX
	stages    stagesrep.RepositoryWithTX
	gates     gatesrep.RepositoryWithTX
	pipelines pipelinesrep.RepositoryWithTX
	projects  projectsrep.RepositoryWithTX
	notes     notesrep.RepositoryWithTX
	appender  EventAppender
}

// NewMachine собирает стейт-машину из готовых зависимостей (ручной DI в
// main, D-80). txm — исполнитель транзакций: repository.TxManager (без
// рассылки) или events.Journal (T-04 — публикация событий в Hub после
// коммита). appender == nil → события пишутся напрямую в репозиторий.
func NewMachine(
	runs runsrep.RepositoryWithTX,
	stages stagesrep.RepositoryWithTX,
	gates gatesrep.RepositoryWithTX,
	pipelines pipelinesrep.RepositoryWithTX,
	projects projectsrep.RepositoryWithTX,
	notes notesrep.RepositoryWithTX,
	txm TxExecutor,
	appender EventAppender,
) *Machine {
	if appender == nil {
		appender = repoAppender{}
	}
	return &Machine{
		txm:       txm,
		runs:      runs,
		stages:    stages,
		gates:     gates,
		pipelines: pipelines,
		projects:  projects,
		notes:     notes,
		appender:  appender,
	}
}

// repoAppender — EventAppender поверх репозитория events (без рассылки).
type repoAppender struct{}

func (repoAppender) Append(ctx context.Context, tx *sql.Tx, ev dtorep.Event) error {
	_, err := eventsrep.NewTx(tx).AppendEvent(ctx, ev)
	return err
}

// --- события журнала -------------------------------------------------------

const (
	EventKindRunStateChanged   = "run.state_changed"
	EventKindStageStateChanged = "stage.state_changed"
	EventKindGateOpened        = "gate.opened"
	EventKindGateResolved      = "gate.resolved"
)

type stateChangedPayload struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// AppendStateEvent пишет событие смены состояния (payload {from,to}) в
// журнал внутри транзакции перехода (D-11). Экспортировано для
// API-сценариев service/runsapi, собирающих свои tx (StopRun и т.п.).
func AppendStateEvent(ctx context.Context, tx *sql.Tx, a EventAppender, runID string, stageID *int64, kind, from, to string) error {
	payload, err := json.Marshal(stateChangedPayload{From: from, To: to})
	if err != nil {
		return fmt.Errorf("failed to marshal event payload: %w", err)
	}
	return a.Append(ctx, tx, dtorep.Event{
		RunID:       runID,
		StageID:     stageID,
		Kind:        kind,
		PayloadJSON: string(payload),
	})
}

// --- переходы рана ---------------------------------------------------------

// TransitionRun переводит ран в состояние to (с валидацией по таблице
// ADR-001). Терминальным переходам выставляется finished_at.
func (m *Machine) TransitionRun(ctx context.Context, runID string, to dtorep.RunState) error {
	return m.txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return m.TransitionRunInTx(ctx, tx, runID, to)
	})
}

// TransitionRunInTx — переход рана внутри уже открытой транзакции
// (CAS + событие журнала, D-10/11). Экспортировано для API-сценариев
// service/runsapi, которым переход нужен в составе большей транзакции.
func (m *Machine) TransitionRunInTx(ctx context.Context, tx *sql.Tx, runID string, to dtorep.RunState) error {
	runsTx := runsrep.NewTx(tx)

	run, err := runsTx.GetRunByID(ctx, runID)
	if err != nil {
		return fmt.Errorf("failed to load run: %w", err)
	}
	if run.State == to {
		return nil // идемпотентный повтор
	}
	if err := validateRunTransition(run.State, to); err != nil {
		return fmt.Errorf("run %s: %w (%s -> %s)", runID, err, run.State, to)
	}

	var finishedAt *time.Time
	if isTerminalRunState(to) {
		now := time.Now().UTC()
		finishedAt = &now
	}

	ok, err := runsTx.TransitionRunState(ctx, runID, run.State, to, finishedAt)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("run %s: %w", runID, cstmerrors.ErrConcurrentModification)
	}

	return AppendStateEvent(ctx, tx, m.appender, runID, nil,
		EventKindRunStateChanged, string(run.State), string(to))
}

func isTerminalRunState(s dtorep.RunState) bool {
	switch s {
	case dtorep.RunStateSucceeded, dtorep.RunStateFailed, dtorep.RunStateStopped:
		return true
	}
	return false
}

// --- переходы стадии -------------------------------------------------------

// TransitionStage переводит стадию (по id строки) в состояние to с полями
// перехода (pid/session/exit_code/...). Валидация — по таблице ADR-001.
func (m *Machine) TransitionStage(ctx context.Context, stageID int64, to dtorep.StageState, fields dtorep.StageTransitionFields) error {
	return m.txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		stagesTx := stagesrep.NewTx(tx)

		stage, err := stagesTx.GetStageByID(ctx, stageID)
		if err != nil {
			return fmt.Errorf("failed to load stage: %w", err)
		}
		if stage.State == to {
			return nil // идемпотентный повтор
		}
		if err := validateStageTransition(stage.State, to); err != nil {
			return fmt.Errorf("stage %d (%s): %w (%s -> %s)",
				stageID, stage.StageKey, err, stage.State, to)
		}

		ok, err := stagesTx.TransitionStageState(ctx, stageID, stage.State, to, fields)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("stage %d: %w", stageID, cstmerrors.ErrConcurrentModification)
		}

		return AppendStateEvent(ctx, tx, m.appender, stage.RunID, &stageID,
			EventKindStageStateChanged, string(stage.State), string(to))
	})
}

// StartStage создаёт первую попытку этапа (iteration=1, pending).
// Для resume-семантики используется ResumeStage.
func (m *Machine) StartStage(ctx context.Context, runID, stageKey, harness string) (*dtorep.Stage, error) {
	var created *dtorep.Stage
	err := m.txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		stagesTx := stagesrep.NewTx(tx)

		// Идемпотентность: попытка с тем же (run, key, iteration=1) уже есть?
		existing, err := stagesTx.GetLatestStage(ctx, runID, stageKey)
		switch {
		case err == nil:
			created = existing
			return nil
		case errors.Is(err, cstmerrors.ErrNotFound):
		default:
			return fmt.Errorf("failed to check existing stage: %w", err)
		}

		id, err := stagesTx.CreateStage(ctx, dtorep.CreateStageRequest{
			RunID:     runID,
			StageKey:  stageKey,
			Iteration: 1,
			Harness:   harness,
		})
		if err != nil {
			return err
		}
		created, err = stagesTx.GetStageByID(ctx, id)
		if err != nil {
			return fmt.Errorf("failed to load created stage: %w", err)
		}

		return AppendStateEvent(ctx, tx, m.appender, runID, &id,
			EventKindStageStateChanged, "", string(dtorep.StageStatePending))
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// ResumeStage — auto-resume прерванного этапа (D-14/16, ADR-001):
// НОВАЯ строка (run_id, stage_key, iteration+1) с resume_count+1.
// Строка создаётся в pending; запуск (pending→running, pid, session_id) —
// supervisor (T-09).
func (m *Machine) ResumeStage(ctx context.Context, stageID int64) (*dtorep.Stage, error) {
	var created *dtorep.Stage
	err := m.txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		stagesTx := stagesrep.NewTx(tx)

		prev, err := stagesTx.GetStageByID(ctx, stageID)
		if err != nil {
			return fmt.Errorf("failed to load stage: %w", err)
		}
		if prev.State != dtorep.StageStateInterrupted {
			return fmt.Errorf("stage %d: %w (resume from %s)",
				stageID, cstmerrors.ErrInvalidTransition, prev.State)
		}
		// resume выполняется только от последней попытки цепочки
		latest, err := stagesTx.GetLatestStage(ctx, prev.RunID, prev.StageKey)
		if err != nil {
			return fmt.Errorf("failed to load latest stage: %w", err)
		}
		if latest.ID != prev.ID {
			return fmt.Errorf("stage %d: %w (resume of non-latest attempt, latest is %d)",
				stageID, cstmerrors.ErrInvalidTransition, latest.ID)
		}
		if prev.ResumeCount >= MaxResumeCount {
			return fmt.Errorf("stage %d: %w (resume limit %d reached)",
				stageID, cstmerrors.ErrInvalidTransition, MaxResumeCount)
		}

		id, err := stagesTx.CreateStage(ctx, dtorep.CreateStageRequest{
			RunID:       prev.RunID,
			StageKey:    prev.StageKey,
			Iteration:   prev.Iteration + 1,
			Harness:     prev.Harness,
			ResumeCount: prev.ResumeCount + 1,
		})
		if err != nil {
			return err
		}
		created, err = stagesTx.GetStageByID(ctx, id)
		if err != nil {
			return fmt.Errorf("failed to load created stage: %w", err)
		}

		return AppendStateEvent(ctx, tx, m.appender, prev.RunID, &id,
			EventKindStageStateChanged, string(dtorep.StageStateInterrupted),
			string(dtorep.StageStatePending))
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// RecoverInterrupted — startup recovery (D-15): все running-стадии —
// сироты прошлого процесса (reattach не делаем) → interrupted.
// Дальше тот же контур auto-resume (NextAction → ResumeStage).
// Дополнительно: у interrupted со stop_requested_by=daemon (graceful
// shutdown прошлого инстанса) флаг очищается — они ДОЛЖНЫ auto-resume'иться
// при подъёме (в отличие от stop_requested_by=user, D-14).
// Возвращает список прерванных стадий.
func (m *Machine) RecoverInterrupted(ctx context.Context) ([]dtorep.Stage, error) {
	running, err := m.stages.ListStagesByState(ctx, dtorep.StageStateRunning)
	if err != nil {
		return nil, fmt.Errorf("failed to list running stages: %w", err)
	}

	var recovered []dtorep.Stage
	for _, stage := range running {
		err := m.TransitionStage(ctx, stage.ID, dtorep.StageStateInterrupted, dtorep.StageTransitionFields{})
		switch {
		case err == nil:
			stage.State = dtorep.StageStateInterrupted
			recovered = append(recovered, stage)
		case errors.Is(err, cstmerrors.ErrConcurrentModification):
			// стадию уже перевёл другой актор — не сирота, пропускаем
		default:
			return nil, fmt.Errorf("failed to interrupt orphan stage %d: %w", stage.ID, err)
		}
	}

	// interrupted со stop_requested_by=daemon → очистить флаг для auto-resume
	interrupted, err := m.stages.ListStagesByState(ctx, dtorep.StageStateInterrupted)
	if err != nil {
		return nil, fmt.Errorf("failed to list interrupted stages: %w", err)
	}
	for _, stage := range interrupted {
		if stage.StopRequestedBy == nil || *stage.StopRequestedBy != "daemon" {
			continue
		}
		cleared := ""
		if err := m.txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			ok, err := stagesrep.NewTx(tx).TransitionStageState(ctx, stage.ID,
				dtorep.StageStateInterrupted, dtorep.StageStateInterrupted,
				dtorep.StageTransitionFields{StopRequestedBy: &cleared})
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("stage %d: %w", stage.ID, cstmerrors.ErrConcurrentModification)
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("failed to clear daemon stop flag for stage %d: %w", stage.ID, err)
		}
	}
	return recovered, nil
}

// --- гейты -----------------------------------------------------------------

// OpenGateRequest — параметры открытия гейта.
type OpenGateRequest struct {
	RunID          string
	StageID        *int64
	Kind           dtorep.GateKind
	Question       string
	ContextJSON    string
	IdempotencyKey string
}

// OpenGate создаёт гейт и переводит ран running→waiting_gate — атомарно,
// с событием gate.opened (D-21, ADR-001). На один (run_id, stage_id)
// допускается только один открытый гейт.
func (m *Machine) OpenGate(ctx context.Context, req OpenGateRequest) (*dtorep.Gate, error) {
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = uuid.New()
	}

	var created *dtorep.Gate
	err := m.txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		gatesTx := gatesrep.NewTx(tx)
		runsTx := runsrep.NewTx(tx)

		// Идемпотентность (D-12): гейт с таким ключом уже создан?
		existing, err := gatesTx.GetGateByIdempotencyKey(ctx, req.IdempotencyKey)
		switch {
		case err == nil:
			created = existing
			return nil
		case errors.Is(err, cstmerrors.ErrNotFound):
		default:
			return fmt.Errorf("failed to check gate idempotency key: %w", err)
		}

		// Инвариант: не более одного открытого гейта на (run_id, stage_id).
		open, err := gatesTx.ListOpenGates(ctx, req.RunID)
		if err != nil {
			return err
		}
		for _, g := range open {
			if sameStageID(g.StageID, req.StageID) {
				return fmt.Errorf("run %s stage %v: %w (open gate %s already exists)",
					req.RunID, req.StageID, cstmerrors.ErrDuplicate, g.ID)
			}
		}

		run, err := runsTx.GetRunByID(ctx, req.RunID)
		if err != nil {
			return fmt.Errorf("failed to load run: %w", err)
		}
		switch run.State {
		case dtorep.RunStateRunning:
			ok, err := runsTx.TransitionRunState(ctx, req.RunID, dtorep.RunStateRunning, dtorep.RunStateWaitingGate, nil)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("run %s: %w", req.RunID, cstmerrors.ErrConcurrentModification)
			}
			if err := AppendStateEvent(ctx, tx, m.appender, req.RunID, nil,
				EventKindRunStateChanged, string(dtorep.RunStateRunning), string(dtorep.RunStateWaitingGate)); err != nil {
				return err
			}
		case dtorep.RunStateWaitingGate:
			// уже ждём другой гейт — ран не трогаем
		default:
			return fmt.Errorf("run %s: %w (open gate in state %s)",
				req.RunID, cstmerrors.ErrInvalidTransition, run.State)
		}

		gateID := uuid.New()
		if err := gatesTx.CreateGate(ctx, dtorep.CreateGateRequest{
			ID:             gateID,
			RunID:          req.RunID,
			StageID:        req.StageID,
			Kind:           req.Kind,
			Question:       req.Question,
			ContextJSON:    req.ContextJSON,
			IdempotencyKey: req.IdempotencyKey,
		}); err != nil {
			return err
		}
		created, err = gatesTx.GetGateByID(ctx, gateID)
		if err != nil {
			return fmt.Errorf("failed to load created gate: %w", err)
		}

		payload, err := json.Marshal(created)
		if err != nil {
			return fmt.Errorf("failed to marshal gate payload: %w", err)
		}
		return m.appender.Append(ctx, tx, dtorep.Event{
			RunID:       req.RunID,
			StageID:     req.StageID,
			Kind:        EventKindGateOpened,
			PayloadJSON: string(payload),
		})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// ResolveGate резолвит гейт (approved/rejected/answered/expired) и, если
// открытых гейтов не осталось, возвращает ран waiting_gate→running — всё
// в одной транзакции с событиями (ADR-001). reject гейта → ран failed
// (через waiting_gate→running→failed, два события — аудит).
func (m *Machine) ResolveGate(ctx context.Context, gateID string, resolution dtorep.GateState, answer *string) error {
	return m.txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		gatesTx := gatesrep.NewTx(tx)
		runsTx := runsrep.NewTx(tx)

		gate, err := gatesTx.GetGateByID(ctx, gateID)
		if err != nil {
			return fmt.Errorf("failed to load gate: %w", err)
		}
		if gate.State == resolution {
			return nil // идемпотентный повтор
		}
		if err := validateGateTransition(gate.State, resolution); err != nil {
			return fmt.Errorf("gate %s: %w (%s -> %s)", gateID, err, gate.State, resolution)
		}

		now := time.Now().UTC()
		ok, err := gatesTx.ResolveGate(ctx, gateID, resolution, answer, now)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("gate %s: %w", gateID, cstmerrors.ErrConcurrentModification)
		}

		payload, err := json.Marshal(map[string]any{
			"gate_id":    gateID,
			"resolution": resolution,
			"answer":     answer,
		})
		if err != nil {
			return fmt.Errorf("failed to marshal gate payload: %w", err)
		}
		if err := m.appender.Append(ctx, tx, dtorep.Event{
			RunID:       gate.RunID,
			StageID:     gate.StageID,
			Kind:        EventKindGateResolved,
			PayloadJSON: string(payload),
		}); err != nil {
			return err
		}

		// Остались ли открытые гейты?
		open, err := gatesTx.ListOpenGates(ctx, gate.RunID)
		if err != nil {
			return err
		}
		if len(open) > 0 {
			return nil // ран остаётся в waiting_gate
		}

		run, err := runsTx.GetRunByID(ctx, gate.RunID)
		if err != nil {
			return fmt.Errorf("failed to load run: %w", err)
		}
		if run.State != dtorep.RunStateWaitingGate {
			return nil // ран уже двигается (например stopped пользователем)
		}

		if err := m.TransitionRunInTx(ctx, tx, gate.RunID, dtorep.RunStateRunning); err != nil {
			return err
		}
		if resolution == dtorep.GateStateRejected {
			// reject гейта проваливает ран (ADR-001)
			return m.TransitionRunInTx(ctx, tx, gate.RunID, dtorep.RunStateFailed)
		}
		return nil
	})
}

func sameStageID(a, b *int64) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// UpdateRunningStage обновляет поля running-стадии без смены состояния
// (session_id/pid после спавна процесса — supervisor, T-09). Это НЕ
// переход состояния: событие не порождается, CAS по state=running.
func (m *Machine) UpdateRunningStage(ctx context.Context, stageID int64, fields dtorep.StageTransitionFields) error {
	return m.txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		stagesTx := stagesrep.NewTx(tx)
		stage, err := stagesTx.GetStageByID(ctx, stageID)
		if err != nil {
			return fmt.Errorf("failed to load stage: %w", err)
		}
		if stage.State != dtorep.StageStateRunning {
			return fmt.Errorf("stage %d in state %s: %w",
				stageID, stage.State, cstmerrors.ErrInvalidTransition)
		}
		ok, err := stagesTx.TransitionStageState(ctx, stageID,
			dtorep.StageStateRunning, dtorep.StageStateRunning, fields)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("stage %d: %w", stageID, cstmerrors.ErrConcurrentModification)
		}
		return nil
	})
}

// ReenterStage — диалоговый ре-вход этапа (D-20/22/23): новая попытка
// (iteration+1) БЕЗ увеличения resume_count — это не auto-resume, а
// продолжение диалога (ответ на вопрос, комментарий, steer). Допустимые
// исходные состояния: succeeded (вопросы-ответы), interrupted (steer),
// failed (ручной ре-вход). message != "" → steer-заметка (уйдёт в промпт
// новой попытки через consumption в supervisor).
func (m *Machine) ReenterStage(ctx context.Context, stageID int64, message string) (*dtorep.Stage, error) {
	var created *dtorep.Stage
	err := m.txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		stagesTx := stagesrep.NewTx(tx)

		prev, err := stagesTx.GetStageByID(ctx, stageID)
		if err != nil {
			return fmt.Errorf("failed to load stage: %w", err)
		}
		switch prev.State {
		case dtorep.StageStateSucceeded, dtorep.StageStateInterrupted, dtorep.StageStateFailed:
		default:
			return fmt.Errorf("stage %d in state %s: %w",
				stageID, prev.State, cstmerrors.ErrInvalidTransition)
		}
		// ре-вход только от последней попытки цепочки
		latest, err := stagesTx.GetLatestStage(ctx, prev.RunID, prev.StageKey)
		if err != nil {
			return fmt.Errorf("failed to load latest stage: %w", err)
		}
		if latest.ID != prev.ID {
			return fmt.Errorf("stage %d: %w (re-enter of non-latest attempt, latest is %d)",
				stageID, cstmerrors.ErrInvalidTransition, latest.ID)
		}

		id, err := stagesTx.CreateStage(ctx, dtorep.CreateStageRequest{
			RunID:       prev.RunID,
			StageKey:    prev.StageKey,
			Iteration:   prev.Iteration + 1,
			Harness:     prev.Harness,
			ResumeCount: prev.ResumeCount, // НЕ инкрементируем: это диалог
		})
		if err != nil {
			return err
		}

		if message != "" {
			notesTx := notesrep.NewTx(tx)
			if _, err := notesTx.CreateNote(ctx, dtorep.CreateNoteRequest{
				RunID:          prev.RunID,
				StageID:        &id,
				Kind:           dtorep.NoteKindSteer,
				Text:           message,
				IdempotencyKey: uuid.New(),
			}); err != nil {
				return err
			}
		}

		created, err = stagesTx.GetStageByID(ctx, id)
		if err != nil {
			return fmt.Errorf("failed to load created stage: %w", err)
		}

		if err := AppendStateEvent(ctx, tx, m.appender, prev.RunID, &id,
			EventKindStageStateChanged, string(prev.State), string(dtorep.StageStatePending)); err != nil {
			return err
		}
		return AppendStateEvent(ctx, tx, m.appender, prev.RunID, &id,
			"stage.resumed", string(prev.State), string(dtorep.StageStatePending))
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

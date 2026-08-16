package runsmachine

import (
	"context"
	"errors"
	"fmt"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
)

// ActionKind — что ядру делать дальше с раном (event-driven tick, ADR-001).
type ActionKind string

const (
	ActionNone        ActionKind = "none"         // терминальный ран
	ActionStartRun    ActionKind = "start_run"    // draft → running
	ActionStartStage  ActionKind = "start_stage"  // запустить/создать попытку этапа
	ActionWaitStage   ActionKind = "wait_stage"   // есть running-стадия — ждём событий
	ActionResumeStage ActionKind = "resume_stage" // auto-resume прерванного этапа (D-16)
	ActionEscalate    ActionKind = "escalate"     // auto-resume исчерпан → эскалация-гейт
	ActionWaitGate    ActionKind = "wait_gate"    // ждём резолва гейта
	ActionFinishRun   ActionKind = "finish_run"   // перевести ран в терминал
)

// Action — решение движка о следующем шаге рана.
type Action struct {
	Kind     ActionKind
	Stage    *dtorep.Stage   // для start/wait/resume/escalate — релевантная стадия
	StageKey string          // для start_stage нового этапа (строки ещё нет)
	Harness  string          // harness нового этапа из спеки
	RunState dtorep.RunState // для finish_run — целевое терминальное состояние
}

// NextAction — чистая функция над состоянием БД: перечитывает
// runs/run_stages/gates и возвращает следующий шаг (ADR-001).
// Loop-рёбра (fix-петля code→review→fix) вычисляются supervisor'ом (T-09)
// на основе verdict-артефактов; машина даёт линейное продвижение по спеке.
func (m *Machine) NextAction(ctx context.Context, runID string) (Action, error) {
	run, err := m.runs.GetRunByID(ctx, runID)
	if err != nil {
		return Action{}, fmt.Errorf("failed to load run: %w", err)
	}

	switch run.State {
	case dtorep.RunStateSucceeded, dtorep.RunStateFailed, dtorep.RunStateStopped:
		return Action{Kind: ActionNone}, nil
	case dtorep.RunStateDraft:
		return Action{Kind: ActionStartRun}, nil
	case dtorep.RunStateWaitingGate:
		return Action{Kind: ActionWaitGate}, nil
	case dtorep.RunStateRunning:
	default:
		return Action{}, fmt.Errorf("run %s: unknown state %q: %w",
			runID, run.State, cstmerrors.ErrInvalidTransition)
	}

	pipeline, err := m.pipelines.GetPipelineByID(ctx, run.PipelineVersionID)
	if err != nil {
		return Action{}, fmt.Errorf("failed to load pipeline: %w", err)
	}
	spec, err := ParseSpec(pipeline.SpecJSON)
	if err != nil {
		return Action{}, err
	}

	for _, stageSpec := range spec.Stages {
		latest, err := m.stages.GetLatestStage(ctx, runID, stageSpec.Key)
		if errors.Is(err, cstmerrors.ErrNotFound) {
			return Action{
				Kind:     ActionStartStage,
				StageKey: stageSpec.Key,
				Harness:  stageSpec.Harness,
			}, nil
		}
		if err != nil {
			return Action{}, fmt.Errorf("failed to load stage %q: %w", stageSpec.Key, err)
		}

		switch latest.State {
		case dtorep.StageStatePending:
			return Action{Kind: ActionStartStage, Stage: latest, StageKey: latest.StageKey, Harness: latest.Harness}, nil
		case dtorep.StageStateRunning:
			return Action{Kind: ActionWaitStage, Stage: latest}, nil
		case dtorep.StageStateInterrupted:
			// D-14: stop_requested_by записан → auto-resume НЕ срабатывает.
			// user-stop: ран останавливается; steer: контур processSteers
			// (supervisor) подхватит; daemon: recovery при подъёме.
			if latest.StopRequestedBy != nil && *latest.StopRequestedBy != "" {
				return Action{Kind: ActionWaitStage, Stage: latest}, nil
			}
			if latest.ResumeCount >= MaxResumeCount {
				return Action{Kind: ActionEscalate, Stage: latest}, nil
			}
			return Action{Kind: ActionResumeStage, Stage: latest}, nil
		case dtorep.StageStateFailed:
			return Action{Kind: ActionFinishRun, RunState: dtorep.RunStateFailed, Stage: latest}, nil
		case dtorep.StageStateSucceeded, dtorep.StageStateSkipped:
			continue
		default:
			return Action{}, fmt.Errorf("stage %d: unknown state %q: %w",
				latest.ID, latest.State, cstmerrors.ErrInvalidTransition)
		}
	}

	return Action{Kind: ActionFinishRun, RunState: dtorep.RunStateSucceeded}, nil
}

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
	Kind      ActionKind
	Stage     *dtorep.Stage   // для start/wait/resume/escalate — релевантная стадия
	StageKey  string          // для start_stage нового этапа (строки ещё нет)
	Harness   string          // harness нового этапа из спеки
	RunState  dtorep.RunState // для finish_run — целевое терминальное состояние
	FinalGate string          // из спеки: гейт перед succeeded (T-17), "" = нет
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

	for i := 0; i < len(spec.Stages); i++ {
		stageSpec := spec.Stages[i]

		// fan-out (T-28, ADR-003): параллельная группа оценивается целиком
		if stageSpec.ParallelGroup != "" {
			groupKeys := []string{}
			for j := i; j < len(spec.Stages); j++ {
				if spec.Stages[j].ParallelGroup == stageSpec.ParallelGroup {
					groupKeys = append(groupKeys, spec.Stages[j].Key)
					i = j // пропускаем членов группы в основном цикле
				}
			}
			action, done, err := m.parallelGroupAction(ctx, runID, spec, stageSpec.ParallelGroup, groupKeys)
			if err != nil {
				return Action{}, err
			}
			if done {
				continue // вся группа завершена — дальше по спеке
			}
			return action, nil
		}

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

	return Action{Kind: ActionFinishRun, RunState: dtorep.RunStateSucceeded, FinalGate: spec.FinalGate}, nil
}

// parallelGroupAction — решение по параллельной группе веток (T-28):
// незапущенные стартуют (по одной на тик — пул supervisor'а параллелит),
// join — когда все succeeded/skipped; падение — по политике on_failure.
func (m *Machine) parallelGroupAction(ctx context.Context, runID string, spec Spec, groupName string, keys []string) (Action, bool, error) {
	onFailure := "fail_fast"
	for _, g := range spec.ParallelGroups {
		if g.Name == groupName && g.OnFailure != "" {
			onFailure = g.OnFailure
		}
	}

	var pending, running, resume *dtorep.Stage
	var failed *dtorep.Stage
	for _, key := range keys {
		latest, err := m.stages.GetLatestStage(ctx, runID, key)
		if errors.Is(err, cstmerrors.ErrNotFound) {
			// незапущенная ветка — стартуем (создание строки на supervisor)
			return Action{Kind: ActionStartStage, StageKey: key, Harness: stageHarnessOf(spec, key)}, false, nil
		}
		if err != nil {
			return Action{}, false, fmt.Errorf("failed to load stage %q: %w", key, err)
		}

		switch latest.State {
		case dtorep.StageStatePending:
			if pending == nil {
				pending = latest
			}
		case dtorep.StageStateRunning:
			if running == nil {
				running = latest
			}
		case dtorep.StageStateInterrupted:
			if latest.StopRequestedBy == nil || *latest.StopRequestedBy == "" {
				if resume == nil {
					resume = latest
				}
			}
		case dtorep.StageStateFailed:
			if failed == nil {
				failed = latest
			}
		case dtorep.StageStateSucceeded, dtorep.StageStateSkipped:
		}
	}

	switch {
	case failed != nil && onFailure == "fail_fast":
		return Action{Kind: ActionFinishRun, RunState: dtorep.RunStateFailed, Stage: failed}, false, nil
	case failed != nil: // wait_all: ждём остальные ветки
		if pending != nil {
			return Action{Kind: ActionStartStage, Stage: pending, StageKey: pending.StageKey, Harness: pending.Harness}, false, nil
		}
		if running != nil || resume != nil {
			if resume != nil {
				return Action{Kind: ActionResumeStage, Stage: resume}, false, nil
			}
			return Action{Kind: ActionWaitStage, Stage: running}, false, nil
		}
		return Action{Kind: ActionFinishRun, RunState: dtorep.RunStateFailed, Stage: failed}, false, nil
	case pending != nil:
		return Action{Kind: ActionStartStage, Stage: pending, StageKey: pending.StageKey, Harness: pending.Harness}, false, nil
	case resume != nil:
		return Action{Kind: ActionResumeStage, Stage: resume}, false, nil
	case running != nil:
		return Action{Kind: ActionWaitStage, Stage: running}, false, nil
	default:
		return Action{}, true, nil // вся группа succeeded/skipped — join
	}
}

func stageHarnessOf(spec Spec, key string) string {
	for _, st := range spec.Stages {
		if st.Key == key {
			return st.Harness
		}
	}
	return ""
}

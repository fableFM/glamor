package pipeline

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/service/runsmachine"
)

// errValidation — ошибка валидации пайплайна (маппится в 400 validation).
var errValidation = errors.New("pipeline validation failed")

// ValidateSpec — блокирующая валидация спеки (T-20/T-21): структура
// (ParseSpec: этапы, уникальные ключи), ссылки loop/гейтов, плейсхолдеры.
func ValidateSpec(specJSON string) error {
	spec, err := runsmachine.ParseSpec(specJSON)
	if err != nil {
		return fmt.Errorf("%w: %v", errValidation, err)
	}

	keys := map[string]bool{}
	for _, st := range spec.Stages {
		keys[st.Key] = true
	}

	// review_policy — только известные значения (D-23)
	switch spec.ReviewPolicy {
	case "", "blocking", "major", "all":
	default:
		return fmt.Errorf("%w: unknown review_policy %q (want blocking|major|all)", errValidation, spec.ReviewPolicy)
	}

	// lessons — гарантия формирования уроков (T-30): on|off (пусто = on)
	switch spec.Lessons {
	case "", "on", "off":
	default:
		return fmt.Errorf("%w: unknown lessons %q (want on|off)", errValidation, spec.Lessons)
	}

	// loop-ребро ссылается на существующие этапы
	if spec.Loop != nil {
		if !keys[spec.Loop.From] {
			return fmt.Errorf("%w: loop.from %q: no such stage", errValidation, spec.Loop.From)
		}
		if !keys[spec.Loop.To] {
			return fmt.Errorf("%w: loop.to %q: no such stage", errValidation, spec.Loop.To)
		}
		if spec.Loop.MaxIters < 1 {
			return fmt.Errorf("%w: loop.max_iters must be >= 1", errValidation)
		}
	}

	// гейты — только известные виды
	if spec.FinalGate != "" && !knownGateKind(spec.FinalGate) {
		return fmt.Errorf("%w: final_gate %q: unknown gate kind", errValidation, spec.FinalGate)
	}

	// параллельные группы (T-28, ADR-003): члены существуют, read_only,
	// группа объявлена, on_failure валиден
	declaredGroups := map[string]bool{}
	for _, g := range spec.ParallelGroups {
		if g.Name == "" {
			return fmt.Errorf("%w: parallel group with empty name", errValidation)
		}
		if g.OnFailure != "" && g.OnFailure != "fail_fast" && g.OnFailure != "wait_all" {
			return fmt.Errorf("%w: parallel group %q: unknown on_failure %q", errValidation, g.Name, g.OnFailure)
		}
		declaredGroups[g.Name] = true
	}
	for _, st := range spec.Stages {
		if st.ParallelGroup != "" {
			if !declaredGroups[st.ParallelGroup] {
				return fmt.Errorf("%w: stage %q: undeclared parallel_group %q",
					errValidation, st.Key, st.ParallelGroup)
			}
			if !st.ReadOnly {
				return fmt.Errorf("%w: stage %q: parallel stages must be read_only (ADR-003)",
					errValidation, st.Key)
			}
		}
	}

	for _, st := range spec.Stages {
		if st.GateAfter != "" && !knownGateKind(st.GateAfter) {
			return fmt.Errorf("%w: stage %q gate_after %q: unknown gate kind",
				errValidation, st.Key, st.GateAfter)
		}
		if st.Artifact != nil && st.Artifact.Required && st.Artifact.Path == "" {
			return fmt.Errorf("%w: stage %q: required artifact with empty path", errValidation, st.Key)
		}
		if err := validatePlaceholders(st.PromptTemplate); err != nil {
			return fmt.Errorf("%w: stage %q: %v", errValidation, st.Key, err)
		}
		if err := validateLLMStageRequired(st); err != nil {
			return fmt.Errorf("%w: %v", errValidation, err)
		}
	}
	return nil
}

// validateLLMStageRequired — обязательные поля llm-этапа (fix-task-3 п.5,
// выравнивание с фронтом web/src/lib/pipelineModel.ts): harness, model,
// prompt_template непустые. В M1 этап без kind — тоже llm-stage
// (kind omitempty, других видов пока нет).
func validateLLMStageRequired(st runsmachine.StageSpec) error {
	if st.Kind != "" && st.Kind != "llm-stage" {
		return nil
	}
	if strings.TrimSpace(st.Harness) == "" {
		return fmt.Errorf("stage %q: empty harness", st.Key)
	}
	if strings.TrimSpace(st.Model) == "" {
		return fmt.Errorf("stage %q: empty model", st.Key)
	}
	if strings.TrimSpace(st.PromptTemplate) == "" {
		return fmt.Errorf("stage %q: empty prompt_template", st.Key)
	}
	return nil
}

func knownGateKind(kind string) bool {
	switch dtorep.GateKind(kind) {
	case dtorep.GateKindPlanApproval, dtorep.GateKindQuestion,
		dtorep.GateKindEscalation, dtorep.GateKindFinalReview, dtorep.GateKindLessonReview:
		return true
	}
	return false
}

var knownPlaceholders = map[string]bool{
	"task": true, "base_branch": true, "branch": true, "run_dir": true,
	"depth": true, "depth_instructions": true, "queue_notes": true,
	"vendor_memory_paths": true, "vendor_memory": true, "verdict": true, "iteration": true,
	"lessons": true, "gate_answers": true, "rejected_lessons": true,
	"behavior_trace": true, "existing_lessons": true, "vendor_lessons": true,
	"max_iterations": true,
}

// placeholderRe — ЕДИНЫЙ источник regex'а плейсхолдеров (F-07): ловит любой
// {{...}}, включая верхний регистр и мусор. И валидация, и рендер требуют
// точного совпадения имени со списком известных (knownPlaceholders /
// artifact.<path>) — иначе ошибка, а не молчаливый литерал в промпте.
var placeholderRe = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

// validatePlaceholders — плейсхолдеры только из известного набора (T-17/T-20).
func validatePlaceholders(template string) error {
	for _, match := range placeholderRe.FindAllStringSubmatch(template, -1) {
		name := match[1]
		if knownPlaceholders[name] {
			continue
		}
		if strings.HasPrefix(name, "artifact.") && len(name) > len("artifact.") {
			continue
		}
		return fmt.Errorf("unknown placeholder {{%s}}", name)
	}
	return nil
}

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/service/runsmachine"
)

// Verdict — машиночитаемый вердикт ревьюера (verdict.json, T-17).
type Verdict struct {
	Verdict  string `json:"verdict"` // approved | changes_required
	Findings []struct {
		ID           string `json:"id"`
		Severity     string `json:"severity"`
		File         string `json:"file"`
		Observed     string `json:"observed"`
		Expected     string `json:"expected"`
		RequiredFix  string `json:"required_fix"`
		ForbiddenFix string `json:"forbidden_fix"`
	} `json:"findings"`
}

// NeedsFix — требуется ли fix-итерация по политике пайплайна
// (review_policy: blocking|major|all; дефолт major). Verdict
// changes_required без подходящих findings трактуем как approved
// (страховка от галлюцинации verdict без findings).
func (v Verdict) NeedsFix(policy string) bool {
	threshold := policy
	if threshold == "" {
		threshold = "major"
	}
	for _, f := range v.Findings {
		switch threshold {
		case "all":
			return true
		case "major":
			if f.Severity == "blocking" || f.Severity == "major" {
				return true
			}
		default: // blocking
			if f.Severity == "blocking" {
				return true
			}
		}
	}
	return false
}

// BlockingFindings — краткая выжимка нерешённых findings для эскалации.
func (v Verdict) BlockingFindings() []string {
	var out []string
	for _, f := range v.Findings {
		if f.Severity == "blocking" || f.Severity == "major" {
			out = append(out, fmt.Sprintf("%s [%s] %s: %s", f.ID, f.Severity, f.File, f.RequiredFix))
		}
	}
	return out
}

// LoopHook — fix-петля (D-23, T-17): после reviewer читает verdict.json:
// approved → fixer skipped; changes_required → ре-вход fixer (итерации <
// max) или эскалация-гейт; после fixer → ре-вход reviewer.
type LoopHook struct {
	machine *runsmachine.Machine
	runsDir string
}

// NewLoopHook собирает хук из готовых зависимостей (ручной DI, D-80).
func NewLoopHook(machine *runsmachine.Machine, runsDir string) *LoopHook {
	return &LoopHook{machine: machine, runsDir: runsDir}
}

// OnStageSucceeded реализует supervisor.OnStageSucceeded.
func (h *LoopHook) OnStageSucceeded(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage, spec runsmachine.Spec) error {
	loop := spec.Loop
	if loop == nil {
		return nil
	}

	switch stage.StageKey {
	case loop.To:
		// fixer отработал → снова ревью (D-23)
		_, err := h.machine.ReenterStageByKey(ctx, run.ID, loop.From, "")
		return err

	case loop.From:
		verdict, err := h.readVerdict(run.ID)
		if err != nil {
			return err
		}
		if !verdict.NeedsFix(spec.ReviewPolicy) {
			// петля не нужна — fixer пропускаем (условный этап)
			return h.machine.SkipStage(ctx, run.ID, loop.To)
		}

		// changes_required: итерации fixer исчерпаны?
		fixerAttempts, err := h.countAttempts(ctx, run.ID, loop.To)
		if err != nil {
			return err
		}
		if fixerAttempts >= loop.MaxIters {
			return h.escalate(ctx, run, stage, loop, verdict)
		}

		// ре-вход fixer (verdict попадёт в промпт через {{verdict}})
		_, err = h.machine.ReenterStageByKey(ctx, run.ID, loop.To, "")
		return err
	}
	return nil
}

// escalate — исчерпание петли → эскалация-гейт с findings (D-23).
func (h *LoopHook) escalate(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage, loop *runsmachine.LoopSpec, verdict Verdict) error {
	findings := verdict.BlockingFindings()
	contextJSON, err := json.Marshal(map[string]any{
		"stage_key": stage.StageKey,
		"max_iters": loop.MaxIters,
		"findings":  findings,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal escalation context: %w", err)
	}

	question := fmt.Sprintf(
		"Петля %s→%s: %d итераций, verdict всё ещё changes_required. Нерешённые findings:\n- %s\nЧто делать?",
		loop.From, loop.To, loop.MaxIters, strings.Join(findings, "\n- "))

	// гейт ссылается на последнюю попытку fixer: ответ пользователя
	// резюмит fixer с его подсказкой (T-11, ResolveGateAPI)
	var gateStageID *int64
	if fixer, err := h.machine.LatestStage(ctx, run.ID, loop.To); err == nil {
		gateStageID = &fixer.ID
	} else {
		gateStageID = &stage.ID
	}

	_, err = h.machine.OpenGate(ctx, runsmachine.OpenGateRequest{
		RunID:       run.ID,
		StageID:     gateStageID,
		Kind:        dtorep.GateKindEscalation,
		Question:    question,
		ContextJSON: string(contextJSON),
	})
	return err
}

// readVerdict читает и валидирует verdict.json рана (D-13).
func (h *LoopHook) readVerdict(runID string) (Verdict, error) {
	path := filepath.Join(h.runsDir, runID, "verdict.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Verdict{}, fmt.Errorf("failed to read verdict %s: %w", path, err)
	}
	var verdict Verdict
	if err := json.Unmarshal(data, &verdict); err != nil {
		return Verdict{}, fmt.Errorf("failed to parse verdict %s: %w", path, err)
	}
	if verdict.Verdict != "approved" && verdict.Verdict != "changes_required" {
		return Verdict{}, fmt.Errorf("verdict %s: unknown verdict %q", path, verdict.Verdict)
	}
	return verdict, nil
}

// countAttempts — число попыток этапа (итераций петли).
func (h *LoopHook) countAttempts(ctx context.Context, runID, stageKey string) (int64, error) {
	latest, err := h.machine.LatestStage(ctx, runID, stageKey)
	if err != nil {
		if errors.Is(err, cstmerrors.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return latest.Iteration, nil
}

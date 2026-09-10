// Package pipeline — дефолтный пайплайн M1 (T-17): planner → coder →
// reviewer → (loop: fixer → reviewer, max_iters=4) → final gate.
// Роли-промпты портированы из ai-pipeline (terminal/worker_done →
// артефакт-контракт, запрет коммитов D-32), шаблонизация {{placeholder}},
// verdict-driven fix-петля, seed глобального пайплайна.
package pipeline

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	"github.com/fableFM/glamor/internal/service/runsmachine"
)

//go:embed prompts
var promptsFS embed.FS

//go:embed default.yaml
var defaultYAML string

// Имена файлов промптов (embed).
const (
	promptPlanner  = "prompts/planner.md"
	promptCoder    = "prompts/coder.md"
	promptReviewer = "prompts/reviewer.md"
	promptFixer    = "prompts/fixer.md"
	promptDistill  = "prompts/distill.md"
)

// DepthName маппит depth рана (0/1/2) на имя пресета.
func DepthName(depth int64) string {
	switch depth {
	case 0:
		return "quick"
	case 2:
		return "deep"
	default:
		return "standard"
	}
}

// Triplets — триплеты harness/model/effort этапов по умолчанию
// (из ai-pipeline config.example.sh; переопределяются конфигом демона).
type Triplets struct {
	Harness string
	Model   string
	Effort  string
}

// Overrides — переопределения триплетов из конфига демона (T-17).
type Overrides struct {
	Planner  Triplets
	Coder    Triplets
	Reviewer Triplets
	Fixer    Triplets
}

// DefaultOverrides — триплеты из ai-pipeline: всё kimi, effort high, fixer low.
func DefaultOverrides() Overrides {
	def := Triplets{Harness: "kimi", Model: "kimi-code/k3", Effort: "high"}
	return Overrides{
		Planner:  def,
		Coder:    def,
		Reviewer: def,
		Fixer:    Triplets{Harness: "kimi", Model: "kimi-code/k3", Effort: "low"},
	}
}

// DefaultSpec собирает spec_json дефолтного пайплайна M1 с inline
// промпт-шаблонами (версионируются вместе с пайплайном).
func DefaultSpec(ov Overrides) (string, error) {
	read := func(path string) (string, error) {
		data, err := promptsFS.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("failed to read embedded prompt %s: %w", path, err)
		}
		return string(data), nil
	}

	templates := map[string]string{}
	for _, path := range []string{promptPlanner, promptCoder, promptReviewer, promptFixer, promptDistill} {
		tpl, err := read(path)
		if err != nil {
			return "", err
		}
		templates[path] = tpl
	}

	stage := func(key string, t Triplets, tpl, artifact string, extra map[string]any) map[string]any {
		st := map[string]any{
			"key":             key,
			"kind":            "llm-stage",
			"harness":         t.Harness,
			"model":           t.Model,
			"effort":          t.Effort,
			"prompt_template": tpl,
			"artifact":        map[string]any{"path": artifact, "required": true},
		}
		for k, v := range extra {
			st[k] = v
		}
		return st
	}

	spec := map[string]any{
		"stages": []map[string]any{
			stage("planner", ov.Planner, templates[promptPlanner], "{run_dir}/spec.md", map[string]any{
				"questions_path": "{run_dir}/questions.md",
				"gate_after":     "plan_approval",
			}),
			stage("coder", ov.Coder, templates[promptCoder], "{run_dir}/handoff.md", nil),
			stage("reviewer", ov.Reviewer, templates[promptReviewer], "{run_dir}/verdict.json", nil),
			stage("fixer", ov.Fixer, templates[promptFixer], "{run_dir}/handoff.md", nil),
			// distill-этап (T-29, D-52): извлечение уроков после fix-петли,
			// до финального гейта; gate_after=lesson_review
			stage("distill", ov.Fixer, templates[promptDistill], "{run_dir}/lessons.md", map[string]any{
				"gate_after": "lesson_review",
			}),
		},
		"loop":       map[string]any{"from": "reviewer", "to": "fixer", "max_iters": 4},
		"final_gate": "final_review",
		"lessons":    "on", // гарантия формирования уроков (T-30, D-81)
	}

	data, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("failed to marshal default pipeline spec: %w", err)
	}
	return string(data), nil
}

// Service — сценарии пайплайнов (seed, CRUD версий, YAML import/export).
// Конструируется в main из готового репозитория (ручной DI, D-80).
type Service struct {
	pipelines pipelinesrep.RepositoryWithTX
}

func NewService(pipelines pipelinesrep.RepositoryWithTX) *Service {
	return &Service{pipelines: pipelines}
}

// Seed вставляет глобальный пайплайн «default» v1 из default.yaml
// (идемпотентно, вызывается при старте демона после миграций, T-21).
func (s *Service) Seed(ctx context.Context) error {
	pipelines := s.pipelines
	_, err := pipelines.GetLatestPipeline(ctx, nil, "default")
	switch {
	case err == nil:
		return nil // уже засеян
	case errors.Is(err, cstmerrors.ErrNotFound):
	default:
		return fmt.Errorf("failed to check default pipeline: %w", err)
	}

	name, specJSON, err := yamlToSpecJSON(defaultYAML)
	if err != nil {
		return fmt.Errorf("failed to parse embedded default.yaml: %w", err)
	}
	if _, err := pipelines.CreatePipeline(ctx, dtorep.CreatePipelineRequest{
		Name:     name,
		Version:  1,
		SpecJSON: specJSON,
	}); err != nil {
		return fmt.Errorf("failed to seed default pipeline: %w", err)
	}
	return nil
}

// CreatePipeline — создание пайплайна (первая версия) с валидацией (T-21).
func (s *Service) CreatePipeline(ctx context.Context,
	projectID *int64, name, specJSON string,
) (*dtorep.Pipeline, error) {
	pipelines := s.pipelines
	if name == "" {
		return nil, fmt.Errorf("pipeline name is empty: %w", errValidation)
	}
	if err := ValidateSpec(specJSON); err != nil {
		return nil, err
	}
	id, err := pipelines.CreatePipeline(ctx, dtorep.CreatePipelineRequest{
		ProjectID: projectID,
		Name:      name,
		Version:   1,
		SpecJSON:  specJSON,
	})
	if err != nil {
		return nil, err
	}
	return pipelines.GetPipelineByID(ctx, id)
}

// CreatePipelineVersion — правка = новая версия с parent_version_id (T-21).
// Версии неизменяемы.
func (s *Service) CreatePipelineVersion(ctx context.Context,
	parentID int64, specJSON string,
) (*dtorep.Pipeline, error) {
	pipelines := s.pipelines
	if err := ValidateSpec(specJSON); err != nil {
		return nil, err
	}
	parent, err := pipelines.GetPipelineByID(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("failed to load parent pipeline: %w", err)
	}

	// версия = max(версий этого пайплайна)+1 — иначе повторный POST на
	// старую версию создавал дубликаты номеров (поймано 2026-08-17);
	// parent_version_id — версия-основание из URL (аудит «на чём основано»)
	versions, err := pipelines.ListPipelineVersions(ctx, parent.ProjectID, parent.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to list pipeline versions: %w", err)
	}
	maxVersion := parent.Version
	for _, v := range versions {
		if v.Version > maxVersion {
			maxVersion = v.Version
		}
	}

	id, err := pipelines.CreatePipeline(ctx, dtorep.CreatePipelineRequest{
		ProjectID:       parent.ProjectID,
		Name:            parent.Name,
		Version:         maxVersion + 1,
		ParentVersionID: &parent.ID,
		SpecJSON:        specJSON,
	})
	if err != nil {
		return nil, err
	}
	return pipelines.GetPipelineByID(ctx, id)
}

// ImportConflict — поведение импорта при совпадении имени.
type ImportConflict string

const (
	ImportNew     ImportConflict = "new"     // всегда новый пайплайн (с суффиксом имени)
	ImportVersion ImportConflict = "version" // новая версия существующего
	ImportFail    ImportConflict = "fail"    // ошибка
)

// ImportYAML — импорт пайплайна из YAML (T-21).
func (s *Service) ImportYAML(ctx context.Context,
	yamlData string, onConflict ImportConflict,
) (*dtorep.Pipeline, error) {
	pipelines := s.pipelines
	name, specJSON, err := yamlToSpecJSON(yamlData)
	if err != nil {
		return nil, err
	}

	existing, err := pipelines.GetLatestPipeline(ctx, nil, name)
	exists := err == nil
	if err != nil && !errors.Is(err, cstmerrors.ErrNotFound) {
		return nil, fmt.Errorf("failed to check pipeline name: %w", err)
	}

	switch {
	case exists && onConflict == ImportFail:
		return nil, fmt.Errorf("pipeline %q already exists: %w", name, errValidation)
	case exists && onConflict == ImportVersion:
		return s.CreatePipelineVersion(ctx, existing.ID, specJSON)
	case exists:
		// new: уникальное имя с суффиксом
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s-%d", name, i)
			if _, err := pipelines.GetLatestPipeline(ctx, nil, candidate); errors.Is(err, cstmerrors.ErrNotFound) {
				name = candidate
				break
			} else if err != nil {
				return nil, err
			}
		}
	}
	return s.CreatePipeline(ctx, nil, name, specJSON)
}

// ExportYAML — экспорт версии в YAML (T-21).
func (s *Service) ExportYAML(ctx context.Context, versionID int64) (string, error) {
	pipelines := s.pipelines
	p, err := pipelines.GetPipelineByID(ctx, versionID)
	if err != nil {
		return "", fmt.Errorf("failed to load pipeline version: %w", err)
	}
	return specJSONToYAML(p.Name, p.Version, p.ParentVersionID, p.SpecJSON)
}

// BuiltinDistillAugmenter — SpecAugmenter для стейт-машины (T-30):
// гарантия «хотя бы один distill в плане» при lessons:on. Если в спеке нет
// distill-этапа (признак — gate_after=lesson_review), детерминированно
// дописывает встроенный (embedded prompts/distill.md, артефакт lessons.md,
// gate_after lesson_review, origin=builtin) в конец цепочки — перед
// финальным гейтом. Пользовательский distill-этап уважается как есть.
// lessons:off — спека возвращается без изменений (мастер-выключатель
// реализует supervisor: distill-этапы пропускаются).
func BuiltinDistillAugmenter(t Triplets) runsmachine.SpecAugmenter {
	return func(spec runsmachine.Spec) runsmachine.Spec {
		if !spec.LessonsOn() || spec.DistillStageKey() != "" {
			return spec
		}
		tpl, err := promptsFS.ReadFile(promptDistill)
		if err != nil {
			return spec // embedded-промпт недоступен — не ломаем ран
		}

		key := "distill"
		used := map[string]bool{}
		for _, st := range spec.Stages {
			used[st.Key] = true
		}
		for i := 2; used[key]; i++ {
			key = fmt.Sprintf("distill-%d", i)
		}

		spec.Stages = append(spec.Stages, runsmachine.StageSpec{
			Key:            key,
			Kind:           "llm-stage",
			Harness:        t.Harness,
			Model:          t.Model,
			Effort:         t.Effort,
			Artifact:       &runsmachine.ArtifactRef{Path: "{run_dir}/lessons.md", Required: true},
			GateAfter:      "lesson_review",
			PromptTemplate: string(tpl),
			Origin:         "builtin",
		})
		return spec
	}
}

// LoopConfig — конфигурация fix-петли из спеки (nil — петли нет).
func LoopConfig(spec runsmachine.Spec) *runsmachine.LoopSpec {
	return spec.Loop
}

package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/internal/service/pipeline"
	"github.com/fableFM/glamor/internal/service/runsmachine"
	"github.com/fableFM/glamor/internal/service/supervisor"
)

func launchCtx(template string) supervisor.LaunchContext {
	return supervisor.LaunchContext{
		Run: &dtorep.Run{
			ID:         "run-1",
			TaskText:   "сделай фичу X",
			BaseBranch: "main",
			Branch:     "glamor/feat-x",
			Depth:      1,
		},
		Stage: &dtorep.Stage{ID: 1, StageKey: "planner", Iteration: 1},
		StageSpec: runsmachine.StageSpec{
			Key:            "planner",
			PromptTemplate: template,
		},
		RunDir:        "/tmp/runs/run-1",
		ProjectPath:   "/tmp/project",
		MaxIterations: 4,
	}
}

func TestPromptBuilder_Placeholders(t *testing.T) {
	b := pipeline.NewPromptBuilder("/home/u/.glamor/vendors")

	out, err := b.Build(context.Background(), launchCtx(
		"Задача: {{task}}\nВетка: {{branch}} от {{base_branch}}\n"+
			"Spec: {{artifact.spec.md}}\nDepth: {{depth}}\nRunDir: {{run_dir}}\n"+
			"Vendors:\n{{vendor_memory_paths}}\nIter: {{iteration}}/{{max_iterations}}"))
	require.NoError(t, err)

	assert.Contains(t, out, "сделай фичу X")
	assert.Contains(t, out, "glamor/feat-x от main")
	assert.Contains(t, out, "/tmp/runs/run-1/spec.md")
	assert.Contains(t, out, "standard")
	assert.Contains(t, out, "/home/u/.glamor/vendors/")
	assert.Contains(t, out, "/tmp/project/.glamor/vendors/")
	assert.Contains(t, out, "1/4")
	assert.NotContains(t, out, "{{")
}

func TestPromptBuilder_DepthInstructions(t *testing.T) {
	b := pipeline.NewPromptBuilder("/tmp/vendors")

	for depth, want := range map[int64]string{
		0: "Режим быстрой задачи",
		1: "Стандартный режим",
		2: "Полный режим",
	} {
		lc := launchCtx("{{depth_instructions}}")
		lc.Run.Depth = depth
		out, err := b.Build(context.Background(), lc)
		require.NoError(t, err)
		assert.Contains(t, out, want)
	}
}

func TestPromptBuilder_UnknownPlaceholderFails(t *testing.T) {
	b := pipeline.NewPromptBuilder("/tmp/vendors")

	_, err := b.Build(context.Background(), launchCtx("{{nosuchthing}}"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown placeholder")
}

// F-07: верхний регистр и пробелы — не плейсхолдеры, а ошибка (и валидация,
// и рендер); рендер не оставляет литералов {{...}} в промпте.
func TestPlaceholders_UppercaseAndSpacesRejected(t *testing.T) {
	b := pipeline.NewPromptBuilder("/tmp/vendors")

	for _, bad := range []string{"{{Task}}", "{{ task }}", "{{TASK}}", "{{task }}", "{{ }}"} {
		spec := `{"stages":[{"key":"plan","harness":"fake","prompt_template":"` + bad + `"}]}`
		err := pipeline.ValidateSpec(spec)
		require.Error(t, err, "ValidateSpec должен отклонить %s", bad)
		assert.Contains(t, err.Error(), "unknown placeholder")

		_, err = b.Build(context.Background(), launchCtx("prefix "+bad+" suffix"))
		require.Error(t, err, "Build должен отклонить %s", bad)
		assert.Contains(t, err.Error(), "unknown placeholder")
	}

	// валидный шаблон не содержит литералов после рендера
	out, err := b.Build(context.Background(), launchCtx("Задача: {{task}}"))
	require.NoError(t, err)
	assert.NotContains(t, out, "{{")
}

func TestDefaultSpec_ValidAndSeeded(t *testing.T) {
	specJSON, err := pipeline.DefaultSpec(pipeline.DefaultOverrides())
	require.NoError(t, err)

	spec, err := runsmachine.ParseSpec(specJSON)
	require.NoError(t, err)

	require.Len(t, spec.Stages, 5)
	assert.Equal(t, []string{"planner", "coder", "reviewer", "fixer", "distill"},
		[]string{spec.Stages[0].Key, spec.Stages[1].Key, spec.Stages[2].Key, spec.Stages[3].Key, spec.Stages[4].Key})
	require.NotNil(t, spec.Loop)
	assert.Equal(t, "reviewer", spec.Loop.From)
	assert.Equal(t, "fixer", spec.Loop.To)
	assert.Equal(t, int64(4), spec.Loop.MaxIters)
	assert.Equal(t, "final_review", spec.FinalGate)

	// промпт-шаблоны inline и рендерятся
	for _, st := range spec.Stages {
		assert.NotEmpty(t, st.PromptTemplate, st.Key)
		assert.NotContains(t, st.PromptTemplate, "@TASK_FILE@", "плейсхолдеры ai-pipeline портированы")
	}
	// планировщик: gate_after + questions
	assert.Equal(t, "plan_approval", spec.Stages[0].GateAfter)
	assert.NotEmpty(t, spec.Stages[0].QuestionsPath)
	// fixer — low effort; distill — lesson_review gate (T-29)
	assert.Equal(t, "low", spec.Stages[3].Effort)
	assert.Equal(t, "lesson_review", spec.Stages[4].GateAfter)

	// seed идемпотентен (из embedded default.yaml, T-21)
	db := testdb.New(t)
	repo := pipelinesrep.NewRepository(db)
	svc := pipeline.NewService(repo)
	require.NoError(t, svc.Seed(context.Background()))
	require.NoError(t, svc.Seed(context.Background()))

	seeded, err := repo.GetLatestPipeline(context.Background(), nil, "default")
	require.NoError(t, err)
	assert.Equal(t, int64(1), seeded.Version)
	assert.Nil(t, seeded.ProjectID, "глобальный пайплайн")
	assert.True(t, strings.Contains(seeded.SpecJSON, "prompt_template"))
}

// fix-task-3 п.5: backend-валидация обязательных полей llm-этапа (как фронт).
func TestValidateSpec_LLMStageRequiredFields(t *testing.T) {
	base := func(stage string) string {
		return `{"stages":[` + stage + `]}`
	}

	// валидный llm-stage (kind явный и опущенный — в M1 оба варианта llm)
	require.NoError(t, pipeline.ValidateSpec(base(
		`{"key":"a","kind":"llm-stage","harness":"kimi","model":"m","prompt_template":"{{task}}"}`)))
	require.NoError(t, pipeline.ValidateSpec(base(
		`{"key":"a","harness":"kimi","model":"m","prompt_template":"{{task}}"}`)))

	for name, stage := range map[string]string{
		"empty harness":         `{"key":"a","kind":"llm-stage","model":"m","prompt_template":"x"}`,
		"empty model":           `{"key":"a","kind":"llm-stage","harness":"kimi","prompt_template":"x"}`,
		"empty prompt_template": `{"key":"a","kind":"llm-stage","harness":"kimi","model":"m"}`,
		"blank model":           `{"key":"a","harness":"kimi","model":"  ","prompt_template":"x"}`,
	} {
		err := pipeline.ValidateSpec(base(stage))
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "empty", name)
	}

	// будущие не-llm виды этапов (janitor/human-gate, M2) проверка не трогает
	require.NoError(t, pipeline.ValidateSpec(base(
		`{"key":"g","kind":"human-gate"}`)))
}

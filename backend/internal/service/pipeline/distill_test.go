package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/service/runsmachine"
)

// Гарантия distill (T-30): lessons:on без distill-этапа → встроенный
// дописывается (origin=builtin); пользовательский distill уважается;
// lessons:off — спека не меняется.
func TestBuiltinDistillAugmenter(t *testing.T) {
	triplets := Triplets{Harness: "kimi", Model: "kimi-code/k3", Effort: "low"}
	augment := BuiltinDistillAugmenter(triplets)

	base := runsmachine.Spec{
		Lessons: "on",
		Stages: []runsmachine.StageSpec{
			{Key: "coder", Kind: "llm-stage", Harness: "kimi", Model: "m"},
		},
	}

	// нет distill → дописан встроенный
	got := augment(base)
	require.Len(t, got.Stages, 2)
	builtin := got.Stages[1]
	assert.Equal(t, "distill", builtin.Key)
	assert.Equal(t, "builtin", builtin.Origin)
	assert.Equal(t, "lesson_review", builtin.GateAfter)
	require.NotNil(t, builtin.Artifact)
	assert.Equal(t, "{run_dir}/lessons.md", builtin.Artifact.Path)
	assert.True(t, builtin.Artifact.Required)
	assert.NotEmpty(t, builtin.PromptTemplate)
	assert.Equal(t, "kimi", builtin.Harness)

	// идемпотентность: повторный прогон не плодит этапы
	assert.Len(t, augment(got).Stages, 2)

	// пользовательский distill уважается (гарантия = «хотя бы один»)
	custom := base
	custom.Stages = append(custom.Stages, runsmachine.StageSpec{
		Key: "my-distill", Kind: "llm-stage", Harness: "qwen", Model: "m",
		GateAfter: "lesson_review",
	})
	assert.Equal(t, custom, augment(custom))

	// lessons:off — спека не меняется
	off := base
	off.Lessons = "off"
	assert.Equal(t, off, augment(off))

	// коллизия ключа: этап "distill" без gate_after lesson_review — встроенный
	// получает уникальный ключ
	collision := base
	collision.Stages = append(collision.Stages, runsmachine.StageSpec{
		Key: "distill", Kind: "llm-stage", Harness: "kimi", Model: "m",
	})
	got = augment(collision)
	require.Len(t, got.Stages, 3)
	assert.Equal(t, "distill-2", got.Stages[2].Key)
}

// LessonsOn: пустое значение = on (обратная совместимость старых spec_json).
func TestSpecLessonsOnDefault(t *testing.T) {
	assert.True(t, runsmachine.Spec{}.LessonsOn())
	assert.True(t, runsmachine.Spec{Lessons: "on"}.LessonsOn())
	assert.False(t, runsmachine.Spec{Lessons: "off"}.LessonsOn())
}

// validate: lessons — только on|off (пусто = on).
func TestValidateSpecLessonsField(t *testing.T) {
	spec := `{"stages":[{"key":"a","kind":"janitor","commands":["true"]}],"lessons":"bogus"}`
	err := ValidateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown lessons")

	spec = `{"stages":[{"key":"a","kind":"janitor","commands":["true"]}],"lessons":"off"}`
	require.NoError(t, ValidateSpec(spec))
}

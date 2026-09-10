package lessons_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/service/lessons"
)

// Relapse (T-30): blocking/major finding, FTS-совпавший с инжектированным
// уроком → relapse_count++ и хит в результате; не связанный finding и
// minor/advisory — не рецидив.
func TestRelapseCheck(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	lesson := saveBehavior(t, svc, "sqlite вместо файлов для очередей",
		"## Ситуация\nОчередь заметок.\n\n## Правило\nКогда очередь в демоне — всегда sqlite, никогда файлы.")
	injected := []lessons.InjectedLesson{{Lesson: lesson}}

	// позитив: blocking finding про ту же тему
	hits, err := svc.RelapseCheck(ctx, injected, []lessons.Finding{
		{
			ID: "REV-001", Severity: "blocking", File: "queue.go",
			Observed: "очередь заметок на файлах", Expected: "sqlite",
			RequiredFix: "перевести очередь на sqlite",
		},
	})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, lesson.ID, hits[0].LessonID)
	assert.Equal(t, "REV-001", hits[0].FindingID)

	got, err := svc.GetByID(ctx, lesson.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.RelapseCount)

	// негатив: finding не про урок
	hits, err = svc.RelapseCheck(ctx, injected, []lessons.Finding{
		{
			ID: "REV-002", Severity: "blocking", File: "main.go",
			Observed: "утечка горутин в воркере телеграма", Expected: "контекст отменяется",
			RequiredFix: "пробросить ctx в цикл",
		},
	})
	require.NoError(t, err)
	assert.Empty(t, hits)

	// minor/advisory — не рецидив, даже при совпадении слов
	hits, err = svc.RelapseCheck(ctx, injected, []lessons.Finding{
		{
			ID: "REV-003", Severity: "minor", File: "queue.go",
			Observed: "очередь sqlite название таблицы", Expected: "единый стиль",
			RequiredFix: "переименовать",
		},
	})
	require.NoError(t, err)
	assert.Empty(t, hits)

	got, err = svc.GetByID(ctx, lesson.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.RelapseCount, "негативные кейсы не двигают счётчик")

	// урок не меняет статуса от эвристики
	assert.Equal(t, lessons.StatusConfirmed, got.Status)
}

// Outcome (T-30): чистый approve → applied_success_count++ и
// last_applied_at всем инжектированным.
func TestOutcomeApprove(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	a := saveBehavior(t, svc, "урок A про sqlite очередь", "## Правило\nА.")
	b := saveBehavior(t, svc, "урок B про sqlite очередь", "## Правило\nБ.")
	before, err := svc.GetByID(ctx, a.ID)
	require.NoError(t, err)

	require.NoError(t, svc.OutcomeApprove(ctx, []lessons.InjectedLesson{{Lesson: a}, {Lesson: b}}))

	for _, id := range []string{a.ID, b.ID} {
		got, err := svc.GetByID(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, int64(1), got.AppliedSuccessCount)
		require.NotNil(t, got.LastAppliedAt)
		assert.Greater(t, got.Importance, before.Importance, "успех двигает importance вверх")
	}

	// пустой список — не ошибка
	require.NoError(t, svc.OutcomeApprove(ctx, nil))
}

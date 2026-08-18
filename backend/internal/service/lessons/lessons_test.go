package lessons_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	lessonsrep "github.com/fableFM/glamor/internal/repository/lessons"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/internal/repository/vendorindex"
	"github.com/fableFM/glamor/internal/service/lessons"
	"github.com/fableFM/glamor/internal/service/pipeline"
	"github.com/fableFM/glamor/internal/service/runsmachine"
	"github.com/fableFM/glamor/internal/service/supervisor"
)

func newService(t *testing.T) *lessons.Service {
	t.Helper()
	db := testdb.New(t)
	return lessons.New(lessonsrep.NewRepository(db), vendorindex.NewRepository(db), t.TempDir())
}

const sampleCards = `
---
title: sqlite вместо файлов для очередей
triggers: [sqlite, очереди]
---

## Ситуация
Делали очередь заметок.

## Симптом
Пользователь поправил: не использовать файлы.

## Причина (почему)
Файлы не атомарны.

## Правило
Когда очередь в демоне — всегда sqlite, никогда не файлы.
`

func TestParseCards(t *testing.T) {
	cards := lessons.ParseCards(sampleCards)
	require.Len(t, cards, 1)
	assert.Equal(t, "sqlite вместо файлов для очередей", cards[0].Title)
	assert.Equal(t, []string{"sqlite", "очереди"}, cards[0].Triggers)
	assert.Contains(t, cards[0].Body, "Правило")

	// мусор не ломает парсер
	assert.Empty(t, lessons.ParseCards("NO_LESSONS"))
	assert.Empty(t, lessons.ParseCards("---\nbroken\n---"))
}

// Полный цикл: карточка → confirmed → попадает в промпт следующего рана
// (T-29 acceptance), applied_count++.
func TestLessonInjectionIntoPrompt(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	projectPath := t.TempDir()

	cards := lessons.ParseCards(sampleCards)
	lesson, err := svc.SaveCard(ctx, cards[0], "project", nil, projectPath,
		nil, "coder", lessons.StatusConfirmed, "")
	require.NoError(t, err)

	// файл на месте
	_, err = os.Stat(lesson.Path)
	require.NoError(t, err)

	// инъекция в промпт через {{lessons}}
	builder := pipeline.NewPromptBuilder(t.TempDir())
	builder.SetLessonsProvider(svc)
	out, err := builder.Build(ctx, supervisor.LaunchContext{
		Run:   &dtorep.Run{TaskText: "сделай очередь заметок на sqlite", Depth: 1},
		Stage: &dtorep.Stage{StageKey: "planner", Iteration: 1},
		StageSpec: runsmachine.StageSpec{
			Key:            "planner",
			PromptTemplate: "Уроки:\n{{lessons}}",
		},
		RunDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Contains(t, out, "sqlite вместо файлов для очередей")

	// applied_count инкрементирован
	after, err := svc.List(ctx, lessons.StatusConfirmed, "", nil)
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, int64(1), after[0].AppliedCount)
}

// Отклонённый урок не предлагается повторно (dedup, T-29).
func TestRejectedDedup(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	cards := lessons.ParseCards(sampleCards)
	_, err := svc.SaveCard(ctx, cards[0], "project", nil, t.TempDir(),
		nil, "coder", lessons.StatusRejected, "")
	require.NoError(t, err)

	// повторное сохранение с тем же title → dedup-ошибка
	_, err = svc.SaveCard(ctx, cards[0], "project", nil, t.TempDir(),
		nil, "coder", lessons.StatusConfirmed, "")
	require.Error(t, err)
	assert.True(t, lessons.IsDuplicate(err))

	// rejected titles попадают в промпт distill'а
	titles, err := svc.RejectedTitles(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"sqlite вместо файлов для очередей"}, titles)
}

// Гейт-флоу: FinalizeLessonGate approve → карточки в проектной памяти;
// reject → rejected для dedup.
func TestFinalizeLessonGate(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)

	svc := lessons.New(lessonsrep.NewRepository(db), vendorindex.NewRepository(db), t.TempDir())
	finalizer := lessons.NewGateFinalizer(svc,
		runsrep.NewRepository(db), projectsrep.NewRepository(db))

	// черновик lessons.md в runDir
	runDir := t.TempDir()
	lessonsPath := filepath.Join(runDir, "lessons.md")
	require.NoError(t, os.WriteFile(lessonsPath, []byte(sampleCards), 0o644))

	gate := &dtorep.Gate{
		RunID:       runID,
		Kind:        dtorep.GateKindLessonReview,
		ContextJSON: `{"lessons_path":"` + lessonsPath + `","stage_key":"distill"}`,
	}

	require.NoError(t, finalizer.FinalizeLessonGate(ctx, gate, "approve", nil))

	list, err := svc.List(ctx, lessons.StatusConfirmed, "project", nil)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "sqlite вместо файлов для очередей", list[0].Title)
	assert.Equal(t, "distill", list[0].StageKey)
	assert.Contains(t, list[0].Path, ".glamor", "файл в проектной памяти")
	assert.Contains(t, list[0].Path, "lessons")

	// reject-дedup через гейт
	gate2 := &dtorep.Gate{
		RunID: runID, Kind: dtorep.GateKindLessonReview,
		ContextJSON: gate.ContextJSON,
	}
	require.NoError(t, finalizer.FinalizeLessonGate(ctx, gate2, "reject", nil))
	// повторный approve того же контента → dedup (ничего не сохраняется, без ошибки)
	require.NoError(t, finalizer.FinalizeLessonGate(ctx, gate, "approve", nil))
	listAll, err := svc.List(ctx, "", "", nil)
	require.NoError(t, err)
	var confirmed int
	for _, l := range listAll {
		if l.Status == lessons.StatusConfirmed {
			confirmed++
		}
	}
	assert.Equal(t, 1, confirmed)
}

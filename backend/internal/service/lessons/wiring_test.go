package lessons_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	lessonsrep "github.com/fableFM/glamor/internal/repository/lessons"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/internal/repository/vendorindex"
	"github.com/fableFM/glamor/internal/service/lessons"
)

// Три NEW-карточки для per-card резолва.
const perCardDraft = `
---
title: урок первый
triggers: [alpha]
---

## Причина (почему)
Причина один.

## Правило
Когда alpha — делай один.

---
title: урок второй
triggers: [beta]
---

## Причина (почему)
Причина два.

## Правило
Когда beta — делай два.

---
title: урок третий
triggers: [gamma]
---

## Причина (почему)
Причина три.

## Правило
Когда gamma — делай три.
`

// Per-card резолв гейта lesson_review (T-30): принятая часть операций
// применяется (confirmed), отклонённая NEW сохраняется rejected (dedup),
// неперечисленная — пропускается нейтрально.
func TestFinalizeLessonGate_PerCard(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)

	svc := lessons.New(lessonsrep.NewRepository(db), vendorindex.NewRepository(db), t.TempDir())
	finalizer := lessons.NewGateFinalizer(svc,
		runsrep.NewRepository(db), projectsrep.NewRepository(db))

	runDir := t.TempDir()
	lessonsPath := filepath.Join(runDir, "lessons.md")
	require.NoError(t, os.WriteFile(lessonsPath, []byte(perCardDraft), 0o644))

	gate := &dtorep.Gate{
		RunID:       runID,
		Kind:        dtorep.GateKindLessonReview,
		ContextJSON: `{"lessons_path":"` + lessonsPath + `","stage_key":"distill"}`,
	}

	sel := &dtorep.LessonOpSelection{Accept: []int{0}, Reject: []int{1}}
	require.NoError(t, finalizer.FinalizeLessonGate(ctx, gate, "approve", nil, sel))

	all, err := svc.List(ctx, "", "", nil)
	require.NoError(t, err)
	byTitle := map[string]string{}
	for _, l := range all {
		byTitle[l.Title] = l.Status
	}
	assert.Equal(t, lessons.StatusConfirmed, byTitle["урок первый"], "принятая операция применена")
	assert.Equal(t, lessons.StatusRejected, byTitle["урок второй"], "отклонённая NEW — rejected для dedup")
	_, exists := byTitle["урок третий"]
	assert.False(t, exists, "неперечисленная операция пропущена нейтрально")
}

// Vendor-карточка через операции distill (T-30): kind=vendor сохраняется с
// vendor-полями и глобальным scope; драфт без точной версии отклоняется,
// не ломая соседние операции.
func TestApplyOperations_VendorCard(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	draft := `
---
kind: vendor
title: goose v3 требует AddMigrationContext
vendor: goose
vendor_version: v3.24.1
area: миграции
triggers: [goose, миграции]
---

## Урок
goose v3 регистрирует миграции через AddMigrationContext.

## Доказательства
run r1, этап planner, go.mod: goose v3.24.1.

---
kind: vendor
title: битый драфт без версии
vendor: sqlite
vendor_version: latest
area: драйвер
triggers: [sqlite]
---

## Урок
Что-то про sqlite.

## Доказательства
Нет.

---
title: обычный урок
triggers: [обычный]
---

## Причина (почему)
Потому что.

## Правило
Делай так.
`
	ops := lessons.ParseOperations(draft)
	require.Len(t, ops, 3)

	projectPath := t.TempDir()
	results := svc.ApplyOperations(ctx, ops, lessons.ApplyParams{
		Scope: "project", ProjectPath: projectPath,
		Status: lessons.StatusConfirmed, Source: lessons.SourceAuto,
	})
	require.Len(t, results, 3)
	assert.NoError(t, results[0].Err)
	assert.Error(t, results[1].Err, "latest — не точная версия, драфт отклонён")
	assert.NoError(t, results[2].Err, "битая операция не ломает соседние")

	vendorLesson, err := svc.GetByID(ctx, results[0].LessonID)
	require.NoError(t, err)
	assert.Equal(t, lessons.KindVendor, vendorLesson.Kind)
	assert.Equal(t, "global", vendorLesson.Scope, "vendor-уроки глобальны (T-30)")
	require.NotNil(t, vendorLesson.Vendor)
	assert.Equal(t, "goose", *vendorLesson.Vendor)
	assert.Contains(t, vendorLesson.Path, "vendor")
}

// Журнал инъекций (T-30): аккумулятивная запись по этапам, дедуп по
// lesson_id, загрузка для петли качества.
func TestInjectedRecordsRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	runDir := t.TempDir()

	cards := lessons.ParseCards(sampleCards)
	l1, err := svc.SaveCard(ctx, lessons.SaveCardParams{
		Card: cards[0], Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceUser,
	})
	require.NoError(t, err)

	injected := []lessons.InjectedLesson{{Lesson: l1, Score: 1.5}}
	require.NoError(t, lessons.AppendInjectedRecords(runDir, "planner", injected))
	// повторная инъекция того же урока в coder — не дублируется
	require.NoError(t, lessons.AppendInjectedRecords(runDir, "coder", injected))

	records := lessons.ReadInjectedRecords(runDir)
	require.Len(t, records, 1)
	assert.Equal(t, l1.ID, records[0].LessonID)
	assert.Equal(t, "planner", records[0].StageKey, "первая инъекция")

	loaded, err := svc.LoadRunInjections(ctx, runDir)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, l1.ID, loaded[0].Lesson.ID)
}

// Сводка confirmed-уроков для промпта distill (T-30): id + title + триггеры.
func TestExistingLessonsSummary(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	summary, err := svc.ExistingLessonsSummary(ctx)
	require.NoError(t, err)
	assert.Empty(t, summary)

	cards := lessons.ParseCards(sampleCards)
	l, err := svc.SaveCard(ctx, lessons.SaveCardParams{
		Card: cards[0], Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceUser,
	})
	require.NoError(t, err)

	summary, err = svc.ExistingLessonsSummary(ctx)
	require.NoError(t, err)
	assert.Contains(t, summary, l.ID)
	assert.Contains(t, summary, "sqlite вместо файлов для очередей")
	assert.Contains(t, summary, "sqlite")
}

// Кандидаты консолидации (T-30): дубли по общему триггеру, superseded
// старше N дней, нездоровые (relapse >= applied_success).
func TestConsolidationCandidates(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	repo := lessonsrep.NewRepository(db)
	svc := lessons.New(repo, vendorindex.NewRepository(db), t.TempDir())

	save := func(title string, triggers []string) *dtorep.Lesson {
		l, err := svc.SaveCard(ctx, lessons.SaveCardParams{
			Card:  lessons.Card{Title: title, Triggers: triggers, Body: "## Правило\nтело"},
			Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceUser,
		})
		require.NoError(t, err)
		// rejected-dedup не мешает: title уникальны
		return l
	}

	a := save("урок про sqlite очереди", []string{"sqlite", "очереди"})
	b := save("другой урок про sqlite", []string{"sqlite", "wal"})
	c := save("вытесненный урок", []string{"старое"})
	d := save("нездоровый урок", []string{"relapse"})

	// superseded давно (backdate updated_at)
	require.NoError(t, repo.SetSuperseded(ctx, c.ID, a.ID))
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	_, err := db.ExecContext(ctx, `UPDATE lessons SET updated_at = ? WHERE id = ?`, old, c.ID)
	require.NoError(t, err)

	// нездоровый: relapse без успехов
	require.NoError(t, repo.IncrementRelapse(ctx, d.ID))

	report, err := svc.ConsolidationCandidates(ctx, 30)
	require.NoError(t, err)

	var foundDup bool
	for _, p := range report.Duplicates {
		if (p.Lesson.ID == a.ID && p.SimilarTo.ID == b.ID) ||
			(p.Lesson.ID == b.ID && p.SimilarTo.ID == a.ID) {
			foundDup = true
			assert.Contains(t, p.Reason, "shared_trigger")
		}
	}
	assert.True(t, foundDup, "общий триггер sqlite → кандидаты в дубли")

	require.Len(t, report.StaleSuperseded, 1)
	assert.Equal(t, c.ID, report.StaleSuperseded[0].ID)

	require.Len(t, report.Unhealthy, 1)
	assert.Equal(t, d.ID, report.Unhealthy[0].ID)

	// фильтр «требуют внимания»
	attention, err := svc.ListFiltered(ctx, "", "", "", true, nil)
	require.NoError(t, err)
	require.Len(t, attention, 1)
	assert.Equal(t, d.ID, attention[0].ID)
}

// C1 (T-30): полный reject гейта НЕ применяет дельта-операции к
// confirmed-урокам — REFINE/SUPERSEDE/LINK при reject пропускаются,
// целевой урок неизменён; NEW сохраняются rejected (dedup).
func TestFinalizeRejectSkipsDeltaOps(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)
	repo := lessonsrep.NewRepository(db)

	svc := lessons.New(repo, vendorindex.NewRepository(db), t.TempDir())
	finalizer := lessons.NewGateFinalizer(svc,
		runsrep.NewRepository(db), projectsrep.NewRepository(db))

	// существующий confirmed-урок — цель REFINE
	target, err := svc.SaveCard(ctx, lessons.SaveCardParams{
		Card: lessons.Card{
			Title: "существующий урок", Triggers: []string{"sqlite"},
			Body: "## Причина (почему)\nстарая причина\n\n## Правило\nстарое правило",
		},
		Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceUser,
	})
	require.NoError(t, err)
	before, err := os.ReadFile(target.Path)
	require.NoError(t, err)

	// черновик: REFINE существующего + новая карточка
	draft := `---
op: REFINE
target: ` + target.ID + `
title: переписанный урок
triggers: [sqlite]
---

## Причина (почему)
новая причина

## Правило
новое правило

---
title: отклоняемый новый урок
triggers: [beta]
---

## Причина (почему)
причина

## Правило
правило
`
	runDir := t.TempDir()
	lessonsPath := filepath.Join(runDir, "lessons.md")
	require.NoError(t, os.WriteFile(lessonsPath, []byte(draft), 0o644))

	gate := &dtorep.Gate{
		RunID:       runID,
		Kind:        dtorep.GateKindLessonReview,
		ContextJSON: `{"lessons_path":"` + lessonsPath + `","stage_key":"distill"}`,
	}
	// полный reject БЕЗ lesson_ops
	require.NoError(t, finalizer.FinalizeLessonGate(ctx, gate, "reject", nil, nil))

	// цель REFINE неизменна: файл, статус
	after, err := os.ReadFile(target.Path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "reject не перезаписывает confirmed-урок")
	l, err := svc.GetByID(ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, lessons.StatusConfirmed, l.Status)
	assert.Equal(t, "существующий урок", l.Title)

	// NEW-карточка сохранена rejected (dedup)
	rejected, err := svc.List(ctx, lessons.StatusRejected, "", nil)
	require.NoError(t, err)
	require.Len(t, rejected, 1)
	assert.Equal(t, "отклоняемый новый урок", rejected[0].Title)
}

// m10 (T-30): REFINE outdated-урока допустим и восстанавливает его в
// confirmed; vendor-метаданные в файле сохраняются (m7).
func TestRefineOutdatedVendorLesson(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	lesson, err := svc.SaveVendorCard(ctx, lessons.VendorCard{
		Card: lessons.Card{
			Title: "goose миграции", Triggers: []string{"goose"},
			Body: "## Урок\nстарое поведение\n\n## Доказательства\nrun 1",
		},
		Vendor: "goose", VendorVersion: "v3.20.0", Area: "миграции",
	}, "global", t.TempDir(), nil, nil, "planner", lessons.StatusConfirmed)
	require.NoError(t, err)
	require.NoError(t, svc.SetStatus(ctx, lesson.ID, lessons.StatusOutdated))

	ops := []lessons.Operation{{
		Op:       lessons.OpRefine,
		TargetID: lesson.ID,
		Card: lessons.Card{
			Title: "goose миграции (уточнено)", Triggers: []string{"goose", "v3.24"},
			Body: "## Урок\nновое поведение\n\n## Доказательства\nrun 2",
		},
	}}
	results := svc.ApplyOperations(ctx, ops, lessons.ApplyParams{Scope: "global", Status: lessons.StatusConfirmed})
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)

	after, err := svc.GetByID(ctx, lesson.ID)
	require.NoError(t, err)
	assert.Equal(t, lessons.StatusConfirmed, after.Status, "refine outdated восстанавливает confirmed")
	assert.Equal(t, "goose миграции (уточнено)", after.Title)

	content, err := os.ReadFile(after.Path)
	require.NoError(t, err)
	assert.Contains(t, string(content), "vendor: goose", "m7: vendor-метаданные сохранены")
	assert.Contains(t, string(content), "vendor_version: v3.20.0")
	assert.Contains(t, string(content), "area: миграции")
	assert.Contains(t, string(content), "новое поведение")
}

// m8 (T-30): инъекция обновляет last_applied_at — recency-decay скоринга
// считается от последнего применения, а не от created_at.
func TestIncrementAppliedUpdatesLastAppliedAt(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	repo := lessonsrep.NewRepository(db)
	svc := lessons.New(repo, vendorindex.NewRepository(db), t.TempDir())

	cards := lessons.ParseCards(sampleCards)
	l, err := svc.SaveCard(ctx, lessons.SaveCardParams{
		Card: cards[0], Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceUser,
	})
	require.NoError(t, err)

	require.NoError(t, repo.IncrementApplied(ctx, l.ID))
	after, err := svc.GetByID(ctx, l.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), after.AppliedCount)
	require.NotNil(t, after.LastAppliedAt, "инъекция обновляет last_applied_at")
	assert.False(t, after.LastAppliedAt.Before(l.CreatedAt.Add(-time.Minute)))
}

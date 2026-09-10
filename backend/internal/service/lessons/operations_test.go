package lessons_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/service/lessons"
)

// Формат операций distill v2 (T-30): op: в frontmatter + target:/target2:.
const sampleOps = `
---
title: sqlite вместо файлов
triggers: [sqlite]
---

## Ситуация
Очередь заметок.

## Правило
Всегда sqlite.

---
op: refine
target: lesson-target1
title: уточнённое правило про sqlite
triggers: [sqlite, wal]
---

## Правило
Всегда sqlite в WAL-режиме.

---
op: supersede
target: lesson-target2
title: новая версия правила
triggers: [sqlite]
---

## Правило
Новая формулировка.

---
op: link
target: lesson-a
target2: lesson-b
---
`

func TestParseOperations(t *testing.T) {
	ops := lessons.ParseOperations(sampleOps)
	require.Len(t, ops, 4)

	assert.Equal(t, lessons.OpNew, ops[0].Op)
	assert.Equal(t, "sqlite вместо файлов", ops[0].Card.Title)

	assert.Equal(t, lessons.OpRefine, ops[1].Op)
	assert.Equal(t, "lesson-target1", ops[1].TargetID)
	assert.Equal(t, []string{"sqlite", "wal"}, ops[1].Card.Triggers)

	assert.Equal(t, lessons.OpSupersede, ops[2].Op)
	assert.Equal(t, "lesson-target2", ops[2].TargetID)

	assert.Equal(t, lessons.OpLink, ops[3].Op)
	assert.Equal(t, "lesson-a", ops[3].TargetID)
	assert.Equal(t, "lesson-b", ops[3].Target2ID)
}

// Мусор LLM не ломает парсер: NO_LESSONS, пустой ввод, битые операции.
func TestParseOperationsGarbage(t *testing.T) {
	assert.Empty(t, lessons.ParseOperations("NO_LESSONS"))
	assert.Empty(t, lessons.ParseOperations(""))
	assert.Empty(t, lessons.ParseOperations("---\nbroken\n---"))

	// refine без target → пропускается, соседние операции выживают
	broken := `
---
op: refine
title: нет target
---
тело

---
title: нормальная карточка
---
тело
`
	ops := lessons.ParseOperations(broken)
	require.Len(t, ops, 1)
	assert.Equal(t, lessons.OpNew, ops[0].Op)

	// link с одним id → пропускается; link сам на себя → пропускается
	assert.Empty(t, lessons.ParseOperations("---\nop: link\ntarget: a\n---\n"))
	assert.Empty(t, lessons.ParseOperations("---\nop: link\ntarget: a\ntarget2: a\n---\n"))

	// «ВОПРОС:» в теле → question (конвенция T-29)
	q := lessons.ParseOperations("---\ntitle: непонятная причина\n---\n## Причина\nВОПРОС: почему так?\n")
	require.Len(t, q, 1)
	assert.Equal(t, lessons.OpQuestion, q[0].Op)
}

// Хелпер: сохранить confirmed behavior-урок через сервис.
func saveConfirmed(t *testing.T, svc *lessons.Service, title, body string) string {
	t.Helper()
	lesson, err := svc.SaveCard(context.Background(), lessons.SaveCardParams{
		Card:  lessons.Card{Title: title, Triggers: []string{"sqlite"}, Body: body},
		Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceAuto,
	})
	require.NoError(t, err)
	return lesson.ID
}

func applyParams() lessons.ApplyParams {
	return lessons.ApplyParams{Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceAuto}
}

func TestApplyOperationsRefine(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	id := saveConfirmed(t, svc, "старое правило", "## Правило\nСтарая формулировка.")

	old, err := svc.GetByID(ctx, id)
	require.NoError(t, err)

	ops := lessons.ParseOperations(`
---
op: refine
target: ` + id + `
title: новое правило
triggers: [sqlite, wal]
---
## Правило
Новая формулировка.
`)
	results := svc.ApplyOperations(ctx, ops, applyParams())
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)
	assert.Equal(t, id, results[0].LessonID)

	// файл обновлён, старая версия — в .bak
	data, err := os.ReadFile(old.Path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Новая формулировка")
	assert.Contains(t, string(data), "id: "+id)

	baks, err := filepath.Glob(old.Path + ".*.bak")
	require.NoError(t, err)
	require.Len(t, baks, 1, "история REFINE — .bak рядом с файлом")
	bak, err := os.ReadFile(baks[0])
	require.NoError(t, err)
	assert.Contains(t, string(bak), "Старая формулировка")

	// БД обновлена, FTS переиндексирован: новая формулировка находится
	updated, err := svc.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "новое правило", updated.Title)
	text, _, err := svc.RelevantLessons(ctx, "sqlite wal формулировка")
	require.NoError(t, err)
	assert.Contains(t, text, "Новая формулировка")
	assert.NotContains(t, text, "Старая формулировка")

	// refine несуществующего/не-confirmed — ошибка операции, остальные живы
	bad := lessons.ParseOperations(`
---
op: refine
target: lesson-missing
title: x
---
тело
`)
	results = svc.ApplyOperations(ctx, bad, applyParams())
	require.Len(t, results, 1)
	require.Error(t, results[0].Err)
}

func TestApplyOperationsSupersede(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	id := saveConfirmed(t, svc, "устаревшее правило", "## Правило\nСтарое.")

	ops := lessons.ParseOperations(`
---
op: supersede
target: ` + id + `
title: актуальное правило
triggers: [sqlite]
---
## Правило
Новое.
`)
	results := svc.ApplyOperations(ctx, ops, applyParams())
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)
	newID := results[0].LessonID
	require.NotEmpty(t, newID)

	// старый → superseded + superseded_by, исключён из инъекции
	old, err := svc.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, lessons.StatusSuperseded, old.Status)
	require.NotNil(t, old.SupersededBy)
	assert.Equal(t, newID, *old.SupersededBy)

	text, injected, err := svc.RelevantLessons(ctx, "sqlite правило")
	require.NoError(t, err)
	assert.Contains(t, text, "актуальное правило")
	assert.NotContains(t, text, "устаревшее правило")
	for _, inj := range injected {
		assert.NotEqual(t, id, inj.Lesson.ID)
	}
}

func TestApplyOperationsLink(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	a := saveConfirmed(t, svc, "правило A", "## Правило\nА.")
	b := saveConfirmed(t, svc, "правило B", "## Правило\nБ.")

	ops := lessons.ParseOperations("---\nop: link\ntarget: " + a + "\ntarget2: " + b + "\n---\n")
	results := svc.ApplyOperations(ctx, ops, applyParams())
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)

	la, err := svc.GetByID(ctx, a)
	require.NoError(t, err)
	assert.JSONEq(t, `["`+b+`"]`, la.RelatedJSON)
	lb, err := svc.GetByID(ctx, b)
	require.NoError(t, err)
	assert.JSONEq(t, `["`+a+`"]`, lb.RelatedJSON)

	// повторный LINK идемпотентен (без дублей)
	results = svc.ApplyOperations(ctx, ops, applyParams())
	require.NoError(t, results[0].Err)
	la, err = svc.GetByID(ctx, a)
	require.NoError(t, err)
	assert.JSONEq(t, `["`+b+`"]`, la.RelatedJSON)
}

// Per-card независимость: битая операция не ломает остальные.
func TestApplyOperationsPerCard(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	ops := lessons.ParseOperations(`
---
op: supersede
target: lesson-missing
title: вытеснение несуществующего
---
тело

---
title: нормальный новый урок
triggers: [sqlite]
---
## Правило
Живой.
`)
	results := svc.ApplyOperations(ctx, ops, applyParams())
	require.Len(t, results, 2)
	require.Error(t, results[0].Err, "supersede несуществующего — ошибка")
	require.NoError(t, results[1].Err, "соседняя NEW применилась")
	require.NotEmpty(t, results[1].LessonID)

	list, err := svc.List(ctx, lessons.StatusConfirmed, "", nil)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "нормальный новый урок", list[0].Title)
}

// Источник влияет на начальную importance (T-30): user > auto.
func TestSaveCardImportanceBySource(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	user, err := svc.SaveCard(ctx, lessons.SaveCardParams{
		Card:  lessons.Card{Title: "от пользователя", Body: "тело"},
		Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceUser,
	})
	require.NoError(t, err)
	auto, err := svc.SaveCard(ctx, lessons.SaveCardParams{
		Card:  lessons.Card{Title: "автоматический", Body: "тело"},
		Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceAuto,
	})
	require.NoError(t, err)
	assert.Greater(t, user.Importance, auto.Importance)
}

// Vendor-карточка через SaveCard: kind + файл в vendor/<name>/.
func TestSaveCardVendorKind(t *testing.T) {
	svc := newService(t)
	lesson, err := svc.SaveCard(context.Background(), lessons.SaveCardParams{
		Card:  lessons.Card{Title: "goose миграции", Body: "## Урок\n..."},
		Scope: "global", Status: lessons.StatusConfirmed,
		Kind: lessons.KindVendor, Vendor: "pressly/goose", VendorVersion: "v3.24.1",
		Area: "миграции", Source: lessons.SourceAuto,
	})
	require.NoError(t, err)
	assert.Equal(t, lessons.KindVendor, lesson.Kind)
	require.NotNil(t, lesson.Vendor)
	assert.True(t, strings.Contains(lesson.Path, "vendor/pressly_goose"),
		"vendor-урок в подкаталоге vendor/<name>/, получено: %s", lesson.Path)
}

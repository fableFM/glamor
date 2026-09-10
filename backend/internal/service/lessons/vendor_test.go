package lessons_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/service/lessons"
)

const sampleVendorCards = `
---
id: lesson-draft1
kind: vendor
vendor: goose
vendor_version: v3.24.1
area: миграции БД
status: proposed
triggers: [goose, миграции]
source: https://pkg.go.dev/github.com/pressly/goose/v3
---

## Урок
goose v3 требует AddMigrationContext для Go-миграций с контекстом.

## Доказательства
run abc, этап coder; официальная дока v3.24.1.

---
title: не vendor-карточка
---
обычный behavior-урок, пропускаем.
`

func TestParseVendorCards(t *testing.T) {
	cards := lessons.ParseVendorCards(sampleVendorCards)
	require.Len(t, cards, 1)

	c := cards[0]
	assert.Equal(t, "goose", c.Vendor)
	assert.Equal(t, "v3.24.1", c.VendorVersion)
	assert.Equal(t, "миграции БД", c.Area)
	assert.Equal(t, "lesson-draft1", c.ID)
	assert.Equal(t, []string{"goose", "миграции"}, c.Triggers)
	assert.Contains(t, c.Body, "## Урок")
	assert.Contains(t, c.Body, "## Доказательства")

	// мусор не ломает
	assert.Empty(t, lessons.ParseVendorCards("NO_LESSONS"))
	assert.Empty(t, lessons.ParseVendorCards("---\nkind: behavior\ntitle: x\n---\nтело\n"))
}

// Vendor-урок без точной версии — не урок (T-30): отсекается до гейта.
func TestValidateVendorDraft(t *testing.T) {
	valid := lessons.VendorCard{
		Card:          lessons.Card{Title: "x", Body: "## Урок\nтело\n\n## Доказательства\nrun 1"},
		Vendor:        "goose",
		VendorVersion: "v3.24.1",
		Area:          "миграции",
	}
	require.NoError(t, lessons.ValidateVendorDraft(valid))

	tests := []struct {
		name   string
		mutate func(*lessons.VendorCard)
	}{
		{"пустой vendor", func(c *lessons.VendorCard) { c.Vendor = "" }},
		{"пустая версия", func(c *lessons.VendorCard) { c.VendorVersion = "" }},
		{"latest", func(c *lessons.VendorCard) { c.VendorVersion = "latest" }},
		{"n/a", func(c *lessons.VendorCard) { c.VendorVersion = "N/A" }},
		{"пустая area", func(c *lessons.VendorCard) { c.Area = "" }},
		{"нет секции Урок", func(c *lessons.VendorCard) { c.Body = "## Доказательства\nrun 1" }},
		{"нет Доказательств", func(c *lessons.VendorCard) { c.Body = "## Урок\nтело" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid
			tt.mutate(&c)
			assert.ErrorIs(t, lessons.ValidateVendorDraft(c), lessons.ErrInvalidVendorDraft)
		})
	}
}

func TestParseGoMod(t *testing.T) {
	gomod := `module github.com/fableFM/glamor

go 1.24

require (
	github.com/pressly/goose/v3 v3.24.1
	modernc.org/sqlite v1.34.4 // комментарий
)

require github.com/stretchr/testify v1.10.0
`
	mods := lessons.ParseGoMod([]byte(gomod))
	assert.Equal(t, "v3.24.1", mods["github.com/pressly/goose/v3"])
	assert.Equal(t, "v1.34.4", mods["modernc.org/sqlite"])
	assert.Equal(t, "v1.10.0", mods["github.com/stretchr/testify"])

	assert.Empty(t, lessons.ParseGoMod([]byte("module x\n")))
}

// Деградация: vendor_version ≠ версии в go.mod → outdated (не удаляем);
// совпадение и отсутствие go.mod — статус не трогаем.
func TestCheckVendorVersions(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	save := func(vendor, version string) string {
		lesson, err := svc.SaveVendorCard(ctx, lessons.VendorCard{
			Card: lessons.Card{
				Title: vendor + " урок",
				Body:  "## Урок\nповедение\n\n## Доказательства\nrun 1",
			},
			Vendor: vendor, VendorVersion: version, Area: "область",
		}, "global", t.TempDir(), nil, nil, "planner", lessons.StatusConfirmed)
		require.NoError(t, err)
		return lesson.ID
	}

	gooseID := save("goose", "v3.20.0")           // в go.mod будет v3.24.1 → outdated
	sqliteID := save("sqlite", "v1.34.4")         // совпадает → confirmed
	missingID := save("nonexistentlib", "v1.0.0") // нет в go.mod → не трогаем

	projectPath := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectPath, "go.mod"), []byte(`module x

require (
	github.com/pressly/goose/v3 v3.24.1
	modernc.org/sqlite v1.34.4
)
`), 0o644))

	outdated, restored, err := svc.CheckVendorVersions(ctx, projectPath)
	require.NoError(t, err)
	require.Len(t, outdated, 1)
	assert.Equal(t, gooseID, outdated[0].ID)
	assert.Empty(t, restored)

	l, err := svc.GetByID(ctx, gooseID)
	require.NoError(t, err)
	assert.Equal(t, lessons.StatusOutdated, l.Status)
	_, err = os.Stat(l.Path)
	require.NoError(t, err, "файл outdated-урока не удаляется")

	l, err = svc.GetByID(ctx, sqliteID)
	require.NoError(t, err)
	assert.Equal(t, lessons.StatusConfirmed, l.Status)

	l, err = svc.GetByID(ctx, missingID)
	require.NoError(t, err)
	assert.Equal(t, lessons.StatusConfirmed, l.Status)

	// m13: версия откатилась обратно → outdated восстанавливается в confirmed
	require.NoError(t, os.WriteFile(filepath.Join(projectPath, "go.mod"), []byte(`module x

require (
	github.com/pressly/goose/v3 v3.20.0
	modernc.org/sqlite v1.34.4
)
`), 0o644))
	outdated, restored, err = svc.CheckVendorVersions(ctx, projectPath)
	require.NoError(t, err)
	assert.Empty(t, outdated)
	require.Len(t, restored, 1)
	assert.Equal(t, gooseID, restored[0].ID)
	l, err = svc.GetByID(ctx, gooseID)
	require.NoError(t, err)
	assert.Equal(t, lessons.StatusConfirmed, l.Status)

	// нет go.mod → не ошибка, ничего не меняется
	outdated, restored, err = svc.CheckVendorVersions(ctx, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, outdated)
	assert.Empty(t, restored)
}

// Черновик без точной версии отклоняется ещё до гейта (SaveVendorCard).
func TestSaveVendorCardRejectsInexact(t *testing.T) {
	svc := newService(t)
	_, err := svc.SaveVendorCard(context.Background(), lessons.VendorCard{
		Card:   lessons.Card{Title: "x", Body: "## Урок\ny\n\n## Доказательства\nz"},
		Vendor: "goose", VendorVersion: "latest", Area: "миграции",
	}, "global", t.TempDir(), nil, nil, "planner", lessons.StatusConfirmed)
	assert.ErrorIs(t, err, lessons.ErrInvalidVendorDraft)
}

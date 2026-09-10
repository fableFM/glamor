package lessons_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	lessonsrep "github.com/fableFM/glamor/internal/repository/lessons"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/internal/repository/vendorindex"
	"github.com/fableFM/glamor/internal/service/lessons"
)

// newServiceWithDB — сервис + доступ к БД (для подкрутки created_at и
// importance в сценариях скоринга).
func newServiceWithDB(t *testing.T) (*lessons.Service, lessonsrep.RepositoryWithTX, *sql.DB) {
	t.Helper()
	db := testdb.New(t)
	repo := lessonsrep.NewRepository(db)
	svc := lessons.New(repo, vendorindex.NewRepository(db),
		filepath.Join(t.TempDir(), "lessons"))
	return svc, repo, db
}

func saveBehavior(t *testing.T, svc *lessons.Service, title, body string) *dtorep.Lesson {
	t.Helper()
	lesson, err := svc.SaveCard(context.Background(), lessons.SaveCardParams{
		Card:  lessons.Card{Title: title, Triggers: []string{"sqlite", "очередь"}, Body: body},
		Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceAuto,
	})
	require.NoError(t, err)
	return lesson
}

// Скоринг (T-30): при равном FTS-ранге свежий важный урок обгоняет старый.
func TestScoringFreshImportantWins(t *testing.T) {
	ctx := context.Background()
	svc, repo, db := newServiceWithDB(t)

	// идентичные тексты → одинаковый FTS-ранг
	body := "## Правило\nКогда очередь в демоне — всегда sqlite, никогда файлы."
	old := saveBehavior(t, svc, "старый урок про sqlite очередь", body)
	fresh := saveBehavior(t, svc, "свежий урок про sqlite очередь", body)

	// старый: создан полгода назад, дефолтная importance
	oldDate := time.Now().UTC().Add(-180 * 24 * time.Hour)
	_, err := db.ExecContext(ctx, `UPDATE lessons SET created_at = ? WHERE id = ?`, oldDate, old.ID)
	require.NoError(t, err)
	// свежий: высокая importance (подтверждён пользователем + успехи)
	require.NoError(t, repo.UpdateImportance(ctx, fresh.ID, 0.95))

	text, injected, err := svc.RelevantLessons(ctx, "очередь заметок sqlite")
	require.NoError(t, err)
	require.Len(t, injected, 2)
	assert.Equal(t, fresh.ID, injected[0].Lesson.ID, "свежий важный первым")
	assert.Equal(t, old.ID, injected[1].Lesson.ID)
	assert.Greater(t, injected[0].Score, injected[1].Score)
	assert.Contains(t, text, "свежий урок про sqlite очередь")
}

// Токен-бюджет соблюдается: при крошечном бюджете инжектится только первый.
func TestScoringTokenBudget(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newServiceWithDB(t)
	svc.SetTokenBudget(64)

	saveBehavior(t, svc, "первый урок про sqlite очередь",
		"## Правило\nДлинное тело урока, которое точно не влезет в крошечный бюджет целиком.")
	saveBehavior(t, svc, "второй урок про sqlite очередь",
		"## Правило\nЕщё одно длинное тело урока про sqlite и очередь заметок.")

	_, injected, err := svc.RelevantLessons(ctx, "sqlite очередь")
	require.NoError(t, err)
	assert.Len(t, injected, 1, "бюджет обрезает инъекцию")
}

// Outdated-урок инжектится с префиксом-предупреждением (T-30).
func TestScoringOutdatedPrefix(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newServiceWithDB(t)

	lesson, err := svc.SaveVendorCard(ctx, lessons.VendorCard{
		Card: lessons.Card{
			Title:    "goose миграции урок",
			Triggers: []string{"goose", "миграции"},
			Body:     "## Урок\nAddMigrationContext обязателен.\n\n## Доказательства\nrun 1",
		},
		Vendor: "goose", VendorVersion: "v3.24.1", Area: "миграции",
	}, "global", t.TempDir(), nil, nil, "planner", lessons.StatusConfirmed)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateLessonStatus(ctx, lesson.ID, lessons.StatusOutdated))

	text, injected, err := svc.RelevantVendorLessons(ctx, "миграции goose")
	require.NoError(t, err)
	require.Len(t, injected, 1)
	assert.True(t, injected[0].Outdated)
	assert.Contains(t, text, lessons.OutdatedPrefix)

	// behavior-секция vendor-урок не возвращает (разделение секций)
	behaviorText, behaviorInjected, err := svc.RelevantLessons(ctx, "миграции goose")
	require.NoError(t, err)
	assert.Empty(t, behaviorInjected)
	assert.Empty(t, behaviorText)
}

// Superseded исключён из инъекции, confirmed рядом — инжектится.
func TestScoringExcludesSuperseded(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newServiceWithDB(t)

	sup := saveBehavior(t, svc, "вытесненный урок про sqlite очередь", "## Правило\nСтарое.")
	live := saveBehavior(t, svc, "живой урок про sqlite очередь", "## Правило\nНовое.")
	require.NoError(t, repo.SetSuperseded(ctx, sup.ID, live.ID))

	_, injected, err := svc.RelevantLessons(ctx, "sqlite очередь")
	require.NoError(t, err)
	require.Len(t, injected, 1)
	assert.Equal(t, live.ID, injected[0].Lesson.ID)
}

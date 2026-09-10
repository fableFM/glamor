package lessons_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/repository/lessons"
	"github.com/fableFM/glamor/internal/repository/testdb"
)

func newRepo(t *testing.T) lessons.RepositoryWithTX {
	t.Helper()
	return lessons.NewRepository(testdb.New(t))
}

func vendorLesson(id string) *dtorep.Lesson {
	vendor, version, area := "goose", "v3.24.1", "миграции"
	return &dtorep.Lesson{
		ID: id, Title: "goose v3: AddMigrationContext", Scope: "global",
		Status: "confirmed", Kind: "vendor", Path: "/tmp/" + id + ".md",
		Vendor: &vendor, VendorVersion: &version, Area: &area,
		Importance: 0.7,
	}
}

func TestCreateAndGetNewFields(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	require.NoError(t, repo.CreateLesson(ctx, vendorLesson("lesson-v1")))

	got, err := repo.GetLessonByID(ctx, "lesson-v1")
	require.NoError(t, err)
	assert.Equal(t, "vendor", got.Kind)
	assert.Equal(t, "goose", *got.Vendor)
	assert.Equal(t, "v3.24.1", *got.VendorVersion)
	assert.Equal(t, "миграции", *got.Area)
	assert.InDelta(t, 0.7, got.Importance, 1e-9)
	assert.Equal(t, "[]", got.RelatedJSON)
	assert.Equal(t, int64(0), got.AppliedSuccessCount)
	assert.Nil(t, got.LastAppliedAt)
	assert.Nil(t, got.SupersededBy)

	// дефолты: kind пустой → behavior, importance 0 → 0.5
	require.NoError(t, repo.CreateLesson(ctx, &dtorep.Lesson{
		ID: "lesson-b1", Title: "t", Scope: "project", Status: "proposed",
		Path: "/tmp/lesson-b1.md",
	}))
	got, err = repo.GetLessonByPath(ctx, "/tmp/lesson-b1.md")
	require.NoError(t, err)
	assert.Equal(t, "behavior", got.Kind)
	assert.InDelta(t, 0.5, got.Importance, 1e-9)

	// GetLessonByPath: промах → ErrNotFound
	_, err = repo.GetLessonByPath(ctx, "/tmp/nope.md")
	assert.ErrorIs(t, err, cstmerrors.ErrNotFound)
}

func TestGetByIDs(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	for _, id := range []string{"lesson-a", "lesson-b", "lesson-c"} {
		require.NoError(t, repo.CreateLesson(ctx, &dtorep.Lesson{
			ID: id, Title: id, Scope: "global", Status: "confirmed", Path: "/tmp/" + id + ".md",
		}))
	}

	got, err := repo.GetByIDs(ctx, []string{"lesson-a", "lesson-c", "lesson-missing"})
	require.NoError(t, err)
	require.Len(t, got, 2)

	empty, err := repo.GetByIDs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestListByKindAndVendor(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	require.NoError(t, repo.CreateLesson(ctx, vendorLesson("lesson-v1")))
	v2 := vendorLesson("lesson-v2")
	*v2.Vendor = "chi"
	require.NoError(t, repo.CreateLesson(ctx, v2))
	require.NoError(t, repo.CreateLesson(ctx, &dtorep.Lesson{
		ID: "lesson-b1", Title: "b", Scope: "global", Status: "confirmed", Path: "/tmp/lesson-b1.md",
	}))

	vendors, err := repo.ListByKind(ctx, "vendor", "")
	require.NoError(t, err)
	assert.Len(t, vendors, 2)

	behaviors, err := repo.ListByKind(ctx, "behavior", "confirmed")
	require.NoError(t, err)
	assert.Len(t, behaviors, 1)

	gooseOnly, err := repo.ListVendor(ctx, "goose", "")
	require.NoError(t, err)
	require.Len(t, gooseOnly, 1)
	assert.Equal(t, "lesson-v1", gooseOnly[0].ID)
}

func TestCountersMoveImportance(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	require.NoError(t, repo.CreateLesson(ctx, &dtorep.Lesson{
		ID: "lesson-q", Title: "q", Scope: "global", Status: "confirmed",
		Path: "/tmp/lesson-q.md", Importance: 0.5,
	}))

	// успех: счётчик++, last_applied_at, importance +0.05
	require.NoError(t, repo.IncrementAppliedSuccess(ctx, "lesson-q"))
	got, err := repo.GetLessonByID(ctx, "lesson-q")
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.AppliedSuccessCount)
	require.NotNil(t, got.LastAppliedAt)
	assert.InDelta(t, 0.55, got.Importance, 1e-9)

	// рецидив: счётчик++, importance -0.10
	require.NoError(t, repo.IncrementRelapse(ctx, "lesson-q"))
	got, err = repo.GetLessonByID(ctx, "lesson-q")
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.RelapseCount)
	assert.InDelta(t, 0.45, got.Importance, 1e-9)

	// importance зажата в [0.1, 1.0]
	for range 20 {
		require.NoError(t, repo.IncrementRelapse(ctx, "lesson-q"))
	}
	got, err = repo.GetLessonByID(ctx, "lesson-q")
	require.NoError(t, err)
	assert.InDelta(t, 0.1, got.Importance, 1e-9)

	require.NoError(t, repo.UpdateImportance(ctx, "lesson-q", 0.9))
	got, err = repo.GetLessonByID(ctx, "lesson-q")
	require.NoError(t, err)
	assert.InDelta(t, 0.9, got.Importance, 1e-9)
}

func TestSetSupersededAndRelated(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	require.NoError(t, repo.CreateLesson(ctx, &dtorep.Lesson{
		ID: "lesson-old", Title: "old", Scope: "global", Status: "confirmed", Path: "/tmp/old.md",
	}))
	require.NoError(t, repo.CreateLesson(ctx, &dtorep.Lesson{
		ID: "lesson-new", Title: "new", Scope: "global", Status: "confirmed", Path: "/tmp/new.md",
	}))

	require.NoError(t, repo.SetSuperseded(ctx, "lesson-old", "lesson-new"))
	got, err := repo.GetLessonByID(ctx, "lesson-old")
	require.NoError(t, err)
	assert.Equal(t, "superseded", got.Status)
	require.NotNil(t, got.SupersededBy)
	assert.Equal(t, "lesson-new", *got.SupersededBy)

	// superseded несуществующего → ErrNotFound
	assert.ErrorIs(t, repo.SetSuperseded(ctx, "lesson-nope", "lesson-new"), cstmerrors.ErrNotFound)

	require.NoError(t, repo.UpdateRelated(ctx, "lesson-new", `["lesson-old"]`))
	got, err = repo.GetLessonByID(ctx, "lesson-new")
	require.NoError(t, err)
	assert.JSONEq(t, `["lesson-old"]`, got.RelatedJSON)

	// статус outdated валиден в новом CHECK
	require.NoError(t, repo.UpdateLessonStatus(ctx, "lesson-new", "outdated"))
	got, err = repo.GetLessonByID(ctx, "lesson-new")
	require.NoError(t, err)
	assert.Equal(t, "outdated", got.Status)
	assert.WithinDuration(t, time.Now(), *got.UpdatedAt, time.Minute)
}

// Package lessons — репозиторий карточек уроков (D-52, T-29).
package lessons

import (
	"context"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/pkg/commontx"
)

type Queries interface {
	CreateLesson(ctx context.Context, lesson *dtorep.Lesson) error
	GetLessonByID(ctx context.Context, id string) (*dtorep.Lesson, error)
	GetLessonByPath(ctx context.Context, path string) (*dtorep.Lesson, error)
	GetByIDs(ctx context.Context, ids []string) ([]dtorep.Lesson, error)
	ListLessons(ctx context.Context, status, scope string, projectID *int64) ([]dtorep.Lesson, error)
	ListByKind(ctx context.Context, kind, status string) ([]dtorep.Lesson, error)
	ListVendor(ctx context.Context, vendor, status string) ([]dtorep.Lesson, error)
	ListAttention(ctx context.Context) ([]dtorep.Lesson, error)
	ListSupersededOlderThan(ctx context.Context, cutoff time.Time) ([]dtorep.Lesson, error)
	UpdateLessonStatus(ctx context.Context, id, status string) error
	UpdateLessonContent(ctx context.Context, id, title, triggersJSON, path string) error
	UpdateRelated(ctx context.Context, id, relatedJSON string) error
	UpdateImportance(ctx context.Context, id string, importance float64) error
	IncrementApplied(ctx context.Context, id string) error
	IncrementAppliedSuccess(ctx context.Context, id string) error
	IncrementRelapse(ctx context.Context, id string) error
	SetSuperseded(ctx context.Context, id, supersededBy string) error
}

type RepositoryWithTX interface {
	OpenTx(ctx context.Context) (Tx, error)
	Queries
}

type Tx interface {
	Queries
	commontx.Tx
}

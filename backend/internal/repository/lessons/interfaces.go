// Package lessons — репозиторий карточек уроков (D-52, T-29).
package lessons

import (
	"context"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/pkg/commontx"
)

type Queries interface {
	CreateLesson(ctx context.Context, lesson *dtorep.Lesson) error
	GetLessonByID(ctx context.Context, id string) (*dtorep.Lesson, error)
	ListLessons(ctx context.Context, status, scope string, projectID *int64) ([]dtorep.Lesson, error)
	UpdateLessonStatus(ctx context.Context, id, status string) error
	UpdateLessonContent(ctx context.Context, id, title, triggersJSON, path string) error
	IncrementApplied(ctx context.Context, id string) error
	IncrementRelapse(ctx context.Context, id string) error
}

type RepositoryWithTX interface {
	OpenTx(ctx context.Context) (Tx, error)
	Queries
}

type Tx interface {
	Queries
	commontx.Tx
}

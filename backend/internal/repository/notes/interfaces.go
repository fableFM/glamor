// Package notes — репозиторий заметок (queue note / steer, D-22).
package notes

import (
	"context"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/pkg/commontx"
)

type Queries interface {
	CreateNote(ctx context.Context, req dtorep.CreateNoteRequest) (int64, error)
	GetNoteByIdempotencyKey(ctx context.Context, key string) (*dtorep.Note, error)
	ListNotesByRun(ctx context.Context, runID string) ([]dtorep.Note, error)
	// ListUnconsumedNotes — очередь заметок рана (consumed=0), FIFO.
	ListUnconsumedNotes(ctx context.Context, runID string) ([]dtorep.Note, error)
	// ListUnconsumedSteers — steer-сообщения для конкретной стадии (D-22).
	ListUnconsumedSteers(ctx context.Context, stageID int64) ([]dtorep.Note, error)
	MarkNoteConsumed(ctx context.Context, id int64) error
}

type RepositoryWithTX interface {
	OpenTx(ctx context.Context) (Tx, error)
	Queries
}

type Tx interface {
	Queries
	commontx.Tx
}

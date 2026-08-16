// Package events — репозиторий append-only журнала событий (D-11).
package events

import (
	"context"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/pkg/commontx"
)

type Queries interface {
	// AppendEvent — вставка события; вызывается внутри транзакции перехода
	// состояния (D-11: outbox в том же tx). Возвращает монотонный id.
	AppendEvent(ctx context.Context, ev dtorep.Event) (int64, error)
	// ReplayEvents — страница событий с id > afterID (догон клиентов, T-04).
	// runID = dtorep.RunIDAll («*») — события всех ранов.
	ReplayEvents(ctx context.Context, runID string, afterID int64, limit int) ([]dtorep.Event, error)
	// LatestEventID — максимальный id в журнале (0 для пустого).
	LatestEventID(ctx context.Context) (int64, error)
}

type RepositoryWithTX interface {
	OpenTx(ctx context.Context) (Tx, error)
	Queries
}

type Tx interface {
	Queries
	commontx.Tx
}

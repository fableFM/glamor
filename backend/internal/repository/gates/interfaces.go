// Package gates — репозиторий гейтов (D-21).
package gates

import (
	"context"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/pkg/commontx"
)

type Queries interface {
	CreateGate(ctx context.Context, req dtorep.CreateGateRequest) error
	GetGateByID(ctx context.Context, id string) (*dtorep.Gate, error)
	GetGateByIdempotencyKey(ctx context.Context, key string) (*dtorep.Gate, error)
	// ListOpenGates — открытые гейты рана (движок держит run в waiting_gate,
	// пока список не пуст, ADR-001).
	ListOpenGates(ctx context.Context, runID string) ([]dtorep.Gate, error)
	// ListGatesByRun — все гейты рана (срез рана для API).
	ListGatesByRun(ctx context.Context, runID string) ([]dtorep.Gate, error)
	// ResolveGate — CAS-резолв: UPDATE ... WHERE id = ? AND state = 'open'.
	ResolveGate(ctx context.Context, id string, to dtorep.GateState, answer *string, resolvedAt time.Time) (bool, error)
}

type RepositoryWithTX interface {
	OpenTx(ctx context.Context) (Tx, error)
	Queries
}

type Tx interface {
	Queries
	commontx.Tx
}

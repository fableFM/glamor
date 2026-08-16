package gates

import (
	"github.com/fableFM/glamor/internal/dto/dtorep"
)

func mapGateToDTO(g gate) dtorep.Gate {
	out := dtorep.Gate{
		ID:             g.id,
		RunID:          g.runID,
		Kind:           dtorep.GateKind(g.kind),
		Question:       g.question,
		ContextJSON:    g.contextJSON,
		State:          dtorep.GateState(g.state),
		IdempotencyKey: g.idempotencyKey,
		CreatedAt:      g.createdAt,
	}
	if g.stageID.Valid {
		out.StageID = &g.stageID.Int64
	}
	if g.answer.Valid {
		out.Answer = &g.answer.String
	}
	if g.resolvedAt.Valid {
		t := g.resolvedAt.Time
		out.ResolvedAt = &t
	}
	return out
}

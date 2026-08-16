package events

import (
	"github.com/fableFM/glamor/internal/dto/dtorep"
)

func mapEventToDTO(e event) dtorep.Event {
	out := dtorep.Event{
		ID:          e.id,
		RunID:       e.runID,
		TS:          e.ts,
		Kind:        e.kind,
		PayloadJSON: e.payloadJSON,
	}
	if e.stageID.Valid {
		out.StageID = &e.stageID.Int64
	}
	return out
}

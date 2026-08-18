package runs

import (
	"github.com/fableFM/glamor/internal/dto/dtorep"
)

func mapRunToDTO(r run) dtorep.Run {
	out := dtorep.Run{
		ID:                r.id,
		ProjectID:         r.projectID,
		PipelineVersionID: r.pipelineVersionID,
		TaskText:          r.taskText,
		BaseBranch:        r.baseBranch,
		Branch:            r.branch,
		State:             dtorep.RunState(r.state),
		Depth:             r.depth,
		NotifyTG:          r.notifyTG,
		IdempotencyKey:    r.idempotencyKey,
		CreatedAt:         r.createdAt,
	}
	if r.finishedAt.Valid {
		t := r.finishedAt.Time
		out.FinishedAt = &t
	}
	if r.tgRootMessageID.Valid {
		id := r.tgRootMessageID.Int64
		out.TgRootMessageID = &id
	}
	return out
}

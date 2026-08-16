package pipelines

import (
	"github.com/fableFM/glamor/internal/dto/dtorep"
)

func mapPipelineToDTO(p pipeline) dtorep.Pipeline {
	out := dtorep.Pipeline{
		ID:        p.id,
		Name:      p.name,
		Version:   p.version,
		SpecJSON:  p.specJSON,
		CreatedAt: p.createdAt,
	}
	if p.projectID.Valid {
		out.ProjectID = &p.projectID.Int64
	}
	if p.parentVersionID.Valid {
		out.ParentVersionID = &p.parentVersionID.Int64
	}
	return out
}

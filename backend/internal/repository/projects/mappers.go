package projects

import "github.com/fableFM/glamor/internal/dto/dtorep"

func mapProjectToDTO(p project) dtorep.Project {
	return dtorep.Project{
		ID:            p.id,
		Path:          p.path,
		Name:          p.name,
		DefaultBranch: p.defaultBranch,
		IDECommand:    p.ideCommand,
		CreatedAt:     p.createdAt,
	}
}

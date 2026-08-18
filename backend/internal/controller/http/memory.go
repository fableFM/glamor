package http

import (
	"context"
	"fmt"

	"github.com/fableFM/glamor/internal/controller/http/genapi"
	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/service/vendormemory"
)

// --- vendor-память (T-23) ----------------------------------------------------

func mapMemoryFile(f *vendormemory.MemoryFile) genapi.MemoryFileEntry {
	return genapi.MemoryFileEntry{
		Path:      f.Path,
		Vendor:    f.Vendor,
		Size:      f.Size,
		UpdatedAt: f.UpdatedAt,
	}
}

func (h *handlers) MemoryTree(ctx context.Context, _ genapi.MemoryTreeRequestObject) (genapi.MemoryTreeResponseObject, error) {
	global, err := h.memory.TreeGlobal()
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.MemoryTreedefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	resp := genapi.MemoryTree200JSONResponse{
		Global: mapSlice(global, mapMemoryFile),
	}

	projects, err := h.api.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		files, err := h.memory.TreeProject(p.Path)
		if err != nil {
			e, status := errorToResponse(err)
			return genapi.MemoryTreedefaultJSONResponse{Body: e, StatusCode: status}, nil
		}
		resp.Projects = append(resp.Projects, struct {
			Files       []genapi.MemoryFileEntry `json:"files"`
			ProjectId   int64                    `json:"project_id"`
			ProjectName string                   `json:"project_name"`
		}{
			ProjectId:   p.ID,
			ProjectName: p.Name,
			Files:       mapSlice(files, mapMemoryFile),
		})
	}
	return resp, nil
}

func (h *handlers) MemoryReadFile(ctx context.Context, req genapi.MemoryReadFileRequestObject) (genapi.MemoryReadFileResponseObject, error) {
	projectPath, err := h.memoryProjectPath(ctx, string(req.Params.Scope), req.Params.ProjectId)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.MemoryReadFiledefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	content, err := h.memory.ReadFile(string(req.Params.Scope), projectPath, req.Params.Vendor)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.MemoryReadFiledefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.MemoryReadFile200JSONResponse(genapi.MemoryFileContent{
		Vendor:  req.Params.Vendor,
		Scope:   string(req.Params.Scope),
		Content: content,
	}), nil
}

func (h *handlers) MemoryWriteFile(ctx context.Context, req genapi.MemoryWriteFileRequestObject) (genapi.MemoryWriteFileResponseObject, error) {
	projectPath, err := h.memoryProjectPath(ctx, string(req.Body.Scope), req.Body.ProjectId)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.MemoryWriteFiledefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	if err := h.memory.WriteFile(ctx, string(req.Body.Scope), projectPath, req.Body.Vendor, req.Body.Content); err != nil {
		e, status := errorToResponse(err)
		return genapi.MemoryWriteFiledefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.MemoryWriteFile200JSONResponse(genapi.MemoryFileContent{
		Vendor:  req.Body.Vendor,
		Scope:   string(req.Body.Scope),
		Content: req.Body.Content,
	}), nil
}

func (h *handlers) MemoryHistory(_ context.Context, req genapi.MemoryHistoryRequestObject) (genapi.MemoryHistoryResponseObject, error) {
	entries, err := h.memory.History(req.Params.Vendor)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.MemoryHistorydefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	return genapi.MemoryHistory200JSONResponse(mapSlice(entries, func(e *vendormemory.HistoryEntry) genapi.MemoryHistoryEntry {
		return genapi.MemoryHistoryEntry{Hash: e.Hash, Date: e.Date, Message: e.Message}
	})), nil
}

func (h *handlers) MemoryPromote(ctx context.Context, req genapi.MemoryPromoteRequestObject) (genapi.MemoryPromoteResponseObject, error) {
	project, err := h.api.GetProject(ctx, req.Body.ProjectId)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.MemoryPromotedefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	path, err := h.memory.PromoteToGlobal(ctx, project.Path, req.Body.Vendor)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.MemoryPromotedefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.MemoryPromote200JSONResponse{Path: &path}, nil
}

// memoryProjectPath резолвит путь проекта для project-scope операций.
func (h *handlers) memoryProjectPath(ctx context.Context, scope string, projectID *int64) (string, error) {
	if scope != "project" {
		return "", nil
	}
	if projectID == nil {
		return "", fmt.Errorf("project_id is required for scope=project: %w", cstmerrors.ErrValidation)
	}
	project, err := h.api.GetProject(ctx, *projectID)
	if err != nil {
		return "", err
	}
	return project.Path, nil
}

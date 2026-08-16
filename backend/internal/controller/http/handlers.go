package http

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/fableFM/glamor/internal/controller/http/genapi"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	artifactsrep "github.com/fableFM/glamor/internal/repository/artifacts"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	usecase "github.com/fableFM/glamor/internal/usecase/runs"
)

// handlers — реализация genapi.StrictServerInterface.
type handlers struct {
	draining  *atomic.Bool
	machine   *usecase.Machine
	journal   *events.Journal
	projects  projectsrep.RepositoryWithTX
	pipelines pipelinesrep.RepositoryWithTX
	runs      runsrep.RepositoryWithTX
	stages    stagesrep.RepositoryWithTX
	gates     gatesrep.RepositoryWithTX
	artifacts artifactsrep.RepositoryWithTX
	notes     notesrep.RepositoryWithTX
	version   string
	harnesses []string
}

// --- system ------------------------------------------------------------------

func (h *handlers) Healthz(_ context.Context, _ genapi.HealthzRequestObject) (genapi.HealthzResponseObject, error) {
	return genapi.Healthz200TextResponse("ok"), nil
}

func (h *handlers) Version(_ context.Context, _ genapi.VersionRequestObject) (genapi.VersionResponseObject, error) {
	info := genapi.VersionInfo{Version: h.version}
	if len(h.harnesses) > 0 {
		info.Harnesses = &h.harnesses
	}
	return genapi.Version200JSONResponse(info), nil
}

// EventsWs обслуживается отдельным контроллером (internal/controller/ws) —
// здесь недостижимо (родительский mux перехватывает /ws).
func (h *handlers) EventsWs(_ context.Context, _ genapi.EventsWsRequestObject) (genapi.EventsWsResponseObject, error) {
	return nil, errors.New("ws endpoint is served by internal/controller/ws")
}

// --- projects ----------------------------------------------------------------

func (h *handlers) ListProjects(ctx context.Context, _ genapi.ListProjectsRequestObject) (genapi.ListProjectsResponseObject, error) {
	projects, err := h.projects.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	return genapi.ListProjects200JSONResponse(mapSlice(projects, mapProject)), nil
}

func (h *handlers) CreateProject(ctx context.Context, req genapi.CreateProjectRequestObject) (genapi.CreateProjectResponseObject, error) {
	// идемпотентность по path (UNIQUE): существующий проект → 200
	existing, err := h.projects.GetProjectByPath(ctx, req.Body.Path)
	if err == nil {
		return genapi.CreateProject200JSONResponse(mapProject(existing)), nil
	}

	id, err := h.projects.CreateProject(ctx, dtorep.CreateProjectRequest{
		Path:          req.Body.Path,
		Name:          req.Body.Name,
		DefaultBranch: deref(req.Body.DefaultBranch),
		IDECommand:    deref(req.Body.IdeCommand),
	})
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.CreateProjectdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	project, err := h.projects.GetProjectByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return genapi.CreateProject201JSONResponse(mapProject(project)), nil
}

func (h *handlers) GetProject(ctx context.Context, req genapi.GetProjectRequestObject) (genapi.GetProjectResponseObject, error) {
	project, err := h.projects.GetProjectByID(ctx, req.Id)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.GetProjectdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	activeStates := []dtorep.RunState{
		dtorep.RunStateDraft, dtorep.RunStateRunning, dtorep.RunStateWaitingGate,
	}
	activeRuns, err := h.runs.ListRuns(ctx, dtorep.ListRunsRequest{
		ProjectID: &req.Id,
		States:    activeStates,
	})
	if err != nil {
		return nil, err
	}

	openGates := 0
	for _, run := range activeRuns {
		gates, err := h.gates.ListOpenGates(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		openGates += len(gates)
	}

	p := mapProject(project)
	return genapi.GetProject200JSONResponse(genapi.ProjectDetail{
		Id:            p.Id,
		Path:          p.Path,
		Name:          p.Name,
		DefaultBranch: p.DefaultBranch,
		IdeCommand:    p.IdeCommand,
		CreatedAt:     p.CreatedAt,
		ActiveRuns:    mapSlice(activeRuns, mapRun),
		OpenGates:     openGates,
	}), nil
}

func (h *handlers) PatchProject(ctx context.Context, req genapi.PatchProjectRequestObject) (genapi.PatchProjectResponseObject, error) {
	err := h.projects.UpdateProject(ctx, req.Id, dtorep.PatchProjectRequest{
		DefaultBranch: req.Body.DefaultBranch,
		IDECommand:    req.Body.IdeCommand,
	})
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.PatchProjectdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	project, err := h.projects.GetProjectByID(ctx, req.Id)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.PatchProjectdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.PatchProject200JSONResponse(mapProject(project)), nil
}

// --- pipelines ---------------------------------------------------------------

func (h *handlers) ListProjectPipelines(ctx context.Context, req genapi.ListProjectPipelinesRequestObject) (genapi.ListProjectPipelinesResponseObject, error) {
	pipelines, err := h.pipelines.ListPipelinesForProject(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return genapi.ListProjectPipelines200JSONResponse(mapSlice(pipelines, mapPipeline)), nil
}

func (h *handlers) GetPipeline(ctx context.Context, req genapi.GetPipelineRequestObject) (genapi.GetPipelineResponseObject, error) {
	pipeline, err := h.pipelines.GetPipelineByID(ctx, req.Id)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.GetPipelinedefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	versions, err := h.pipelines.ListPipelineVersions(ctx, pipeline.ProjectID, pipeline.Name)
	if err != nil {
		return nil, err
	}

	p := mapPipeline(pipeline)
	return genapi.GetPipeline200JSONResponse(genapi.PipelineDetail{
		Id:              p.Id,
		ProjectId:       p.ProjectId,
		Name:            p.Name,
		Version:         p.Version,
		ParentVersionId: p.ParentVersionId,
		SpecJson:        p.SpecJson,
		CreatedAt:       p.CreatedAt,
		Versions:        mapSlice(versions, mapPipeline),
	}), nil
}

// --- runs --------------------------------------------------------------------

func (h *handlers) CreateRun(ctx context.Context, req genapi.CreateRunRequestObject) (genapi.CreateRunResponseObject, error) {
	if h.draining.Load() {
		return genapi.CreateRundefaultJSONResponse{
			Body:       genapi.Error{Code: "draining", Message: "daemon is shutting down, new runs are not accepted"},
			StatusCode: 503,
		}, nil
	}

	var idempotencyKey string
	if req.Params.IdempotencyKey != nil {
		idempotencyKey = *req.Params.IdempotencyKey
	}

	run, alreadyExisted, err := h.machine.CreateRun(ctx, usecase.CreateRunParams{
		ProjectID:         req.Body.ProjectId,
		PipelineVersionID: req.Body.PipelineVersionId,
		TaskText:          req.Body.TaskText,
		BaseBranch:        deref(req.Body.BaseBranch),
		Branch:            deref(req.Body.Branch),
		Depth:             deref(req.Body.Depth),
		NotifyTG:          derefDefault(req.Body.NotifyTg, true),
		Force:             deref(req.Body.Force),
		IdempotencyKey:    idempotencyKey,
	})
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.CreateRundefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	if alreadyExisted {
		return genapi.CreateRun200JSONResponse(mapRun(run)), nil
	}
	return genapi.CreateRun201JSONResponse(mapRun(run)), nil
}

func (h *handlers) ListRuns(ctx context.Context, req genapi.ListRunsRequestObject) (genapi.ListRunsResponseObject, error) {
	filter := dtorep.ListRunsRequest{
		ProjectID:         req.Params.ProjectId,
		PipelineVersionID: req.Params.PipelineId,
	}
	if req.Params.State != nil {
		filter.States = []dtorep.RunState{dtorep.RunState(*req.Params.State)}
	}
	if req.Params.Limit != nil {
		filter.Limit = *req.Params.Limit
	}

	runs, err := h.runs.ListRuns(ctx, filter)
	if err != nil {
		return nil, err
	}
	return genapi.ListRuns200JSONResponse(mapSlice(runs, mapRun)), nil
}

func (h *handlers) GetRun(ctx context.Context, req genapi.GetRunRequestObject) (genapi.GetRunResponseObject, error) {
	run, err := h.runs.GetRunByID(ctx, req.Id)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.GetRundefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	stages, err := h.stages.ListStagesByRun(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	gates, err := h.gates.ListGatesByRun(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	artifacts, err := h.artifacts.ListArtifactsByRun(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	notes, err := h.notes.ListNotesByRun(ctx, req.Id)
	if err != nil {
		return nil, err
	}

	r := mapRun(run)
	return genapi.GetRun200JSONResponse(genapi.RunDetail{
		Id:                r.Id,
		ProjectId:         r.ProjectId,
		PipelineVersionId: r.PipelineVersionId,
		TaskText:          r.TaskText,
		BaseBranch:        r.BaseBranch,
		Branch:            r.Branch,
		State:             r.State,
		Depth:             r.Depth,
		NotifyTg:          r.NotifyTg,
		CreatedAt:         r.CreatedAt,
		FinishedAt:        r.FinishedAt,
		Stages:            mapSlice(stages, mapStage),
		Gates:             mapSlice(gates, mapGate),
		Artifacts:         mapSlice(artifacts, mapArtifact),
		Notes:             mapSlice(notes, mapNote),
	}), nil
}

func (h *handlers) StopRun(ctx context.Context, req genapi.StopRunRequestObject) (genapi.StopRunResponseObject, error) {
	run, err := h.machine.StopRun(ctx, req.Id)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.StopRundefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.StopRun200JSONResponse(mapRun(run)), nil
}

func (h *handlers) ResumeRun(ctx context.Context, req genapi.ResumeRunRequestObject) (genapi.ResumeRunResponseObject, error) {
	run, err := h.machine.ResumeRun(ctx, req.Id)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.ResumeRundefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.ResumeRun200JSONResponse(mapRun(run)), nil
}

func (h *handlers) CreateNote(ctx context.Context, req genapi.CreateNoteRequestObject) (genapi.CreateNoteResponseObject, error) {
	var idempotencyKey string
	if req.Params.IdempotencyKey != nil {
		idempotencyKey = *req.Params.IdempotencyKey
	}

	note, err := h.machine.CreateNote(ctx, req.Id, req.Body.Text, idempotencyKey)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.CreateNotedefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.CreateNote201JSONResponse(mapNote(note)), nil
}

func (h *handlers) InterruptStage(ctx context.Context, req genapi.InterruptStageRequestObject) (genapi.InterruptStageResponseObject, error) {
	stage, err := h.machine.InterruptStageSteer(ctx, req.Id, req.Body.Message)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.InterruptStagedefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.InterruptStage200JSONResponse(mapStage(stage)), nil
}

// --- gates -------------------------------------------------------------------

func (h *handlers) ResolveGate(ctx context.Context, req genapi.ResolveGateRequestObject) (genapi.ResolveGateResponseObject, error) {
	alreadyResolved, err := h.machine.ResolveGateAPI(ctx, req.Id,
		usecase.GateAction(req.Body.Action), req.Body.Text)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.ResolveGatedefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	gate, err := h.gates.GetGateByID(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return genapi.ResolveGate200JSONResponse(genapi.ResolveGateResponse{
		Gate:            mapGate(gate),
		AlreadyResolved: alreadyResolved,
	}), nil
}

// --- events ------------------------------------------------------------------

func (h *handlers) ListRunEvents(ctx context.Context, req genapi.ListRunEventsRequestObject) (genapi.ListRunEventsResponseObject, error) {
	if _, err := h.runs.GetRunByID(ctx, req.Id); err != nil {
		e, status := errorToResponse(err)
		return genapi.ListRunEventsdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}

	runID := req.Id
	var afterID int64
	if req.Params.AfterId != nil {
		afterID = *req.Params.AfterId
	}
	limit := 0
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}

	evs, err := h.journal.Replay(ctx, runID, afterID, limit)
	if err != nil {
		return nil, err
	}
	_ = eventsrep.RunIDAll // run_id всегда конкретный в этом endpoint
	return genapi.ListRunEvents200JSONResponse(mapSlice(evs, mapEvent)), nil
}

// --- helpers -----------------------------------------------------------------

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func derefDefault[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

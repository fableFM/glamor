// Package catalog — read-фасад и CRUD-сценарии HTTP API, которым не нужна
// стейт-машина: чтения-агрегации проектов/пайплайнов/ранов (D-80:
// controller → service → repository; контроллер репозитории не знает).
package catalog

import (
	"context"
	"fmt"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	artifactsrep "github.com/fableFM/glamor/internal/repository/artifacts"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
)

// ActiveRunStates — «активные» состояния рана для сводки проекта
// (доменное правило service-слоя, не транспорта).
var ActiveRunStates = []dtorep.RunState{
	dtorep.RunStateDraft, dtorep.RunStateRunning, dtorep.RunStateWaitingGate,
}

// Service — фасад API-сценариев. Зависимости — готовые интерфейсы
// репозиториев (ручной DI в main, D-80).
type Service struct {
	projects  projectsrep.RepositoryWithTX
	pipelines pipelinesrep.RepositoryWithTX
	runs      runsrep.RepositoryWithTX
	stages    stagesrep.RepositoryWithTX
	gates     gatesrep.RepositoryWithTX
	artifacts artifactsrep.RepositoryWithTX
	notes     notesrep.RepositoryWithTX
	journal   *events.Journal
}

func New(
	projects projectsrep.RepositoryWithTX,
	pipelines pipelinesrep.RepositoryWithTX,
	runs runsrep.RepositoryWithTX,
	stages stagesrep.RepositoryWithTX,
	gates gatesrep.RepositoryWithTX,
	artifacts artifactsrep.RepositoryWithTX,
	notes notesrep.RepositoryWithTX,
	journal *events.Journal,
) *Service {
	return &Service{
		projects:  projects,
		pipelines: pipelines,
		runs:      runs,
		stages:    stages,
		gates:     gates,
		artifacts: artifacts,
		notes:     notes,
		journal:   journal,
	}
}

// --- projects ----------------------------------------------------------------

func (u *Service) ListProjects(ctx context.Context) ([]dtorep.Project, error) {
	return u.projects.ListProjects(ctx)
}

// CreateProject создаёт проект; идемпотентность по path (UNIQUE):
// существующий проект возвращается с alreadyExisted=true (контроллер по
// флагу выбирает 200/201).
func (u *Service) CreateProject(ctx context.Context, req dtorep.CreateProjectRequest) (project *dtorep.Project, alreadyExisted bool, err error) {
	if existing, err := u.projects.GetProjectByPath(ctx, req.Path); err == nil {
		return existing, true, nil
	}

	id, err := u.projects.CreateProject(ctx, req)
	if err != nil {
		return nil, false, err
	}
	project, err = u.projects.GetProjectByID(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load created project: %w", err)
	}
	return project, false, nil
}

// ProjectDetail — проект + активные раны + число открытых гейтов.
type ProjectDetail struct {
	Project    *dtorep.Project
	ActiveRuns []dtorep.Run
	OpenGates  int
}

func (u *Service) GetProjectDetail(ctx context.Context, id int64) (*ProjectDetail, error) {
	project, err := u.projects.GetProjectByID(ctx, id)
	if err != nil {
		return nil, err
	}

	activeRuns, err := u.runs.ListRuns(ctx, dtorep.ListRunsRequest{
		ProjectID: &id,
		States:    ActiveRunStates,
	})
	if err != nil {
		return nil, err
	}

	openGates := 0
	for _, run := range activeRuns {
		gates, err := u.gates.ListOpenGates(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		openGates += len(gates)
	}

	return &ProjectDetail{Project: project, ActiveRuns: activeRuns, OpenGates: openGates}, nil
}

func (u *Service) PatchProject(ctx context.Context, id int64, req dtorep.PatchProjectRequest) (*dtorep.Project, error) {
	if err := u.projects.UpdateProject(ctx, id, req); err != nil {
		return nil, err
	}
	return u.projects.GetProjectByID(ctx, id)
}

// --- pipelines ---------------------------------------------------------------

func (u *Service) ListProjectPipelines(ctx context.Context, projectID int64) ([]dtorep.Pipeline, error) {
	return u.pipelines.ListPipelinesForProject(ctx, projectID)
}

// PipelineDetail — версия пайплайна + все версии того же пайплайна.
type PipelineDetail struct {
	Pipeline *dtorep.Pipeline
	Versions []dtorep.Pipeline
}

func (u *Service) GetPipelineDetail(ctx context.Context, id int64) (*PipelineDetail, error) {
	pipeline, err := u.pipelines.GetPipelineByID(ctx, id)
	if err != nil {
		return nil, err
	}
	versions, err := u.pipelines.ListPipelineVersions(ctx, pipeline.ProjectID, pipeline.Name)
	if err != nil {
		return nil, err
	}
	return &PipelineDetail{Pipeline: pipeline, Versions: versions}, nil
}

// --- runs --------------------------------------------------------------------

func (u *Service) ListRuns(ctx context.Context, filter dtorep.ListRunsRequest) ([]dtorep.Run, error) {
	return u.runs.ListRuns(ctx, filter)
}

// RunDetail — полный срез рана (агрегация из 5 репозиториев).
type RunDetail struct {
	Run       *dtorep.Run
	Stages    []dtorep.Stage
	Gates     []dtorep.Gate
	Artifacts []dtorep.Artifact
	Notes     []dtorep.Note
}

func (u *Service) GetRunDetail(ctx context.Context, runID string) (*RunDetail, error) {
	run, err := u.runs.GetRunByID(ctx, runID)
	if err != nil {
		return nil, err
	}
	stages, err := u.stages.ListStagesByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	gates, err := u.gates.ListGatesByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	artifacts, err := u.artifacts.ListArtifactsByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	notes, err := u.notes.ListNotesByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	return &RunDetail{
		Run: run, Stages: stages, Gates: gates, Artifacts: artifacts, Notes: notes,
	}, nil
}

// --- events ------------------------------------------------------------------

// ListRunEvents — страница событий рана с id > afterID (догон клиентов).
// runID здесь всегда конкретный; wildcard dtorep.RunIDAll — для /ws.
func (u *Service) ListRunEvents(ctx context.Context, runID string, afterID int64, limit int) ([]dtorep.Event, error) {
	if _, err := u.runs.GetRunByID(ctx, runID); err != nil {
		return nil, err
	}
	return u.journal.Replay(ctx, runID, afterID, limit)
}

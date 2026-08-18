// Package catalog — read-фасад и CRUD-сценарии HTTP API, которым не нужна
// стейт-машина: чтения-агрегации проектов/пайплайнов/ранов (D-80:
// controller → service → repository; контроллер репозитории не знает).
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/cstmerrors"
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
// репозиториев (ручной DI в main, D-80). runsDir — корень каталогов
// артефактов ранов (~/.glamor/runs, конфиг supervisor.runs_dir, F-02).
type Service struct {
	projects  projectsrep.RepositoryWithTX
	pipelines pipelinesrep.RepositoryWithTX
	runs      runsrep.RepositoryWithTX
	stages    stagesrep.RepositoryWithTX
	gates     gatesrep.RepositoryWithTX
	artifacts artifactsrep.RepositoryWithTX
	notes     notesrep.RepositoryWithTX
	journal   *events.Journal
	runsDir   string
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
	runsDir string,
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
		runsDir:   runsDir,
	}
}

// --- projects ----------------------------------------------------------------

// DeleteProject — каскадное удаление проекта со всей историей.
// Активные раны удалять нельзя (сначала stop).
func (u *Service) DeleteProject(ctx context.Context, id int64) error {
	active, err := u.runs.ListRuns(ctx, dtorep.ListRunsRequest{
		ProjectID: &id,
		States:    []dtorep.RunState{dtorep.RunStateDraft, dtorep.RunStateRunning, dtorep.RunStateWaitingGate},
	})
	if err != nil {
		return err
	}
	if len(active) > 0 {
		return fmt.Errorf("project %d has %d active runs, stop them first: %w", id, len(active), cstmerrors.ErrValidation)
	}
	if err := u.projects.DeleteProjectCascade(ctx, id); err != nil {
		return err
	}
	return nil
}

// GetProject — проект по id.
func (u *Service) GetProject(ctx context.Context, id int64) (*dtorep.Project, error) {
	project, err := u.projects.GetProjectByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to load project: %w", err)
	}
	return project, nil
}

func (u *Service) ListProjects(ctx context.Context) ([]dtorep.Project, error) {
	return u.projects.ListProjects(ctx)
}

// CreateProject создаёт проект; идемпотентность по path (UNIQUE):
// существующий проект возвращается с alreadyExisted=true (контроллер по
// флагу выбирает 200/201). NotifyTgDefault нового проекта — true
// (DEFAULT 1 миграции 20260817121000, F-04).
func (u *Service) CreateProject(ctx context.Context, req dtorep.CreateProjectRequest) (project *dtorep.Project, alreadyExisted bool, err error) {
	if existing, err := u.projects.GetProjectByPath(ctx, req.Path); err == nil {
		return existing, true, nil
	}

	// дефолт IDE — goland (D-63, запрос пользователя 2026-08-17)
	if req.IDECommand == "" {
		req.IDECommand = "goland"
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

// --- artifact content (F-02, fix-task-4) --------------------------------------

// MaxArtifactContentBytes — cap отдачи содержимого артефакта (5 МБ);
// больше — ErrTooLarge (контроллер → 413, фронт предложит «скачать»).
const MaxArtifactContentBytes = 5 << 20

// ArtifactContent — содержимое файла артефакта.
type ArtifactContent struct {
	Name    string // базовое имя файла (для скачивания)
	Content []byte
}

// GetArtifactContent читает содержимое артефакта с диска. Путь берётся
// ТОЛЬКО из записи БД по (run_id, artifact_id); resolved path обязан
// оставаться внутри run_dir рана (защита от скомпрометированной записи).
// Чужой/несуществующий артефакт, выход за run_dir и отсутствующий файл —
// ErrNotFound (404); размер > cap — ErrTooLarge (413).
func (u *Service) GetArtifactContent(ctx context.Context, runID string, artifactID int64) (*ArtifactContent, error) {
	artifact, err := u.artifacts.GetArtifactByID(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if artifact.RunID != runID {
		return nil, fmt.Errorf("artifact %d of another run: %w", artifactID, cstmerrors.ErrNotFound)
	}

	runDir := filepath.Join(u.runsDir, runID)
	resolved := filepath.Clean(artifact.Path)
	if resolved != runDir && !strings.HasPrefix(resolved, runDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("artifact %d path escapes run dir: %w", artifactID, cstmerrors.ErrNotFound)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("artifact %d file: %w", artifactID, cstmerrors.ErrNotFound)
	}
	if info.Size() > MaxArtifactContentBytes {
		return nil, fmt.Errorf("artifact %d is %d bytes (cap %d): %w",
			artifactID, info.Size(), MaxArtifactContentBytes, cstmerrors.ErrTooLarge)
	}

	content, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("artifact %d file: %w", artifactID, cstmerrors.ErrNotFound)
	}
	return &ArtifactContent{Name: filepath.Base(resolved), Content: content}, nil
}

// --- telegram-адаптер (T-19, F-09) --------------------------------------------
// Бизнес-чтения других доменов для TG-пульта идут через service (D-80 доп.
// 2026-08-17): доменный пакет notify/telegram владеет только своим
// repository/telegram, чтения runs/stages/gates/projects — отсюда.

// GetRun — ран по id (пульт: mute-флаг, tg_root_message_id).
func (u *Service) GetRun(ctx context.Context, runID string) (*dtorep.Run, error) {
	return u.runs.GetRunByID(ctx, runID)
}

// GetGate — гейт по id (пульт: /stop reply на сообщение гейта).
func (u *Service) GetGate(ctx context.Context, gateID string) (*dtorep.Gate, error) {
	return u.gates.GetGateByID(ctx, gateID)
}

// GetStage — стадия по id (пульт: маппинг stage-событий в текст).
func (u *Service) GetStage(ctx context.Context, stageID int64) (*dtorep.Stage, error) {
	return u.stages.GetStageByID(ctx, stageID)
}

// GetRunByTgRootMessageID — ран по id корневого TG-сообщения «треда»
// (reply на корень → queue note / stop).
func (u *Service) GetRunByTgRootMessageID(ctx context.Context, messageID int64) (*dtorep.Run, error) {
	return u.runs.GetRunByTgRootMessageID(ctx, messageID)
}

// SetRunTgRootMessageID — CAS-установка корневого TG-сообщения рана;
// «уже выставлен конкурентным sender» (false) — не ошибка.
func (u *Service) SetRunTgRootMessageID(ctx context.Context, runID string, messageID int64) error {
	_, err := u.runs.SetTgRootMessageID(ctx, runID, messageID)
	return err
}

// StatusRun — срез активного рана для /status TG-пульта (read-агрегация).
type StatusRun struct {
	Run         *dtorep.Run
	ProjectName string
	Stages      []dtorep.Stage
	OpenGates   []dtorep.Gate
}

// ListActiveRunsStatus — running/waiting_gate раны + проект + стадии +
// открытые гейты каждого (команда /status, T-19).
func (u *Service) ListActiveRunsStatus(ctx context.Context) ([]StatusRun, error) {
	runs, err := u.runs.ListRuns(ctx, dtorep.ListRunsRequest{
		States: []dtorep.RunState{dtorep.RunStateRunning, dtorep.RunStateWaitingGate},
	})
	if err != nil {
		return nil, err
	}

	out := make([]StatusRun, 0, len(runs))
	for i := range runs {
		run := runs[i]
		entry := StatusRun{Run: &run, ProjectName: fmt.Sprintf("project#%d", run.ProjectID)}

		if project, err := u.projects.GetProjectByID(ctx, run.ProjectID); err == nil {
			entry.ProjectName = project.Name
		}
		stages, err := u.stages.ListStagesByRun(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		entry.Stages = stages
		openGates, err := u.gates.ListOpenGates(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		entry.OpenGates = openGates

		out = append(out, entry)
	}
	return out, nil
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

// ListPipelineVersionsByVersion — все версии пайплайна по id любой его
// версии (T-21).
func (u *Service) ListPipelineVersions(ctx context.Context, versionID int64) ([]dtorep.Pipeline, error) {
	pipeline, err := u.pipelines.GetPipelineByID(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load pipeline: %w", err)
	}
	versions, err := u.pipelines.ListPipelineVersions(ctx, pipeline.ProjectID, pipeline.Name)
	if err != nil {
		return nil, err
	}
	return versions, nil
}

// GetPipelineVersion — конкретная версия пайплайна (T-21).
func (u *Service) GetPipelineVersion(ctx context.Context, versionID int64) (*dtorep.Pipeline, error) {
	pipeline, err := u.pipelines.GetPipelineByID(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load pipeline version: %w", err)
	}
	return pipeline, nil
}

// --- метрики (D-51, T-24) ---------------------------------------------------

// StageMetrics — метрики попытки этапа.
type StageMetrics struct {
	StageID     int64
	StageKey    string
	Iteration   int64
	State       string
	TokensIn    int64
	TokensOut   int64
	DurationSec *float64
	ResumeCount int64
	ExitCode    *int64
}

// RunMetrics — метрики рана: per stage + итоги + ожидание гейтов.
type RunMetrics struct {
	RunID           string
	Stages          []StageMetrics
	TokensIn        int64
	TokensOut       int64
	DurationSec     *float64
	StagesCount     int
	GateWaitSeconds float64
}

// RunMetrics собирает метрики рана: токены/длительность из run_stages,
// gate_wait_seconds — производное событий журнала (не хранится).
func (u *Service) RunMetrics(ctx context.Context, runID string) (*RunMetrics, error) {
	run, err := u.runs.GetRunByID(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to load run: %w", err)
	}

	stages, err := u.stages.ListStagesByRun(ctx, runID)
	if err != nil {
		return nil, err
	}

	out := &RunMetrics{RunID: runID}
	for _, st := range stages {
		m := StageMetrics{
			StageID:     st.ID,
			StageKey:    st.StageKey,
			Iteration:   st.Iteration,
			State:       string(st.State),
			TokensIn:    st.TokensIn,
			TokensOut:   st.TokensOut,
			ResumeCount: st.ResumeCount,
			ExitCode:    st.ExitCode,
		}
		if st.StartedAt != nil && st.FinishedAt != nil {
			d := st.FinishedAt.Sub(*st.StartedAt).Seconds()
			m.DurationSec = &d
		}
		out.Stages = append(out.Stages, m)
		out.TokensIn += st.TokensIn
		out.TokensOut += st.TokensOut
	}
	out.StagesCount = len(out.Stages)
	if run.FinishedAt != nil {
		d := run.FinishedAt.Sub(run.CreatedAt).Seconds()
		out.DurationSec = &d
	}

	gateWait, err := u.gateWaitSeconds(ctx, runID)
	if err != nil {
		return nil, err
	}
	out.GateWaitSeconds = gateWait
	return out, nil
}

// journalEvents — все события рана из журнала (страницами).
func (u *Service) journalEvents(ctx context.Context, runID string) ([]dtorep.Event, error) {
	var out []dtorep.Event
	var afterID int64
	for {
		page, err := u.journal.Replay(ctx, runID, afterID, 1000)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return out, nil
		}
		out = append(out, page...)
		afterID = page[len(page)-1].ID
		if len(page) < 1000 {
			return out, nil
		}
	}
}

// gateWaitSeconds — суммарное время ожидания гейтов (интервалы
// gate.opened → gate.resolved по журналу).
func (u *Service) gateWaitSeconds(ctx context.Context, runID string) (float64, error) {
	events, err := u.journalEvents(ctx, runID)
	if err != nil {
		return 0, err
	}

	type gateTimes struct {
		opened   time.Time
		resolved *time.Time
	}
	gates := map[string]*gateTimes{} // ключ: gate_id из payload (или stage для opened)

	for _, ev := range events {
		switch ev.Kind {
		case "gate.opened":
			// gate_id (контракт GatePayload); "ID"/"id" — легаси события
			// до 2026-08-18 (json.Marshal без тегов)
			var p struct {
				GateID   string `json:"gate_id"`
				ID       string `json:"id"`
				LegacyID string `json:"ID"`
			}
			if err := json.Unmarshal([]byte(ev.PayloadJSON), &p); err == nil {
				id := p.GateID
				if id == "" {
					id = p.ID
				}
				if id == "" {
					id = p.LegacyID
				}
				if id != "" {
					gates[id] = &gateTimes{opened: ev.TS}
				}
			}
		case "gate.resolved":
			var p struct {
				GateID string `json:"gate_id"`
			}
			if err := json.Unmarshal([]byte(ev.PayloadJSON), &p); err == nil && p.GateID != "" {
				if g, ok := gates[p.GateID]; ok && g.resolved == nil {
					ts := ev.TS
					g.resolved = &ts
				}
			}
		}
	}

	var total float64
	now := time.Now()
	for _, g := range gates {
		if g.resolved != nil {
			total += g.resolved.Sub(g.opened).Seconds()
		} else {
			total += now.Sub(g.opened).Seconds() // открытый гейт ждёт до сих пор
		}
	}
	return total, nil
}

// ProjectMetrics — агрегация по ранам проекта за период.
type ProjectMetrics struct {
	ProjectID        int64
	Period           string
	RunsTotal        int
	TokensIn         int64
	TokensOut        int64
	TotalDurationSec float64
	ByState          map[string]int
}

// ProjectMetrics агрегирует метрики ранов проекта за период
// (24h/7d/30d/all; фильтр по created_at через sqlbuilder, T-24).
func (u *Service) ProjectMetrics(ctx context.Context, projectID int64, period string) (*ProjectMetrics, error) {
	if _, err := u.projects.GetProjectByID(ctx, projectID); err != nil {
		return nil, fmt.Errorf("failed to load project: %w", err)
	}

	var since *time.Time
	switch period {
	case "24h":
		t := time.Now().Add(-24 * time.Hour)
		since = &t
	case "30d":
		t := time.Now().Add(-30 * 24 * time.Hour)
		since = &t
	case "all":
	default: // "7d" и неизвестные — неделя
		period = "7d"
		t := time.Now().Add(-7 * 24 * time.Hour)
		since = &t
	}

	runs, err := u.runs.ListRuns(ctx, dtorep.ListRunsRequest{
		ProjectID: &projectID,
		Since:     since,
	})
	if err != nil {
		return nil, err
	}

	out := &ProjectMetrics{
		ProjectID: projectID,
		Period:    period,
		ByState:   map[string]int{},
	}
	for _, run := range runs {
		out.RunsTotal++
		out.ByState[string(run.State)]++
		if run.FinishedAt != nil {
			out.TotalDurationSec += run.FinishedAt.Sub(run.CreatedAt).Seconds()
		}

		stages, err := u.stages.ListStagesByRun(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		for _, st := range stages {
			out.TokensIn += st.TokensIn
			out.TokensOut += st.TokensOut
		}
	}
	return out, nil
}

// --- файловый браузер для выбора папки проекта (T-16) -------------------------

// FsDirEntry — подкаталог в листинге.
type FsDirEntry struct {
	Name      string
	Path      string
	IsGitRepo bool
}

// BrowseDir — листинг подкаталогов (локальный демон, endpoint за токеном).
// Скрытые каталоги пропускаем (кроме детекции .git).
func (u *Service) BrowseDir(path string) (parent *string, dirs []FsDirEntry, err error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get home dir: %w", err)
		}
		path = home
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory: %w", path, cstmerrors.ErrValidation)
	}

	if parentDir := filepath.Dir(path); parentDir != path {
		parent = &parentDir
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read dir %s: %w", path, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		full := filepath.Join(path, entry.Name())
		_, gitErr := os.Stat(filepath.Join(full, ".git"))
		dirs = append(dirs, FsDirEntry{
			Name:      entry.Name(),
			Path:      full,
			IsGitRepo: gitErr == nil,
		})
	}
	return parent, dirs, nil
}

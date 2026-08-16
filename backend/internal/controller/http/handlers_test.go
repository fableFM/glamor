package http_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpctrl "github.com/fableFM/glamor/internal/controller/http"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/repository"
	artifactsrep "github.com/fableFM/glamor/internal/repository/artifacts"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/service/catalog"
	"github.com/fableFM/glamor/internal/service/runsapi"
	"github.com/fableFM/glamor/internal/service/runsmachine"
	_ "github.com/fableFM/glamor/migrations"
	"github.com/fableFM/glamor/pkg/uuid"
)

const testToken = "test-token"

type fixture struct {
	server    *httptest.Server
	machine   *runsmachine.Machine
	client    *http.Client
	pipelines pipelinesrep.RepositoryWithTX
	db        *sql.DB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	ctx := context.Background()
	dbPath := t.TempDir() + "/test.db"
	db, err := repository.Open(ctx, dbPath)
	require.NoError(t, err)
	require.NoError(t, repository.Migrate(ctx, db))
	t.Cleanup(func() { _ = db.Close() })

	hub := events.NewHub()
	journal := events.NewJournal(eventsrep.NewRepository(db), repository.NewTxManager(db), hub)
	machine := runsmachine.NewMachine(
		runsrep.NewRepository(db), stagesrep.NewRepository(db),
		gatesrep.NewRepository(db), pipelinesrep.NewRepository(db),
		projectsrep.NewRepository(db), notesrep.NewRepository(db),
		journal, journal)
	runsSvc := runsapi.New(machine, journal, journal,
		runsrep.NewRepository(db), stagesrep.NewRepository(db),
		notesrep.NewRepository(db), projectsrep.NewRepository(db),
		pipelinesrep.NewRepository(db), gatesrep.NewRepository(db))

	rest := httpctrl.NewHandler(httpctrl.Deps{
		Machine: runsSvc,
		API: catalog.New(
			projectsrep.NewRepository(db), pipelinesrep.NewRepository(db),
			runsrep.NewRepository(db), stagesrep.NewRepository(db),
			gatesrep.NewRepository(db), artifactsrep.NewRepository(db),
			notesrep.NewRepository(db), journal),
		Token:   testToken,
		Version: "test",
	})

	server := httptest.NewServer(rest)
	t.Cleanup(server.Close)
	return &fixture{server: server, machine: machine, pipelines: pipelinesrep.NewRepository(db), db: db, client: server.Client()}
}

func (f *fixture) do(t *testing.T, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, f.server.URL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testToken)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := f.client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var out map[string]any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out)
	}
	return resp.StatusCode, out
}

func (f *fixture) createProject(t *testing.T, path string) map[string]any {
	t.Helper()
	status, body := f.do(t, "POST", "/projects", map[string]any{
		"path": path, "name": "test", "default_branch": "main",
	}, nil)
	require.Equal(t, 201, status, "body: %v", body)
	return body
}

func (f *fixture) createPipeline(t *testing.T) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := f.pipelines.CreatePipeline(ctx, dtorep.CreatePipelineRequest{
		Name: "default", Version: 1,
		SpecJSON: `{"stages":[{"key":"plan","harness":"kimi"},{"key":"code","harness":"kimi"}]}`,
	})
	require.NoError(t, err)
	return id
}

// --- тесты -------------------------------------------------------------------

// CRUD проектов + идемпотентность по path.
func TestProjects_CRUD(t *testing.T) {
	f := newFixture(t)

	project := f.createProject(t, "/tmp/glamor-test-1")
	projectID := int64(project["id"].(float64))

	// повторный POST с тем же path → 200 с тем же проектом
	status, dup := f.do(t, "POST", "/projects", map[string]any{
		"path": "/tmp/glamor-test-1", "name": "other",
	}, nil)
	require.Equal(t, 200, status)
	assert.Equal(t, project["id"], dup["id"])

	// list
	status, list := f.do(t, "GET", "/projects", nil, nil)
	require.Equal(t, 200, status)
	_ = list

	// get detail
	status, detail := f.do(t, "GET", fmt.Sprintf("/projects/%d", projectID), nil, nil)
	require.Equal(t, 200, status)
	assert.Equal(t, float64(0), detail["open_gates"])
	assert.Empty(t, detail["active_runs"])

	// patch
	status, patched := f.do(t, "PATCH", fmt.Sprintf("/projects/%d", projectID), map[string]any{
		"ide_command": "goland",
	}, nil)
	require.Equal(t, 200, status)
	assert.Equal(t, "goland", patched["ide_command"])

	// 404
	status, errBody := f.do(t, "GET", "/projects/9999", nil, nil)
	require.Equal(t, 404, status)
	assert.Equal(t, "not_found", errBody["code"])
}

// Создание рана с повтором Idempotency-Key → один ран (D-12).
func TestCreateRun_IdempotencyKey(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "/tmp/glamor-test-2")
	pipelineID := f.createPipeline(t)

	key := uuid.New()
	body := map[string]any{
		"project_id": project["id"], "pipeline_version_id": pipelineID,
		"task_text": "Fix the bug in parser",
	}
	status, run := f.do(t, "POST", "/runs", body, map[string]string{"Idempotency-Key": key})
	require.Equal(t, 201, status, "body: %v", run)
	assert.Equal(t, "draft", run["state"])
	assert.Equal(t, "glamor/fix-the-bug-in-parser", run["branch"])
	assert.Equal(t, "main", run["base_branch"])

	// повтор с тем же ключом → 200, тот же ран
	status, dup := f.do(t, "POST", "/runs", body, map[string]string{"Idempotency-Key": key})
	require.Equal(t, 200, status)
	assert.Equal(t, run["id"], dup["id"])

	// другой ключ, та же ветка → 409 run_locked (D-33)
	status, locked := f.do(t, "POST", "/runs", body, map[string]string{"Idempotency-Key": uuid.New()})
	require.Equal(t, 409, status, "body: %v", locked)
	assert.Equal(t, "run_locked", locked["code"])

	// та же ветка, но явно другая → 201
	body["branch"] = "feature/other"
	status, run2 := f.do(t, "POST", "/runs", body, map[string]string{"Idempotency-Key": uuid.New()})
	require.Equal(t, 201, status, "body: %v", run2)
}

// Полный срез рана + stop/resume + события.
func TestRunLifecycleAPI(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "/tmp/glamor-test-3")
	pipelineID := f.createPipeline(t)

	status, run := f.do(t, "POST", "/runs", map[string]any{
		"project_id": project["id"], "pipeline_version_id": pipelineID, "task_text": "task",
	}, nil)
	require.Equal(t, 201, status)
	runID := run["id"].(string)

	// запускаем ран и стадию напрямую через машину (supervisor — T-09)
	m := f.machine
	require.NoError(t, m.TransitionRun(context.Background(), runID, dtorep.RunStateRunning))

	// stop → stopped, идемпотентно
	status, stopped := f.do(t, "POST", "/runs/"+runID+"/stop", nil, nil)
	require.Equal(t, 200, status)
	assert.Equal(t, "stopped", stopped["state"])
	assert.NotNil(t, stopped["finished_at"])

	status, again := f.do(t, "POST", "/runs/"+runID+"/stop", nil, nil)
	require.Equal(t, 200, status)
	assert.Equal(t, "stopped", again["state"])

	// resume из stopped запрещён → 409 invalid_transition
	status, errBody := f.do(t, "POST", "/runs/"+runID+"/resume", nil, nil)
	require.Equal(t, 409, status)
	assert.Equal(t, "invalid_transition", errBody["code"])

	// срез рана
	status, detail := f.do(t, "GET", "/runs/"+runID, nil, nil)
	require.Equal(t, 200, status)
	assert.Equal(t, runID, detail["id"])
	assert.NotNil(t, detail["stages"])
	assert.NotNil(t, detail["gates"])
	assert.NotNil(t, detail["artifacts"])
	assert.NotNil(t, detail["notes"])

	// журнал событий рана
	status, eventsBody := f.do(t, "GET", "/runs/"+runID+"/events", nil, nil)
	require.Equal(t, 200, status)
	_ = eventsBody
}

// failed → resume → running (ручной рестарт, ADR-001 доп.).
func TestResumeFailedRun(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "/tmp/glamor-test-4")
	pipelineID := f.createPipeline(t)

	_, run := f.do(t, "POST", "/runs", map[string]any{
		"project_id": project["id"], "pipeline_version_id": pipelineID, "task_text": "task",
	}, nil)
	runID := run["id"].(string)

	m := f.machine
	ctx := context.Background()
	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateFailed))

	status, resumed := f.do(t, "POST", "/runs/"+runID+"/resume", nil, nil)
	require.Equal(t, 200, status, "body: %v", resumed)
	assert.Equal(t, "running", resumed["state"])

	// повторный resume — идемпотентный no-op
	status, again := f.do(t, "POST", "/runs/"+runID+"/resume", nil, nil)
	require.Equal(t, 200, status)
	assert.Equal(t, "running", again["state"])
}

// Resolve гейта дважды: второй раз 200 + already_resolved=true (T-05).
func TestResolveGate_Idempotent(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "/tmp/glamor-test-5")
	pipelineID := f.createPipeline(t)

	_, run := f.do(t, "POST", "/runs", map[string]any{
		"project_id": project["id"], "pipeline_version_id": pipelineID, "task_text": "task",
	}, nil)
	runID := run["id"].(string)

	m := f.machine
	ctx := context.Background()
	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))

	gate, err := m.OpenGate(ctx, runsmachine.OpenGateRequest{
		RunID: runID, Kind: dtorep.GateKindPlanApproval, Question: "ok?",
	})
	require.NoError(t, err)

	// первый резолв → 200, already_resolved=false
	status, resp := f.do(t, "POST", "/gates/"+gate.ID+"/resolve", map[string]any{
		"action": "approve",
	}, nil)
	require.Equal(t, 200, status, "body: %v", resp)
	assert.Equal(t, false, resp["already_resolved"])

	// повтор тем же действием → 200, already_resolved=true
	status, resp = f.do(t, "POST", "/gates/"+gate.ID+"/resolve", map[string]any{
		"action": "approve",
	}, nil)
	require.Equal(t, 200, status)
	assert.Equal(t, true, resp["already_resolved"])

	// другим действием → 409 gate_already_resolved
	status, resp = f.do(t, "POST", "/gates/"+gate.ID+"/resolve", map[string]any{
		"action": "reject",
	}, nil)
	require.Equal(t, 409, status)
	assert.Equal(t, "gate_already_resolved", resp["code"])
}

// Notes: создание + идемпотентность; interrupt&steer на running-стадии.
func TestNotesAndInterrupt(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "/tmp/glamor-test-6")
	pipelineID := f.createPipeline(t)

	_, run := f.do(t, "POST", "/runs", map[string]any{
		"project_id": project["id"], "pipeline_version_id": pipelineID, "task_text": "task",
	}, nil)
	runID := run["id"].(string)

	key := uuid.New()
	status, note := f.do(t, "POST", "/runs/"+runID+"/notes", map[string]any{
		"text": "не забудь про миграции",
	}, map[string]string{"Idempotency-Key": key})
	require.Equal(t, 201, status, "body: %v", note)

	// повтор ключа → та же заметка
	status, dup := f.do(t, "POST", "/runs/"+runID+"/notes", map[string]any{
		"text": "не забудь про миграции",
	}, map[string]string{"Idempotency-Key": key})
	require.Equal(t, 201, status)
	assert.Equal(t, note["id"], dup["id"])

	// interrupt&steer на не-running стадии → 409
	status, errBody := f.do(t, "POST", "/stages/123/interrupt", map[string]any{"message": "стоп"}, nil)
	require.True(t, status == 404 || status == 409, "status=%d body=%v", status, errBody)

	// running-стадия → interrupt&steer
	m := f.machine
	ctx := context.Background()
	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	stage, err := m.StartStage(ctx, runID, "plan", "kimi")
	require.NoError(t, err)
	require.NoError(t, m.TransitionStage(ctx, stage.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}))

	status, interrupted := f.do(t, "POST", fmt.Sprintf("/stages/%d/interrupt", stage.ID),
		map[string]any{"message": "используй sqlite вместо файлов"}, nil)
	require.Equal(t, 200, status, "body: %v", interrupted)
	assert.Equal(t, "interrupted", interrupted["state"])
	assert.Equal(t, "user", interrupted["stop_requested_by"])

	// steer-заметка появилась в срезе рана
	status, detail := f.do(t, "GET", "/runs/"+runID, nil, nil)
	require.Equal(t, 200, status)
	notes := detail["notes"].([]any)
	var steerFound bool
	for _, n := range notes {
		if n.(map[string]any)["kind"] == "steer" {
			steerFound = true
			assert.Equal(t, "используй sqlite вместо файлов", n.(map[string]any)["text"])
		}
	}
	assert.True(t, steerFound, "steer note must be in run detail")
}

// Auth: без токена — 401 (кроме /healthz); неверный — 401.
func TestAuth(t *testing.T) {
	f := newFixture(t)

	resp, err := f.client.Get(f.server.URL + "/healthz")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	resp, err = f.client.Get(f.server.URL + "/projects")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)

	req, _ := http.NewRequest("GET", f.server.URL+"/projects", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = f.client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}

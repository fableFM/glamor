package runsapi_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/repository"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	lessonsrep "github.com/fableFM/glamor/internal/repository/lessons"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/internal/repository/vendorindex"
	"github.com/fableFM/glamor/internal/service/lessons"
	"github.com/fableFM/glamor/internal/service/runsapi"
	"github.com/fableFM/glamor/internal/service/runsmachine"
	"github.com/fableFM/glamor/pkg/uuid"
)

// M3 (T-30): per-card approve с битой операцией — резолв гейта успешен,
// успешные операции применены, ошибка — в журнале (lessons.finalize_error),
// а не в HTTP-ответе (гейт уже резолвнут CAS'ом, повторный финализ опасен
// дублями).
func TestResolveLessonGate_PartialFailureDoesNotFailResolve(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)

	projects := projectsrep.NewRepository(db)
	projectID, err := projects.CreateProject(ctx, dtorep.CreateProjectRequest{
		Path: t.TempDir(), Name: "test", DefaultBranch: "main",
	})
	require.NoError(t, err)
	pipelines := pipelinesrep.NewRepository(db)
	pipelineID, err := pipelines.CreatePipeline(ctx, dtorep.CreatePipelineRequest{
		Name: "default", Version: 1,
		SpecJSON: `{"stages":[{"key":"distill","harness":"fake"}]}`,
	})
	require.NoError(t, err)

	journal := events.NewJournal(eventsrep.NewRepository(db), repository.NewTxManager(db), events.NewHub())
	runsRepo := runsrep.NewRepository(db)
	machine := runsmachine.NewMachine(
		runsRepo, stagesrep.NewRepository(db), gatesrep.NewRepository(db),
		pipelines, projects, notesrep.NewRepository(db), journal, journal)
	svc := runsapi.New(machine, journal, journal,
		runsRepo, stagesrep.NewRepository(db), notesrep.NewRepository(db),
		projects, pipelines, gatesrep.NewRepository(db))
	lessonsSvc := lessons.New(lessonsrep.NewRepository(db), vendorindex.NewRepository(db), t.TempDir())
	svc.SetLessonsFinalizer(lessons.NewGateFinalizer(lessonsSvc, runsRepo, projects))

	run, _, err := svc.CreateRun(ctx, runsapi.CreateRunParams{
		ProjectID: projectID, PipelineVersionID: pipelineID,
		TaskText: "тест", IdempotencyKey: uuid.New(),
	})
	require.NoError(t, err)
	require.NoError(t, machine.TransitionRun(ctx, run.ID, dtorep.RunStateRunning))

	// черновик: валидная NEW + REFINE на несуществующий урок (битая операция)
	lessonsPath := filepath.Join(t.TempDir(), "lessons.md")
	draft := `---
title: хороший урок
triggers: [alpha]
---

## Причина (почему)
причина

## Правило
правило

---
op: REFINE
target: lesson-nonexistent
title: битый refine
triggers: [beta]
---

## Причина (почему)
причина

## Правило
правило
`
	require.NoError(t, os.WriteFile(lessonsPath, []byte(draft), 0o644))

	gate, err := machine.OpenGate(ctx, runsmachine.OpenGateRequest{
		RunID: run.ID, Kind: dtorep.GateKindLessonReview,
		Question:    "Сохранить уроки?",
		ContextJSON: fmt.Sprintf(`{"lessons_path":%q,"stage_key":"distill"}`, lessonsPath),
	})
	require.NoError(t, err)

	// per-card approve обеих операций: одна битая — резолв НЕ падает
	resolved, already, err := svc.ResolveGateAPI(ctx, gate.ID, runsapi.GateActionApprove, nil,
		&dtorep.LessonOpSelection{Accept: []int{0, 1}})
	require.NoError(t, err, "частичный сбой применения не валит резолв")
	assert.False(t, already)
	assert.Equal(t, dtorep.GateStateApproved, resolved.State)

	// успешная операция применена
	confirmed, err := lessonsSvc.List(ctx, lessons.StatusConfirmed, "", nil)
	require.NoError(t, err)
	require.Len(t, confirmed, 1)
	assert.Equal(t, "хороший урок", confirmed[0].Title)

	// ошибка — в журнале
	evs, err := eventsrep.NewRepository(db).ReplayEvents(ctx, run.ID, 0, 1000)
	require.NoError(t, err)
	var found bool
	for _, ev := range evs {
		if ev.Kind == "lessons.finalize_error" {
			found = true
			assert.Contains(t, ev.PayloadJSON, "lesson-nonexistent")
		}
	}
	assert.True(t, found, "событие lessons.finalize_error в журнале")
}

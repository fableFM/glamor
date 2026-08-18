package supervisor_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
)

func janitorSpec(commands []string, onFail string) string {
	cmds, _ := json.Marshal(commands)
	return `{"stages":[
		{"key":"build","harness":"fake","artifact":{"path":"{run_dir}/out.txt","required":true}},
		{"key":"checks","kind":"janitor","commands":` + string(cmds) + `,"on_fail":"` + onFail + `"},
		{"key":"review","harness":"fake","artifact":{"path":"{run_dir}/review.md","required":true}}
	]}`
}

// Janitor-этап: команды выполняются, лог — артефакт, стрим-события есть (T-22).
func TestJanitorSuccess(t *testing.T) {
	// fake harness пишет out.txt/review.md по маркеру промпта; janitor между ними
	f := newFixture(t, "testdata/janitor_stages.sh",
		janitorSpec([]string{"echo hello", "echo world"}, ""), nil)
	ctx := context.Background()

	require.Eventually(t, func() bool {
		run, err := f.machineRun()
		return err == nil && run.State == dtorep.RunStateSucceeded
	}, 15*time.Second, 50*time.Millisecond)

	stage, err := f.stages.GetLatestStage(ctx, f.runID, "checks")
	require.NoError(t, err)
	assert.Equal(t, dtorep.StageStateSucceeded, stage.State)
	require.NotNil(t, stage.ExitCode)
	assert.Equal(t, int64(0), *stage.ExitCode)

	// лог-артефакт с содержимым команд
	arts, err := f.artifacts.ListArtifactsByRun(ctx, f.runID)
	require.NoError(t, err)
	var janitorLog string
	for _, a := range arts {
		if a.Kind == "janitor_log" {
			janitorLog = a.Path
		}
	}
	require.NotEmpty(t, janitorLog)
	data, err := os.ReadFile(janitorLog)
	require.NoError(t, err)
	assert.Contains(t, string(data), "$ echo hello")
	assert.Contains(t, string(data), "hello")
	assert.Contains(t, string(data), "[exit 0]")
}

// Падение команды с fail_stage → этап failed, ран → failed (T-22).
func TestJanitorFailStage(t *testing.T) {
	f := newFixture(t, "testdata/janitor_stages.sh",
		janitorSpec([]string{"echo ok", "exit 3", "echo never"}, "fail_stage"), nil)

	require.Eventually(t, func() bool {
		run, err := f.machineRun()
		return err == nil && run.State == dtorep.RunStateFailed
	}, 15*time.Second, 50*time.Millisecond)

	stage, err := f.stages.GetLatestStage(context.Background(), f.runID, "checks")
	require.NoError(t, err)
	assert.Equal(t, dtorep.StageStateFailed, stage.State)
	require.NotNil(t, stage.Error)
	assert.Contains(t, *stage.Error, "exit 3")
}

// Политика warn: падение команды не роняет этап (T-22).
func TestJanitorWarn(t *testing.T) {
	f := newFixture(t, "testdata/janitor_stages.sh",
		janitorSpec([]string{"exit 5", "echo after"}, "warn"), nil)

	require.Eventually(t, func() bool {
		run, err := f.machineRun()
		return err == nil && run.State == dtorep.RunStateSucceeded
	}, 15*time.Second, 50*time.Millisecond)

	stage, err := f.stages.GetLatestStage(context.Background(), f.runID, "checks")
	require.NoError(t, err)
	assert.Equal(t, dtorep.StageStateSucceeded, stage.State)
}

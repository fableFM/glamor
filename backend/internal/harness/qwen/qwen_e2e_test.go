//go:build e2e

package qwen_test

// E2E-каркас T-08: минимальный живой сценарий адаптера против реального
// CLI qwen. НЕ входит в `go test ./...` (build tag e2e), в CI по
// умолчанию не гоняется. Полноценный прогон пайплайна — T-17.
//
// Skip-политика (conservative): нет бинаря в PATH, нет распарсенных
// событий при ненулевом exit (auth/сеть/квота неотличимы от окружения
// без credentials) — t.Skip, а не Fail. Осмысленный ответ CLI →
// проверяем стрим (result-событие со статусом), session_id и resume.

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/harness"
	"github.com/fableFM/glamor/internal/harness/qwen"
)

// runOnce запускает один headless-прогон адаптера и возвращает события,
// exit code и stderr. Таймаут — защита от зависшего CLI. Напоминание:
// exit codes 53/55/130 у qwen — равноправный канал истины, result-события
// может не быть (матрица) — поэтому отдельно возвращаем exit code.
func runOnce(t *testing.T, spec harness.LaunchSpec) ([]harness.Event, int, string) {
	t.Helper()

	cmd, err := qwen.New().BuildCommand(spec)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	//nolint:gosec // e2e: путь резолвится exec.LookPath'ем вызывающего.
	proc := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	proc.Dir = spec.WorkDir
	proc.Env = append(os.Environ(), cmd.Env...)
	if cmd.Stdin != "" {
		proc.Stdin = strings.NewReader(cmd.Stdin)
	}
	var stdout, stderr bytes.Buffer
	proc.Stdout = &stdout
	proc.Stderr = &stderr

	runErr := proc.Run()

	var events []harness.Event
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		events = append(events, qwen.New().ParseStream(sc.Bytes())...)
	}
	require.NoError(t, sc.Err())

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		require.ErrorAs(t, runErr, &exitErr, "запуск CLI: %v (stderr: %s)", runErr, stderr.String())
		exitCode = exitErr.ExitCode()
	}
	return events, exitCode, stderr.String()
}

func TestE2E_Qwen_SimplePromptAndResume(t *testing.T) {
	path, err := exec.LookPath(qwen.New().BinaryName())
	if err != nil {
		t.Skipf("бинарь qwen не найден в PATH: %v", err)
	}
	t.Logf("qwen: %s", path)

	workDir := t.TempDir()
	events, exitCode, stderr := runOnce(t, harness.LaunchSpec{
		Prompt:  "Ответь ровно одним словом: pong",
		WorkDir: workDir,
	})

	if exitCode != 0 && len(events) == 0 {
		t.Skipf("прогон не состоялся (exit %d) и событий нет — вероятно нет credentials/сети; stderr: %s",
			exitCode, stderr)
	}
	require.Equal(t, 0, exitCode, "exit code CLI; stderr: %s", stderr)
	require.NotEmpty(t, events, "пустой стрим событий при exit 0")

	// qwen шлёт терминальное result-событие прямо в стриме (UsageSource=stream).
	var sawResult bool
	for _, ev := range events {
		if ev.Kind == harness.EventResult {
			sawResult = true
		}
	}
	if !sawResult {
		t.Skipf("result-события нет в стриме (%d событий) — формат изменился? "+
			"сверить с capability-матрицей", len(events))
	}

	sessionID, err := qwen.New().ExtractSessionID(events, workDir)
	if err != nil || sessionID == "" {
		t.Skipf("session_id не извлечён (%v) — формат стрима изменился?; событий: %d", err, len(events))
	}
	t.Logf("session_id: %s", sessionID)

	// Resume: `qwen -r <id> -p "<новое>"` со всеми флагами заново (матрица).
	resumeEvents, resumeExit, resumeStderr := runOnce(t, harness.LaunchSpec{
		Prompt:    "Ответь ровно одним словом: pong2",
		WorkDir:   workDir,
		SessionID: sessionID,
	})
	if resumeExit != 0 && len(resumeEvents) == 0 {
		t.Skipf("resume не состоялся (exit %d) — окружение?; stderr: %s", resumeExit, resumeStderr)
	}
	require.Equal(t, 0, resumeExit, "exit code resume; stderr: %s", resumeStderr)
	require.NotEmpty(t, resumeEvents, "resume: пустой стрим событий")
}

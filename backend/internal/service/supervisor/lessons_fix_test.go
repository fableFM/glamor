package supervisor_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	lessonsrep "github.com/fableFM/glamor/internal/repository/lessons"
	"github.com/fableFM/glamor/internal/repository/vendorindex"
	"github.com/fableFM/glamor/internal/service/lessons"
)

// M2 (T-30): терминальный distill не теряется при рестарте — pending-строка
// distill failed-рана (крах между StartStage и запуском) доводится до
// выполнения после рестарта supervisor'а, гейт открывается.
func TestTerminalDistillRecoversAfterRestart(t *testing.T) {
	f := newLessonsFixture(t, "testdata/pipeline_fail_coder.sh", lessonsSpecJSON(false, ""))
	ctx := context.Background()

	f.resolveOpenGateKind(t, dtorep.GateKindPlanApproval)

	// coder падает → ран failed; демон «падает» до grace терминального distill
	require.Eventually(t, func() bool {
		return f.runState(t) == dtorep.RunStateFailed
	}, 15*time.Second, 100*time.Millisecond)
	f.stop()

	// имитируем крах между StartStage и launchStage: pending-строка distill
	_, err := f.machine.StartStage(ctx, f.runID, "distill", "fake")
	require.NoError(t, err)

	// рестарт демона: recovery доводит distill до конца
	f.start(t, defaultTestCfg(f.runsDir))

	require.Eventually(t, func() bool {
		gates, err := f.gates.ListOpenGates(ctx, f.runID)
		return err == nil && len(gates) == 1 && gates[0].Kind == dtorep.GateKindLessonReview
	}, 30*time.Second, 100*time.Millisecond, "pending distill recovered after restart")

	distill, err := f.stages.GetLatestStage(ctx, f.runID, "distill")
	require.NoError(t, err)
	assert.Equal(t, dtorep.StageStateSucceeded, distill.State)
	assert.Equal(t, dtorep.RunStateFailed, f.runState(t))
}

// M4 (T-30): один finding на двух итерациях reviewer → relapse инкрементится
// один раз; урок с relapse в этом ране НЕ получает applied_success при
// outcome, остальные инжектированные — получают.
func TestRelapseDedupAndOutcome(t *testing.T) {
	f := newLessonsFixture(t, "testdata/pipeline_relapse.sh", lessonsSpecJSON(false, ""))
	ctx := context.Background()

	lessonsSvc := lessons.New(lessonsrep.NewRepository(f.db), vendorindex.NewRepository(f.db), t.TempDir())

	// outcome-хук как в main: успех только инжектированным без relapse
	f.runsSvc.SetOutcomeHook(func(ctx context.Context, runID string) {
		injected, err := lessonsSvc.LoadSuccessfulInjections(ctx, filepath.Join(f.runsDir, runID))
		if err != nil || len(injected) == 0 {
			return
		}
		_ = lessonsSvc.OutcomeApprove(ctx, injected)
	})
	f.sup.SetLessonsHooks(lessonsSvc)

	// урок, тема которого совпадает с finding REV-001 (sqlite/очередь)
	relapsedLesson, err := lessonsSvc.SaveCard(ctx, lessons.SaveCardParams{
		Card: lessons.Card{
			Title: "sqlite вместо файлов для очередей", Triggers: []string{"sqlite", "очереди"},
			Body: "## Причина (почему)\nФайлы не атомарны, очередь в демоне — в sqlite.\n\n## Правило\nКогда очередь — всегда sqlite.",
		},
		Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceUser,
	})
	require.NoError(t, err)
	// урок вне темы finding
	cleanLesson, err := lessonsSvc.SaveCard(ctx, lessons.SaveCardParams{
		Card: lessons.Card{
			Title: "прочее про конфиги yaml", Triggers: []string{"yaml"},
			Body: "## Причина (почему)\nКонфиги валидировать.\n\n## Правило\nВалидируй.",
		},
		Scope: "global", Status: lessons.StatusConfirmed, Source: lessons.SourceUser,
	})
	require.NoError(t, err)

	// план-гейт открылся — фиксируем инъекции обоих уроков ДО reviewer
	f.resolveOpenGateKind(t, dtorep.GateKindPlanApproval)
	runDir := filepath.Join(f.runsDir, f.runID)
	require.NoError(t, lessons.AppendInjectedRecords(runDir, "planner",
		[]lessons.InjectedLesson{{Lesson: relapsedLesson}, {Lesson: cleanLesson}}))

	// reviewer ×3 (дважды REV-001, затем approved) → distill NO_LESSONS →
	// финальный гейт
	f.resolveOpenGateKind(t, dtorep.GateKindFinalReview)

	require.Eventually(t, func() bool {
		return f.runState(t) == dtorep.RunStateSucceeded
	}, 15*time.Second, 100*time.Millisecond)

	// relapse — ровно один, несмотря на 2 итерации с тем же finding
	after, err := lessonsSvc.GetByID(ctx, relapsedLesson.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), after.RelapseCount, "finding REV-001 штрафует один раз за ран")
	assert.Equal(t, int64(0), after.AppliedSuccessCount, "урок с relapse не получает success")

	clean, err := lessonsSvc.GetByID(ctx, cleanLesson.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), clean.RelapseCount)
	assert.Equal(t, int64(1), clean.AppliedSuccessCount, "чистый урок получает success на approve")

	// событие relapse в журнале — одно
	var relapseEvents int
	for _, kind := range f.eventKinds(t) {
		if kind == "lessons.relapse" {
			relapseEvents++
		}
	}
	assert.Equal(t, 1, relapseEvents)
}

// m15 (T-30): повторная попытка distill не плодит дубликаты артефакта
// run_facts.json в БД (дедуп по пути).
func TestRunFactsArtifactDeduplicated(t *testing.T) {
	f := newLessonsFixture(t, "testdata/pipeline_distill.sh", lessonsSpecJSON(false, ""))
	ctx := context.Background()

	f.resolveOpenGateKind(t, dtorep.GateKindPlanApproval)
	f.resolveOpenGateKind(t, dtorep.GateKindLessonReview)
	f.resolveOpenGateKind(t, dtorep.GateKindFinalReview)

	require.Eventually(t, func() bool {
		return f.runState(t) == dtorep.RunStateSucceeded
	}, 15*time.Second, 100*time.Millisecond)

	artifacts, err := f.artifacts.ListArtifactsByRun(ctx, f.runID)
	require.NoError(t, err)
	var facts int
	for _, a := range artifacts {
		if a.Kind == "run_facts" {
			facts++
		}
	}
	assert.Equal(t, 1, facts, "run_facts зарегистрирован один раз")
}

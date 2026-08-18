package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/service/runsmachine"
)

// stateChangedPayload — payload событий *.state_changed (см. runsmachine).
type stateChangedPayload struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// replyHint — подсказка, как ответить на гейт текстом.
const replyHint = "\n\nОтветьте reply на это сообщение."

// callbackData — формат callback_data inline-кнопок гейта: gate:<id>:<action>.
func callbackData(gateID, action string) string {
	return "gate:" + gateID + ":" + action
}

// mapEvent — маппинг события журнала в (текст, клавиатуру, gateID).
// Пустой текст — событие не уведомляется (run.*, stream.*, черновые переходы).
func (a *Adapter) mapEvent(ctx context.Context, ev dtorep.Event) (text string, keyboard [][]Button, gateID string) {
	switch ev.Kind {
	case runsmachine.EventKindStageStateChanged, "stage.resumed":
		return a.mapStageEvent(ctx, ev)
	case runsmachine.EventKindGateOpened:
		return a.mapGateOpened(ev)
	default:
		return "", nil, ""
	}
}

// mapStageEvent — stage.state_changed → ✅/❌/⚠️, stage.resumed → 🔁 (D-70).
func (a *Adapter) mapStageEvent(ctx context.Context, ev dtorep.Event) (string, [][]Button, string) {
	var p stateChangedPayload
	if err := json.Unmarshal([]byte(ev.PayloadJSON), &p); err != nil {
		return "", nil, ""
	}

	if ev.StageID == nil {
		return "", nil, ""
	}
	stage, err := a.catalog.GetStage(ctx, *ev.StageID)
	if err != nil {
		return "", nil, ""
	}

	if ev.Kind == "stage.resumed" {
		return fmt.Sprintf("🔁 %s · возобновлён %d/%d",
			stage.StageKey, stage.ResumeCount, runsmachine.MaxResumeCount), nil, ""
	}

	switch dtorep.StageState(p.To) {
	case dtorep.StageStateSucceeded:
		return fmt.Sprintf("✅ %s · iter %d", stage.StageKey, stage.Iteration), nil, ""
	case dtorep.StageStateFailed:
		return fmt.Sprintf("❌ %s · iter %d", stage.StageKey, stage.Iteration), nil, ""
	case dtorep.StageStateInterrupted:
		return fmt.Sprintf("⚠️ %s · прерван (iter %d)", stage.StageKey, stage.Iteration), nil, ""
	default:
		return "", nil, ""
	}
}

// firstNonEmpty — первая непустая строка.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// mapGateOpened — gate.opened → сообщение по виду гейта (D-21/D-70):
// question/escalation — reply-ответ; plan_approval/final_review — кнопки.
func (a *Adapter) mapGateOpened(ev dtorep.Event) (string, [][]Button, string) {
	// payload по контракту GatePayload (snake_case, openapi); "ID"/"Kind"/...
	// — легаси события до 2026-08-18 (json.Marshal без тегов)
	var p struct {
		GateID      string `json:"gate_id"`
		Kind        string `json:"kind"`
		Question    string `json:"question"`
		ContextJSON string `json:"context_json"`

		LegacyID       string `json:"ID"`
		LegacyKind     string `json:"Kind"`
		LegacyQuestion string `json:"Question"`
		LegacyContext  string `json:"ContextJSON"`
	}
	if err := json.Unmarshal([]byte(ev.PayloadJSON), &p); err != nil {
		return "", nil, ""
	}
	gate := dtorep.Gate{
		ID:          firstNonEmpty(p.GateID, p.LegacyID),
		Kind:        dtorep.GateKind(firstNonEmpty(p.Kind, p.LegacyKind)),
		Question:    firstNonEmpty(p.Question, p.LegacyQuestion),
		ContextJSON: firstNonEmpty(p.ContextJSON, p.LegacyContext),
	}
	if gate.ID == "" {
		return "", nil, ""
	}

	question := truncate(gate.Question, 3072) // усечение ~3КБ (T-19)

	switch gate.Kind {
	case dtorep.GateKindQuestion:
		return "❓ Вопрос:\n\n" + question + replyHint, nil, gate.ID
	case dtorep.GateKindEscalation:
		text := "🚨 Эскалация:\n\n" + question
		// findings из context_json — если вопрос их ещё не содержит
		if findings := gateFindings(gate.ContextJSON); findings != "" && !strings.Contains(question, findings) {
			text += "\n\nFindings:\n" + findings
		}
		return text + replyHint, nil, gate.ID
	case dtorep.GateKindPlanApproval:
		kb := [][]Button{{
			{Text: "Approve", CallbackData: callbackData(gate.ID, "approve")},
			{Text: "Reject", CallbackData: callbackData(gate.ID, "reject")},
		}}
		return "📋 Утверждение плана:\n\n" + question + "\n\nКомментарий — reply на это сообщение.", kb, gate.ID
	case dtorep.GateKindFinalReview:
		kb := [][]Button{{
			{Text: "Принять", CallbackData: callbackData(gate.ID, "approve")},
			{Text: "Отклонить", CallbackData: callbackData(gate.ID, "reject")},
		}}
		return "🏁 Финальное ревью:\n\n" + question + "\n\nКомментарий (→ fix) — reply на это сообщение.", kb, gate.ID
	default:
		return "", nil, ""
	}
}

// gateFindings — findings из context_json эскалации (см. pipeline.LoopHook).
func gateFindings(contextJSON string) string {
	if contextJSON == "" {
		return ""
	}
	var ctx struct {
		Findings []string `json:"findings"`
	}
	if err := json.Unmarshal([]byte(contextJSON), &ctx); err != nil || len(ctx.Findings) == 0 {
		return ""
	}
	return "- " + strings.Join(ctx.Findings, "\n- ")
}

// truncate обрезает текст до limit байт с маркером усечения.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n… (усечено)"
}

// --- оффлайн-сводка (D-72) ---------------------------------------------------

// missedStat — счётчики пропущенных за простой событий.
type missedStat struct {
	total          int
	gatesOpened    int
	gatesResolved  int
	stagesFinished int
	runsFinished   int
}

// add учитывает событие в сводке.
func (s *missedStat) add(ev dtorep.Event) {
	s.total++
	switch ev.Kind {
	case runsmachine.EventKindGateOpened:
		s.gatesOpened++
	case runsmachine.EventKindGateResolved:
		s.gatesResolved++
	case runsmachine.EventKindStageStateChanged:
		var p stateChangedPayload
		if err := json.Unmarshal([]byte(ev.PayloadJSON), &p); err != nil {
			return
		}
		switch dtorep.StageState(p.To) {
		case dtorep.StageStateSucceeded, dtorep.StageStateFailed:
			s.stagesFinished++
		}
	case runsmachine.EventKindRunStateChanged:
		var p stateChangedPayload
		if err := json.Unmarshal([]byte(ev.PayloadJSON), &p); err != nil {
			return
		}
		switch dtorep.RunState(p.To) {
		case dtorep.RunStateSucceeded, dtorep.RunStateFailed, dtorep.RunStateStopped:
			s.runsFinished++
		}
	}
}

// formatMissedSummary — текст ОДНОГО сводного сообщения за простой.
// Пустая строка — сводка не нужна.
func formatMissedSummary(s missedStat) string {
	if s.total == 0 {
		return ""
	}
	parts := []string{fmt.Sprintf("%d событий", s.total)}
	if s.gatesOpened > 0 {
		parts = append(parts, fmt.Sprintf("открыто гейтов: %d", s.gatesOpened))
	}
	if s.gatesResolved > 0 {
		parts = append(parts, fmt.Sprintf("резолвнуто гейтов: %d", s.gatesResolved))
	}
	if s.stagesFinished > 0 {
		parts = append(parts, fmt.Sprintf("завершено этапов: %d", s.stagesFinished))
	}
	if s.runsFinished > 0 {
		parts = append(parts, fmt.Sprintf("завершено ранов: %d", s.runsFinished))
	}
	return "💤 За время простоя: " + strings.Join(parts, " · ")
}

// --- /status -----------------------------------------------------------------

// formatStatus — компактный стейт активных ранов (T-19): проект · этап ·
// сколько ждёт гейт. Read-агрегация — service/catalog (F-09).
func (a *Adapter) formatStatus(ctx context.Context) string {
	runs, err := a.catalog.ListActiveRunsStatus(ctx)
	if err != nil {
		return "⚠️ Не удалось загрузить список ранов."
	}
	if len(runs) == 0 {
		return "Активных ранов нет."
	}

	var b strings.Builder
	b.WriteString("📊 Активные раны:\n")
	for i := range runs {
		entry := &runs[i]
		b.WriteString("\n• ")
		b.WriteString(entry.ProjectName)
		b.WriteString(" · ")
		b.WriteString(entry.Run.Branch)
		b.WriteString("\n  ")
		b.WriteString(string(entry.Run.State))
		if stage := currentStage(entry.Stages); stage != "" {
			b.WriteString(" · этап ")
			b.WriteString(stage)
		}
		if entry.Run.State == dtorep.RunStateWaitingGate {
			if d := gateWaitDuration(entry.OpenGates); d > 0 {
				b.WriteString(" · ждёт гейт ")
				b.WriteString(formatDuration(d))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// currentStage — «ключ (iter N, state)» последней попытки рана.
func currentStage(stages []dtorep.Stage) string {
	if len(stages) == 0 {
		return ""
	}
	latest := stages[0]
	for _, st := range stages[1:] {
		if st.ID > latest.ID {
			latest = st
		}
	}
	return fmt.Sprintf("%s (iter %d, %s)", latest.StageKey, latest.Iteration, latest.State)
}

// gateWaitDuration — сколько ждёт самый старый открытый гейт рана.
func gateWaitDuration(open []dtorep.Gate) time.Duration {
	if len(open) == 0 {
		return 0
	}
	oldest := open[0].CreatedAt
	for _, g := range open[1:] {
		if g.CreatedAt.Before(oldest) {
			oldest = g.CreatedAt
		}
	}
	return time.Since(oldest)
}

// formatDuration — компактная длительность (4m12s → «4m», 50s → «50s»).
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

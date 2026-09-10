package lessons

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BehaviorTrace — детерминированный трейс поведения рана (T-30, D-81):
// факты (findings ревьюера, fix-петля, падения этапов, ответы на гейтах,
// исход) без участия LLM. Ядро собирает трейс перед distill-этапом;
// результат — артефакт run_facts.json + компактный текст для промпта.
//
// Вход — явные DTO этого пакета (НЕ типы supervisor: пакет не импортирует
// supervisor, чтобы не было циклических зависимостей; маппинг делает
// вызывающий).

// GateSignal — ответ/комментарий пользователя на гейте.
type GateSignal struct {
	Kind     string `json:"kind"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// RunNote — заметка рана (queue note / steer).
type RunNote struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// StageFact — итог попытки этапа (для выжимки упавших проверок).
type StageFact struct {
	Key       string `json:"key"`
	Iteration int64  `json:"iteration"`
	State     string `json:"state"`
	ExitCode  *int64 `json:"exit_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

// RunOutcome — исход рана.
type RunOutcome struct {
	State         string `json:"state"`          // succeeded | failed | ...
	FinalVerdict  string `json:"final_verdict"`  // approved | changes_required | ""
	Iterations    int    `json:"iterations"`     // итераций fix-петли
	GatesApproved int    `json:"gates_approved"` // approve-резолвы гейтов
	GatesRejected int    `json:"gates_rejected"` // reject-резолвы гейтов
}

// TraceInput — вход сборщика трейса.
type TraceInput struct {
	RunID    string
	TaskText string
	RunDir   string // каталог артефактов рана (~/.glamor/runs/<id>)
	Gates    []GateSignal
	Notes    []RunNote
	Stages   []StageFact
	Outcome  RunOutcome
}

// Finding — finding ревьюера из verdict.json (схема — default.yaml
// пайплайна: severity/file/line/observed/expected/required_fix/
// forbidden_fix).
type Finding struct {
	ID           string  `json:"id"`
	Severity     string  `json:"severity"`
	File         string  `json:"file"`
	Line         int     `json:"line"`
	Observed     string  `json:"observed"`
	Expected     string  `json:"expected"`
	RequiredFix  string  `json:"required_fix"`
	ForbiddenFix *string `json:"forbidden_fix"`
}

// Verdict — машиночитаемый вердикт ревьюера (verdict.json).
type Verdict struct {
	Verdict   string    `json:"verdict"` // approved | changes_required
	Findings  []Finding `json:"findings"`
	Questions []string  `json:"questions"`
}

// IterationVerdict — вердикт одной итерации ревью.
type IterationVerdict struct {
	Iteration int     `json:"iteration"`
	Verdict   Verdict `json:"verdict"`
}

// FailedStage — упавшая попытка этапа с выжимкой лога.
type FailedStage struct {
	Key        string `json:"key"`
	Iteration  int64  `json:"iteration"`
	ExitCode   int64  `json:"exit_code"`
	Error      string `json:"error,omitempty"`
	LogExcerpt string `json:"log_excerpt"` // хвост stage-<key>-<iter>.log
}

// FixLoopEntry — судьба одного finding в fix-петле: на какой итерации
// появился и на какой закрылся (0 — не закрыт к концу рана).
type FixLoopEntry struct {
	FindingID string `json:"finding_id"`
	Severity  string `json:"severity"`
	FirstSeen int    `json:"first_seen"`
	FixedAt   int    `json:"fixed_at"` // 0 — остался открытым
}

// BehaviorTrace — собранный трейс (сериализуется в run_facts.json).
type BehaviorTrace struct {
	RunID        string             `json:"run_id"`
	TaskText     string             `json:"task_text"`
	Outcome      RunOutcome         `json:"outcome"`
	Gates        []GateSignal       `json:"gates,omitempty"`
	Notes        []RunNote          `json:"notes,omitempty"`
	Verdicts     []IterationVerdict `json:"verdicts,omitempty"`
	FailedStages []FailedStage      `json:"failed_stages,omitempty"`
	Disputes     []string           `json:"disputes,omitempty"` // DISPUTED-споры фиксера
	FixLoop      []FixLoopEntry     `json:"fix_loop,omitempty"`
	// ParseIssues — битые артефакты (невалидный verdict.json и т.п.):
	// факт для distill, не ошибка сборки.
	ParseIssues []string `json:"parse_issues,omitempty"`
}

// Лимиты выжимок трейса (защита промпта distill от раздувания).
const (
	// TraceLogTailLines — сколько строк хвоста лога брать на упавший этап.
	TraceLogTailLines = 30
	// traceLogExcerptMax — потолок выжимки лога (символов).
	traceLogExcerptMax = 2000
	// TracePromptBudget — дефолтный лимит PromptText (символов).
	TracePromptBudget = 6000
)

// CollectTrace собирает трейс из входных DTO и артефактов run_dir:
// verdict.json (текущий) + verdict-<n>.json (per-итерационные снапшоты,
// если wiring их архивирует), логи упавших этапов, DISPUTED-споры из
// handoff.md. Отсутствующие/битые файлы — ParseIssues, не ошибка.
func CollectTrace(in TraceInput) *BehaviorTrace {
	trace := &BehaviorTrace{
		RunID:    in.RunID,
		TaskText: in.TaskText,
		Outcome:  in.Outcome,
		Gates:    in.Gates,
		Notes:    in.Notes,
	}

	trace.Verdicts = collectVerdicts(in.RunDir, trace)
	trace.FailedStages = collectFailedStages(in.RunDir, in.Stages)
	trace.Disputes = collectDisputes(in.RunDir)
	trace.FixLoop = buildFixLoop(trace.Verdicts)
	return trace
}

// collectVerdicts парсит вердикты итераций из run_dir. verdict.json —
// последний (его перезаписывает каждая итерация reviewer, путь фиксирован
// в спеке пайплайна); verdict-<n>.json — per-итерационные архивы, если они
// есть. Дубликаты по итерациям не возникают: нумерованные файлы идут в
// своём номере, голый verdict.json — как «последняя» итерация (номер =
// max+1 либо 1, если нумерованных нет).
func collectVerdicts(runDir string, trace *BehaviorTrace) []IterationVerdict {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return nil
	}

	var numbered []int
	hasPlain := false
	for _, e := range entries {
		name := e.Name()
		if name == "verdict.json" {
			hasPlain = true
			continue
		}
		var n int
		if rest, ok := strings.CutPrefix(name, "verdict-"); ok && strings.HasSuffix(rest, ".json") {
			if _, err := fmt.Sscanf(strings.TrimSuffix(rest, ".json"), "%d", &n); err == nil && n > 0 {
				numbered = append(numbered, n)
			}
		}
	}
	sort.Ints(numbered)

	var out []IterationVerdict
	for _, n := range numbered {
		if v, ok := readVerdictFile(runDir, fmt.Sprintf("verdict-%d.json", n), trace); ok {
			out = append(out, IterationVerdict{Iteration: n, Verdict: v})
		}
	}
	if hasPlain {
		last := 1
		if len(numbered) > 0 {
			last = numbered[len(numbered)-1] + 1
		}
		if v, ok := readVerdictFile(runDir, "verdict.json", trace); ok {
			out = append(out, IterationVerdict{Iteration: last, Verdict: v})
		}
	}
	return out
}

func readVerdictFile(runDir, name string, trace *BehaviorTrace) (Verdict, bool) {
	data, err := os.ReadFile(filepath.Join(runDir, name))
	if err != nil {
		return Verdict{}, false
	}
	var v Verdict
	if err := json.Unmarshal(data, &v); err != nil {
		trace.ParseIssues = append(trace.ParseIssues,
			fmt.Sprintf("%s: невалидный JSON (%v)", name, err))
		return Verdict{}, false
	}
	return v, true
}

// collectFailedStages — выжимки логов упавших попыток этапов
// (stage-<key>-<iter>.log, receipts D-13): exit code + хвост лога.
func collectFailedStages(runDir string, stages []StageFact) []FailedStage {
	var out []FailedStage
	for _, st := range stages {
		failed := st.State == "failed" || (st.ExitCode != nil && *st.ExitCode != 0)
		if !failed {
			continue
		}
		fs := FailedStage{Key: st.Key, Iteration: st.Iteration, Error: st.Error}
		if st.ExitCode != nil {
			fs.ExitCode = *st.ExitCode
		}
		fs.LogExcerpt = logExcerpt(filepath.Join(runDir,
			fmt.Sprintf("stage-%s-%d.log", st.Key, st.Iteration)))
		out = append(out, fs)
	}
	return out
}

// logExcerpt — последние TraceLogTailLines строк лога, усечённые до
// traceLogExcerptMax символов (хвост важнее середины: там exit-ошибка).
func logExcerpt(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > TraceLogTailLines {
		lines = lines[len(lines)-TraceLogTailLines:]
	}
	excerpt := strings.Join(lines, "\n")
	if len(excerpt) > traceLogExcerptMax {
		excerpt = excerpt[len(excerpt)-traceLogExcerptMax:]
	}
	return excerpt
}

// collectDisputes — DISPUTED-споры фиксера из handoff.md (машиночитаемый
// сигнал «finding оспорен» — ценный вход distill).
func collectDisputes(runDir string) []string {
	data, err := os.ReadFile(filepath.Join(runDir, "handoff.md"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "DISPUTED") {
			if t := strings.TrimSpace(line); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// buildFixLoop — пары «провал → исправление» (ExpeL, T-30): finding
// считается закрытым на первой итерации, где его id перестаёт встречаться.
func buildFixLoop(verdicts []IterationVerdict) []FixLoopEntry {
	seen := map[string]int{} // finding id → итерация последнего появления
	var order []string
	severities := map[string]string{}
	firstSeen := map[string]int{}

	for _, iv := range verdicts {
		for _, f := range iv.Verdict.Findings {
			if f.ID == "" {
				continue
			}
			if _, ok := seen[f.ID]; !ok {
				order = append(order, f.ID)
				firstSeen[f.ID] = iv.Iteration
				severities[f.ID] = f.Severity
			}
			seen[f.ID] = iv.Iteration
		}
	}
	lastIteration := 0
	for _, iv := range verdicts {
		if iv.Iteration > lastIteration {
			lastIteration = iv.Iteration
		}
	}

	var out []FixLoopEntry
	for _, id := range order {
		entry := FixLoopEntry{FindingID: id, Severity: severities[id], FirstSeen: firstSeen[id]}
		if seen[id] < lastIteration {
			entry.FixedAt = seen[id] + 1
		}
		out = append(out, entry)
	}
	return out
}

// WriteRunFacts пишет трейс артефактом run_facts.json в run_dir (tmp+rename;
// виден в UI как артефакт рана, T-30).
func WriteRunFacts(runDir string, trace *BehaviorTrace) (string, error) {
	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal run facts: %w", err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(runDir, "run_facts.json")
	if err := writeFileAtomic(path, data); err != nil {
		return "", fmt.Errorf("failed to write run facts: %w", err)
	}
	return path, nil
}

// PromptText — компактное текстовое представление трейса для промпта
// distill (плейсхолдер {{behavior_trace}}). Лимит maxLen символов; при
// превышении секции усекаются с конца (исход и гейты важнее хвостов логов).
func (t *BehaviorTrace) PromptText(maxLen int) string {
	if maxLen <= 0 {
		maxLen = TracePromptBudget
	}
	var sb strings.Builder

	fmt.Fprintf(&sb, "Исход рана: %s", t.Outcome.State)
	if t.Outcome.FinalVerdict != "" {
		fmt.Fprintf(&sb, ", финальный verdict: %s", t.Outcome.FinalVerdict)
	}
	if t.Outcome.Iterations > 0 {
		fmt.Fprintf(&sb, ", итераций fix-петли: %d", t.Outcome.Iterations)
	}
	fmt.Fprintf(&sb, ", гейтов approved: %d, rejected: %d\n",
		t.Outcome.GatesApproved, t.Outcome.GatesRejected)

	for _, g := range t.Gates {
		fmt.Fprintf(&sb, "\nГейт %s (%s) → ответ пользователя: %s\n", g.Kind, g.Question, g.Answer)
	}
	for _, n := range t.Notes {
		fmt.Fprintf(&sb, "\nЗаметка (%s): %s\n", n.Kind, n.Text)
	}

	for _, iv := range t.Verdicts {
		fmt.Fprintf(&sb, "\n## Verdict итерации %d: %s\n", iv.Iteration, iv.Verdict.Verdict)
		for _, f := range iv.Verdict.Findings {
			fmt.Fprintf(&sb, "- [%s] %s (%s:%d): %s → требуется: %s",
				f.Severity, f.ID, f.File, f.Line, f.Observed, f.RequiredFix)
			if f.ForbiddenFix != nil && *f.ForbiddenFix != "" {
				fmt.Fprintf(&sb, " (запрещено: %s)", *f.ForbiddenFix)
			}
			sb.WriteString("\n")
		}
		for _, q := range iv.Verdict.Questions {
			fmt.Fprintf(&sb, "- вопрос ревьюера: %s\n", q)
		}
	}

	if len(t.FixLoop) > 0 {
		sb.WriteString("\n## Fix-петля\n")
		for _, e := range t.FixLoop {
			if e.FixedAt > 0 {
				fmt.Fprintf(&sb, "- %s [%s]: найден на итерации %d, исправлен на %d\n",
					e.FindingID, e.Severity, e.FirstSeen, e.FixedAt)
			} else {
				fmt.Fprintf(&sb, "- %s [%s]: найден на итерации %d, НЕ закрыт\n",
					e.FindingID, e.Severity, e.FirstSeen)
			}
		}
	}

	for _, d := range t.Disputes {
		fmt.Fprintf(&sb, "\nСпор фиксера: %s\n", d)
	}

	for _, fs := range t.FailedStages {
		fmt.Fprintf(&sb, "\n## Этап %s (итерация %d) упал: exit %d %s\n", fs.Key, fs.Iteration, fs.ExitCode, fs.Error)
		if fs.LogExcerpt != "" {
			fmt.Fprintf(&sb, "Хвост лога:\n%s\n", fs.LogExcerpt)
		}
	}

	for _, issue := range t.ParseIssues {
		fmt.Fprintf(&sb, "\n(проблема парсинга артефакта: %s)\n", issue)
	}

	text := sb.String()
	if len(text) > maxLen {
		text = text[:maxLen] + "\n…(трейс усечён, полный — run_facts.json)"
	}
	return text
}

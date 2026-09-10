package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/service/lessons"
	"github.com/fableFM/glamor/internal/service/supervisor"
)

// MemorySearcher — FTS-поиск по vendor-памяти (T-23); реализация —
// internal/service/vendormemory.
type MemorySearcher interface {
	Search(ctx context.Context, query string, limit int) ([]dtorep.VendorSearchHit, error)
}

// LessonsProvider — подтверждённые уроки для промпта (T-29; реализация —
// internal/service/lessons). RelevantLessons возвращает текст секции и
// список инжектированных уроков (T-30: для relapse/outcome трекинга).
type LessonsProvider interface {
	RelevantLessons(ctx context.Context, taskText string) (string, []lessons.InjectedLesson, error)
	// RelevantVendorLessons — секция «Уроки по вендорам» (T-30, planner).
	RelevantVendorLessons(ctx context.Context, taskText string) (string, []lessons.InjectedLesson, error)
	// ExistingLessonsSummary — id+title+триггеры confirmed-уроков для
	// distill (T-30: дельта-операции REFINE/SUPERSEDE/LINK).
	ExistingLessonsSummary(ctx context.Context) (string, error)
	// RejectedTitles — заголовки отклонённых уроков (dedup для distill).
	RejectedTitles(ctx context.Context) ([]string, error)
}

// PromptBuilder — PromptBuilder дефолтного пайплайна (T-17): рендерит
// prompt_template этапа с плейсхолдерами {{...}}. Неизвестный плейсхолдер —
// ошибка (fail fast, а не молча пустой промпт).
type PromptBuilder struct {
	vendorGlobalDir string // ~/.glamor/vendors
	searcher        MemorySearcher
	lessons         LessonsProvider
}

func NewPromptBuilder(vendorGlobalDir string) *PromptBuilder {
	return &PromptBuilder{vendorGlobalDir: vendorGlobalDir}
}

// SetMemorySearcher подключает FTS-поиск памяти (T-23).
func (b *PromptBuilder) SetMemorySearcher(s MemorySearcher) {
	b.searcher = s
}

// SetLessonsProvider подключает инъекцию уроков (T-29).
func (b *PromptBuilder) SetLessonsProvider(p LessonsProvider) {
	b.lessons = p
}

// placeholderRe — общий regex плейсхолдеров, объявлен в validate.go (F-07):
// ловит любой {{...}}; resolve требует точного совпадения имени — неизвестное
// (включая верхний регистр и пробелы) = ошибка, литералов не остаётся.

// Build реализует supervisor.PromptBuilder.
func (b *PromptBuilder) Build(ctx context.Context, lc supervisor.LaunchContext) (string, error) {
	tpl := lc.StageSpec.PromptTemplate
	if tpl == "" {
		return "", fmt.Errorf("stage %q: empty prompt_template", lc.Stage.StageKey)
	}

	var renderErr error
	out := placeholderRe.ReplaceAllStringFunc(tpl, func(match string) string {
		if renderErr != nil {
			return match
		}
		name := placeholderRe.FindStringSubmatch(match)[1]
		value, err := b.resolve(ctx, name, lc)
		if err != nil {
			renderErr = err
			return match
		}
		return value
	})
	if renderErr != nil {
		return "", fmt.Errorf("stage %q: %w", lc.Stage.StageKey, renderErr)
	}
	return out, nil
}

// resolve вычисляет плейсхолдер (T-17).
func (b *PromptBuilder) resolve(ctx context.Context, name string, lc supervisor.LaunchContext) (string, error) {
	switch {
	case name == "task":
		return lc.Run.TaskText, nil
	case name == "base_branch":
		return lc.Run.BaseBranch, nil
	case name == "branch":
		return lc.Run.Branch, nil
	case name == "run_dir":
		return lc.RunDir, nil
	case name == "depth":
		return DepthName(lc.Run.Depth), nil
	case name == "depth_instructions":
		return b.depthInstructions(lc)
	case name == "queue_notes":
		return strings.Join(lc.SteerMessages, "\n\n"), nil
	case name == "vendor_memory_paths":
		return b.vendorMemoryPaths(lc), nil
	case name == "vendor_memory":
		return b.vendorMemory(ctx, lc)
	case name == "lessons":
		return b.confirmedLessons(ctx, lc)
	case name == "vendor_lessons":
		return b.vendorLessons(ctx, lc)
	case name == "behavior_trace":
		if lc.BehaviorTrace == "" {
			return "(трейс поведения не собран)", nil
		}
		return lc.BehaviorTrace, nil
	case name == "existing_lessons":
		return b.existingLessons(ctx)
	case name == "gate_answers":
		if lc.LessonSignals == "" {
			return "(сигналов нет)", nil
		}
		return lc.LessonSignals, nil
	case name == "rejected_lessons":
		return b.rejectedLessons(ctx)
	case name == "verdict":
		return readRunFile(lc.RunDir, "verdict.json"), nil
	case name == "iteration":
		return fmt.Sprintf("%d", fixerIteration(lc)), nil
	case name == "max_iterations":
		return fmt.Sprintf("%d", maxIterations(lc)), nil
	case strings.HasPrefix(name, "artifact."):
		// {{artifact.spec}} → <run_dir>/spec.md
		return filepath.Join(lc.RunDir, strings.TrimPrefix(name, "artifact.")), nil
	}
	return "", fmt.Errorf("unknown placeholder {{%s}}", name)
}

// depthInstructions — подмешиваемый кусок промпта планировщика по глубине
// (embedded depth-<name>.md из ai-pipeline).
func (b *PromptBuilder) depthInstructions(lc supervisor.LaunchContext) (string, error) {
	path := fmt.Sprintf("prompts/depth-%s.md", DepthName(lc.Run.Depth))
	data, err := promptsFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read depth preset %s: %w", path, err)
	}
	return string(data), nil
}

// vendorMemoryPaths — двухуровневая vendor-память (D-50): глобальная
// ~/.glamor/vendors + локальная <repo>/.glamor/vendors (в M1 — только чтение).
func (b *PromptBuilder) vendorMemoryPaths(lc supervisor.LaunchContext) string {
	local := filepath.Join(lc.ProjectPath, ".glamor", "vendors")
	return fmt.Sprintf("- глобальная: %s/\n- локальная (приоритетнее): %s/",
		b.vendorGlobalDir, local)
}

// vendorMemory — релевантные выдержки памяти по задаче (FTS5, top-5,
// ~2k токенов; пусто, если памяти нет — не ошибка, T-23).
func (b *PromptBuilder) vendorMemory(ctx context.Context, lc supervisor.LaunchContext) (string, error) {
	if b.searcher == nil {
		return "(поиск памяти не подключён)", nil
	}
	query := ftsQueryFromText(lc.Run.TaskText)
	if query == "" {
		return "(нет ключевых слов для поиска)", nil
	}
	hits, err := b.searcher.Search(ctx, query, 5)
	if err != nil || len(hits) == 0 {
		return "(релевантной памяти не найдено)", nil
	}

	var sb strings.Builder
	const budget = 2000 * 4 // ~2k токенов грубо в символах
	for _, hit := range hits {
		if sb.Len() > budget {
			break
		}
		fmt.Fprintf(&sb, "### %s\n%s\n\n", hit.Path, hit.Snippet)
	}
	return sb.String(), nil
}

// ftsQueryFromText — слова задачи → FTS5-запрос (OR), спецсимволы убраны.
func ftsQueryFromText(text string) string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	var terms []string
	seen := map[string]bool{}
	for _, w := range words {
		w = strings.ToLower(w)
		if len(w) < 3 || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, `"`+w+`"`)
		if len(terms) >= 12 {
			break
		}
	}
	return strings.Join(terms, " OR ")
}

// confirmedLessons — подтверждённые behavior-уроки для промпта (T-29/T-30:
// скоринг-ретрив, инъекция и в planner, и в coder). Инъекции журналируются
// в run_dir/injected-lessons.jsonl — петля качества (relapse/outcome)
// трекает только реально инжектированные в ран уроки.
func (b *PromptBuilder) confirmedLessons(ctx context.Context, lc supervisor.LaunchContext) (string, error) {
	if b.lessons == nil {
		return "(уроки не подключены)", nil
	}
	out, injected, err := b.lessons.RelevantLessons(ctx, lc.Run.TaskText)
	if err != nil || out == "" {
		return "(подтверждённых уроков нет)", nil
	}
	b.recordInjections(lc, injected)
	return out, nil
}

// vendorLessons — секция «Уроки по вендорам» (T-30, planner): kind=vendor,
// триггер — упоминание вендора в задаче; outdated инжектятся с префиксом-
// предупреждением.
func (b *PromptBuilder) vendorLessons(ctx context.Context, lc supervisor.LaunchContext) (string, error) {
	if b.lessons == nil {
		return "(уроки не подключены)", nil
	}
	out, injected, err := b.lessons.RelevantVendorLessons(ctx, lc.Run.TaskText)
	if err != nil || out == "" {
		return "(уроков по вендорам нет)", nil
	}
	b.recordInjections(lc, injected)
	return out, nil
}

// recordInjections — журнал инъекций рана (best-effort: сбой записи не
// должен ломать рендер промпта).
func (b *PromptBuilder) recordInjections(lc supervisor.LaunchContext, injected []lessons.InjectedLesson) {
	if lc.RunDir == "" {
		return
	}
	_ = lessons.AppendInjectedRecords(lc.RunDir, lc.Stage.StageKey, injected)
}

// existingLessons — сводка confirmed-уроков для distill (T-30).
func (b *PromptBuilder) existingLessons(ctx context.Context) (string, error) {
	if b.lessons == nil {
		return "(n/a)", nil
	}
	summary, err := b.lessons.ExistingLessonsSummary(ctx)
	if err != nil || summary == "" {
		return "(подтверждённых уроков нет)", nil
	}
	return summary, nil
}

// rejectedLessons — заголовки отклонённых уроков (dedup для distill, T-29).
func (b *PromptBuilder) rejectedLessons(ctx context.Context) (string, error) {
	if b.lessons == nil {
		return "(n/a)", nil
	}
	titles, err := b.lessons.RejectedTitles(ctx)
	if err != nil || len(titles) == 0 {
		return "(нет)", nil
	}
	return strings.Join(titles, "\n"), nil
}

// readRunFile читает файл артефактов рана (для {{verdict}}); отсутствует —
// пометка, а не ошибка (первая итерация fixer не бывает без verdict).
func readRunFile(runDir, name string) string {
	data, err := os.ReadFile(filepath.Join(runDir, name))
	if err != nil {
		return "(файл " + name + " пока отсутствует)"
	}
	return string(data)
}

// fixerIteration — номер текущей итерации fix-петли (попытка этапа).
func fixerIteration(lc supervisor.LaunchContext) int64 {
	return lc.Stage.Iteration
}

// maxIterations — лимит петли из LaunchContext (супервайзор кладёт из спеки;
// 0 → дефолт D-23).
func maxIterations(lc supervisor.LaunchContext) int64 {
	if lc.MaxIterations > 0 {
		return lc.MaxIterations
	}
	return 4
}

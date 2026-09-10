// Package lessons — причинно-следственная память (D-52, T-29; эволюция —
// T-30, D-81): карточки уроков «ситуация → симптом → причина → правило»,
// distill-этап, гейт «Сохранить урок?», инъекция в промпты с FTS и
// токен-бюджетом. Файлы пакета:
//   - lessons.go — карточки, SaveCard, статусы/виды, дедуп;
//   - trace.go — BehaviorTrace: детерминированный трейс поведения рана
//     (verdict.json, логи этапов, fix-петля) → run_facts.json + текст для
//     промпта distill;
//   - operations.go — операции distill v2 (NEW/REFINE/SUPERSEDE/LINK/
//     QUESTION) и их per-card применение;
//   - scoring.go — ретрив v2: score = w_rel·fts + w_imp·importance +
//     w_rec·recency (Generative Agents), RelevantLessons /
//     RelevantVendorLessons возвращают текст + список инъекций;
//   - vendor.go — vendor-уроки: парсинг, валидация точной версии,
//     деградация outdated по go.mod;
//   - quality.go — петля качества: RelapseCheck / OutcomeApprove;
//   - finalize.go — резолв-эффекты гейта lesson_review.
package lessons

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	lessonsrep "github.com/fableFM/glamor/internal/repository/lessons"
	"github.com/fableFM/glamor/internal/repository/vendorindex"
	"github.com/fableFM/glamor/pkg/uuid"
)

// Статусы карточек (миграция 20260817140000 + outdated из 20260819120000).
const (
	StatusProposed   = "proposed"
	StatusConfirmed  = "confirmed"
	StatusRejected   = "rejected"
	StatusSuperseded = "superseded"
	// StatusOutdated — vendor-урок, чья vendor_version разошлась с lockfile
	// проекта (T-30): не удаляем, инжектим с префиксом-предупреждением.
	StatusOutdated = "outdated"
)

// Виды уроков (T-30): behavior — поведенческие (как в T-29), vendor —
// знание о поведении конкретной версии вендора.
const (
	KindBehavior = "behavior"
	KindVendor   = "vendor"
)

// LessonTokenBudget — дефолтный бюджет инъекции уроков в промпт
// (~символов); переопределяется через SetTokenBudget (T-30: бюджет —
// параметр Service, константа остаётся дефолтом).
const LessonTokenBudget = 2000 * 4

// Service — сценарии уроков. Файлы — источник содержимого, БД —
// статусы/счётчики/индекс; FTS — через vendorindex (те же механики, T-23).
type Service struct {
	repo        lessonsrep.RepositoryWithTX
	index       *vendorindex.Repository
	globalDir   string // ~/.glamor/lessons
	tokenBudget int
}

func New(repo lessonsrep.RepositoryWithTX, index *vendorindex.Repository, globalDir string) *Service {
	return &Service{repo: repo, index: index, globalDir: globalDir, tokenBudget: LessonTokenBudget}
}

// SetTokenBudget переопределяет бюджет инъекции (из конфига демона, T-30).
// Неположительное значение игнорируется (остаётся дефолт).
func (s *Service) SetTokenBudget(budget int) {
	if budget > 0 {
		s.tokenBudget = budget
	}
}

// LessonsDir — каталог уроков: глобальный или проектный.
func (s *Service) LessonsDir(scope, projectPath string) string {
	if scope == "project" {
		return filepath.Join(projectPath, ".glamor", "lessons")
	}
	return s.globalDir
}

// Card — распарсенный черновик урока из lessons.md distill-этапа.
type Card struct {
	Title    string
	Triggers []string
	Body     string
}

// rawCard — сырая карточка: весь frontmatter как map + тело. Общий
// сплиттер для ParseCards (T-29), ParseOperations и ParseVendorCards (T-30).
type rawCard struct {
	fm   map[string]string
	body string
}

// splitCards разбирает markdown на карточки: frontmatter между парой "---"
// + тело до следующей карточки. Устойчив к мусору: невалидные куски
// пропускаются (LLM-вывод не ломает ядро). Ручной разбор — RE2 не
// поддерживает lookahead.
func splitCards(content string) []rawCard {
	var cards []rawCard

	const (
		stOutside = iota
		stFrontmatter
		stBody
	)
	state := stOutside
	var fm, body strings.Builder

	finishCard := func() {
		card := rawCard{fm: map[string]string{}, body: strings.TrimSpace(body.String())}
		for _, line := range strings.Split(fm.String(), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			card.fm[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
		if len(card.fm) > 0 || card.body != "" {
			cards = append(cards, card)
		}
		fm.Reset()
		body.Reset()
	}

	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "---" {
			switch state {
			case stOutside:
				state = stFrontmatter
			case stFrontmatter:
				state = stBody
			case stBody:
				finishCard()
				state = stFrontmatter
			}
			continue
		}
		switch state {
		case stFrontmatter:
			fm.WriteString(line + "\n")
		case stBody:
			body.WriteString(line + "\n")
		}
	}
	if state == stBody {
		finishCard()
	}
	return cards
}

// parseList — значение frontmatter вида "[a, b]" → срез строк.
func parseList(value string) []string {
	value = strings.Trim(strings.TrimSpace(value), "[]")
	var out []string
	for _, item := range strings.Split(value, ",") {
		if t := strings.TrimSpace(item); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ParseCards разбирает lessons.md на карточки уроков (поведение T-29:
// валидная карточка = title + тело).
func ParseCards(content string) []Card {
	var cards []Card
	for _, raw := range splitCards(content) {
		card := Card{
			Title:    raw.fm["title"],
			Triggers: parseList(raw.fm["triggers"]),
			Body:     raw.body,
		}
		if card.Title != "" && card.Body != "" {
			cards = append(cards, card)
		}
	}
	return cards
}

// Источники урока (T-30): влияют на начальную importance — сигнал от
// пользователя (answer/comment на гейте) весомее автоматического вывода
// distill из трейса рана.
const (
	SourceUser = "user"
	SourceAuto = "auto"
)

// Начальная importance по источнику (T-30; дальше важность двигают
// счётчики applied_success/relapse — см. scoring.go и repository).
const (
	ImportanceUser = 0.7 // урок из ответа/комментария пользователя
	ImportanceAuto = 0.5 // урок, выведенный из трейса без участия пользователя
)

// importanceForSource — начальная важность по источнику карточки.
func importanceForSource(source string) float64 {
	if source == SourceUser {
		return ImportanceUser
	}
	return ImportanceAuto
}

// SaveCardParams — параметры сохранения карточки (T-30: kind, vendor-поля,
// источник для importance). Kind пустой → behavior.
type SaveCardParams struct {
	Card          Card
	Scope         string // global | project
	ProjectID     *int64
	ProjectPath   string
	RunID         *string
	StageKey      string
	Status        string
	UserAnswer    string // ответ пользователя — дописывается в «Причину»
	Kind          string // behavior | vendor
	Vendor        string // только kind=vendor
	VendorVersion string
	Area          string // только kind=vendor
	Source        string // SourceUser | SourceAuto → начальная importance
}

// SaveCard сохраняет карточку: файл + строка в БД (status) + FTS-индекс.
// Дедуп: урок с таким же title и status=rejected уже есть → не сохраняем
// (T-29: отклонённый урок не предлагается повторно в том же виде).
// Vendor-карточки кладутся в подкаталог vendor/<name>/ (T-30).
func (s *Service) SaveCard(ctx context.Context, p SaveCardParams) (*dtorep.Lesson, error) {
	existing, err := s.repo.ListLessons(ctx, StatusRejected, "", nil)
	if err != nil {
		return nil, err
	}
	for _, l := range existing {
		if l.Title == p.Card.Title {
			return nil, fmt.Errorf("lesson %q was rejected before — not saved: %w", p.Card.Title, errDuplicate)
		}
	}

	kind := p.Kind
	if kind == "" {
		kind = KindBehavior
	}
	id := "lesson-" + uuid.New()
	dir := s.LessonsDir(p.Scope, p.ProjectPath)
	if kind == KindVendor && p.Vendor != "" {
		dir = filepath.Join(dir, "vendor", sanitizePathPart(p.Vendor))
	}
	path := filepath.Join(dir, id+".md")

	body := p.Card.Body
	if p.UserAnswer != "" {
		body += fmt.Sprintf("\n\n## Причина (ответ пользователя)\n%s\n", p.UserAnswer)
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "---\nid: %s\nkind: %s\ntitle: %s\ntriggers: [%s]\nstatus: %s\ncreated: %s\n",
		id, kind, p.Card.Title, strings.Join(p.Card.Triggers, ", "), p.Status,
		time.Now().UTC().Format("2006-01-02"))
	if kind == KindVendor {
		fmt.Fprintf(&buf, "vendor: %s\nvendor_version: %s\narea: %s\n",
			p.Vendor, p.VendorVersion, p.Area)
	}
	fmt.Fprintf(&buf, "---\n\n%s\n", body)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, []byte(buf.String())); err != nil {
		return nil, err
	}

	triggersJSON := "[]"
	if data, err := json.Marshal(p.Card.Triggers); err == nil {
		triggersJSON = string(data)
	}
	lesson := &dtorep.Lesson{
		ID:           id,
		Title:        p.Card.Title,
		Scope:        p.Scope,
		Status:       p.Status,
		Kind:         kind,
		ProjectID:    p.ProjectID,
		Path:         path,
		TriggersJSON: triggersJSON,
		RunID:        p.RunID,
		StageKey:     p.StageKey,
		Importance:   importanceForSource(p.Source),
	}
	if kind == KindVendor {
		lesson.Vendor = strPtr(p.Vendor)
		lesson.VendorVersion = strPtr(p.VendorVersion)
		lesson.Area = strPtr(p.Area)
	}
	if err := s.repo.CreateLesson(ctx, lesson); err != nil {
		return nil, err
	}

	data, _ := os.ReadFile(path)
	if err := s.index.Upsert(ctx, path, string(data)); err != nil {
		return nil, fmt.Errorf("failed to index lesson: %w", err)
	}
	return lesson, nil
}

// writeFileAtomic — tmp+rename (атомарная запись файла урока).
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sanitizePathPart — имя вендора → безопасный кусок пути каталога.
func sanitizePathPart(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\':
			return '_'
		default:
			return r
		}
	}, s)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RejectedTitles — заголовки отклонённых уроков (dedup для distill, T-29).
func (s *Service) RejectedTitles(ctx context.Context) ([]string, error) {
	rejected, err := s.repo.ListLessons(ctx, StatusRejected, "", nil)
	if err != nil {
		return nil, err
	}
	var titles []string
	for _, l := range rejected {
		titles = append(titles, l.Title)
	}
	return titles, nil
}

// GetByID — урок по id.
func (s *Service) GetByID(ctx context.Context, id string) (*dtorep.Lesson, error) {
	return s.repo.GetLessonByID(ctx, id)
}

// List — список уроков (UI «Память → Уроки»).
func (s *Service) List(ctx context.Context, status, scope string, projectID *int64) ([]dtorep.Lesson, error) {
	return s.repo.ListLessons(ctx, status, scope, projectID)
}

// SetStatus — смена статуса (confirm/reject из UI вне гейта).
func (s *Service) SetStatus(ctx context.Context, id, status string) error {
	return s.repo.UpdateLessonStatus(ctx, id, status)
}

// ReadContent — содержимое файла урока.
func (s *Service) ReadContent(ctx context.Context, id string) (string, error) {
	lesson, err := s.repo.GetLessonByID(ctx, id)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(lesson.Path)
	if err != nil {
		return "", fmt.Errorf("failed to read lesson file: %w", err)
	}
	return string(data), nil
}

// ftsQueryFromText — слова задачи → FTS5-запрос (та же логика, что pipeline).
func ftsQueryFromText(text string) string {
	var terms []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
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

// errDuplicate — дедуп отклонённых уроков.
var errDuplicate = fmt.Errorf("duplicate lesson")

// IsDuplicate — признак дедупа (для API: не ошибка, а skip).
func IsDuplicate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate lesson")
}

// Package lessons — причинно-следственная память (D-52, T-29): карточки
// уроков «ситуация → симптом → причина → правило», distill-этап, гейт
// «Сохранить урок?», инъекция в промпты с FTS и токен-бюджетом.
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

// Статусы карточек (миграция 20260817140000).
const (
	StatusProposed   = "proposed"
	StatusConfirmed  = "confirmed"
	StatusRejected   = "rejected"
	StatusSuperseded = "superseded"
)

// LessonTokenBudget — бюджет инъекции уроков в промпт (~символов).
const LessonTokenBudget = 2000 * 4

// Service — сценарии уроков. Файлы — источник содержимого, БД —
// статусы/счётчики/индекс; FTS — через vendorindex (те же механики, T-23).
type Service struct {
	repo      lessonsrep.RepositoryWithTX
	index     *vendorindex.Repository
	globalDir string // ~/.glamor/lessons
}

func New(repo lessonsrep.RepositoryWithTX, index *vendorindex.Repository, globalDir string) *Service {
	return &Service{repo: repo, index: index, globalDir: globalDir}
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

// ParseCards разбирает lessons.md на карточки: frontmatter между парой
// "---" + тело до следующей карточки. Устойчив к мусору: невалидные
// куски пропускаются (LLM-вывод не ломает ядро). Ручной разбор — RE2
// не поддерживает lookahead.
func ParseCards(content string) []Card {
	var cards []Card

	const (
		stOutside = iota
		stFrontmatter
		stBody
	)
	state := stOutside
	var fm, body strings.Builder

	finishCard := func() {
		card := Card{Body: strings.TrimSpace(body.String())}
		for _, line := range strings.Split(fm.String(), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			switch strings.TrimSpace(key) {
			case "title":
				card.Title = strings.TrimSpace(value)
			case "triggers":
				value = strings.Trim(strings.TrimSpace(value), "[]")
				for _, trg := range strings.Split(value, ",") {
					if t := strings.TrimSpace(trg); t != "" {
						card.Triggers = append(card.Triggers, t)
					}
				}
			}
		}
		if card.Title != "" && card.Body != "" {
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

// SaveCard сохраняет карточку: файл + строка в БД (status) + FTS-индекс.
// Дедуп: урок с таким же title и status=rejected уже есть → не сохраняем
// (T-29: отклонённый урок не предлагается повторно в том же виде).
func (s *Service) SaveCard(ctx context.Context, card Card, scope string, projectID *int64, projectPath string,
	runID *string, stageKey, status, userAnswer string,
) (*dtorep.Lesson, error) {
	existing, err := s.repo.ListLessons(ctx, StatusRejected, "", nil)
	if err != nil {
		return nil, err
	}
	for _, l := range existing {
		if l.Title == card.Title {
			return nil, fmt.Errorf("lesson %q was rejected before — not saved: %w", card.Title, errDuplicate)
		}
	}

	id := "lesson-" + uuid.New()
	dir := s.LessonsDir(scope, projectPath)
	path := filepath.Join(dir, id+".md")

	body := card.Body
	if userAnswer != "" {
		body += fmt.Sprintf("\n\n## Причина (ответ пользователя)\n%s\n", userAnswer)
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "---\nid: %s\ntitle: %s\ntriggers: [%s]\nstatus: %s\ncreated: %s\n---\n\n%s\n",
		id, card.Title, strings.Join(card.Triggers, ", "), status,
		time.Now().UTC().Format("2006-01-02"), body)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(buf.String()), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}

	triggersJSON := "[]"
	if data, err := json.Marshal(card.Triggers); err == nil {
		triggersJSON = string(data)
	}
	lesson := &dtorep.Lesson{
		ID:           id,
		Title:        card.Title,
		Scope:        scope,
		Status:       status,
		ProjectID:    projectID,
		Path:         path,
		TriggersJSON: triggersJSON,
		RunID:        runID,
		StageKey:     stageKey,
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

// RelevantLessons — confirmed-уроки для промпта (FTS по задаче,
// токен-бюджет, applied_count++), T-29.
func (s *Service) RelevantLessons(ctx context.Context, taskText string) (string, error) {
	query := ftsQueryFromText(taskText)
	if query == "" {
		return "", nil
	}
	hits, err := s.index.Search(ctx, query, 10)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, hit := range hits {
		if !strings.Contains(hit.Path, string(filepath.Separator)+"lessons"+string(filepath.Separator)) {
			continue // vendor-память инжектится своим плейсхолдером (T-23)
		}
		if sb.Len() > LessonTokenBudget {
			break
		}

		// подтверждаем, что урок confirmed, и инкрементируем счётчик
		lesson, err := s.findByPath(ctx, hit.Path)
		if err != nil || lesson.Status != StatusConfirmed {
			continue
		}
		data, err := os.ReadFile(hit.Path)
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "### %s\n%s\n\n", lesson.Title, string(data))
		_ = s.repo.IncrementApplied(ctx, lesson.ID)
	}
	return sb.String(), nil
}

// findByPath — урок по пути файла (для FTS-инъекции).
func (s *Service) findByPath(ctx context.Context, path string) (*dtorep.Lesson, error) {
	all, err := s.repo.ListLessons(ctx, "", "", nil)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Path == path {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("lesson at %s: not found", path)
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

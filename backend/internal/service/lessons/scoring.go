package lessons

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fableFM/glamor/internal/dto/dtorep"
)

// Веса скоринга ретрива (T-30) по мотивам Generative Agents
// (score = w_rel·relevance + w_imp·importance + w_rec·recency), но с
// детерминированными компонентами: relevance — нормированный ранг FTS5
// (bm25), importance — производная источника и счётчиков (хранится в БД),
// recency — экспоненциальный затухатель от last_applied_at/created_at.
//
// Обоснование весов: релевантность задаче — доминирующий сигнал (1.0),
// иначе в промпт попадут «важные, но не про задачу» уроки; важность и
// свежесть — тай-брейкеры равного порядка (0.5/0.5): урок, подтверждённый
// пользователем или часто срабатывающий, обгоняет свежий, но слабый, и
// наоборот. Тюнинг на статистике ранов — отдельная задача (T-30, вне
// скоупа).
const (
	scoreWeightRelevance  = 1.0
	scoreWeightImportance = 0.5
	scoreWeightRecency    = 0.5

	// recencyHalfLifeDays — период полураспада свежести (дней): урок,
	// не применявшийся месяц, теряет половину recency-компоненты.
	recencyHalfLifeDays = 30.0

	// ftsCandidateLimit — ширина FTS-выборки до скоринга (был top-10
	// без скоринга; теперь ранжируем шире и отрезаем бюджетом).
	ftsCandidateLimit = 25
)

// OutdatedPrefix — предупреждение для уроков, чья vendor-версия разошлась
// с lockfile проекта (T-30): инжектим, но с явным маркером перепроверки.
const OutdatedPrefix = "ВЕРСИЯ ИЗМЕНИЛАСЬ — ПЕРЕПРОВЕРИТЬ"

// InjectedLesson — урок, попавший в промпт рана (T-30). Список инъекций
// нужен supervisor'у для петли качества: relapse/outcome трекинга
// (quality.go) и событий журнала.
type InjectedLesson struct {
	Lesson   *dtorep.Lesson
	Score    float64
	Outdated bool // инжектирован с префиксом OutdatedPrefix
}

// RelevantLessons — behavior-уроки для промпта (скоринг-ретрив, T-30):
// FTS-кандидаты → скоринг → токен-бюджет. Superseded исключены; outdated
// инжектятся с префиксом OutdatedPrefix. Возвращает текст секции и список
// инжектированных уроков (для relapse/outcome трекинга вызывающим).
func (s *Service) RelevantLessons(ctx context.Context, taskText string) (string, []InjectedLesson, error) {
	return s.relevant(ctx, taskText, KindBehavior)
}

// RelevantVendorLessons — секция «Уроки по вендорам» (T-30): только
// kind=vendor, триггер — упоминание вендора/технологии в задаче (FTS).
func (s *Service) RelevantVendorLessons(ctx context.Context, taskText string) (string, []InjectedLesson, error) {
	return s.relevant(ctx, taskText, KindVendor)
}

// relevant — общий скоринг-ретрив по виду уроков.
func (s *Service) relevant(ctx context.Context, taskText, kind string) (string, []InjectedLesson, error) {
	query := ftsQueryFromText(taskText)
	if query == "" {
		return "", nil, nil
	}
	hits, err := s.index.Search(ctx, query, ftsCandidateLimit)
	if err != nil {
		return "", nil, err
	}

	var candidates []scoreCandidate
	for _, hit := range hits {
		if !strings.Contains(hit.Path, string(filepath.Separator)+"lessons"+string(filepath.Separator)) {
			continue // vendor-память инжектится своим плейсхолдером (T-23)
		}
		lesson, err := s.repo.GetLessonByPath(ctx, hit.Path)
		if err != nil {
			continue // файл без строки в БД (или наоборот) — не инжектим
		}
		if lesson.Kind != kind {
			continue
		}
		switch lesson.Status {
		case StatusConfirmed, StatusOutdated:
			// инжектим; superseded/rejected/proposed — нет
		default:
			continue
		}
		candidates = append(candidates, scoreCandidate{lesson: lesson, rank: hit.Rank})
	}
	if len(candidates) == 0 {
		return "", nil, nil
	}

	scored := scoreCandidates(candidates, time.Now().UTC())

	var sb strings.Builder
	totalRunes := 0 // m9: бюджет в символах (рунах), не в байтах
	var injected []InjectedLesson
	for _, c := range scored {
		if totalRunes > s.tokenBudget {
			break
		}
		data, err := os.ReadFile(c.lesson.Path)
		if err != nil {
			continue
		}
		outdated := c.lesson.Status == StatusOutdated
		if outdated {
			fmt.Fprintf(&sb, "### %s: %s\n%s\n\n", OutdatedPrefix, c.lesson.Title, string(data))
		} else {
			fmt.Fprintf(&sb, "### %s\n%s\n\n", c.lesson.Title, string(data))
		}
		totalRunes = utf8.RuneCountInString(sb.String())
		injected = append(injected, InjectedLesson{Lesson: c.lesson, Score: c.score, Outdated: outdated})
		_ = s.repo.IncrementApplied(ctx, c.lesson.ID)
	}
	return sb.String(), injected, nil
}

type scoreCandidate struct {
	lesson *dtorep.Lesson
	rank   float64
}

type scoredLesson struct {
	lesson *dtorep.Lesson
	score  float64
}

// scoreCandidates вычисляет score каждого кандидата и сортирует по убыванию.
func scoreCandidates(candidates []scoreCandidate, now time.Time) []scoredLesson {
	// Нормировка FTS-ранга: bm25 возвращает отрицательные значения, лучший
	// — самый отрицательный. Делим на минимум → лучший хит = 1.0, остальные
	// в (0,1]. Вырожденный случай (ранги нулевые) → всем 1.0.
	minRank := 0.0
	for _, c := range candidates {
		if c.rank < minRank {
			minRank = c.rank
		}
	}

	scored := make([]scoredLesson, 0, len(candidates))
	for _, c := range candidates {
		relevance := 1.0
		if minRank < 0 {
			relevance = c.rank / minRank
		}
		score := scoreWeightRelevance*relevance +
			scoreWeightImportance*c.lesson.Importance +
			scoreWeightRecency*recencyDecay(c.lesson, now)
		scored = append(scored, scoredLesson{lesson: c.lesson, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	return scored
}

// recencyDecay — экспоненциальное затухание 0.5^(дни/halfLife) от
// last_applied_at (последнее применение) или created_at (ещё не применялся).
func recencyDecay(l *dtorep.Lesson, now time.Time) float64 {
	base := l.CreatedAt
	if l.LastAppliedAt != nil {
		base = *l.LastAppliedAt
	}
	ageDays := now.Sub(base).Hours() / 24
	if ageDays < 0 {
		ageDays = 0 // часы на машине пользователя могут шалить
	}
	return math.Pow(0.5, ageDays/recencyHalfLifeDays)
}

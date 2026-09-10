package lessons

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
)

// Консолидация уроков (T-30, management): ручной prune из UI. Ядро только
// считает кандидатов детерминированными запросами/правилами — никакой
// авто-консолидации без человека (D-52 неизменен).

// DefaultSupersededStaleDays — возраст superseded-урока (дней), после
// которого он считается кандидатом на prune.
const DefaultSupersededStaleDays = 30

// DuplicatePair — пара confirmed-уроков-дублей: пересечение триггеров или
// FTS-схожесть заголовков.
type DuplicatePair struct {
	Lesson    dtorep.Lesson `json:"lesson"`
	SimilarTo dtorep.Lesson `json:"similar_to"`
	Reason    string        `json:"reason"` // shared_triggers:<список> | similar_title
}

// ConsolidationReport — кандидаты на консолидацию (вкладка «Консолидация»).
type ConsolidationReport struct {
	Duplicates      []DuplicatePair `json:"duplicates"`
	StaleSuperseded []dtorep.Lesson `json:"stale_superseded"`
	Unhealthy       []dtorep.Lesson `json:"unhealthy"` // relapse >= applied_success
}

// ConsolidationCandidates собирает кандидатов: дубли confirmed-уроков
// (overlap триггеров / FTS-схожесть title), superseded старше
// supersededDays дней, уроки с relapse >= applied_success (0 дней → дефолт).
func (s *Service) ConsolidationCandidates(ctx context.Context, supersededDays int) (*ConsolidationReport, error) {
	if supersededDays <= 0 {
		supersededDays = DefaultSupersededStaleDays
	}
	report := &ConsolidationReport{}

	stale, err := s.repo.ListSupersededOlderThan(ctx,
		time.Now().UTC().Add(-time.Duration(supersededDays)*24*time.Hour))
	if err != nil {
		return nil, err
	}
	report.StaleSuperseded = stale

	attention, err := s.repo.ListAttention(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range attention {
		if l.RelapseCount > 0 && l.RelapseCount >= l.AppliedSuccessCount {
			report.Unhealthy = append(report.Unhealthy, l)
		}
	}

	confirmed, err := s.repo.ListLessons(ctx, StatusConfirmed, "", nil)
	if err != nil {
		return nil, err
	}
	report.Duplicates = s.findDuplicates(ctx, confirmed)
	return report, nil
}

// findDuplicates — детерминированный поиск дублей среди confirmed-уроков:
// 1) общий триггер (регистронезависимо) — самый сильный сигнал;
// 2) FTS-схожесть заголовка (заголовок одного урока FTS-матчится с файлом
// другого confirmed-урока).
// Пары отсортированы по id — вывод стабилен.
func (s *Service) findDuplicates(ctx context.Context, confirmed []dtorep.Lesson) []DuplicatePair {
	byPath := make(map[string]*dtorep.Lesson, len(confirmed))
	for i := range confirmed {
		byPath[confirmed[i].Path] = &confirmed[i]
	}

	seen := map[string]bool{} // "id1|id2" (id1 < id2) — пара учтена один раз
	var pairs []DuplicatePair
	add := func(a, b *dtorep.Lesson, reason string) {
		if a.ID == b.ID {
			return
		}
		key := a.ID + "|" + b.ID
		if a.ID > b.ID {
			key = b.ID + "|" + a.ID
		}
		if seen[key] {
			return
		}
		seen[key] = true
		pairs = append(pairs, DuplicatePair{Lesson: *a, SimilarTo: *b, Reason: reason})
	}

	// overlap триггеров: триггер → уроки с ним
	byTrigger := map[string][]*dtorep.Lesson{}
	for i := range confirmed {
		l := &confirmed[i]
		for _, t := range triggerNames(l.TriggersJSON) {
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "" {
				continue
			}
			byTrigger[t] = append(byTrigger[t], l)
		}
	}
	triggers := make([]string, 0, len(byTrigger))
	for t := range byTrigger {
		triggers = append(triggers, t)
	}
	sort.Strings(triggers)
	for _, t := range triggers {
		group := byTrigger[t]
		if len(group) < 2 {
			continue
		}
		for i := 0; i < len(group); i++ {
			for j := i + 1; j < len(group); j++ {
				add(group[i], group[j], "shared_trigger:"+t)
			}
		}
	}

	// FTS-схожесть заголовков
	for i := range confirmed {
		l := &confirmed[i]
		query := ftsQueryFromText(l.Title)
		if query == "" {
			continue
		}
		hits, err := s.index.Search(ctx, query, 5)
		if err != nil {
			continue // индекс недоступен — overlap-триггеры уже посчитаны
		}
		for _, hit := range hits {
			if !strings.Contains(hit.Path, string(filepath.Separator)+"lessons"+string(filepath.Separator)) {
				continue
			}
			other, ok := byPath[hit.Path]
			if !ok || other.ID == l.ID {
				continue
			}
			add(l, other, "similar_title")
		}
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Lesson.ID != pairs[j].Lesson.ID {
			return pairs[i].Lesson.ID < pairs[j].Lesson.ID
		}
		return pairs[i].SimilarTo.ID < pairs[j].SimilarTo.ID
	})
	return pairs
}

// ListFiltered — список уроков с фильтрами UI (T-30): kind (behavior|
// vendor), attention («требуют внимания»: outdated или relapse >= applied).
func (s *Service) ListFiltered(ctx context.Context, status, scope, kind string,
	attention bool, projectID *int64,
) ([]dtorep.Lesson, error) {
	var (
		base []dtorep.Lesson
		err  error
	)
	switch {
	case attention:
		base, err = s.repo.ListAttention(ctx)
	case kind != "":
		base, err = s.repo.ListByKind(ctx, kind, status)
	default:
		base, err = s.repo.ListLessons(ctx, status, scope, projectID)
	}
	if err != nil {
		return nil, err
	}

	// attention/kind-выборки дофильтровываем в памяти (списки уроков малы).
	if !attention && kind == "" {
		return base, nil
	}
	out := make([]dtorep.Lesson, 0, len(base))
	for _, l := range base {
		if status != "" && l.Status != status {
			continue
		}
		if scope != "" && l.Scope != scope {
			continue
		}
		if kind != "" && l.Kind != kind {
			continue
		}
		if projectID != nil && (l.ProjectID == nil || *l.ProjectID != *projectID) {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

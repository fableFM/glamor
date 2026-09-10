package lessons

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Петля качества уроков (T-30, Dynamic Cheatsheet): различение «урок
// инжектирован» и «урок помог». Эвристики детерминированные и могут
// ошибаться → меняются только счётчики (relapse_count /
// applied_success_count), статусы уроков автоматически не трогаются
// (подсветка «требует внимания» — забота UI).

// RelapseHit — сработавший relapse: blocking/major finding совпал с телом/
// триггерами урока, инжектированного в этот же ран. Возвращается
// вызывающему для события в журнале (D-11) и UI-флага.
type RelapseHit struct {
	LessonID    string
	LessonTitle string
	FindingID   string
	Severity    string
}

// relapseSeverities — finding каких severity считаются рецидивом урока.
var relapseSeverities = map[string]bool{"blocking": true, "major": true}

// RelapseCheck — relapse-детекция после этапа reviewer: если blocking/major
// finding FTS-матчится с содержимым инжектированного в ран урока — урок не
// сработал: IncrementRelapse + хит в результате (для события журнала).
// Один урок штрафуется один раз за вызов, даже если совпало несколько
// findings.
func (s *Service) RelapseCheck(ctx context.Context, injected []InjectedLesson, findings []Finding) ([]RelapseHit, error) {
	if len(injected) == 0 {
		return nil, nil
	}
	byPath := make(map[string]InjectedLesson, len(injected))
	for _, inj := range injected {
		byPath[inj.Lesson.Path] = inj
	}

	relapsed := map[string]bool{}
	var hits []RelapseHit
	var errs []error
	for _, f := range findings {
		if !relapseSeverities[f.Severity] {
			continue // minor/advisory — шум, не рецидив
		}
		query := ftsQueryFromText(strings.Join(
			[]string{f.Observed, f.Expected, f.RequiredFix}, " "))
		if query == "" {
			continue
		}
		found, err := s.index.Search(ctx, query, 5)
		if err != nil {
			errs = append(errs, fmt.Errorf("relapse fts for finding %s: %w", f.ID, err))
			continue
		}
		for _, hit := range found {
			inj, ok := byPath[hit.Path]
			if !ok || relapsed[inj.Lesson.ID] {
				continue
			}
			relapsed[inj.Lesson.ID] = true
			if err := s.repo.IncrementRelapse(ctx, inj.Lesson.ID); err != nil {
				errs = append(errs, fmt.Errorf("relapse increment %s: %w", inj.Lesson.ID, err))
				continue
			}
			hits = append(hits, RelapseHit{
				LessonID:    inj.Lesson.ID,
				LessonTitle: inj.Lesson.Title,
				FindingID:   f.ID,
				Severity:    f.Severity,
			})
		}
	}
	return hits, errors.Join(errs...)
}

// OutcomeApprove — успешный исход: ран завершился approve'ом финального
// гейта без blocking-findings в области инжектированных уроков (вызывающий
// это гарантирует — RelapseCheck отдельно) → applied_success_count++ всем
// инжектированным.
func (s *Service) OutcomeApprove(ctx context.Context, injected []InjectedLesson) error {
	var errs []error
	for _, inj := range injected {
		if err := s.repo.IncrementAppliedSuccess(ctx, inj.Lesson.ID); err != nil {
			errs = append(errs, fmt.Errorf("outcome increment %s: %w", inj.Lesson.ID, err))
		}
	}
	return errors.Join(errs...)
}

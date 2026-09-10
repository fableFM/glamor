package lessons

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fableFM/glamor/internal/dto/dtorep"
)

// VendorCard — распарсенная vendor-карточка (T-30): знание «вендор X
// версии Y в области Z ведёт себя так». Секции тела — «Урок» и
// «Доказательства».
type VendorCard struct {
	Card                 // Title/Triggers/Body
	ID            string // id из frontmatter черновика (может отсутствовать)
	Vendor        string // обязательно
	VendorVersion string // обязательно, точная («latest» отклоняется)
	Area          string // обязательно: область (миграции / API X / ...)
	SourceRef     string // URL/путь к доке или vendor-памяти
	Status        string // из frontmatter; пусто → proposed
}

// ParseVendorCards разбирает vendor-lessons.md / lessons.md на vendor-
// карточки (kind: vendor во frontmatter). Устойчив к мусору, как ParseCards:
// карточки без kind: vendor пропускаются.
func ParseVendorCards(content string) []VendorCard {
	var cards []VendorCard
	for _, raw := range splitCards(content) {
		if strings.ToLower(raw.fm["kind"]) != KindVendor {
			continue
		}
		cards = append(cards, VendorCard{
			Card: Card{
				Title:    raw.fm["title"],
				Triggers: parseList(raw.fm["triggers"]),
				Body:     raw.body,
			},
			ID:            raw.fm["id"],
			Vendor:        raw.fm["vendor"],
			VendorVersion: raw.fm["vendor_version"],
			Area:          raw.fm["area"],
			SourceRef:     raw.fm["source"],
			Status:        raw.fm["status"],
		})
	}
	return cards
}

// ErrInvalidVendorDraft — драфт vendor-урока отклонён валидацией (T-30:
// vendor-урок без точной версии — не урок, отсекается ещё до гейта).
var ErrInvalidVendorDraft = fmt.Errorf("invalid vendor lesson draft")

// ValidateVendorDraft — валидация черновика vendor-урока до гейта:
// обязательные vendor/vendor_version/area, точная версия (не «latest» и
// не пусто), секции «Урок» и «Доказательства» (урок без источника не
// принимается, D-81).
func ValidateVendorDraft(c VendorCard) error {
	if strings.TrimSpace(c.Vendor) == "" {
		return fmt.Errorf("vendor is empty: %w", ErrInvalidVendorDraft)
	}
	version := strings.ToLower(strings.TrimSpace(c.VendorVersion))
	if version == "" || version == "latest" || version == "n/a" || version == "-" {
		return fmt.Errorf("vendor_version %q is not exact: %w", c.VendorVersion, ErrInvalidVendorDraft)
	}
	if strings.TrimSpace(c.Area) == "" {
		return fmt.Errorf("area is empty: %w", ErrInvalidVendorDraft)
	}
	if !strings.Contains(c.Body, "## Урок") {
		return fmt.Errorf("section «Урок» is missing: %w", ErrInvalidVendorDraft)
	}
	if !strings.Contains(c.Body, "## Доказательства") {
		return fmt.Errorf("section «Доказательства» is missing: %w", ErrInvalidVendorDraft)
	}
	return nil
}

// SaveVendorCard сохраняет подтверждённый vendor-урок. Scope (решение
// T-30): по умолчанию глобальный (~/.glamor/lessons/vendor/<name>/) —
// версия вендора общая для всех проектов; scope=project допустим, только
// если передан явно (свой форк/pin версии у проекта).
func (s *Service) SaveVendorCard(ctx context.Context, card VendorCard, scope, projectPath string,
	projectID *int64, runID *string, stageKey, status string,
) (*dtorep.Lesson, error) {
	if err := ValidateVendorDraft(card); err != nil {
		return nil, err
	}
	if scope != "project" {
		scope = "global"
		projectID = nil
	}
	if status == "" {
		status = StatusProposed
	}
	return s.SaveCard(ctx, SaveCardParams{
		Card: card.Card, Scope: scope, ProjectID: projectID,
		ProjectPath: projectPath, RunID: runID, StageKey: stageKey,
		Status: status, Kind: KindVendor, Vendor: card.Vendor,
		VendorVersion: card.VendorVersion, Area: card.Area,
		Source: SourceAuto,
	})
}

// CheckVendorVersions — версионная деградация (T-30): сверяет
// vendor_version vendor-уроков с go.mod проекта. Расхождение confirmed →
// outdated (урок не удаляется: поведение могло не измениться; инжектится с
// префиксом OutdatedPrefix). Обратное совпадение версии восстанавливает
// outdated → confirmed (m13: откат/pin версии — штатный сценарий).
// Возвращает уроки, переведённые в outdated и восстановленные на этом
// прогоне. Отсутствующий go.mod или отсутствие модуля в go.mod — не
// ошибка: судить не можем, статус не трогаем.
func (s *Service) CheckVendorVersions(ctx context.Context, projectPath string) (outdated, restored []dtorep.Lesson, err error) {
	data, err := os.ReadFile(filepath.Join(projectPath, "go.mod"))
	if err != nil {
		return nil, nil, nil // не Go-проект или нет go.mod — нечего сверять
	}
	modules := ParseGoMod(data)
	if len(modules) == 0 {
		return nil, nil, nil
	}

	confirmed, err := s.repo.ListVendor(ctx, "", StatusConfirmed)
	if err != nil {
		return nil, nil, err
	}
	for _, l := range confirmed {
		if l.Vendor == nil || l.VendorVersion == nil {
			continue
		}
		current, ok := matchModule(modules, *l.Vendor)
		if !ok || current == *l.VendorVersion {
			continue
		}
		if err := s.repo.UpdateLessonStatus(ctx, l.ID, StatusOutdated); err != nil {
			return outdated, restored, fmt.Errorf("failed to mark lesson %s outdated: %w", l.ID, err)
		}
		l.Status = StatusOutdated
		outdated = append(outdated, l)
	}

	stale, err := s.repo.ListVendor(ctx, "", StatusOutdated)
	if err != nil {
		return outdated, restored, err
	}
	for _, l := range stale {
		if l.Vendor == nil || l.VendorVersion == nil {
			continue
		}
		current, ok := matchModule(modules, *l.Vendor)
		if !ok || current != *l.VendorVersion {
			continue
		}
		if err := s.repo.UpdateLessonStatus(ctx, l.ID, StatusConfirmed); err != nil {
			return outdated, restored, fmt.Errorf("failed to restore lesson %s: %w", l.ID, err)
		}
		l.Status = StatusConfirmed
		restored = append(restored, l)
	}
	return outdated, restored, nil
}

// ParseGoMod — минимальный парсер go.mod: module → version из require
// (блочной и однострочной форм). Без внешних зависимостей (x/mod не
// подключён в проекте); комментарии и exclude/replace игнорируются —
// для сверки версий достаточно require.
func ParseGoMod(data []byte) map[string]string {
	modules := map[string]string{}
	inRequireBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i]) // комментарии
		}
		switch {
		case line == "require (":
			inRequireBlock = true
			continue
		case inRequireBlock && line == ")":
			inRequireBlock = false
			continue
		case strings.HasPrefix(line, "require ") && !inRequireBlock:
			line = strings.TrimPrefix(line, "require ")
		case !inRequireBlock:
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			modules[parts[0]] = parts[1]
		}
	}
	return modules
}

// matchModule — версия модуля, соответствующего имени вендора из урока.
// Имя вендора обычно короткое («goose»), модуль — полный путь
// («github.com/pressly/goose/v3»). Приоритет: точное совпадение пути,
// затем последний сегмент пути, затем вхождение подстроки. Обход —
// по отсортированным путям: результат детерминирован.
func matchModule(modules map[string]string, vendor string) (string, bool) {
	if v, ok := modules[vendor]; ok {
		return v, true
	}
	paths := make([]string, 0, len(modules))
	for mod := range modules {
		paths = append(paths, mod)
	}
	sort.Strings(paths)

	vendor = strings.ToLower(vendor)
	containsVersion := ""
	containsOK := false
	for _, mod := range paths {
		lower := strings.ToLower(mod)
		base := lower
		if i := strings.LastIndex(lower, "/"); i >= 0 {
			base = lower[i+1:]
		}
		if base == vendor || strings.HasSuffix(lower, "/"+vendor) {
			return modules[mod], true
		}
		if !containsOK && strings.Contains(lower, vendor) {
			containsVersion, containsOK = modules[mod], true
		}
	}
	return containsVersion, containsOK
}

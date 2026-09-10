package lessons

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fableFM/glamor/internal/dto/dtorep"
)

// Журналы рана (T-30): какие уроки реально попали в промпты этапов и какие
// relapse сработали. Нужны петле качества (relapse/outcome трекаются только
// по инжектированным в ЭТОТ ран урокам; один finding штрафует урок один
// раз за ран). Хранение — JSONL-артефакты run_dir (решение T-30: не
// контекст рана в БД — артефакты видны в UI и переживают рестарты без
// миграции схемы).
//
// Формат JSONL + O_APPEND (m12): read-modify-write JSON-файла при
// параллельных этапах (fan-out, T-28) терял бы записи; однострочный append
// атомарен на локальной ФС (запись < PIPE_BUF). Дедуп — на чтении.

const (
	// InjectedFileName — журнал инъекций уроков в промпты (JSONL).
	InjectedFileName = "injected-lessons.jsonl"
	// RelapseFileName — учтённые relapse-хиты рана (JSONL): дедуп по
	// finding id — finding, живущий несколько итераций reviewer,
	// инкрементирует relapse один раз за ран (M4).
	RelapseFileName = "relapse-hits.jsonl"
)

// InjectedRecord — одна инъекция урока в промпт этапа.
type InjectedRecord struct {
	LessonID string  `json:"lesson_id"`
	Title    string  `json:"title"`
	Kind     string  `json:"kind"` // behavior | vendor
	StageKey string  `json:"stage_key"`
	Score    float64 `json:"score"`
	Outdated bool    `json:"outdated"`
}

// RelapseHitRecord — учтённый relapse (дедуп M4): урок + finding.
type RelapseHitRecord struct {
	LessonID  string `json:"lesson_id"`
	FindingID string `json:"finding_id"`
}

// appendJSONL дописывает записи однострочным JSONL (O_APPEND).
func appendJSONL(path string, records []json.RawMessage) error {
	if len(records) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for _, rec := range records {
		if _, err := f.Write(append(rec, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// readJSONL читает JSONL-файл (нет файла/битые строки — пропускаются).
func readJSONL(path string, unmarshal func(line []byte)) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) > 0 {
			unmarshal(line)
		}
	}
}

// AppendInjectedRecords дописывает инъекции этапа в журнал рана (JSONL,
// append-only). Вызывается рендером промптов (service/pipeline), в т.ч.
// параллельно из fan-out веток.
func AppendInjectedRecords(runDir, stageKey string, injected []InjectedLesson) error {
	records := make([]json.RawMessage, 0, len(injected))
	for _, inj := range injected {
		if inj.Lesson == nil {
			continue
		}
		data, err := json.Marshal(InjectedRecord{
			LessonID: inj.Lesson.ID,
			Title:    inj.Lesson.Title,
			Kind:     inj.Lesson.Kind,
			StageKey: stageKey,
			Score:    inj.Score,
			Outdated: inj.Outdated,
		})
		if err != nil {
			return fmt.Errorf("failed to marshal injected lesson: %w", err)
		}
		records = append(records, data)
	}
	if err := appendJSONL(filepath.Join(runDir, InjectedFileName), records); err != nil {
		return fmt.Errorf("failed to write injected lessons: %w", err)
	}
	return nil
}

// ReadInjectedRecords читает журнал инъекций рана с дедупом по lesson_id
// (урок, инжектированный в planner и coder, учитывается один раз — первая
// инъекция).
func ReadInjectedRecords(runDir string) []InjectedRecord {
	seen := map[string]bool{}
	var out []InjectedRecord
	readJSONL(filepath.Join(runDir, InjectedFileName), func(line []byte) {
		var rec InjectedRecord
		if err := json.Unmarshal(line, &rec); err != nil || rec.LessonID == "" || seen[rec.LessonID] {
			return
		}
		seen[rec.LessonID] = true
		out = append(out, rec)
	})
	return out
}

// AppendRelapseHits фиксирует учтённые relapse-хиты рана (M4).
func AppendRelapseHits(runDir string, hits []RelapseHit) error {
	records := make([]json.RawMessage, 0, len(hits))
	for _, h := range hits {
		data, err := json.Marshal(RelapseHitRecord{LessonID: h.LessonID, FindingID: h.FindingID})
		if err != nil {
			return fmt.Errorf("failed to marshal relapse hit: %w", err)
		}
		records = append(records, data)
	}
	if err := appendJSONL(filepath.Join(runDir, RelapseFileName), records); err != nil {
		return fmt.Errorf("failed to write relapse hits: %w", err)
	}
	return nil
}

// ReadRelapseHits — учтённые relapse-хиты рана.
func ReadRelapseHits(runDir string) []RelapseHitRecord {
	var out []RelapseHitRecord
	readJSONL(filepath.Join(runDir, RelapseFileName), func(line []byte) {
		var rec RelapseHitRecord
		if err := json.Unmarshal(line, &rec); err == nil && rec.FindingID != "" {
			out = append(out, rec)
		}
	})
	return out
}

// LoadRunInjections — инжектированные в ран уроки как InjectedLesson
// (петля качества: relapse/outcome, T-30). Уроки, удалённые из БД после
// инъекции, пропускаются. relapsed уроки исключаются, если excludeRelapsed
// (outcome-трекинг: relapse в этом ране — урок не сработал, M4).
func (s *Service) LoadRunInjections(ctx context.Context, runDir string) ([]InjectedLesson, error) {
	records := ReadInjectedRecords(runDir)
	if len(records) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.LessonID)
	}
	found, err := s.repo.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*dtorep.Lesson, len(found))
	for i := range found {
		byID[found[i].ID] = &found[i]
	}
	outdatedByID := make(map[string]bool, len(records))
	for _, r := range records {
		outdatedByID[r.LessonID] = r.Outdated
	}
	out := make([]InjectedLesson, 0, len(found))
	for _, id := range ids {
		l, ok := byID[id]
		if !ok {
			continue
		}
		out = append(out, InjectedLesson{Lesson: l, Outdated: outdatedByID[id]})
	}
	return out, nil
}

// LoadSuccessfulInjections — инъекции рана БЕЗ relapse (outcome-трекинг,
// M4): урок, получивший relapse в этом ране, не засчитывается как успешно
// применённый.
func (s *Service) LoadSuccessfulInjections(ctx context.Context, runDir string) ([]InjectedLesson, error) {
	injected, err := s.LoadRunInjections(ctx, runDir)
	if err != nil || len(injected) == 0 {
		return injected, err
	}
	relapsed := map[string]bool{}
	for _, h := range ReadRelapseHits(runDir) {
		relapsed[h.LessonID] = true
	}
	out := injected[:0]
	for _, inj := range injected {
		if !relapsed[inj.Lesson.ID] {
			out = append(out, inj)
		}
	}
	return out, nil
}

// ExistingLessonsSummary — сводка confirmed-уроков для промпта distill
// (T-30, ACE: distill видит существующие карточки и предлагает дельты
// REFINE/SUPERSEDE/LINK вместо дублей). Формат строки:
// «- <id> | <kind> | <title> | triggers: a, b». Лимит — защита промпта.
func (s *Service) ExistingLessonsSummary(ctx context.Context) (string, error) {
	const maxEntries = 50
	confirmed, err := s.repo.ListLessons(ctx, StatusConfirmed, "", nil)
	if err != nil {
		return "", err
	}
	outdated, err := s.repo.ListLessons(ctx, StatusOutdated, "", nil)
	if err != nil {
		return "", err
	}
	all := make([]dtorep.Lesson, 0, len(confirmed)+len(outdated))
	all = append(all, confirmed...)
	all = append(all, outdated...)
	if len(all) == 0 {
		return "", nil
	}

	var sb strings.Builder
	for i, l := range all {
		if i >= maxEntries {
			fmt.Fprintf(&sb, "- …и ещё %d (полный список — UI «Память → Уроки»)\n", len(all)-maxEntries)
			break
		}
		triggers := triggerNames(l.TriggersJSON)
		status := ""
		if l.Status == StatusOutdated {
			status = " | OUTDATED"
		}
		fmt.Fprintf(&sb, "- %s | %s%s | %s | triggers: %s\n",
			l.ID, l.Kind, status, l.Title, strings.Join(triggers, ", "))
	}
	return sb.String(), nil
}

// triggerNames — triggers_json урока → срез имён (битый JSON → пусто).
func triggerNames(triggersJSON string) []string {
	var triggers []string
	if err := json.Unmarshal([]byte(triggersJSON), &triggers); err != nil {
		return nil
	}
	return triggers
}

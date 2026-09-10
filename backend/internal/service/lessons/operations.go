package lessons

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Операции distill v2 (T-30, ACE/A-MEM): distill возвращает не только новые
// карточки, но и дельты над существующими. Формат в lessons.md — поле `op:`
// в frontmatter карточки + `target:`/`target2:` (выбор T-30: frontmatter
// устойчив к мусору LLM так же, как ParseCards, — битая карточка просто
// пропускается, JSON-массив рядом с карточками ломался бы целиком от
// одной невалидной запятой).
type OpKind string

const (
	OpNew       OpKind = "new"       // новая карточка (по умолчанию, op: не задан)
	OpRefine    OpKind = "refine"    // уточнение confirmed-урока (target)
	OpSupersede OpKind = "supersede" // новая карточка вытесняет target
	OpLink      OpKind = "link"      // связь target ↔ target2
	OpQuestion  OpKind = "question"  // вопрос пользователю (карточка с «ВОПРОС:»)
)

// Operation — одна дельта-операция над карточками.
type Operation struct {
	Op        OpKind
	Card      Card   // NEW/REFINE/SUPERSEDE/QUESTION: формулировка
	TargetID  string // REFINE/SUPERSEDE/LINK: первый id
	Target2ID string // LINK: второй id
	// Vendor-поля (T-30): kind=vendor из frontmatter карточки; применение
	// NEW для vendor-карточки идёт через валидацию точной версии
	// (ValidateVendorDraft) — битый vendor-драфт отклоняется до сохранения.
	Kind          string
	Vendor        string
	VendorVersion string
	Area          string
}

// ParseOperations разбирает lessons.md в операции. Карточка без `op:` —
// NEW (обратная совместимость с форматом T-29). Карточка с «ВОПРОС:» в
// теле — QUESTION (конвенция distill-промпта). NO_LESSONS и мусор дают
// пустой список; битые операции (нет target у REFINE и т.п.) пропускаются.
func ParseOperations(content string) []Operation {
	var ops []Operation
	for _, raw := range splitCards(content) {
		op := Operation{
			Card: Card{
				Title:    raw.fm["title"],
				Triggers: parseList(raw.fm["triggers"]),
				Body:     raw.body,
			},
			TargetID:      raw.fm["target"],
			Target2ID:     raw.fm["target2"],
			Kind:          strings.ToLower(raw.fm["kind"]),
			Vendor:        raw.fm["vendor"],
			VendorVersion: raw.fm["vendor_version"],
			Area:          raw.fm["area"],
		}

		switch OpKind(strings.ToLower(raw.fm["op"])) {
		case OpRefine, OpSupersede:
			op.Op = OpKind(strings.ToLower(raw.fm["op"]))
		case OpLink:
			op.Op = OpLink
		case OpQuestion:
			op.Op = OpQuestion
		default:
			op.Op = OpNew
			// конвенция T-29: вопрос — карточка с «ВОПРОС:» в Причине
			if strings.Contains(raw.body, "ВОПРОС:") {
				op.Op = OpQuestion
			}
		}

		// валидация полноты операции (битая не должна долететь до apply)
		switch op.Op {
		case OpNew, OpQuestion:
			if op.Card.Title == "" || op.Card.Body == "" {
				continue
			}
		case OpRefine, OpSupersede:
			if op.TargetID == "" || op.Card.Title == "" || op.Card.Body == "" {
				continue
			}
		case OpLink:
			if op.TargetID == "" || op.Target2ID == "" || op.TargetID == op.Target2ID {
				continue
			}
		}
		ops = append(ops, op)
	}
	return ops
}

// ApplyParams — контекст применения операций (scope рана/гейта).
type ApplyParams struct {
	Scope       string // global | project — куда сохранять новые карточки
	ProjectID   *int64
	ProjectPath string
	RunID       *string
	StageKey    string
	Status      string // статус новых карточек (confirmed при approve гейта)
	UserAnswer  string // ответ пользователя — дописывается в «Причину» NEW
	Source      string // SourceUser | SourceAuto → начальная importance
}

// OpResult — итог применения одной операции (per-card независимость:
// битая операция не ломает остальные, результат — по каждой).
type OpResult struct {
	Op       Operation
	LessonID string // id созданной/изменённой карточки (пусто при ошибке)
	Skipped  bool   // дедуп: отклонённый ранее урок повторно не сохраняем
	Err      error
}

// ApplyOperations применяет операции distill v2. Каждая операция —
// последовательность «файл (tmp+rename) → БД → FTS-реиндекс»; транзакция
// охватывает БД-шаги одной операции, файловые операции атомарны сами
// (tmp+rename). Между операциями отката нет осознанно: per-card resolve
// (T-30) — принятая часть операций не должна зависеть от битой соседней.
func (s *Service) ApplyOperations(ctx context.Context, ops []Operation, p ApplyParams) []OpResult {
	results := make([]OpResult, 0, len(ops))
	for _, op := range ops {
		res := OpResult{Op: op}
		switch op.Op {
		case OpNew, OpQuestion:
			res.LessonID, res.Skipped, res.Err = s.applyNew(ctx, op, p)
		case OpRefine:
			res.LessonID, res.Err = s.applyRefine(ctx, op)
		case OpSupersede:
			res.LessonID, res.Skipped, res.Err = s.applySupersede(ctx, op, p)
		case OpLink:
			res.Err = s.applyLink(ctx, op)
		}
		results = append(results, res)
	}
	return results
}

func (s *Service) applyNew(ctx context.Context, op Operation, p ApplyParams) (id string, skipped bool, err error) {
	params := SaveCardParams{
		Card: op.Card, Scope: p.Scope, ProjectID: p.ProjectID,
		ProjectPath: p.ProjectPath, RunID: p.RunID, StageKey: p.StageKey,
		Status: p.Status, UserAnswer: p.UserAnswer, Source: p.Source,
	}
	if op.Kind == KindVendor {
		// vendor-драфт без точной версии/области — не урок: отклоняем до
		// сохранения (T-30), ошибка в OpResult не ломает остальные операции
		if err := ValidateVendorDraft(VendorCard{
			Card: op.Card, Vendor: op.Vendor, VendorVersion: op.VendorVersion, Area: op.Area,
		}); err != nil {
			return "", false, err
		}
		params.Kind = KindVendor
		params.Vendor = op.Vendor
		params.VendorVersion = op.VendorVersion
		params.Area = op.Area
		// vendor-уроки глобальны по природе (версия вендора общая, решение
		// T-30 как в SaveVendorCard): гейтный scope (project при approve)
		// к ним не применяем
		params.Scope = "global"
		params.ProjectID = nil
	}
	lesson, err := s.SaveCard(ctx, params)
	if err != nil {
		if IsDuplicate(err) {
			return "", true, nil
		}
		return "", false, err
	}
	return lesson.ID, false, nil
}

// applyRefine — уточнение confirmed-урока (T-30): файл перезаписывается
// новой формулировкой, старая версия сохраняется рядом как
// <id>.md.<timestamp>.bak (простая история без отдельного хранилища),
// БД (title/triggers) и FTS обновляются. REFINE outdated-урока (m10)
// допустим и восстанавливает его в confirmed — это и есть смысл refine
// outdated (версия перепроверена, формулировка уточнена).
func (s *Service) applyRefine(ctx context.Context, op Operation) (string, error) {
	lesson, err := s.repo.GetLessonByID(ctx, op.TargetID)
	if err != nil {
		return "", fmt.Errorf("refine target %s: %w", op.TargetID, err)
	}
	if lesson.Status != StatusConfirmed && lesson.Status != StatusOutdated {
		return "", fmt.Errorf("refine target %s: status %q, refine только confirmed/outdated", op.TargetID, lesson.Status)
	}

	old, err := os.ReadFile(lesson.Path)
	if err != nil {
		return "", fmt.Errorf("refine: failed to read current file: %w", err)
	}
	// история: копия прежней версии рядом с файлом
	bak := fmt.Sprintf("%s.%s.bak", lesson.Path, time.Now().UTC().Format("20060102150405"))
	if err := os.WriteFile(bak, old, 0o644); err != nil {
		return "", fmt.Errorf("refine: failed to write history file: %w", err)
	}

	status := StatusConfirmed // outdated восстанавливается refine'ом (m10)
	triggersJSON := "[]"
	if data, err := json.Marshal(op.Card.Triggers); err == nil {
		triggersJSON = string(data)
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "---\nid: %s\nkind: %s\ntitle: %s\ntriggers: [%s]\nstatus: %s\ncreated: %s\n",
		lesson.ID, lesson.Kind, op.Card.Title, strings.Join(op.Card.Triggers, ", "),
		status, lesson.CreatedAt.Format("2006-01-02"))
	// m7: vendor-метаданные не теряем при перезаписи frontmatter
	if lesson.Kind == KindVendor {
		fmt.Fprintf(&buf, "vendor: %s\nvendor_version: %s\narea: %s\n",
			derefStr(lesson.Vendor), derefStr(lesson.VendorVersion), derefStr(lesson.Area))
	}
	fmt.Fprintf(&buf, "---\n\n%s\n", op.Card.Body)
	if err := writeFileAtomic(lesson.Path, []byte(buf.String())); err != nil {
		return "", fmt.Errorf("refine: failed to rewrite file: %w", err)
	}
	if err := s.repo.UpdateLessonContent(ctx, lesson.ID, op.Card.Title, triggersJSON, lesson.Path); err != nil {
		return "", err
	}
	if lesson.Status != status {
		if err := s.repo.UpdateLessonStatus(ctx, lesson.ID, status); err != nil {
			return "", err
		}
	}
	if err := s.index.Upsert(ctx, lesson.Path, buf.String()); err != nil {
		return "", fmt.Errorf("refine: failed to reindex: %w", err)
	}
	return lesson.ID, nil
}

// derefStr — *string → строка (пусто для nil).
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// applySupersede — новая карточка вытесняет старую (T-30): новая сохраняется
// обычным SaveCard, старая → superseded + superseded_by (файл старой не
// удаляется, из инъекции исключается статус-фильтром скоринга).
func (s *Service) applySupersede(ctx context.Context, op Operation, p ApplyParams) (id string, skipped bool, err error) {
	if _, err := s.repo.GetLessonByID(ctx, op.TargetID); err != nil {
		return "", false, fmt.Errorf("supersede target %s: %w", op.TargetID, err)
	}
	newID, skipped, err := s.applyNew(ctx, op, p)
	if err != nil || skipped {
		return newID, skipped, err
	}
	if err := s.repo.SetSuperseded(ctx, op.TargetID, newID); err != nil {
		return "", false, err
	}
	return newID, false, nil
}

// applyLink — связь двух уроков (A-MEM related, T-30): related_json обоих
// дополняется взаимными ссылками (идемпотентно, без дублей).
func (s *Service) applyLink(ctx context.Context, op Operation) error {
	if err := s.linkOneWay(ctx, op.TargetID, op.Target2ID); err != nil {
		return err
	}
	return s.linkOneWay(ctx, op.Target2ID, op.TargetID)
}

func (s *Service) linkOneWay(ctx context.Context, id, otherID string) error {
	lesson, err := s.repo.GetLessonByID(ctx, id)
	if err != nil {
		return fmt.Errorf("link %s: %w", id, err)
	}
	var related []string
	if lesson.RelatedJSON != "" {
		if err := json.Unmarshal([]byte(lesson.RelatedJSON), &related); err != nil {
			related = nil // битый JSON — пересобираем с нуля
		}
	}
	for _, r := range related {
		if r == otherID {
			return nil // связь уже есть
		}
	}
	related = append(related, otherID)
	data, err := json.Marshal(related)
	if err != nil {
		return fmt.Errorf("link: failed to marshal related: %w", err)
	}
	return s.repo.UpdateRelated(ctx, id, string(data))
}

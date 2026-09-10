package lessons

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	store "github.com/fableFM/glamor/internal/repository"
)

type query struct {
	conn store.Conn
}

type scanner interface {
	Scan(dest ...any) error
}

// lesson — приватная модель строки таблицы lessons.
type lesson struct {
	id                  string
	title               string
	scope               string
	status              string
	kind                string
	projectID           sql.NullInt64
	path                string
	triggersJSON        string
	runID               sql.NullString
	stageKey            string
	vendor              sql.NullString
	vendorVersion       sql.NullString
	area                sql.NullString
	importance          float64
	appliedCount        int64
	appliedSuccessCount int64
	relapseCount        int64
	lastAppliedAt       sql.NullTime
	supersededBy        sql.NullString
	relatedJSON         string
	createdAt           time.Time
	updatedAt           sql.NullTime
}

const lessonColumns = `id, title, scope, status, kind, project_id, path, triggers_json,
	run_id, stage_key, vendor, vendor_version, area, importance,
	applied_count, applied_success_count, relapse_count,
	last_applied_at, superseded_by, related_json, created_at, updated_at`

func mapLessonToDTO(l lesson) *dtorep.Lesson {
	out := &dtorep.Lesson{
		ID:                  l.id,
		Title:               l.title,
		Scope:               l.scope,
		Status:              l.status,
		Kind:                l.kind,
		Path:                l.path,
		TriggersJSON:        l.triggersJSON,
		StageKey:            l.stageKey,
		Importance:          l.importance,
		AppliedCount:        l.appliedCount,
		AppliedSuccessCount: l.appliedSuccessCount,
		RelapseCount:        l.relapseCount,
		RelatedJSON:         l.relatedJSON,
		CreatedAt:           l.createdAt,
	}
	if l.projectID.Valid {
		out.ProjectID = &l.projectID.Int64
	}
	if l.runID.Valid {
		out.RunID = &l.runID.String
	}
	if l.vendor.Valid {
		out.Vendor = &l.vendor.String
	}
	if l.vendorVersion.Valid {
		out.VendorVersion = &l.vendorVersion.String
	}
	if l.area.Valid {
		out.Area = &l.area.String
	}
	if l.lastAppliedAt.Valid {
		t := l.lastAppliedAt.Time
		out.LastAppliedAt = &t
	}
	if l.supersededBy.Valid {
		out.SupersededBy = &l.supersededBy.String
	}
	if l.updatedAt.Valid {
		t := l.updatedAt.Time
		out.UpdatedAt = &t
	}
	return out
}

func scanLesson(s scanner) (lesson, error) {
	var l lesson
	err := s.Scan(&l.id, &l.title, &l.scope, &l.status, &l.kind, &l.projectID,
		&l.path, &l.triggersJSON, &l.runID, &l.stageKey,
		&l.vendor, &l.vendorVersion, &l.area, &l.importance,
		&l.appliedCount, &l.appliedSuccessCount, &l.relapseCount,
		&l.lastAppliedAt, &l.supersededBy, &l.relatedJSON,
		&l.createdAt, &l.updatedAt)
	return l, err
}

func (q *query) CreateLesson(ctx context.Context, l *dtorep.Lesson) error {
	kind := l.Kind
	if kind == "" {
		kind = "behavior"
	}
	importance := l.Importance
	if importance == 0 {
		importance = 0.5 // дефолт схемы; явные значения задает service
	}
	relatedJSON := l.RelatedJSON
	if relatedJSON == "" {
		relatedJSON = "[]"
	}
	_, err := q.conn.ExecContext(ctx,
		`INSERT INTO lessons (id, title, scope, status, kind, project_id, path,
			triggers_json, run_id, stage_key, vendor, vendor_version, area,
			importance, related_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.Title, l.Scope, l.Status, kind, l.ProjectID, l.Path,
		l.TriggersJSON, l.RunID, l.StageKey, l.Vendor, l.VendorVersion,
		l.Area, importance, relatedJSON)
	if err != nil {
		return fmt.Errorf("failed to insert lesson: %w", store.MapError(err))
	}
	return nil
}

func (q *query) GetLessonByID(ctx context.Context, id string) (*dtorep.Lesson, error) {
	row := q.conn.QueryRowContext(ctx, `SELECT `+lessonColumns+` FROM lessons WHERE id = ?`, id)
	l, err := scanLesson(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get lesson: %w", store.MapError(err))
	}
	return mapLessonToDTO(l), nil
}

// GetLessonByPath — урок по пути файла (замена линейного findByPath в
// service, T-30: FTS-хит резолвится одним запросом, а не сканом всех строк).
func (q *query) GetLessonByPath(ctx context.Context, path string) (*dtorep.Lesson, error) {
	row := q.conn.QueryRowContext(ctx, `SELECT `+lessonColumns+` FROM lessons WHERE path = ?`, path)
	l, err := scanLesson(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get lesson by path: %w", store.MapError(err))
	}
	return mapLessonToDTO(l), nil
}

// GetByIDs — пакетная выборка по id (скоринг инжектированных, T-30).
func (q *query) GetByIDs(ctx context.Context, ids []string) ([]dtorep.Lesson, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := q.conn.QueryContext(ctx,
		`SELECT `+lessonColumns+` FROM lessons WHERE id IN (`+
			strings.Join(placeholders, ", ")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get lessons by ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Lesson
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan lesson: %w", err)
		}
		out = append(out, *mapLessonToDTO(l))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate lessons: %w", err)
	}
	return out, nil
}

func (q *query) ListLessons(ctx context.Context, status, scope string, projectID *int64) ([]dtorep.Lesson, error) {
	sqlStr := `SELECT ` + lessonColumns + ` FROM lessons WHERE 1=1`
	args := []any{}
	if status != "" {
		sqlStr += ` AND status = ?`
		args = append(args, status)
	}
	if scope != "" {
		sqlStr += ` AND scope = ?`
		args = append(args, scope)
	}
	if projectID != nil {
		sqlStr += ` AND project_id = ?`
		args = append(args, *projectID)
	}
	sqlStr += ` ORDER BY created_at DESC`

	rows, err := q.conn.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list lessons: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Lesson
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan lesson: %w", err)
		}
		out = append(out, *mapLessonToDTO(l))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate lessons: %w", err)
	}
	return out, nil
}

// ListByKind — уроки заданного вида (behavior|vendor) и статуса
// (пустой status = все статусы), T-30.
func (q *query) ListByKind(ctx context.Context, kind, status string) ([]dtorep.Lesson, error) {
	sqlStr := `SELECT ` + lessonColumns + ` FROM lessons WHERE kind = ?`
	args := []any{kind}
	if status != "" {
		sqlStr += ` AND status = ?`
		args = append(args, status)
	}
	sqlStr += ` ORDER BY created_at DESC`

	rows, err := q.conn.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list lessons by kind: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Lesson
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan lesson: %w", err)
		}
		out = append(out, *mapLessonToDTO(l))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate lessons: %w", err)
	}
	return out, nil
}

// ListVendor — vendor-уроки (T-30), опционально по конкретному вендору.
func (q *query) ListVendor(ctx context.Context, vendor, status string) ([]dtorep.Lesson, error) {
	sqlStr := `SELECT ` + lessonColumns + ` FROM lessons WHERE kind = 'vendor'`
	args := []any{}
	if vendor != "" {
		sqlStr += ` AND vendor = ?`
		args = append(args, vendor)
	}
	if status != "" {
		sqlStr += ` AND status = ?`
		args = append(args, status)
	}
	sqlStr += ` ORDER BY created_at DESC`

	rows, err := q.conn.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list vendor lessons: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Lesson
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan lesson: %w", err)
		}
		out = append(out, *mapLessonToDTO(l))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate lessons: %w", err)
	}
	return out, nil
}

// ListAttention — уроки, «требующие внимания» (T-30): outdated (версия
// вендора разошлась с lockfile) или нездоровые (relapse_count > 0 и
// relapse >= applied_success — урок инжектят, а область ломается снова).
func (q *query) ListAttention(ctx context.Context) ([]dtorep.Lesson, error) {
	return q.listWhere(ctx,
		` WHERE status = 'outdated'
		   OR (relapse_count > 0 AND relapse_count >= applied_success_count)
		 ORDER BY updated_at DESC`)
}

// ListSupersededOlderThan — superseded-уроки, вытесненные раньше cutoff
// (кандидаты на prune в консолидации, T-30).
func (q *query) ListSupersededOlderThan(ctx context.Context, cutoff time.Time) ([]dtorep.Lesson, error) {
	return q.listWhere(ctx,
		` WHERE status = 'superseded' AND updated_at < ? ORDER BY updated_at ASC`, cutoff)
}

// listWhere — общий селектор по WHERE-фрагменту (запросы консолидации, T-30).
func (q *query) listWhere(ctx context.Context, where string, args ...any) ([]dtorep.Lesson, error) {
	rows, err := q.conn.QueryContext(ctx, `SELECT `+lessonColumns+` FROM lessons`+where, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query lessons: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Lesson
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan lesson: %w", err)
		}
		out = append(out, *mapLessonToDTO(l))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate lessons: %w", err)
	}
	return out, nil
}

func (q *query) UpdateLessonStatus(ctx context.Context, id, status string) error {
	res, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET status = ?, updated_at = ? WHERE id = ?`, status, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to update lesson status: %w", err)
	}
	return mustAffected(res, id)
}

func (q *query) UpdateLessonContent(ctx context.Context, id, title, triggersJSON, path string) error {
	res, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET title = ?, triggers_json = ?, path = ?, updated_at = ? WHERE id = ?`,
		title, triggersJSON, path, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to update lesson content: %w", err)
	}
	return mustAffected(res, id)
}

func (q *query) IncrementApplied(ctx context.Context, id string) error {
	// m8 (T-30): last_applied_at обновляется при инъекции — recency-decay
	// скоринга считается от последнего применения, а не от created_at.
	_, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET applied_count = applied_count + 1,
			last_applied_at = ?, updated_at = ? WHERE id = ?`,
		time.Now().UTC(), time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to increment applied_count: %w", err)
	}
	return nil
}

// Дельты importance при смене счётчиков (T-30, формула в scoring.go
// service-слоя): успешное применение повышает важность, рецидив — сильнее
// понижает (ложный урок дороже отсутствующего). Зажато в [0.1, 1.0].
const (
	importanceSuccessDelta = 0.05
	importanceRelapseDelta = -0.10
	importanceMin          = 0.1
	importanceMax          = 1.0
)

// IncrementAppliedSuccess — успешный исход рана с инжектированным уроком:
// счётчик++, last_applied_at=now, importance += successDelta (детерминиро-
// ванный пересчёт при изменении счётчиков, T-30).
func (q *query) IncrementAppliedSuccess(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET applied_success_count = applied_success_count + 1,
			last_applied_at = ?,
			importance = MIN(?, MAX(?, importance + ?)),
			updated_at = ?
		 WHERE id = ?`,
		now, importanceMax, importanceMin, importanceSuccessDelta, now, id)
	if err != nil {
		return fmt.Errorf("failed to increment applied_success_count: %w", err)
	}
	return nil
}

// IncrementRelapse — рецидив: урок инжектировали, а область снова сломалась.
// Счётчик++ и importance += relapseDelta (T-30).
func (q *query) IncrementRelapse(ctx context.Context, id string) error {
	_, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET relapse_count = relapse_count + 1,
			importance = MIN(?, MAX(?, importance + ?)),
			updated_at = ?
		 WHERE id = ?`,
		importanceMax, importanceMin, importanceRelapseDelta, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to increment relapse_count: %w", err)
	}
	return nil
}

// SetSuperseded — вытеснение урока новым (T-30): статус superseded +
// ссылка superseded_by. Старый файл не удаляется (история).
func (q *query) SetSuperseded(ctx context.Context, id, supersededBy string) error {
	res, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET status = 'superseded', superseded_by = ?, updated_at = ? WHERE id = ?`,
		supersededBy, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to set superseded: %w", err)
	}
	return mustAffected(res, id)
}

// UpdateRelated — связи урока (A-MEM related, T-30): related_json целиком.
func (q *query) UpdateRelated(ctx context.Context, id, relatedJSON string) error {
	res, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET related_json = ?, updated_at = ? WHERE id = ?`,
		relatedJSON, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to update related_json: %w", err)
	}
	return mustAffected(res, id)
}

// UpdateImportance — явная установка важности (T-30; пересчёт при смене
// источника/ручная корректировка).
func (q *query) UpdateImportance(ctx context.Context, id string, importance float64) error {
	res, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET importance = ?, updated_at = ? WHERE id = ?`,
		importance, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to update importance: %w", err)
	}
	return mustAffected(res, id)
}

func mustAffected(res sql.Result, id string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get affected rows: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("lesson %s: %w", id, cstmerrors.ErrNotFound)
	}
	return nil
}

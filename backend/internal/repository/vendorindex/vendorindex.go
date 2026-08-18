// Package vendorindex — FTS5-индекс vendor-памяти (D-50, T-23; таблица
// vendor_index создана миграцией T-02). Хранит path → content; поиск —
// MATCH с snippet для инъекции выдержек в промпты.
package vendorindex

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fableFM/glamor/internal/dto/dtorep"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Upsert обновляет содержимое файла в индексе (delete+insert).
func (r *Repository) Upsert(ctx context.Context, path, content string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM vendor_index WHERE path = ?`, path); err != nil {
		return fmt.Errorf("failed to delete from vendor_index: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO vendor_index (path, content) VALUES (?, ?)`, path, content); err != nil {
		return fmt.Errorf("failed to insert into vendor_index: %w", err)
	}
	return nil
}

// Delete удаляет файл из индекса.
func (r *Repository) Delete(ctx context.Context, path string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM vendor_index WHERE path = ?`, path); err != nil {
		return fmt.Errorf("failed to delete from vendor_index: %w", err)
	}
	return nil
}

// IndexedContent — текущее проиндексированное содержимое (для dirty-check).
func (r *Repository) IndexedContent(ctx context.Context, path string) (string, bool, error) {
	var content string
	err := r.db.QueryRowContext(ctx,
		`SELECT content FROM vendor_index WHERE path = ?`, path).Scan(&content)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to query vendor_index: %w", err)
	}
	return content, true, nil
}

// Search — FTS5-запрос со сниппетами (для промптов планировщика).
// Пустой/битый запрос — пустой результат, не ошибка (не роняем ран).
func (r *Repository) Search(ctx context.Context, query string, limit int) ([]dtorep.VendorSearchHit, error) {
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT path, snippet(vendor_index, 1, '[', ']', '…', 32), rank
		 FROM vendor_index WHERE vendor_index MATCH ?
		 ORDER BY rank LIMIT ?`, query, limit)
	if err != nil {
		// битый FTS-запрос (спецсимволы) — не ошибка рана
		return nil, nil //nolint:nilerr // см. комментарий выше
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.VendorSearchHit
	for rows.Next() {
		var hit dtorep.VendorSearchHit
		if err := rows.Scan(&hit.Path, &hit.Snippet, &hit.Rank); err != nil {
			return nil, fmt.Errorf("failed to scan vendor hit: %w", err)
		}
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate vendor hits: %w", err)
	}
	return out, nil
}

// Package repository — инфраструктура хранения glamor (D-05, D-10, D-80):
// подключение к SQLite (modernc, WAL, один писатель), миграции goose,
// общий Conn-интерфейс поверх *sql.DB/*sql.Tx и менеджер транзакций.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // драйвер database/sql (имя "sqlite")

	"github.com/fableFM/glamor/internal/cstmerrors"
)

// Conn — общий интерфейс *sql.DB и *sql.Tx: query-struct'ы репозиториев
// работают и на пуле, и на транзакции (D-80, аналог pgxconn.Conn).
type Conn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Open открывает SQLite-БД: WAL, busy_timeout, foreign_keys, один писатель
// (single connection — зафиксировано здесь по T-02; для локального демона
// пропускной способности достаточно, корректность CAS/транзакций важнее).
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		// родительский каталог (например ~/.glamor) может ещё не существовать
		if err := mkdirAll(dir); err != nil {
			return nil, fmt.Errorf("failed to create db directory %s: %w", dir, err)
		}
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping sqlite db %s: %w", path, err)
	}

	return db, nil
}

// Migrate применяет goose-миграции (Go-файлы из пакета migrations,
// зарегистрированные через blank-import). Вызывается при старте демона,
// до открытия listeners.
func Migrate(ctx context.Context, db *sql.DB) error {
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("failed to set goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("failed to apply migrations: %w", err)
	}
	return nil
}

// IntegrityCheck выполняет PRAGMA integrity_check (используется в тестах).
func IntegrityCheck(ctx context.Context, db *sql.DB) error {
	var res string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&res); err != nil {
		return fmt.Errorf("failed to run integrity_check: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("integrity_check failed: %s", res)
	}
	return nil
}

// MapError переводит ошибки драйвера в сентинелы cstmerrors
// (sql.ErrNoRows → ErrNotFound, unique violation → ErrDuplicate).
func MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return cstmerrors.ErrNotFound
	}
	if isUniqueViolation(err) {
		return cstmerrors.ErrDuplicate
	}
	return err
}

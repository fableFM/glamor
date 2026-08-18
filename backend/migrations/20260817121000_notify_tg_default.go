package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upNotifyTgDefault, downNotifyTgDefault)
}

func upNotifyTgDefault(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		// Дефолт notify_tg для новых ранов проекта (T-16, F-04 fix-task-2):
		// 1 = уведомлять (прежнее поведение формы). UI читает поле проекта.
		`ALTER TABLE projects ADD COLUMN notify_tg_default INTEGER NOT NULL DEFAULT 1`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}
	return nil
}

func downNotifyTgDefault(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`ALTER TABLE projects DROP COLUMN notify_tg_default`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}
	return nil
}

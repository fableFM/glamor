package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upNotesAndBranchLock, downNotesAndBranchLock)
}

func upNotesAndBranchLock(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		// Queue note / steer-сообщения (D-22, T-11).
		`CREATE TABLE notes (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id          TEXT NOT NULL REFERENCES runs(id),
			stage_id        INTEGER NULL REFERENCES run_stages(id),
			kind            TEXT NOT NULL DEFAULT 'note' CHECK (kind IN ('note','steer')),
			text            TEXT NOT NULL,
			consumed        INTEGER NOT NULL DEFAULT 0,
			idempotency_key TEXT NOT NULL UNIQUE,
			created_at      TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			consumed_at     TIMESTAMP NULL
		)`,
		`CREATE INDEX idx_notes_run ON notes (run_id, consumed)`,

		// Lock: один активный ран на (project_id, branch) — D-33.
		// Частичный UNIQUE-индекс: терминальные раны не блокируют переиспользование ветки.
		`CREATE UNIQUE INDEX idx_runs_active_branch ON runs (project_id, branch)
		 WHERE state IN ('draft','running','waiting_gate')`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}
	return nil
}

func downNotesAndBranchLock(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`DROP INDEX IF EXISTS idx_runs_active_branch`,
		`DROP TABLE IF EXISTS notes`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}
	return nil
}

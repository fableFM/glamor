package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upLessons, downLessons)
}

func upLessons(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		// Карточки уроков (D-52, T-29): метаданные и счётчики в БД,
		// содержимое — markdown-файлы (двухуровнево, как vendor-память).
		`CREATE TABLE lessons (
			id            TEXT PRIMARY KEY,
			title         TEXT NOT NULL,
			scope         TEXT NOT NULL CHECK (scope IN ('global','project')),
			status        TEXT NOT NULL CHECK (status IN ('proposed','confirmed','rejected','superseded')),
			project_id    INTEGER NULL REFERENCES projects(id),
			path          TEXT NOT NULL,
			triggers_json TEXT NOT NULL DEFAULT '[]',
			run_id        TEXT NULL REFERENCES runs(id),
			stage_key     TEXT NOT NULL DEFAULT '',
			applied_count INTEGER NOT NULL DEFAULT 0,
			relapse_count INTEGER NOT NULL DEFAULT 0,
			created_at    TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			updated_at    TIMESTAMP NULL
		)`,
		`CREATE INDEX idx_lessons_status ON lessons (status, scope)`,

		// gates: новый вид lesson_review (T-29) — CHECK не расширить,
		// пересоздаём таблицу (SQLite).
		`CREATE TABLE gates_new (
			id              TEXT PRIMARY KEY,
			run_id          TEXT NOT NULL REFERENCES runs(id),
			stage_id        INTEGER NULL REFERENCES run_stages(id),
			kind            TEXT NOT NULL CHECK (kind IN ('plan_approval','question','escalation','final_review','lesson_review')),
			question        TEXT NOT NULL,
			context_json    TEXT NOT NULL DEFAULT '{}',
			state           TEXT NOT NULL CHECK (state IN ('open','answered','approved','rejected','expired')),
			answer          TEXT NULL,
			idempotency_key TEXT NOT NULL UNIQUE,
			created_at      TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			resolved_at     TIMESTAMP NULL
		)`,
		`INSERT INTO gates_new SELECT id, run_id, stage_id, kind, question, context_json, state, answer, idempotency_key, created_at, resolved_at FROM gates`,
		`DROP TABLE gates`,
		`ALTER TABLE gates_new RENAME TO gates`,
		`CREATE INDEX idx_gates_run ON gates (run_id, state)`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}
	return nil
}

func downLessons(ctx context.Context, tx *sql.Tx) error {
	// gates обратно не сжимаем (данные lesson_review потерялись бы) —
	// только lessons
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS lessons`); err != nil {
		return fmt.Errorf("failed to drop lessons table: %w", err)
	}
	return nil
}

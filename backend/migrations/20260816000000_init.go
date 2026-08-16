// Package migrations — goose v3 Go-миграции схемы SQLite (D-05, по образу
// dashboard-manager/migrations). Пакет blank-import'ится в cmd/glamord,
// накат — repository.Migrate при старте демона.
package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upInit, downInit)
}

func upInit(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE projects (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			path           TEXT NOT NULL UNIQUE,
			name           TEXT NOT NULL,
			default_branch TEXT NOT NULL DEFAULT '',
			ide_command    TEXT NOT NULL DEFAULT '',
			created_at     TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		)`,

		`CREATE TABLE pipelines (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id        INTEGER NULL REFERENCES projects(id),
			name              TEXT NOT NULL,
			version           INTEGER NOT NULL,
			parent_version_id INTEGER NULL REFERENCES pipelines(id),
			spec_json         TEXT NOT NULL,
			created_at        TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			UNIQUE (project_id, name, version)
		)`,

		`CREATE TABLE runs (
			id                  TEXT PRIMARY KEY,
			project_id          INTEGER NOT NULL REFERENCES projects(id),
			pipeline_version_id INTEGER NOT NULL REFERENCES pipelines(id),
			task_text           TEXT NOT NULL,
			base_branch         TEXT NOT NULL,
			branch              TEXT NOT NULL,
			state               TEXT NOT NULL CHECK (state IN ('draft','running','waiting_gate','succeeded','failed','stopped')),
			depth               INTEGER NOT NULL DEFAULT 0,
			notify_tg           INTEGER NOT NULL DEFAULT 1,
			idempotency_key     TEXT NOT NULL UNIQUE,
			created_at          TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			finished_at         TIMESTAMP NULL
		)`,
		`CREATE INDEX idx_runs_project ON runs (project_id, state)`,
		`CREATE INDEX idx_runs_pipeline ON runs (pipeline_version_id)`,

		`CREATE TABLE run_stages (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id            TEXT NOT NULL REFERENCES runs(id),
			stage_key         TEXT NOT NULL,
			iteration         INTEGER NOT NULL,
			state             TEXT NOT NULL CHECK (state IN ('pending','running','succeeded','failed','interrupted','skipped')),
			harness           TEXT NOT NULL DEFAULT '',
			session_id        TEXT NULL,
			pid               INTEGER NULL,
			exit_code         INTEGER NULL,
			stop_requested_by TEXT NULL,
			resume_count      INTEGER NOT NULL DEFAULT 0,
			started_at        TIMESTAMP NULL,
			finished_at       TIMESTAMP NULL,
			tokens_in         INTEGER NOT NULL DEFAULT 0,
			tokens_out        INTEGER NOT NULL DEFAULT 0,
			error             TEXT NULL,
			UNIQUE (run_id, stage_key, iteration)
		)`,
		`CREATE INDEX idx_run_stages_run ON run_stages (run_id, state)`,

		`CREATE TABLE events (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id       TEXT NOT NULL REFERENCES runs(id),
			stage_id     INTEGER NULL REFERENCES run_stages(id),
			ts           TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			kind         TEXT NOT NULL,
			payload_json TEXT NOT NULL
		)`,
		`CREATE INDEX idx_events_run ON events (run_id, id)`,

		`CREATE TABLE gates (
			id              TEXT PRIMARY KEY,
			run_id          TEXT NOT NULL REFERENCES runs(id),
			stage_id        INTEGER NULL REFERENCES run_stages(id),
			kind            TEXT NOT NULL CHECK (kind IN ('plan_approval','question','escalation','final_review')),
			question        TEXT NOT NULL,
			context_json    TEXT NOT NULL DEFAULT '{}',
			state           TEXT NOT NULL CHECK (state IN ('open','answered','approved','rejected','expired')),
			answer          TEXT NULL,
			idempotency_key TEXT NOT NULL UNIQUE,
			created_at      TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			resolved_at     TIMESTAMP NULL
		)`,
		`CREATE INDEX idx_gates_run ON gates (run_id, state)`,

		`CREATE TABLE artifacts (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id     TEXT NOT NULL REFERENCES runs(id),
			stage_id   INTEGER NULL REFERENCES run_stages(id),
			path       TEXT NOT NULL,
			kind       TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		)`,
		`CREATE INDEX idx_artifacts_run ON artifacts (run_id)`,

		// Заготовка под vendor-память (D-50), наполнение — T-23.
		`CREATE VIRTUAL TABLE vendor_index USING fts5(path UNINDEXED, content)`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}
	return nil
}

func downInit(ctx context.Context, tx *sql.Tx) error {
	tables := []string{
		"vendor_index",
		"artifacts",
		"gates",
		"events",
		"run_stages",
		"runs",
		"pipelines",
		"projects",
	}
	for _, table := range tables {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", table)); err != nil {
			return fmt.Errorf("failed to drop table %s: %w", table, err)
		}
	}
	return nil
}

package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upLessonsV2, downLessonsV2)
}

// Миграция T-30 (эволюция уроков, D-81): новые колонки kind/vendor/
// vendor_version/area/importance/applied_success_count/last_applied_at/
// superseded_by/related_json и статус 'outdated' в CHECK. CHECK в SQLite
// не расширить — таблица пересоздаётся (прецедент — миграция T-29).
// Обратная совместимость: существующие строки получают kind='behavior'
// и дефолтную importance=0.5 (DEFAULT'ы INSERT SELECT не подставляет —
// значения перечислены явно).
//
// FTS-таблицы/триггеров у lessons нет: индекс — общий vendor_index
// (миграция T-02), наполняется из service-слоя, поэтому пересозданию
// подлежит только сама таблица lessons.
func upLessonsV2(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		// superseded_by ссылается на lessons(id) — старую таблицу на момент
		// создания; после RENAME ссылка указывает на саму таблицу.
		`CREATE TABLE lessons_new (
			id                    TEXT PRIMARY KEY,
			title                 TEXT NOT NULL,
			scope                 TEXT NOT NULL CHECK (scope IN ('global','project')),
			status                TEXT NOT NULL CHECK (status IN ('proposed','confirmed','rejected','superseded','outdated')),
			kind                  TEXT NOT NULL DEFAULT 'behavior' CHECK (kind IN ('behavior','vendor')),
			project_id            INTEGER NULL REFERENCES projects(id),
			path                  TEXT NOT NULL,
			triggers_json         TEXT NOT NULL DEFAULT '[]',
			run_id                TEXT NULL REFERENCES runs(id),
			stage_key             TEXT NOT NULL DEFAULT '',
			vendor                TEXT NULL,
			vendor_version        TEXT NULL,
			area                  TEXT NULL,
			importance            REAL NOT NULL DEFAULT 0.5,
			applied_count         INTEGER NOT NULL DEFAULT 0,
			applied_success_count INTEGER NOT NULL DEFAULT 0,
			relapse_count         INTEGER NOT NULL DEFAULT 0,
			last_applied_at       TIMESTAMP NULL,
			superseded_by         TEXT NULL REFERENCES lessons(id),
			related_json          TEXT NOT NULL DEFAULT '[]',
			created_at            TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			updated_at            TIMESTAMP NULL
		)`,
		`INSERT INTO lessons_new (
			id, title, scope, status, kind, project_id, path, triggers_json,
			run_id, stage_key, vendor, vendor_version, area, importance,
			applied_count, applied_success_count, relapse_count,
			last_applied_at, superseded_by, related_json, created_at, updated_at
		) SELECT
			id, title, scope, status, 'behavior', project_id, path, triggers_json,
			run_id, stage_key, NULL, NULL, NULL, 0.5,
			applied_count, 0, relapse_count,
			NULL, NULL, '[]', created_at, updated_at
		FROM lessons`,
		`DROP TABLE lessons`,
		`ALTER TABLE lessons_new RENAME TO lessons`,
		`CREATE INDEX idx_lessons_status ON lessons (status, scope)`,
		`CREATE INDEX idx_lessons_kind ON lessons (kind, status)`,
		`CREATE INDEX idx_lessons_vendor ON lessons (vendor)`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}
	return nil
}

func downLessonsV2(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE lessons_old (
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
		// outdated-уроки при откате теряют статус (CHECK старой схемы) —
		// переводим в confirmed, остальные колонки v2 отбрасываются.
		`INSERT INTO lessons_old
			SELECT id, title, scope,
				CASE status WHEN 'outdated' THEN 'confirmed' ELSE status END,
				project_id, path, triggers_json, run_id, stage_key,
				applied_count, relapse_count, created_at, updated_at
			FROM lessons`,
		`DROP TABLE lessons`,
		`ALTER TABLE lessons_old RENAME TO lessons`,
		`CREATE INDEX idx_lessons_status ON lessons (status, scope)`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}
	return nil
}

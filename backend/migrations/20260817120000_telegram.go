package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upTelegram, downTelegram)
}

func upTelegram(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		// Whitelist привязанных чатов (T-19, D-70): привязка через код /start
		// + REST POST /telegram/pair. Чужие чаты игнорируются.
		`CREATE TABLE tg_chats (
			chat_id    INTEGER PRIMARY KEY,
			created_at TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		)`,

		// Ключ-значение состояние адаптера: last_update_id (idempotency-key
		// входящих, D-12), last_delivered_event_id (оффлайн-сводка, D-72),
		// pending_pair_code/pending_pair_chat_id (привязка).
		`CREATE TABLE tg_state (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,

		// Маппинг гейт → TG-сообщение: ответ на вопрос/эскалацию — reply на
		// сообщение гейта (привязка по reply_to_message_id, D-21/T-19).
		`CREATE TABLE tg_gate_messages (
			gate_id    TEXT PRIMARY KEY,
			chat_id    INTEGER NOT NULL,
			message_id INTEGER NOT NULL
		)`,

		// «Тред» рана в TG: первое сообщение рана — корневое, остальные —
		// reply на него (T-19). NULL — уведомлений по рану ещё не было.
		`ALTER TABLE runs ADD COLUMN tg_root_message_id INTEGER NULL`,
	}

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}
	return nil
}

func downTelegram(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`ALTER TABLE runs DROP COLUMN tg_root_message_id`,
		`DROP TABLE IF EXISTS tg_gate_messages`,
		`DROP TABLE IF EXISTS tg_state`,
		`DROP TABLE IF EXISTS tg_chats`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute migration statement: %w", err)
		}
	}
	return nil
}

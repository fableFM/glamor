package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	store "github.com/fableFM/glamor/internal/repository"
)

type query struct {
	conn store.Conn
}

func (q *query) AddChat(ctx context.Context, chatID int64) error {
	_, err := q.conn.ExecContext(ctx,
		`INSERT OR IGNORE INTO tg_chats (chat_id) VALUES (?)`, chatID)
	if err != nil {
		return fmt.Errorf("failed to add tg chat: %w", store.MapError(err))
	}
	return nil
}

func (q *query) IsChatAllowed(ctx context.Context, chatID int64) (bool, error) {
	var one int
	err := q.conn.QueryRowContext(ctx,
		`SELECT 1 FROM tg_chats WHERE chat_id = ?`, chatID).Scan(&one)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("failed to check tg chat: %w", err)
	}
}

func (q *query) ListChats(ctx context.Context) ([]int64, error) {
	rows, err := q.conn.QueryContext(ctx, `SELECT chat_id FROM tg_chats ORDER BY chat_id`)
	if err != nil {
		return nil, fmt.Errorf("failed to list tg chats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan tg chat: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate tg chats: %w", err)
	}
	return out, nil
}

func (q *query) GetState(ctx context.Context, key string) (string, error) {
	var value string
	err := q.conn.QueryRowContext(ctx,
		`SELECT value FROM tg_state WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return "", fmt.Errorf("failed to get tg state %q: %w", key, store.MapError(err))
	}
	return value, nil
}

func (q *query) SetState(ctx context.Context, key, value string) error {
	_, err := q.conn.ExecContext(ctx,
		`INSERT INTO tg_state (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("failed to set tg state %q: %w", key, err)
	}
	return nil
}

func (q *query) DeleteState(ctx context.Context, key string) error {
	_, err := q.conn.ExecContext(ctx, `DELETE FROM tg_state WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("failed to delete tg state %q: %w", key, err)
	}
	return nil
}

func (q *query) SaveGateMessage(ctx context.Context, msg dtorep.TgGateMessage) error {
	_, err := q.conn.ExecContext(ctx,
		`INSERT INTO tg_gate_messages (gate_id, chat_id, message_id) VALUES (?, ?, ?)
		 ON CONFLICT (gate_id) DO UPDATE SET chat_id = excluded.chat_id, message_id = excluded.message_id`,
		msg.GateID, msg.ChatID, msg.MessageID)
	if err != nil {
		return fmt.Errorf("failed to save tg gate message: %w", store.MapError(err))
	}
	return nil
}

func (q *query) GetGateMessageByMessageID(ctx context.Context, chatID, messageID int64) (*dtorep.TgGateMessage, error) {
	var m dtorep.TgGateMessage
	err := q.conn.QueryRowContext(ctx,
		`SELECT gate_id, chat_id, message_id FROM tg_gate_messages
		 WHERE chat_id = ? AND message_id = ?`, chatID, messageID).
		Scan(&m.GateID, &m.ChatID, &m.MessageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get tg gate message: %w", store.MapError(err))
	}
	return &m, nil
}

// проверка на уровне компиляции: query реализует Queries
var _ Queries = (*query)(nil)

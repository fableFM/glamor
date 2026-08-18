// Package telegram — репозиторий состояния TG-адаптера (T-19): whitelist
// чатов, key-value состояние (offsets привязки/доставки) и маппинг
// гейт → сообщение для reply-ответов.
package telegram

import (
	"context"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/pkg/commontx"
)

type Queries interface {
	// AddChat добавляет чат в whitelist (идемпотентно: повтор — no-op).
	AddChat(ctx context.Context, chatID int64) error
	// IsChatAllowed — чат в whitelist?
	IsChatAllowed(ctx context.Context, chatID int64) (bool, error)
	// ListChats — все привязанные чаты (по возрастанию chat_id).
	ListChats(ctx context.Context) ([]int64, error)

	// GetState — значение по ключу; нет ключа → cstmerrors.ErrNotFound.
	GetState(ctx context.Context, key string) (string, error)
	// SetState — upsert значения по ключу.
	SetState(ctx context.Context, key, value string) error
	// DeleteState — удалить ключ (нет ключа — no-op).
	DeleteState(ctx context.Context, key string) error

	// SaveGateMessage — маппинг гейт → сообщение; повтор по gate_id
	// перезаписывает (upsert).
	SaveGateMessage(ctx context.Context, msg dtorep.TgGateMessage) error
	// GetGateMessageByMessageID — гейт по (chat_id, message_id) сообщения,
	// на которое пришёл reply; нет маппинга → cstmerrors.ErrNotFound.
	GetGateMessageByMessageID(ctx context.Context, chatID, messageID int64) (*dtorep.TgGateMessage, error)
}

type RepositoryWithTX interface {
	OpenTx(ctx context.Context) (Tx, error)
	Queries
}

type Tx interface {
	Queries
	commontx.Tx
}

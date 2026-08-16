// Package events — журнал событий как единая шина состояния (D-11):
// append-only запись в транзакциях store + in-process pub/sub
// (публикация ПОСЛЕ коммита) + Replay для догоняющей синхронизации.
package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/repository"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
)

// Journal — фасад журнала: атомарная запись событий в транзакциях переходов
// (реализует runs.EventAppender), рассылка в Hub после коммита и Replay.
type Journal struct {
	repo eventsrep.RepositoryWithTX
	txm  *repository.TxManager
	hub  *Hub
}

// NewJournal собирает журнал из готовых зависимостей (ручной DI в main, D-80).
func NewJournal(repo eventsrep.RepositoryWithTX, txm *repository.TxManager, hub *Hub) *Journal {
	return &Journal{
		repo: repo,
		txm:  txm,
		hub:  hub,
	}
}

// pendingKey — ключ collector'а отложенных событий в ctx (публикация после коммита).
type pendingKey struct{}

type pendingCollector struct {
	events []dtorep.Event
}

// WithTx выполняет fn в транзакции; события, записанные через Append внутри
// fn (с ctx из аргументов fn!), публикуются в Hub только ПОСЛЕ успешного
// коммита (D-11: подписчик никогда не видит событие, которого нет в журнале).
func (j *Journal) WithTx(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
	collector := &pendingCollector{}
	ctx = context.WithValue(ctx, pendingKey{}, collector)

	if err := j.txm.WithTx(ctx, fn); err != nil {
		return err
	}

	j.hub.Publish(collector.events...)
	return nil
}

// Append пишет событие в открытую транзакцию (outbox, D-11) и откладывает
// публикацию до коммита. Если Append вызван вне Journal.WithTx (collector
// отсутствует), событие просто пишется в журнал без рассылки.
func (j *Journal) Append(ctx context.Context, tx *sql.Tx, ev dtorep.Event) error {
	id, err := eventsrep.NewTx(tx).AppendEvent(ctx, ev)
	if err != nil {
		return err
	}
	ev.ID = id

	if collector, ok := ctx.Value(pendingKey{}).(*pendingCollector); ok {
		collector.events = append(collector.events, ev)
	}
	return nil
}

// Replay — страница событий с id > afterID (догон клиентов по last_event_id).
// runID = dtorep.RunIDAll («*») — события всех ранов.
func (j *Journal) Replay(ctx context.Context, runID string, afterID int64, limit int) ([]dtorep.Event, error) {
	if limit <= 0 {
		limit = 500
	}
	events, err := j.repo.ReplayEvents(ctx, runID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to replay events: %w", err)
	}
	return events, nil
}

// LatestID — максимальный id в журнале (0 для пустого).
func (j *Journal) LatestID(ctx context.Context) (int64, error) {
	id, err := j.repo.LatestEventID(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get latest event id: %w", err)
	}
	return id, nil
}

// ErrSubscriptionDropped — подписчик отключён из-за переполнения буфера
// (backpressure: клиент обязан пересинхронизироваться по last_event_id).
var ErrSubscriptionDropped = errors.New("subscription dropped: slow consumer")

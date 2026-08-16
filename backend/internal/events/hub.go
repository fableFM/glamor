package events

import (
	"sync"

	"github.com/fableFM/glamor/internal/dto/dtorep"
)

// SubscriberBuffer — размер буфера подписчика. Переполнение = отключение
// медленного клиента (drop → клиент пересинхронизируется), а не торможение
// шины (T-04).
const SubscriberBuffer = 256

// Hub — in-process pub/sub поверх журнала. Подписчики: WS hub, TG-адаптер
// (M2). Publish вызывается только после коммита транзакции (см. Journal).
type Hub struct {
	mu   sync.RWMutex
	subs map[chan dtorep.Event]string // канал → run_id подписки
}

func NewHub() *Hub {
	return &Hub{subs: make(map[chan dtorep.Event]string)}
}

// Subscribe подписывает на события рана (runID = "*" — все раны).
// Возвращает канал и функцию отписки. Закрытый канал = подписка удалена
// (переполнение буфера) — клиент должен пересинхронизироваться.
func (h *Hub) Subscribe(runID string) (<-chan dtorep.Event, func()) {
	ch := make(chan dtorep.Event, SubscriberBuffer)

	h.mu.Lock()
	h.subs[ch] = runID
	h.mu.Unlock()

	unsubscribe := func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
	return ch, unsubscribe
}

// Publish рассылает события подписчикам (неблокирующе). Подписчик с
// переполненным буфером отключается: ран не должен тормозить из-за
// медленного клиента.
func (h *Hub) Publish(events ...dtorep.Event) {
	if len(events) == 0 {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, ev := range events {
		for ch, runID := range h.subs {
			if runID != dtorep.RunIDAll && runID != ev.RunID {
				continue
			}
			select {
			case ch <- ev:
			default:
				// медленный клиент — drop подписки
				delete(h.subs, ch)
				close(ch)
			}
		}
	}
}

// SubscriberCount — число активных подписчиков (метрики/тесты).
func (h *Hub) SubscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

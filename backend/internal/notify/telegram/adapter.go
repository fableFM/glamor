package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	telegramrep "github.com/fableFM/glamor/internal/repository/telegram"
	"github.com/fableFM/glamor/internal/service/catalog"
	"github.com/fableFM/glamor/internal/service/runsapi"
)

// Ключи tg_state (D-12/D-72, привязка).
const (
	stateKeyLastUpdateID     = "last_update_id"
	stateKeyLastDeliveredEvt = "last_delivered_event_id"
	stateKeyPendingPairCode  = "pending_pair_code"
	stateKeyPendingPairChat  = "pending_pair_chat_id"
)

// Config — настройки адаптера (T-19, D-72).
type Config struct {
	// PollTimeoutSec — серверный таймаут long polling (дефолт 30).
	PollTimeoutSec int
	// PollRetryDelay — пауза между попытками getUpdates после ошибки
	// (дефолт 3s).
	PollRetryDelay time.Duration
	// GlobalSendInterval — глобальный троттлинг исходящих (дефолт 1/25s —
	// лимит TG 25 msg/s).
	GlobalSendInterval time.Duration
	// ChatSendInterval — троттлинг на чат (дефолт 1s — лимит TG 1 msg/s
	// в чат).
	ChatSendInterval time.Duration
}

func (c Config) withDefaults() Config {
	if c.PollTimeoutSec <= 0 {
		c.PollTimeoutSec = 30
	}
	if c.PollRetryDelay <= 0 {
		c.PollRetryDelay = 3 * time.Second
	}
	if c.GlobalSendInterval <= 0 {
		c.GlobalSendInterval = time.Second / 25
	}
	if c.ChatSendInterval <= 0 {
		c.ChatSendInterval = time.Second
	}
	return c
}

// Adapter — TG-пульт (T-19): poller входящих команд (long polling) +
// sender нотификаций (подписка hub «*»). Действия над ядром — только через
// API-сценарии runsapi (те же пути, что у REST); бизнес-чтения других
// доменов — через service/catalog (F-09, D-80 доп. 2026-08-17). Свой
// репозиторий состояния — repository/telegram (whitelist, маркеры,
// маппинги сообщений).
type Adapter struct {
	client  BotClient
	hub     *events.Hub
	journal *events.Journal
	runsSvc *runsapi.Service
	catalog *catalog.Service
	tg      telegramrep.RepositoryWithTX
	cfg     Config

	limiter *rateLimiter
}

// NewAdapter собирает адаптер из готовых зависимостей (ручной DI в main,
// D-80). Конструктор не ходит в сеть и не открывает БД.
func NewAdapter(
	client BotClient,
	hub *events.Hub,
	journal *events.Journal,
	runsSvc *runsapi.Service,
	catalog *catalog.Service,
	tg telegramrep.RepositoryWithTX,
	cfg Config,
) *Adapter {
	cfg = cfg.withDefaults()
	return &Adapter{
		client:  client,
		hub:     hub,
		journal: journal,
		runsSvc: runsSvc,
		catalog: catalog,
		tg:      tg,
		cfg:     cfg,
		limiter: newRateLimiter(cfg.GlobalSendInterval, cfg.ChatSendInterval),
	}
}

// Run запускает poller и sender и блокируется до отмены ctx.
// Ошибки сети/БД логируются и ретраятся — адаптер не роняет демон.
func (a *Adapter) Run(ctx context.Context) error {
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		a.senderLoop(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		a.pollerLoop(ctx)
	}()

	<-ctx.Done()
	wg.Wait()
	return nil
}

// --- sender: события журнала → TG -------------------------------------------

// senderLoop — подписка на все раны (hub «*») с оффлайн-догоном (D-72).
// Подписка оформляется ДО replay, чтобы не потерять live-события в разрыве.
func (a *Adapter) senderLoop(ctx context.Context) {
	for {
		ch, unsubscribe := a.hub.Subscribe(dtorep.RunIDAll)

		// Оффлайн-догон обязателен перед live-лентой: пока сводка не ушла,
		// live-события не обрабатываем — иначе маркер перепрыгнет через
		// недоставленное событие и оно потеряется (fix-task-3 п.2). При сбое —
		// ретрай с паузой PollRetryDelay, чтобы не крутить цикл при упавшей сети.
		for {
			err := a.syncMissed(ctx)
			if err == nil {
				break
			}
			if errors.Is(err, context.Canceled) {
				unsubscribe()
				return
			}
			slog.WarnContext(ctx, "tg: offline sync failed, retrying",
				slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
			select {
			case <-ctx.Done():
				unsubscribe()
				return
			case <-time.After(a.cfg.PollRetryDelay):
			}
		}

		dropped := false
		for {
			select {
			case <-ctx.Done():
				unsubscribe()
				return
			case ev, ok := <-ch:
				if !ok {
					// медленный консьюмер — подписку сбросили: пересинхронизация
					// по last_delivered_event_id и новая подписка (D-72)
					slog.WarnContext(ctx, "tg: subscription dropped, resyncing",
						slog.String("component", "notify/telegram"))
					dropped = true
				} else if !a.handleEvent(ctx, ev) {
					// send не удался, маркер не двинут: пересинхронизация —
					// событие доедет оффлайн-сводкой (fix-task-3 п.2)
					dropped = true
				}
			}
			if dropped {
				break
			}
		}
	}
}

// syncMissed — оффлайн-сводка (D-72): если со времени last_delivered_event_id
// в журнале появились события — ОДНО сводное сообщение (не лента), затем
// маркер подтягивается до актуального id.
func (a *Adapter) syncMissed(ctx context.Context) error {
	lastID := a.loadInt64State(ctx, stateKeyLastDeliveredEvt)

	var stat missedStat
	muted := map[string]bool{} // D-73: события ранов с notify_tg=false не считаем
	after := lastID
	maxID := lastID
	for {
		evs, err := a.journal.Replay(ctx, dtorep.RunIDAll, after, 500)
		if err != nil {
			return fmt.Errorf("failed to replay missed events: %w", err)
		}
		if len(evs) == 0 {
			break
		}
		for _, ev := range evs {
			if !a.isMuted(ctx, ev.RunID, muted) {
				stat.add(ev)
			}
			if ev.ID > maxID {
				maxID = ev.ID
			}
			after = ev.ID
		}
		if len(evs) < 500 {
			break
		}
	}

	if maxID == lastID {
		return nil // ничего не пропущено
	}

	text := formatMissedSummary(stat)
	if text == "" {
		// пропущены только события замьюченных ранов (D-73): маркер двигаем,
		// слать нечего
		return a.persistDeliveredMarker(ctx, maxID)
	}
	chatID, ok := a.primaryChat(ctx)
	if !ok {
		// некуда слать: маркер двигаем — повторный старт не должен пересчитывать
		// ту же историю (зафиксированный trade-off T-19)
		return a.persistDeliveredMarker(ctx, maxID)
	}
	if _, err := a.send(ctx, SendMessageRequest{ChatID: chatID, Text: text}); err != nil {
		// маркер НЕ двигаем (F-06): следующий старт пересчитает и пошлёт сводку
		return fmt.Errorf("failed to send missed summary: %w", err)
	}
	return a.persistDeliveredMarker(ctx, maxID)
}

// persistDeliveredMarker — запись last_delivered_event_id (D-72).
func (a *Adapter) persistDeliveredMarker(ctx context.Context, maxID int64) error {
	if err := a.tg.SetState(ctx, stateKeyLastDeliveredEvt, strconv.FormatInt(maxID, 10)); err != nil {
		return fmt.Errorf("failed to persist delivered marker: %w", err)
	}
	return nil
}

// handleEvent — маппинг события журнала в TG-сообщение (D-70..73).
// Маркер last_delivered_event_id двигается за каждым УЧТЁННЫМ событием:
// успешно отправленным либо не требующим отправки (нет маппинга, ран
// замьючен, whitelist пуст, ран не читается). Возвращает false при фейле
// send — маркер НЕ двигается (fix-task-3 п.2, консистентно с syncMissed
// F-06): senderLoop уходит на пересинхронизацию, событие доедет
// оффлайн-сводкой. Неблокируемость Hub сохраняется: senderLoop — тот же
// синхронный консьюмер, что и раньше (send уже троттлится), сбой лишь
// переключает на resync по тому же механизму, что и drop подписки.
func (a *Adapter) handleEvent(ctx context.Context, ev dtorep.Event) bool {
	text, keyboard, gateID := a.mapEvent(ctx, ev)
	if text == "" {
		a.persistEventMarker(ctx, ev.ID)
		return true
	}

	// D-73: notify_tg=false → ран полностью молчит (читаем ран через catalog)
	run, err := a.catalog.GetRun(ctx, ev.RunID)
	if err != nil {
		if !errors.Is(err, cstmerrors.ErrNotFound) {
			slog.WarnContext(ctx, "tg: failed to load run for event",
				slog.String("component", "notify/telegram"),
				slog.String("run_id", ev.RunID), slog.String("error", err.Error()))
		}
		a.persistEventMarker(ctx, ev.ID)
		return true
	}
	if !run.NotifyTG {
		a.persistEventMarker(ctx, ev.ID)
		return true
	}

	chatID, ok := a.primaryChat(ctx)
	if !ok {
		a.persistEventMarker(ctx, ev.ID)
		return true // whitelist пуст — некуда слать
	}

	// «тред» рана: первое сообщение — корневое (запоминаем в runs), остальные
	// — reply на него (T-19)
	var replyTo int64
	if run.TgRootMessageID != nil {
		replyTo = *run.TgRootMessageID
	}

	msgID, err := a.send(ctx, SendMessageRequest{
		ChatID:           chatID,
		Text:             text,
		ReplyToMessageID: replyTo,
		InlineKeyboard:   keyboard,
	})
	if err != nil {
		slog.WarnContext(ctx, "tg: failed to send notification",
			slog.String("component", "notify/telegram"),
			slog.String("run_id", ev.RunID), slog.String("error", err.Error()))
		return false
	}
	a.persistEventMarker(ctx, ev.ID)

	if run.TgRootMessageID == nil {
		// CAS: корень мог выставить конкурентный sender — не страшно
		if err := a.catalog.SetRunTgRootMessageID(ctx, ev.RunID, msgID); err != nil {
			slog.WarnContext(ctx, "tg: failed to store root message",
				slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		}
	}
	if gateID != "" {
		if err := a.tg.SaveGateMessage(ctx, dtorep.TgGateMessage{
			GateID:    gateID,
			ChatID:    chatID,
			MessageID: msgID,
		}); err != nil {
			slog.WarnContext(ctx, "tg: failed to store gate message",
				slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
		}
	}
	return true
}

// persistEventMarker — продвижение last_delivered_event_id за учтённым
// событием (fix-task-3 п.2). Ошибка записи — только лог: следующий resync
// пересчитает по последнему сохранённому маркеру.
func (a *Adapter) persistEventMarker(ctx context.Context, id int64) {
	if err := a.persistDeliveredMarker(ctx, id); err != nil {
		slog.WarnContext(ctx, "tg: failed to persist delivered marker",
			slog.String("component", "notify/telegram"), slog.String("error", err.Error()))
	}
}

// send — отправка с троттлингом (D-72: глобально 25 msg/s, 1 msg/s в чат).
func (a *Adapter) send(ctx context.Context, req SendMessageRequest) (int64, error) {
	if err := a.limiter.wait(ctx, req.ChatID); err != nil {
		return 0, err
	}
	return a.client.SendMessage(ctx, req)
}

// isMuted — ран замьючен (notify_tg=false)? Кэш на время replay (D-73).
func (a *Adapter) isMuted(ctx context.Context, runID string, cache map[string]bool) bool {
	if m, ok := cache[runID]; ok {
		return m
	}
	m := false
	if run, err := a.catalog.GetRun(ctx, runID); err == nil {
		m = !run.NotifyTG
	}
	cache[runID] = m
	return m
}

// primaryChat — основной чат нотификаций: минимальный chat_id из whitelist
// (команды принимаются от любого привязанного чата, нотификации идут в
// основной — маппинги сообщений хранятся по нему).
func (a *Adapter) primaryChat(ctx context.Context) (int64, bool) {
	chats, err := a.tg.ListChats(ctx)
	if err != nil || len(chats) == 0 {
		return 0, false
	}
	return chats[0], true // ListChats отсортирован по chat_id
}

// loadInt64State — int64 из tg_state; нет ключа/мусор → 0.
func (a *Adapter) loadInt64State(ctx context.Context, key string) int64 {
	raw, err := a.tg.GetState(ctx, key)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// --- rate limiter ------------------------------------------------------------

// rateLimiter — простой троттлер исходящих (D-72): минимальный интервал
// между любыми отправками (глобально) и между отправками в один чат.
type rateLimiter struct {
	mu            sync.Mutex
	globalLast    time.Time
	perChat       map[int64]time.Time
	globalSpacing time.Duration
	chatSpacing   time.Duration
}

func newRateLimiter(globalSpacing, chatSpacing time.Duration) *rateLimiter {
	return &rateLimiter{
		perChat:       make(map[int64]time.Time),
		globalSpacing: globalSpacing,
		chatSpacing:   chatSpacing,
	}
}

// wait блокируется до разрешённого момента отправки (или отмены ctx).
func (l *rateLimiter) wait(ctx context.Context, chatID int64) error {
	for {
		l.mu.Lock()
		now := time.Now()
		delay := l.globalSpacing - now.Sub(l.globalLast)
		if d := l.chatSpacing - now.Sub(l.perChat[chatID]); d > delay {
			delay = d
		}
		if delay <= 0 {
			l.globalLast = now
			l.perChat[chatID] = now
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

package events

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
)

const (
	// DefaultBatchInterval — период сброса стрим-событий (T-04: не транзакция
	// на каждый токен).
	DefaultBatchInterval = 200 * time.Millisecond
	// DefaultBatchMaxBytes — порог принудительного сброса батча.
	DefaultBatchMaxBytes = 64 * 1024
)

// StreamBatcher батчит стрим-события этапа (stream.*): запись в журнал
// одной транзакцией на батч — по таймеру или по порогу байт (T-04).
type StreamBatcher struct {
	journal *Journal
	runID   string
	stageID *int64

	interval time.Duration
	maxBytes int

	mu           sync.Mutex
	pending      []dtorep.Event
	pendingBytes int
	flushCh      chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
	wg           sync.WaitGroup
}

func NewStreamBatcher(journal *Journal, runID string, stageID *int64) *StreamBatcher {
	return &StreamBatcher{
		journal:  journal,
		runID:    runID,
		stageID:  stageID,
		interval: DefaultBatchInterval,
		maxBytes: DefaultBatchMaxBytes,
		flushCh:  make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

// Start запускает фоновый цикл сброса по таймеру. Останавливается Close'ом
// или отменой ctx.
func (b *StreamBatcher) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		<-b.done
		cancel()
	}()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(b.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = b.Flush(context.Background())
			case <-b.flushCh:
				_ = b.Flush(context.Background())
			}
		}
	}()
}

// Append добавляет стрим-событие в батч (неблокирующе). Превышение порога
// байт инициирует внеочередной сброс.
func (b *StreamBatcher) Append(kind string, payload []byte) {
	b.mu.Lock()
	b.pending = append(b.pending, dtorep.Event{
		RunID:       b.runID,
		StageID:     b.stageID,
		Kind:        kind,
		PayloadJSON: string(payload),
	})
	b.pendingBytes += len(payload)
	over := b.pendingBytes >= b.maxBytes
	b.mu.Unlock()

	if over {
		select {
		case b.flushCh <- struct{}{}:
		default: // сброс уже запрошен
		}
	}
}

// Flush атомарно записывает накопленный батч одной транзакцией.
func (b *StreamBatcher) Flush(ctx context.Context) error {
	b.mu.Lock()
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return nil
	}
	batch := b.pending
	b.pending = nil
	b.pendingBytes = 0
	b.mu.Unlock()

	err := b.journal.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, ev := range batch {
			if err := b.journal.Append(ctx, tx, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to flush stream batch: %w", err)
	}
	return nil
}

// Close останавливает цикл и сбрасывает остаток батча.
func (b *StreamBatcher) Close(ctx context.Context) error {
	b.closeOnce.Do(func() {
		close(b.done)
	})
	b.wg.Wait()
	return b.Flush(ctx)
}

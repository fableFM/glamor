package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// TxManager открывает *sql.Tx для кросс-доменных транзакций
// (например «CAS-переход + событие журнала в одном tx», D-11).
// Внутри fn доменные Tx-обёртки строятся через NewTx(tx) каждого пакета.
// Обогащённый ctx (например с collector'ом событий журнала) пробрасывается
// в fn — используйте его, а не захваченный снаружи.
type TxManager struct {
	db *sql.DB
}

func NewTxManager(db *sql.DB) *TxManager {
	return &TxManager{db: db}
}

// WithTx выполняет fn в транзакции: commit при nil-ошибке, rollback иначе.
func (m *TxManager) WithTx(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}

	if err := fn(ctx, tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("%w (rollback failed: %v)", err, rbErr)
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit tx: %w", err)
	}
	return nil
}

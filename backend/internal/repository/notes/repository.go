package notes

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const componentName = "repository/notes"

type repository struct {
	*query
	db *sql.DB
}

func NewRepository(db *sql.DB) RepositoryWithTX {
	return &repository{
		query: &query{conn: db},
		db:    db,
	}
}

// NewTx оборачивает уже открытую *sql.Tx — для кросс-доменных транзакций
// через repository.TxManager.
func NewTx(tx *sql.Tx) Tx {
	return &txRepo{tx: tx, query: &query{conn: tx}}
}

func (r *repository) OpenTx(ctx context.Context) (Tx, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to begin tx: %w", componentName, err)
	}
	return NewTx(tx), nil
}

type txRepo struct {
	tx *sql.Tx
	*query
}

func (t *txRepo) Commit(_ context.Context) error   { return t.tx.Commit() }
func (t *txRepo) Rollback(_ context.Context) error { return t.tx.Rollback() }

// note — приватная модель строки таблицы notes.
type note struct {
	id             int64
	runID          string
	stageID        sql.NullInt64
	kind           string
	text           string
	consumed       bool
	idempotencyKey string
	createdAt      time.Time
	consumedAt     sql.NullTime
}

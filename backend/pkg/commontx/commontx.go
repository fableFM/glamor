// Package commontx — общий интерфейс транзакции для repository-слоя (D-80),
// по образу pkg/postgres/commontx в dashboard-manager.
package commontx

import "context"

// Tx — минимальный контракт транзакции репозитория.
type Tx interface {
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

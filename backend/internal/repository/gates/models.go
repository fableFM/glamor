package gates

import (
	"database/sql"
	"time"
)

// gate — приватная модель строки таблицы gates.
type gate struct {
	id             string
	runID          string
	stageID        sql.NullInt64
	kind           string
	question       string
	contextJSON    string
	state          string
	answer         sql.NullString
	idempotencyKey string
	createdAt      time.Time
	resolvedAt     sql.NullTime
}

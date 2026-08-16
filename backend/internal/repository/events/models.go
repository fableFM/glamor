package events

import (
	"database/sql"
	"time"
)

// event — приватная модель строки таблицы events.
type event struct {
	id          int64
	runID       string
	stageID     sql.NullInt64
	ts          time.Time
	kind        string
	payloadJSON string
}

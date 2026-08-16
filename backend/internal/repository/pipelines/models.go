package pipelines

import (
	"database/sql"
	"time"
)

// pipeline — приватная модель строки таблицы pipelines.
type pipeline struct {
	id              int64
	projectID       sql.NullInt64
	name            string
	version         int64
	parentVersionID sql.NullInt64
	specJSON        string
	createdAt       time.Time
}

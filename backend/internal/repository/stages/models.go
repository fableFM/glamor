package stages

import (
	"database/sql"
)

// stage — приватная модель строки таблицы run_stages.
type stage struct {
	id              int64
	runID           string
	stageKey        string
	iteration       int64
	state           string
	harness         string
	sessionID       sql.NullString
	pid             sql.NullInt64
	exitCode        sql.NullInt64
	stopRequestedBy sql.NullString
	resumeCount     int64
	startedAt       sql.NullTime
	finishedAt      sql.NullTime
	tokensIn        int64
	tokensOut       int64
	err             sql.NullString
}

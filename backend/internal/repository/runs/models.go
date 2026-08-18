package runs

import (
	"database/sql"
	"time"
)

// run — приватная модель строки таблицы runs.
type run struct {
	id                string
	projectID         int64
	pipelineVersionID int64
	taskText          string
	baseBranch        string
	branch            string
	state             string
	depth             int64
	notifyTG          bool
	idempotencyKey    string
	createdAt         time.Time
	finishedAt        sql.NullTime
	tgRootMessageID   sql.NullInt64
}

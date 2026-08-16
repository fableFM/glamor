package projects

import (
	"time"
)

// project — приватная модель строки таблицы projects.
type project struct {
	id            int64
	path          string
	name          string
	defaultBranch string
	ideCommand    string
	createdAt     time.Time
}

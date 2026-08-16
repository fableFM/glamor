package gitx

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNotARepo — путь проекта не является git-репозиторием.
	ErrNotARepo = errors.New("not a git repository")
	// ErrBaseBranchMissing — базовая ветка не существует.
	ErrBaseBranchMissing = errors.New("base branch missing")
	// ErrBranchMismatch — чекаут переключён на другую ветку (T-10).
	ErrBranchMismatch = errors.New("branch mismatch")
)

// DirtyCheckoutError — чекаут грязный (D-34): содержит список файлов.
type DirtyCheckoutError struct {
	Files []string
}

func (e *DirtyCheckoutError) Error() string {
	const maxShow = 10
	files := e.Files
	suffix := ""
	if len(files) > maxShow {
		suffix = fmt.Sprintf(" and %d more", len(files)-maxShow)
		files = files[:maxShow]
	}
	return fmt.Sprintf("checkout is dirty: %s%s", strings.Join(files, ", "), suffix)
}

// IsDirtyCheckout распознаёт DirtyCheckoutError (для API-маппинга 400
// dirty_checkout с details.files).
func IsDirtyCheckout(err error) (*DirtyCheckoutError, bool) {
	var dirty *DirtyCheckoutError
	if errors.As(err, &dirty) {
		return dirty, true
	}
	return nil, false
}

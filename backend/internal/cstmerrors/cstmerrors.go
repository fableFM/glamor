// Package cstmerrors — сентинел-ошибки домена glamor (D-80).
// Ошибки объявляются здесь, оборачиваются fmt.Errorf("...: %w") на пути наверх
// и распознаются через errors.Is/As на границах слоёв.
package cstmerrors

import "errors"

var (
	// ErrNotFound — сущность не найдена в хранилище.
	ErrNotFound = errors.New("not found")

	// ErrInvalidTransition — переход состояния не разрешён стейт-машиной (ADR-001).
	ErrInvalidTransition = errors.New("invalid state transition")

	// ErrConcurrentModification — CAS-переход не применён: состояние уже изменено
	// конкурентным писателем.
	ErrConcurrentModification = errors.New("concurrent modification")

	// ErrDuplicate — нарушен уникальный ключ (idempotency-key и т.п.).
	ErrDuplicate = errors.New("duplicate key")

	// ErrRunLocked — ветка проекта занята активным раном (D-33).
	ErrRunLocked = errors.New("run locked")

	// ErrGateAlreadyResolved — гейт уже резолвнут другим резолюшном.
	ErrGateAlreadyResolved = errors.New("gate already resolved")

	// ErrValidation — невалидный запрос (бизнес-валидация, не схема).
	ErrValidation = errors.New("validation failed")

	// ErrTooLarge — содержимое больше допустимого cap'а (413; артефакты, F-02).
	ErrTooLarge = errors.New("content too large")
)

// RunLockedError — lock-конфликт (D-33) с контекстом: какая ветка занята
// и каким активным раном. Разворачивается в ErrRunLocked (errors.Is),
// контроллер маппит поля в details ответа 409.
type RunLockedError struct {
	Branch string
	RunID  string
}

func (e *RunLockedError) Error() string {
	return "branch " + e.Branch + " is locked by run " + e.RunID + ": " + ErrRunLocked.Error()
}

func (e *RunLockedError) Unwrap() error { return ErrRunLocked }

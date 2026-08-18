package http

import (
	"errors"

	"github.com/fableFM/glamor/internal/controller/http/genapi"
	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/gitx"
)

// errorToResponse переводит доменные ошибки в единый формат Error
// (коды — из api/openapi.yaml: components.schemas.Error).
func errorToResponse(err error) (genapi.Error, int) {
	// грязный чекаут (D-34): 400 dirty_checkout + список файлов в details
	if dirty, ok := gitx.IsDirtyCheckout(err); ok {
		details := map[string]interface{}{"files": dirty.Files}
		return genapi.Error{Code: "dirty_checkout", Message: err.Error(), Details: &details}, 400
	}

	// run_locked (D-33): 409 + details.branch/run_id занятой ветки (F-02)
	var locked *cstmerrors.RunLockedError
	if errors.As(err, &locked) {
		details := map[string]interface{}{"branch": locked.Branch, "run_id": locked.RunID}
		return genapi.Error{Code: "run_locked", Message: err.Error(), Details: &details}, 409
	}

	code := "internal"
	status := 500

	switch {
	case errors.Is(err, cstmerrors.ErrNotFound):
		code, status = "not_found", 404
	case errors.Is(err, cstmerrors.ErrInvalidTransition),
		errors.Is(err, cstmerrors.ErrConcurrentModification):
		code, status = "invalid_transition", 409
	case errors.Is(err, cstmerrors.ErrRunLocked):
		code, status = "run_locked", 409
	case errors.Is(err, cstmerrors.ErrGateAlreadyResolved):
		code, status = "gate_already_resolved", 409
	case errors.Is(err, cstmerrors.ErrDuplicate):
		code, status = "duplicate", 409
	case errors.Is(err, cstmerrors.ErrValidation):
		code, status = "validation", 400
	case errors.Is(err, cstmerrors.ErrTooLarge):
		code, status = "too_large", 413
	}

	return genapi.Error{Code: code, Message: err.Error()}, status
}

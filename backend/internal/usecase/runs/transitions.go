// Package runs — доменная стейт-машина ранов/стадий/гейтов (ADR-001, D-10/11/14/16).
// Движок не хранит состояние в памяти: каждое решение принимается чтением
// runs/run_stages/gates из SQLite, переходы — только CAS + событие журнала
// в одной транзакции.
package runs

import (
	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
)

// Таблицы разрешённых переходов (ADR-001). Переход вне таблицы — ошибка
// программиста: машина возвращает cstmerrors.ErrInvalidTransition
// (в тестах это обязано проверяться явно).

var runTransitions = map[dtorep.RunState][]dtorep.RunState{
	dtorep.RunStateDraft: {dtorep.RunStateRunning},
	dtorep.RunStateRunning: {
		dtorep.RunStateWaitingGate,
		dtorep.RunStateSucceeded,
		dtorep.RunStateFailed,
		dtorep.RunStateStopped,
	},
	dtorep.RunStateWaitingGate: {
		dtorep.RunStateRunning, // все гейты резолвнуты — продолжаем
		dtorep.RunStateStopped, // пользователь может остановить ран и на гейте (D-14)
	},
	dtorep.RunStateFailed: {
		dtorep.RunStateRunning, // ручной resume (POST /runs/{id}/resume, ADR-001 доп. 2026-08-16)
	},
	// succeeded/stopped — терминальные, исходящих нет.
}

var stageTransitions = map[dtorep.StageState][]dtorep.StageState{
	dtorep.StageStatePending: {
		dtorep.StageStateRunning,
		dtorep.StageStateSkipped, // условные этапы (пост-M1, в enum сразу)
	},
	dtorep.StageStateRunning: {
		dtorep.StageStateSucceeded,
		dtorep.StageStateFailed,
		dtorep.StageStateInterrupted,
	},
	// succeeded/failed/skipped — терминальные.
	// interrupted — НЕ терминал, но исходящего перехода у СТРОКИ нет:
	// auto-resume создаёт НОВУЮ строку (iteration+1, resume_count+1), ADR-001.
}

var gateTransitions = map[dtorep.GateState][]dtorep.GateState{
	dtorep.GateStateOpen: {
		dtorep.GateStateAnswered,
		dtorep.GateStateApproved,
		dtorep.GateStateRejected,
		dtorep.GateStateExpired,
	},
}

func validateRunTransition(from, to dtorep.RunState) error {
	if !transitionAllowed(runTransitions[from], to) {
		return cstmerrors.ErrInvalidTransition
	}
	return nil
}

func validateStageTransition(from, to dtorep.StageState) error {
	if !transitionAllowed(stageTransitions[from], to) {
		return cstmerrors.ErrInvalidTransition
	}
	return nil
}

func validateGateTransition(from, to dtorep.GateState) error {
	if !transitionAllowed(gateTransitions[from], to) {
		return cstmerrors.ErrInvalidTransition
	}
	return nil
}

func transitionAllowed[S ~string](allowed []S, to S) bool {
	for _, s := range allowed {
		if s == to {
			return true
		}
	}
	return false
}

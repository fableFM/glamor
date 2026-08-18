package http

import (
	"context"

	"github.com/fableFM/glamor/internal/controller/http/genapi"
	"github.com/fableFM/glamor/internal/service/settings"
)

// --- настройки демона (экран настроек UI) -------------------------------------

func mapTelegramSettings(t settings.TelegramSettings) genapi.TelegramSettings {
	out := genapi.TelegramSettings{
		Enabled:  t.Enabled,
		HasToken: t.HasToken,
	}
	if t.TokenMasked != "" {
		out.TokenMasked = &t.TokenMasked
	}
	if t.BotUsername != "" {
		out.BotUsername = &t.BotUsername
	}
	return out
}

func mapSupervisorSettings(s settings.SupervisorSettings) genapi.SupervisorSettings {
	return genapi.SupervisorSettings{
		StallTimeoutSec: s.StallTimeoutSec,
		StageTimeoutMin: s.StageTimeoutMin,
		MaxParallel:     s.MaxParallel,
		MaxAutoResumes:  s.MaxAutoResumes,
	}
}

func (h *handlers) GetSettings(_ context.Context, _ genapi.GetSettingsRequestObject) (genapi.GetSettingsResponseObject, error) {
	tg, sup := h.settings.Get()
	return genapi.GetSettings200JSONResponse{
		Telegram:   mapTelegramSettings(tg),
		Supervisor: mapSupervisorSettings(sup),
	}, nil
}

func (h *handlers) PutTelegramSettings(ctx context.Context, req genapi.PutTelegramSettingsRequestObject) (genapi.PutTelegramSettingsResponseObject, error) {
	var token string
	if req.Body.Token != nil {
		token = *req.Body.Token
	}

	tg, err := h.settings.PutTelegram(ctx, token, req.Body.Enabled)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.PutTelegramSettingsdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.PutTelegramSettings200JSONResponse(mapTelegramSettings(tg)), nil
}

func (h *handlers) PutSupervisorSettings(_ context.Context, req genapi.PutSupervisorSettingsRequestObject) (genapi.PutSupervisorSettingsResponseObject, error) {
	var update settings.SupervisorSettings
	if req.Body.StallTimeoutSec != nil {
		update.StallTimeoutSec = *req.Body.StallTimeoutSec
	}
	if req.Body.StageTimeoutMin != nil {
		update.StageTimeoutMin = *req.Body.StageTimeoutMin
	}
	if req.Body.MaxParallel != nil {
		update.MaxParallel = *req.Body.MaxParallel
	}
	if req.Body.MaxAutoResumes != nil {
		update.MaxAutoResumes = *req.Body.MaxAutoResumes
	}

	sup, err := h.settings.PutSupervisor(update)
	if err != nil {
		e, status := errorToResponse(err)
		return genapi.PutSupervisorSettingsdefaultJSONResponse{Body: e, StatusCode: status}, nil
	}
	return genapi.PutSupervisorSettings200JSONResponse(mapSupervisorSettings(sup)), nil
}

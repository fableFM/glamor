package http

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/fableFM/glamor/internal/controller/http/genapi"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/service/catalog"
	"github.com/fableFM/glamor/internal/service/runsapi"
)

// Machine — методы стейт-машины, нужные REST-контроллеру. Интерфейс
// объявлен у потребителя (D-80); реализация — сценарии service/runsapi.
type Machine interface {
	CreateRun(ctx context.Context, params runsapi.CreateRunParams) (run *dtorep.Run, alreadyExisted bool, err error)
	StopRun(ctx context.Context, runID string) (*dtorep.Run, error)
	ResumeRun(ctx context.Context, runID string) (*dtorep.Run, error)
	CreateNote(ctx context.Context, runID, text, idempotencyKey string) (*dtorep.Note, error)
	InterruptStageSteer(ctx context.Context, stageID int64, message string) (*dtorep.Stage, error)
	ResolveGateAPI(ctx context.Context, gateID string, action runsapi.GateAction, text *string) (gate *dtorep.Gate, alreadyResolved bool, err error)
}

// Deps — зависимости REST-контроллера (ручной DI из main, D-80).
// Контроллер работает только через service-слой, репозитории не знает.
type Deps struct {
	Machine Machine
	API     *catalog.Service

	Token     string // D-08; пустой = auth выключен (dev)
	Version   string
	Harnesses []string
	// Draining — graceful shutdown (T-12): новые раны не принимаем (503).
	Draining *atomic.Bool
}

// NewHandler собирает http.Handler REST API: strict-server поверх
// сгенерированного роутинга + auth-мидлварь. /ws НЕ входит (родительский
// mux в main перехватывает его раньше — см. MountOn).
func NewHandler(deps Deps) http.Handler {
	draining := deps.Draining
	if draining == nil {
		draining = &atomic.Bool{}
	}

	h := &handlers{
		draining:  draining,
		machine:   deps.Machine,
		api:       deps.API,
		version:   deps.Version,
		harnesses: deps.Harnesses,
	}

	strict := genapi.NewStrictHandler(h, nil)
	inner := http.NewServeMux()
	genapi.HandlerFromMux(strict, inner)

	return authMiddleware(deps.Token, inner)
}

// MountOn регистрирует REST и WS на одном mux: /ws обслуживает
// ws-контроллер (паттерн специфичнее catch-all REST).
func MountOn(mux *http.ServeMux, rest, ws http.Handler) {
	mux.Handle("GET /ws", ws)
	mux.Handle("/", rest)
}

// authMiddleware — Bearer-токен (D-08) на всё, кроме /healthz
// (для Tauri-пинга без чувствительных данных, T-12).
func authMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix || auth[len(prefix):] != token {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(genapi.Error{
				Code:    "unauthorized",
				Message: "invalid or missing bearer token",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/fableFM/glamor/internal/controller/http/genapi"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/service/catalog"
	"github.com/fableFM/glamor/internal/service/lessons"
	"github.com/fableFM/glamor/internal/service/pipeline"
	"github.com/fableFM/glamor/internal/service/runsapi"
	"github.com/fableFM/glamor/internal/service/settings"
	"github.com/fableFM/glamor/internal/service/vendormemory"
)

// Machine — методы стейт-машины, нужные REST-контроллеру. Интерфейс
// объявлен у потребителя (D-80); реализация — сценарии service/runsapi.
type Machine interface {
	CreateRun(ctx context.Context, params runsapi.CreateRunParams) (run *dtorep.Run, alreadyExisted bool, err error)
	StopRun(ctx context.Context, runID string) (*dtorep.Run, error)
	ResumeRun(ctx context.Context, runID string) (*dtorep.Run, error)
	CreateNote(ctx context.Context, runID, text, idempotencyKey string) (*dtorep.Note, error)
	InterruptStageSteer(ctx context.Context, stageID int64, message string) (*dtorep.Stage, error)
	ResolveGateAPI(ctx context.Context, gateID string, action runsapi.GateAction, text *string, sel *dtorep.LessonOpSelection) (gate *dtorep.Gate, alreadyResolved bool, err error)
}

// TelegramPairer — завершение привязки TG-чата по коду /start (T-19).
// Интерфейс у потребителя (D-80); реализация — адаптер
// internal/notify/telegram. nil — TG-контур выключен (404 на /telegram/pair).
type TelegramPairer interface {
	PairByCode(ctx context.Context, code string) (int64, error)
}

// Deps — зависимости REST-контроллера (ручной DI из main, D-80).
// Контроллер работает только через service-слой, репозитории не знает.
type Deps struct {
	Machine Machine
	API     *catalog.Service

	Pipelines *pipeline.Service
	Memory    *vendormemory.Service
	Lessons   *lessons.Service
	Settings  *settings.Service

	// Telegram — привязка чатов TG-адаптера (T-19); nil — адаптер выключен.
	Telegram TelegramPairer

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
		pipelines: deps.Pipelines,
		memory:    deps.Memory,
		lessons:   deps.Lessons,
		settings:  deps.Settings,
		telegram:  deps.Telegram,
		version:   deps.Version,
		harnesses: deps.Harnesses,
	}

	strict := genapi.NewStrictHandler(h, nil)
	inner := http.NewServeMux()
	genapi.HandlerFromMux(strict, inner)

	return corsMiddleware(authMiddleware(deps.Token, inner))
}

// corsMiddleware — локальный демон для UI из Vite/Tauri (D-06): разрешаем
// origins localhost/127.0.0.1/tauri. Без этого браузер блокирует ответы
// (CORS) и UI показывает «демон недоступен». Preflight (OPTIONS) — до auth.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && isLocalOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
			w.Header().Set("Access-Control-Max-Age", "3600")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLocalOrigin — origin локального UI (vite dev, tauri webview).
func isLocalOrigin(origin string) bool {
	lower := strings.ToLower(origin)
	for _, prefix := range []string{
		"http://localhost", "https://localhost",
		"http://127.0.0.1", "https://127.0.0.1",
		"http://tauri.localhost", "tauri://localhost",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
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

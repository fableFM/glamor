package http

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/fableFM/glamor/internal/controller/http/genapi"
	"github.com/fableFM/glamor/internal/events"
	artifactsrep "github.com/fableFM/glamor/internal/repository/artifacts"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	usecase "github.com/fableFM/glamor/internal/usecase/runs"
)

// Deps — зависимости REST-контроллера (ручной DI из main, D-80).
type Deps struct {
	Machine   *usecase.Machine
	Journal   *events.Journal
	Projects  projectsrep.RepositoryWithTX
	Pipelines pipelinesrep.RepositoryWithTX
	Runs      runsrep.RepositoryWithTX
	Stages    stagesrep.RepositoryWithTX
	Gates     gatesrep.RepositoryWithTX
	Artifacts artifactsrep.RepositoryWithTX
	Notes     notesrep.RepositoryWithTX

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
		journal:   deps.Journal,
		projects:  deps.Projects,
		pipelines: deps.Pipelines,
		runs:      deps.Runs,
		stages:    deps.Stages,
		gates:     deps.Gates,
		artifacts: deps.Artifacts,
		notes:     deps.Notes,
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

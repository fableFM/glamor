package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	httpctrl "github.com/fableFM/glamor/internal/controller/http"
	wsctrl "github.com/fableFM/glamor/internal/controller/ws"
	"github.com/fableFM/glamor/internal/daemon"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/gitx"
	"github.com/fableFM/glamor/internal/harness"
	"github.com/fableFM/glamor/internal/harness/kimi"
	"github.com/fableFM/glamor/internal/harness/qwen"
	"github.com/fableFM/glamor/internal/repository"
	artifactsrep "github.com/fableFM/glamor/internal/repository/artifacts"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/service/supervisor"
	runsusecase "github.com/fableFM/glamor/internal/usecase/runs"
	// регистрация goose-миграций (D-05)
	_ "github.com/fableFM/glamor/migrations"
)

// version — версия демона (GET /version).
const version = "0.1.0"

func main() {
	if err := run(); err != nil {
		slog.Error("glamord failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	home := glamorHome()
	lockPath := filepath.Join(home, "glamord.lock")
	daemonJSONPath := filepath.Join(home, "daemon.json")

	// единственный инстанс (T-12): второй запуск → сообщение и exit 1
	lock, err := daemon.AcquireLock(lockPath)
	if err != nil {
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			info := daemon.InspectLock(lockPath, daemonJSONPath)
			fmt.Fprintf(os.Stderr, "glamord already running (pid %d, port %d)\n", info.PID, info.Port)
			os.Exit(1)
		}
		return err
	}
	defer lock.Release()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// токен localhost-API (D-08): первый запуск генерирует (perms 600)
	if cfg.Security.Token == "" {
		token, err := daemon.LoadOrCreateToken(filepath.Join(home, "token"))
		if err != nil {
			return err
		}
		cfg.Security.Token = token
	}

	// лог: stderr + файл с ротацией (T-12)
	logCloser := setupLogger(cfg.Log.Level, filepath.Join(home, "glamord.log"))
	defer func() { _ = logCloser.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// D-05: демон стартует «поверх мигрированной БД» — миграции до listeners.
	db, err := repository.Open(ctx, cfg.DB.Path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := repository.Migrate(ctx, db); err != nil {
		return err
	}

	// журнал событий + WS hub (D-04, D-11)
	hub := events.NewHub()
	journal := events.NewJournal(db, hub)

	// стейт-машина поверх журнала (события переходов → hub после коммита)
	machine := runsusecase.NewMachine(db, journal)
	machine.SetTxExecutor(journal)

	// startup recovery (D-15, T-12): сироты → interrupted → auto-resume;
	// stop_requested_by=daemon прошлого shutdown — очищается для resume
	recovered, err := machine.RecoverInterrupted(ctx)
	if err != nil {
		return fmt.Errorf("startup recovery failed: %w", err)
	}
	if len(recovered) > 0 {
		slog.InfoContext(ctx, "recovered orphan stages",
			slog.String("component", "main"), slog.Int("count", len(recovered)))
	}

	// git-контур (T-10): preflight при создании рана, имена веток,
	// prepare/branch-check в supervisor'е
	machine.SetPreflight(gitx.Preflight)
	machine.SetBranchNamer(gitx.SuggestBranch)

	// harness-реестр (T-06..T-08): проверка бинарей при старте
	registry := harness.NewRegistry(cfg.Harnesses)
	registry.Register(kimi.New())
	registry.Register(qwen.New())
	var availableHarnesses []string
	for _, st := range registry.CheckBinaries() {
		if st.Available {
			availableHarnesses = append(availableHarnesses, st.Name)
		} else {
			slog.WarnContext(ctx, "harness binary not found",
				slog.String("component", "main"), slog.String("harness", st.Name))
		}
	}

	// supervisor — процессный контур этапов (T-09)
	supCfg := supervisor.DefaultConfig(cfg.Supervisor.RunsDir)
	supCfg.StallTimeout = time.Duration(cfg.Supervisor.StallTimeoutSec) * time.Second
	supCfg.StageTimeout = time.Duration(cfg.Supervisor.StageTimeoutMin) * time.Minute
	supCfg.MaxParallel = cfg.Supervisor.MaxParallel
	supCfg.MaxAutoResumes = cfg.Supervisor.MaxAutoResumes
	sup := supervisor.New(machine, registry, journal, supCfg, nil,
		runsrep.NewRepository(db), stagesrep.NewRepository(db),
		projectsrep.NewRepository(db), pipelinesrep.NewRepository(db),
		notesrep.NewRepository(db))
	sup.SetPostRunHook(func(ctx context.Context, run *dtorep.Run, projectPath string) error {
		return gitx.PrepareBranch(ctx, projectPath, run.BaseBranch, run.Branch)
	})
	sup.SetPreStageHook(func(ctx context.Context, run *dtorep.Run, projectPath string) error {
		return gitx.CheckBranchMismatch(ctx, projectPath, run.Branch)
	})
	go func() { _ = sup.Run(ctx) }()

	draining := &atomic.Bool{}
	rest := httpctrl.NewHandler(httpctrl.Deps{
		Draining:  draining,
		Machine:   machine,
		Journal:   journal,
		Projects:  projectsrep.NewRepository(db),
		Pipelines: pipelinesrep.NewRepository(db),
		Runs:      runsrep.NewRepository(db),
		Stages:    stagesrep.NewRepository(db),
		Gates:     gatesrep.NewRepository(db),
		Artifacts: artifactsrep.NewRepository(db),
		Notes:     notesrep.NewRepository(db),
		Token:     cfg.Security.Token,
		Version:   version,
		Harnesses: availableHarnesses,
	})
	wsHandler := wsctrl.NewHandler(journal, hub, cfg.Security.Token)

	mux := http.NewServeMux()
	httpctrl.MountOn(mux, rest, wsHandler)

	addr := net.JoinHostPort(cfg.HTTP.Host, fmt.Sprintf("%d", cfg.HTTP.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	slog.InfoContext(ctx, "glamord started",
		slog.String("component", "main"),
		slog.String("addr", listener.Addr().String()),
	)

	// daemon.json — как клиентам найти демона (D-08, T-12)
	port := listener.Addr().(*net.TCPAddr).Port
	if err := daemon.WriteInfo(daemonJSONPath, daemon.Info{
		PID:     os.Getpid(),
		Port:    port,
		URL:     fmt.Sprintf("http://%s", listener.Addr().String()),
		Version: version,
	}); err != nil {
		return fmt.Errorf("failed to write daemon.json: %w", err)
	}
	defer daemon.RemoveInfo(daemonJSONPath)

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		slog.Info("glamord shutting down", slog.String("component", "main"))
		// D-15 (T-12): стоп приёма новых ранов (POST /runs → 503 draining) →
		// прерывание живых этапов (stop_requested_by=daemon → auto-resume при
		// подъёме) → закрытие listeners → выход.
		draining.Store(true)
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer drainCancel()
		if err := sup.Drain(drainCtx); err != nil {
			slog.Error("supervisor drain failed", slog.String("component", "main"),
				slog.String("error", err.Error()))
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("failed to shutdown http server: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server failed: %w", err)
	}
}

// setupLogger настраивает slog: stderr + файл с ротацией (T-12).
// Возвращает closer лог-файла.
func setupLogger(level, logPath string) io.Closer {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	const maxLogBytes = 10 << 20 // 10 МБ
	rotate, err := daemon.NewRotateWriter(logPath, maxLogBytes)
	if err != nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
		slog.Warn("failed to open log file, logging to stderr only",
			slog.String("error", err.Error()))
		return nopCloser{}
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(
		io.MultiWriter(os.Stderr, rotate), &slog.HandlerOptions{Level: lvl})))
	return rotate
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

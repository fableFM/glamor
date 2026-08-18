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
	"sync"
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
	"github.com/fableFM/glamor/internal/harness/claude"
	"github.com/fableFM/glamor/internal/harness/codex"
	"github.com/fableFM/glamor/internal/harness/kimi"
	"github.com/fableFM/glamor/internal/harness/opencode"
	"github.com/fableFM/glamor/internal/harness/qwen"
	"github.com/fableFM/glamor/internal/notify/telegram"
	"github.com/fableFM/glamor/internal/repository"
	artifactsrep "github.com/fableFM/glamor/internal/repository/artifacts"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	lessonsrep "github.com/fableFM/glamor/internal/repository/lessons"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	telegramrep "github.com/fableFM/glamor/internal/repository/telegram"
	"github.com/fableFM/glamor/internal/repository/vendorindex"
	"github.com/fableFM/glamor/internal/service/catalog"
	"github.com/fableFM/glamor/internal/service/lessons"
	"github.com/fableFM/glamor/internal/service/pipeline"
	"github.com/fableFM/glamor/internal/service/runsapi"
	"github.com/fableFM/glamor/internal/service/runsmachine"
	"github.com/fableFM/glamor/internal/service/settings"
	"github.com/fableFM/glamor/internal/service/supervisor"
	"github.com/fableFM/glamor/internal/service/vendormemory"
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

	// репозитории конструируются один раз (ручной DI, D-80) и раздаются
	// всем потребителям готовыми интерфейсами
	runsRepo := runsrep.NewRepository(db)
	stagesRepo := stagesrep.NewRepository(db)
	gatesRepo := gatesrep.NewRepository(db)
	pipelinesRepo := pipelinesrep.NewRepository(db)
	projectsRepo := projectsrep.NewRepository(db)
	notesRepo := notesrep.NewRepository(db)
	artifactsRepo := artifactsrep.NewRepository(db)
	eventsRepo := eventsrep.NewRepository(db)
	txm := repository.NewTxManager(db)

	// журнал событий + WS hub (D-04, D-11)
	hub := events.NewHub()
	journal := events.NewJournal(eventsRepo, txm, hub)

	// стейт-машина поверх журнала (транзакции машины публикуют события в
	// Hub после коммита: journal — одновременно TxExecutor и EventAppender)
	machine := runsmachine.NewMachine(runsRepo, stagesRepo, gatesRepo,
		pipelinesRepo, projectsRepo, notesRepo, journal, journal)

	// API-сценарии ранов (идемпотентность, git-preflight) поверх машины
	runsSvc := runsapi.New(machine, journal, journal,
		runsRepo, stagesRepo, notesRepo, projectsRepo, pipelinesRepo, gatesRepo)

	// T-17/T-21: seed глобального дефолтного пайплайна из default.yaml
	// (идемпотентно)
	pipelineSvc := pipeline.NewService(pipelinesRepo)
	if err := pipelineSvc.Seed(ctx); err != nil {
		return fmt.Errorf("failed to seed default pipeline: %w", err)
	}

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
	runsSvc.SetPreflight(gitx.Preflight)
	runsSvc.SetBranchNamer(gitx.SuggestBranch)

	// harness-реестр (T-06..T-08, T-25, T-26): проверка бинарей при старте
	registry := harness.NewRegistry(cfg.Harnesses)
	registry.Register(kimi.New())
	registry.Register(qwen.New())
	registry.Register(opencode.New())
	// T-25: этапы пайплайна авто-одобрены → bypassPermissions; codex —
	// только по докам (не установлен локально), e2e отложен до установки.
	registry.Register(claude.New("bypassPermissions"))
	registry.Register(codex.New())
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
	// T-23: vendor-память (FTS5-индекс, дельты, промоушн)
	vendorSvc := vendormemory.New(filepath.Join(home, "vendors"),
		vendorindex.NewRepository(db), artifactsRepo)
	vendorIndex := vendorindex.NewRepository(db)

	// T-29: причинно-следственная память (lessons)
	lessonsSvc := lessons.New(lessonsrep.NewRepository(db), vendorIndex,
		filepath.Join(home, "lessons"))
	runsSvc.SetLessonsFinalizer(lessons.NewGateFinalizer(lessonsSvc, runsRepo, projectsRepo))

	// T-17: промпт-билдер дефолтного пайплайна + fix-петля
	promptBuilder := pipeline.NewPromptBuilder(filepath.Join(home, "vendors"))
	promptBuilder.SetMemorySearcher(vendorSvc)
	promptBuilder.SetLessonsProvider(lessonsSvc)

	// T-23: переиндексация vendor-памяти при старте (dirty-check по содержимому)
	var projectPaths []string
	if projects, err := projectsRepo.ListProjects(ctx); err == nil {
		for _, p := range projects {
			projectPaths = append(projectPaths, p.Path)
		}
	}
	if err := vendorSvc.ReindexAll(ctx, projectPaths); err != nil {
		slog.WarnContext(ctx, "vendor memory reindex failed",
			slog.String("component", "main"), slog.String("error", err.Error()))
	}
	// T-29: реиндекс уроков (глобальные + проектные)
	lessonDirs := []string{filepath.Join(home, "lessons")}
	for _, p := range projectPaths {
		lessonDirs = append(lessonDirs, filepath.Join(p, ".glamor", "lessons"))
	}
	if err := vendorSvc.ReindexDirs(ctx, lessonDirs); err != nil {
		slog.WarnContext(ctx, "lessons reindex failed",
			slog.String("component", "main"), slog.String("error", err.Error()))
	}
	sup := supervisor.New(machine, registry, journal, supCfg, promptBuilder.Build,
		runsRepo, stagesRepo, projectsRepo, pipelinesRepo, notesRepo, artifactsRepo, gatesRepo)
	// T-17 fix-петля + T-23 применение дельт памяти после успешного этапа
	loopHook := pipeline.NewLoopHook(machine, supCfg.RunsDir)
	sup.SetOnStageSucceeded(func(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage, spec runsmachine.Spec) error {
		if project, err := projectsRepo.GetProjectByID(ctx, run.ProjectID); err == nil {
			if _, err := vendorSvc.ApplyDeltas(ctx, run.ID, stage.ID,
				filepath.Join(supCfg.RunsDir, run.ID), project.Path, spec.MemoryScope); err != nil {
				slog.ErrorContext(ctx, "failed to apply vendor deltas",
					slog.String("component", "main"), slog.String("error", err.Error()))
			}
		}
		return loopHook.OnStageSucceeded(ctx, run, stage, spec)
	})
	sup.SetPostRunHook(func(ctx context.Context, run *dtorep.Run, projectPath string) error {
		return gitx.PrepareBranch(ctx, projectPath, run.BaseBranch, run.Branch)
	})
	sup.SetPreStageHook(func(ctx context.Context, run *dtorep.Run, projectPath string) error {
		return gitx.CheckBranchMismatch(ctx, projectPath, run.Branch)
	})
	go func() { _ = sup.Run(ctx) }()

	// read-фасад REST и TG-пульта (D-80: controller/адаптер репозитории не знают)
	catalogSvc := catalog.New(projectsRepo, pipelinesRepo, runsRepo, stagesRepo,
		gatesRepo, artifactsRepo, notesRepo, journal, cfg.Supervisor.RunsDir)

	// TG-адаптер (T-19, D-70..73) + hot-apply из экрана настроек:
	// менеджер перезапуска адаптера при смене токена/enabled.
	var tgPairer httpctrl.TelegramPairer
	var tgMu sync.Mutex
	var tgCancel context.CancelFunc
	tgRepo := telegramrep.NewRepository(db)
	startTelegram := func(token string) {
		tgMu.Lock()
		defer tgMu.Unlock()
		if tgCancel != nil {
			tgCancel()
			tgCancel = nil
		}
		adapterCtx, cancel := context.WithCancel(ctx)
		tgCancel = cancel
		tgAdapter := telegram.NewAdapter(
			telegram.NewHTTPClient(token, nil),
			hub, journal, runsSvc, catalogSvc, tgRepo,
			telegram.Config{},
		)
		tgPairer = tgAdapter
		go func() { _ = tgAdapter.Run(adapterCtx) }()
		slog.InfoContext(ctx, "telegram adapter started", slog.String("component", "main"))
	}
	stopTelegram := func() {
		tgMu.Lock()
		defer tgMu.Unlock()
		if tgCancel != nil {
			tgCancel()
			tgCancel = nil
			slog.InfoContext(ctx, "telegram adapter stopped", slog.String("component", "main"))
		}
	}
	if cfg.Telegram.Enabled && cfg.Telegram.Token != "" {
		startTelegram(cfg.Telegram.Token)
	}

	// настройки демона (экран настроек UI): персист в config.yaml + hot-apply
	settingsSvc := settings.New(filepath.Join(home, "config.yaml"),
		settings.SupervisorSettings{
			StallTimeoutSec: cfg.Supervisor.StallTimeoutSec,
			StageTimeoutMin: cfg.Supervisor.StageTimeoutMin,
			MaxParallel:     cfg.Supervisor.MaxParallel,
			MaxAutoResumes:  cfg.Supervisor.MaxAutoResumes,
		}, cfg.Telegram.Token, cfg.Telegram.Enabled)
	settingsSvc.SetTelegramHook(func(token string, enabled bool) {
		if enabled && token != "" {
			startTelegram(token)
		} else {
			stopTelegram()
		}
	})
	settingsSvc.SetSupervisorHook(func(s settings.SupervisorSettings) {
		newCfg := sup.GetConfig()
		newCfg.StallTimeout = time.Duration(s.StallTimeoutSec) * time.Second
		newCfg.StageTimeout = time.Duration(s.StageTimeoutMin) * time.Minute
		newCfg.MaxParallel = s.MaxParallel
		newCfg.MaxAutoResumes = s.MaxAutoResumes
		sup.SetConfig(newCfg)
		slog.InfoContext(ctx, "supervisor config applied", slog.String("component", "main"))
	})

	draining := &atomic.Bool{}
	rest := httpctrl.NewHandler(httpctrl.Deps{
		Draining:  draining,
		Machine:   runsSvc,
		API:       catalogSvc,
		Pipelines: pipelineSvc,
		Memory:    vendorSvc,
		Lessons:   lessonsSvc,
		Settings:  settingsSvc,
		Telegram:  tgPairer,
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

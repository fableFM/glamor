// Command glamord — демон-оркестратор AI-пайплайнов glamor (D-01).
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

// Config — конфигурация демона glamord (D-80).
// Источники по приоритету (возрастанию): defaults < ~/.glamor/config.yaml < env.
type Config struct {
	HTTP struct {
		Host string `yaml:"host" validate:"required,ip|hostname"`
		// Port = 0 означает динамический порт (D-08); фактический порт
		// записывается в ~/.glamor/daemon.json (T-12).
		Port int `yaml:"port" validate:"min=0,max=65535"`
	} `yaml:"http"`

	DB struct {
		// Path к SQLite-файлу. Пустое значение = ~/.glamor/glamor.db.
		Path string `yaml:"path" validate:"required"`
	} `yaml:"db"`

	Security struct {
		// Token — токен localhost-API (D-08). Пустой = auth выключен (dev).
		Token string `yaml:"token"`
	} `yaml:"security"`

	Log struct {
		Level string `yaml:"level" validate:"required,oneof=debug info warn error"`
	} `yaml:"log"`

	// Telegram — TG-адаптер (T-19, D-70): нотификации и пульт в Telegram.
	// Токен никогда не логируется и не уходит в API-ответы.
	Telegram struct {
		Enabled bool   `yaml:"enabled"`
		Token   string `yaml:"token"`
	} `yaml:"telegram"`

	// Harnesses — переопределение путей бинарей: имя → путь (T-06).
	Harnesses map[string]string `yaml:"harnesses"`

	Supervisor struct {
		StallTimeoutSec int    `yaml:"stall_timeout_sec" validate:"required,min=1"`
		StageTimeoutMin int    `yaml:"stage_timeout_min" validate:"required,min=1"`
		MaxParallel     int    `yaml:"max_parallel" validate:"required,min=1"`
		MaxAutoResumes  int64  `yaml:"max_auto_resumes" validate:"min=0"`
		RunsDir         string `yaml:"runs_dir" validate:"required"`
	} `yaml:"supervisor"`
}

// defaultConfig возвращает конфигурацию с дефолтами.
func defaultConfig() Config {
	var cfg Config
	cfg.HTTP.Host = "127.0.0.1"
	cfg.HTTP.Port = 0
	cfg.DB.Path = defaultDBPath()
	cfg.Log.Level = "info"
	cfg.Supervisor.StallTimeoutSec = 120
	cfg.Supervisor.StageTimeoutMin = 60
	cfg.Supervisor.MaxParallel = 4
	cfg.Supervisor.MaxAutoResumes = 3
	cfg.Supervisor.RunsDir = filepath.Join(glamorHome(), "runs")
	return cfg
}

// loadConfig читает ~/.glamor/config.yaml (если есть), применяет env-оверрайды
// и валидирует результат.
func loadConfig() (Config, error) {
	cfg := defaultConfig()

	path := os.Getenv("GLAMOR_CONFIG")
	if path == "" {
		path = defaultConfigPath()
	}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("failed to parse config file %s: %w", path, err)
		}
	case errors.Is(err, fs.ErrNotExist):
		// первый запуск (T-12): создать конфиг с дефолтами
		if os.Getenv("GLAMOR_CONFIG") == "" {
			if err := writeDefaultConfig(path, cfg); err != nil {
				slogWarnDefaultConfig(err)
			}
		}
	default:
		return Config{}, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	applyEnvOverrides(&cfg)

	if err := validator.New().Struct(cfg); err != nil {
		return Config{}, fmt.Errorf("failed to validate config: %w", err)
	}

	return cfg, nil
}

// applyEnvOverrides применяет переменные окружения GLAMOR_*.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("GLAMOR_HTTP_HOST"); v != "" {
		cfg.HTTP.Host = v
	}
	if v := os.Getenv("GLAMOR_HTTP_PORT"); v != "" {
		var port int
		if _, err := fmt.Sscanf(v, "%d", &port); err == nil {
			cfg.HTTP.Port = port
		}
	}
	if v := os.Getenv("GLAMOR_DB_PATH"); v != "" {
		cfg.DB.Path = v
	}
	if v := os.Getenv("GLAMOR_TOKEN"); v != "" {
		cfg.Security.Token = v
	}
	if v := os.Getenv("GLAMOR_LOG_LEVEL"); v != "" {
		cfg.Log.Level = strings.ToLower(v)
	}
	if v := os.Getenv("GLAMOR_TELEGRAM_TOKEN"); v != "" {
		cfg.Telegram.Token = v
	}
	if v := os.Getenv("GLAMOR_TELEGRAM_ENABLED"); v != "" {
		cfg.Telegram.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
}

func glamorHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".glamor"
	}
	return filepath.Join(home, ".glamor")
}

func defaultConfigPath() string {
	return filepath.Join(glamorHome(), "config.yaml")
}

func defaultDBPath() string {
	return filepath.Join(glamorHome(), "glamor.db")
}

// writeDefaultConfig записывает дефолтный конфиг при первом запуске (T-12).
func writeDefaultConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal default config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("failed to write default config %s: %w", path, err)
	}
	return nil
}

func slogWarnDefaultConfig(err error) {
	// логгер ещё не настроен — пишем в stderr напрямую
	fmt.Fprintf(os.Stderr, "warn: failed to write default config: %v\n", err)
}

// Package settings — настройки демона (экран настроек UI): чтение/запись
// ~/.glamor/config.yaml в рантайме + hot-apply хуки (telegram-адаптер,
// supervisor). Секреты наружу не отдаются (маскируются).
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"gopkg.in/yaml.v3"
)

// SupervisorSettings — ручки supervisor'а (T-12 конфиг, рантайм-правка).
type SupervisorSettings struct {
	StallTimeoutSec int   `json:"stall_timeout_sec"`
	StageTimeoutMin int   `json:"stage_timeout_min"`
	MaxParallel     int   `json:"max_parallel"`
	MaxAutoResumes  int64 `json:"max_auto_resumes"`
}

// TelegramSettings — настройки TG-бота (D-70..73).
type TelegramSettings struct {
	Enabled     bool   `json:"enabled"`
	HasToken    bool   `json:"has_token"`
	TokenMasked string `json:"token_masked,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
	token       string // никогда наружу
}

// Service — рантайм-настройки с персистом в config.yaml.
type Service struct {
	mu         sync.Mutex
	configPath string

	telegram   TelegramSettings
	supervisor SupervisorSettings

	onTelegramChange   func(token string, enabled bool)
	onSupervisorChange func(s SupervisorSettings)
}

// New читает текущий config.yaml (уже существующий к моменту старта, T-12).
func New(configPath string, current SupervisorSettings, tgToken string, tgEnabled bool) *Service {
	s := &Service{
		configPath: configPath,
		supervisor: current,
		telegram: TelegramSettings{
			Enabled: tgEnabled,
			token:   tgToken,
		},
	}
	s.telegram.HasToken = tgToken != ""
	s.telegram.TokenMasked = maskToken(tgToken)
	return s
}

// SetTelegramHook — hot-apply telegram-адаптера (main).
func (s *Service) SetTelegramHook(fn func(token string, enabled bool)) {
	s.onTelegramChange = fn
}

// SetSupervisorHook — hot-apply supervisor (main).
func (s *Service) SetSupervisorHook(fn func(s SupervisorSettings)) {
	s.onSupervisorChange = fn
}

// Get — текущие настройки (токен замаскирован).
func (s *Service) Get() (TelegramSettings, SupervisorSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.telegram, s.supervisor
}

// PutTelegram — обновление настроек TG: токен валидируется через getMe
// (пустой token = оставить текущий), hot-apply адаптера.
func (s *Service) PutTelegram(ctx context.Context, token string, enabled bool) (TelegramSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if token == "" {
		token = s.telegram.token
	}

	var botUsername string
	if token != "" {
		name, err := validateBotToken(ctx, token)
		if err != nil {
			return TelegramSettings{}, err
		}
		botUsername = name
	}
	if enabled && token == "" {
		return TelegramSettings{}, fmt.Errorf("telegram enabled without token: %w", cstmerrors.ErrValidation)
	}

	s.telegram = TelegramSettings{
		Enabled:     enabled,
		HasToken:    token != "",
		TokenMasked: maskToken(token),
		BotUsername: botUsername,
		token:       token,
	}

	if err := s.persistLocked(); err != nil {
		return TelegramSettings{}, err
	}
	if s.onTelegramChange != nil {
		s.onTelegramChange(token, enabled)
	}
	return s.telegram, nil
}

// PutSupervisor — обновление ручек supervisor'а (partial; нули = не менять).
func (s *Service) PutSupervisor(update SupervisorSettings) (SupervisorSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur := s.supervisor
	if update.StallTimeoutSec > 0 {
		cur.StallTimeoutSec = update.StallTimeoutSec
	}
	if update.StageTimeoutMin > 0 {
		cur.StageTimeoutMin = update.StageTimeoutMin
	}
	if update.MaxParallel > 0 {
		cur.MaxParallel = update.MaxParallel
	}
	if update.MaxAutoResumes > 0 {
		cur.MaxAutoResumes = update.MaxAutoResumes
	}
	s.supervisor = cur

	if err := s.persistLocked(); err != nil {
		return SupervisorSettings{}, err
	}
	if s.onSupervisorChange != nil {
		s.onSupervisorChange(cur)
	}
	return cur, nil
}

// persistLocked — запись настроек в config.yaml (read-modify-write,
// остальные ключи сохраняются).
func (s *Service) persistLocked() error {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	cfg["telegram"] = map[string]any{
		"token":   s.telegram.token,
		"enabled": s.telegram.Enabled,
	}

	// supervisor: merge в существующую секцию (runs_dir и пр. сохраняются)
	sup, _ := cfg["supervisor"].(map[string]any)
	if sup == nil {
		sup = map[string]any{}
	}
	sup["stall_timeout_sec"] = s.supervisor.StallTimeoutSec
	sup["stage_timeout_min"] = s.supervisor.StageTimeoutMin
	sup["max_parallel"] = s.supervisor.MaxParallel
	sup["max_auto_resumes"] = s.supervisor.MaxAutoResumes
	cfg["supervisor"] = sup

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	tmp := s.configPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	if err := os.Rename(tmp, s.configPath); err != nil {
		return fmt.Errorf("failed to replace config: %w", err)
	}
	return nil
}

// validateBotToken — getMe к Bot API: валидность токена + username бота.
func validateBotToken(ctx context.Context, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// токен только в URL запроса; в ошибки/логи не попадает
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.telegram.org/bot"+token+"/getMe", nil)
	if err != nil {
		return "", fmt.Errorf("failed to build getMe request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to reach telegram api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse getMe response: %w", err)
	}
	if !result.OK {
		return "", fmt.Errorf("telegram rejected the token: %w", cstmerrors.ErrValidation)
	}
	return result.Result.Username, nil
}

// maskToken — первые/последние 4 символа, остальное звёздочки.
func maskToken(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 8 {
		return strings.Repeat("*", len(token))
	}
	return token[:4] + strings.Repeat("*", 8) + token[len(token)-4:]
}

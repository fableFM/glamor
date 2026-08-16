package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Info — daemon.json (D-08): как клиентам (CLI, Tauri, UI) найти демона.
type Info struct {
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
	URL     string `json:"url"`
	Version string `json:"version"`
}

// WriteInfo атомарно записывает daemon.json (600).
func WriteInfo(path string, info Info) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal daemon info: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create daemon info dir: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("failed to write daemon info %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("failed to rename daemon info %s: %w", path, err)
	}
	return nil
}

// ReadInfo читает daemon.json.
func ReadInfo(path string) (Info, error) {
	var info Info
	data, err := os.ReadFile(path)
	if err != nil {
		return info, fmt.Errorf("failed to read daemon info %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, fmt.Errorf("failed to parse daemon info %s: %w", path, err)
	}
	return info, nil
}

// RemoveInfo удаляет daemon.json (при graceful shutdown).
func RemoveInfo(path string) {
	_ = os.Remove(path)
}

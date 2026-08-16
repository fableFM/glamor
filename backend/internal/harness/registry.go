package harness

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"sync"
)

// Registry — реестр адаптеров: имя harness'а → адаптер + путь бинаря.
// Конфиг демона может переопределить путь бинаря (harnesses: name: path).
type Registry struct {
	mu        sync.RWMutex
	adapters  map[string]Harness
	overrides map[string]string // name → путь бинаря из конфига
}

func NewRegistry(overrides map[string]string) *Registry {
	if overrides == nil {
		overrides = map[string]string{}
	}
	return &Registry{
		adapters:  map[string]Harness{},
		overrides: overrides,
	}
}

// Register добавляет адаптер (вызывается из main при сборке DI).
func (r *Registry) Register(h Harness) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[h.Name()] = h
}

// Get возвращает адаптер по имени.
func (r *Registry) Get(name string) (Harness, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.adapters[name]
	if !ok {
		return nil, fmt.Errorf("harness %q: %w", name, ErrUnknownHarness)
	}
	return h, nil
}

// BinaryPath — путь бинаря адаптера: override из конфига или BinaryName
// (резолв через exec.LookPath на стороне вызывающего).
func (r *Registry) BinaryPath(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if path, ok := r.overrides[name]; ok && path != "" {
		return path
	}
	if h, ok := r.adapters[name]; ok {
		return h.BinaryName()
	}
	return name
}

// BinaryStatus — доступность бинаря harness'а (для /version и логов старта).
type BinaryStatus struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Available bool   `json:"available"`
}

// CheckBinaries проверяет доступность бинарей через exec.LookPath
// (вызывается при старте демона).
func (r *Registry) CheckBinaries() []BinaryStatus {
	r.mu.RLock()
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	r.mu.RUnlock()
	sort.Strings(names)

	out := make([]BinaryStatus, 0, len(names))
	for _, name := range names {
		binary := r.BinaryPath(name)
		path, err := exec.LookPath(binary)
		out = append(out, BinaryStatus{
			Name:      name,
			Path:      path,
			Available: err == nil,
		})
	}
	return out
}

// ErrUnknownHarness — адаптер с таким именем не зарегистрирован.
var ErrUnknownHarness = errors.New("unknown harness")

// Package vendormemory — механика роста памяти (D-50, T-23): дельты как
// артефакты этапов, атомарное применение к файлам памяти, FTS5-индекс,
// промоушн в глобальную, git-версионирование глобальной памяти.
package vendormemory

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	artifactsrep "github.com/fableFM/glamor/internal/repository/artifacts"
	"github.com/fableFM/glamor/internal/repository/vendorindex"
)

// MemoryScope — политика записи в глобальную память (spec.memory_scope).
const (
	ScopeAuto = "auto" // локальная + глобальная (дефолт)
	ScopeGate = "gate" // только локальная; в глобальную — через промоушн (UI/API)
)

// Service — операции над vendor-памятью. Конструируется в main (D-80).
type Service struct {
	globalDir string // ~/.glamor/vendors (git-репозиторий)
	index     *vendorindex.Repository
	artifacts artifactsrep.RepositoryWithTX
}

func New(globalDir string, index *vendorindex.Repository, artifacts artifactsrep.RepositoryWithTX) *Service {
	return &Service{globalDir: globalDir, index: index, artifacts: artifacts}
}

// GlobalDir — каталог глобальной памяти.
func (s *Service) GlobalDir() string { return s.globalDir }

// LocalDir — локальная память проекта (<repo>/.glamor/vendors).
func LocalDir(projectPath string) string {
	return filepath.Join(projectPath, ".glamor", "vendors")
}

// AppliedDelta — применённая дельта памяти (для summary рана).
type AppliedDelta struct {
	Vendor string
	Scope  string // local | global | both
	Path   string
}

// ApplyDeltas применяет дельты этапа: файлы `<runDir>/vendor-updates/
// <vendor>.md` дописываются в файлы памяти (локальную всегда; глобальную
// при scope=auto). Запись атомарна (tmp+rename), глобальная память —
// git-репозиторий с коммитом на дельту (наша память, не проект
// пользователя — D-32 не применим, T-23). Регистрирует артефакты
// vendor_update и переиндексирует изменённые файлы.
func (s *Service) ApplyDeltas(ctx context.Context, runID string, stageID int64,
	runDir, projectPath, memoryScope string,
) ([]AppliedDelta, error) {
	updatesDir := filepath.Join(runDir, "vendor-updates")
	entries, err := os.ReadDir(updatesDir)
	if err != nil {
		return nil, nil // нет каталога дельт — этап ничего не узнал
	}

	var applied []AppliedDelta
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		vendor := strings.TrimSuffix(entry.Name(), ".md")
		data, err := os.ReadFile(filepath.Join(updatesDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("failed to read vendor delta %s: %w", entry.Name(), err)
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			continue
		}

		// локальная — всегда
		localPath := filepath.Join(LocalDir(projectPath), entry.Name())
		if err := appendAtomic(localPath, data); err != nil {
			return nil, fmt.Errorf("failed to apply delta to local memory: %w", err)
		}
		delta := AppliedDelta{Vendor: vendor, Scope: "local", Path: localPath}

		// глобальная — по политике
		if memoryScope != ScopeGate {
			globalPath := filepath.Join(s.globalDir, entry.Name())
			if err := appendAtomic(globalPath, data); err != nil {
				return nil, fmt.Errorf("failed to apply delta to global memory: %w", err)
			}
			if err := s.gitCommit(globalPath, fmt.Sprintf("delta %s (run %s)", vendor, runID)); err != nil {
				return nil, err
			}
			delta.Scope = "both"
			if err := s.reindexFile(ctx, globalPath); err != nil {
				return nil, err
			}
		}
		if err := s.reindexFile(ctx, localPath); err != nil {
			return nil, err
		}

		if _, err := s.artifacts.CreateArtifact(ctx, dtorep.CreateArtifactRequest{
			RunID:   runID,
			StageID: &stageID,
			Path:    filepath.Join(updatesDir, entry.Name()),
			Kind:    "vendor_update",
		}); err != nil {
			return nil, fmt.Errorf("failed to register vendor_update artifact: %w", err)
		}

		applied = append(applied, delta)
	}
	return applied, nil
}

// appendAtomic дописывает содержимое в файл атомарно (tmp+rename).
func appendAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var buf strings.Builder
	buf.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		buf.WriteString("\n")
	}
	fmt.Fprintf(&buf, "\n<!-- delta %s -->\n", time.Now().UTC().Format("2006-01-02"))
	buf.Write(data)

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(buf.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// gitCommit — коммит дельты в репозиторий памяти (T-23 рекомендация:
// глобальная память — git-репо для аудита/отката).
func (s *Service) gitCommit(path, message string) error {
	if err := s.ensureGitRepo(); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"add", filepath.Base(path)},
		{"commit", "-m", message, "--allow-empty-message"},
	} {
		cmd := exec.Command("git", append([]string{"-C", s.globalDir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			// «nothing to commit» — не ошибка
			if strings.Contains(string(out), "nothing to commit") {
				return nil
			}
			return fmt.Errorf("git %s in memory repo: %w (%s)", args[0], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// ensureGitRepo инициализирует git-репозиторий глобальной памяти (T-23).
func (s *Service) ensureGitRepo() error {
	if _, err := os.Stat(filepath.Join(s.globalDir, ".git")); err == nil {
		return nil
	}
	if err := os.MkdirAll(s.globalDir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("git", "-C", s.globalDir, "init", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to init memory git repo: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// reindexFile обновляет файл в FTS5-индексе, если содержимое изменилось
// (dirty-check по содержимому, T-23).
func (s *Service) reindexFile(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	indexed, ok, err := s.index.IndexedContent(ctx, path)
	if err != nil {
		return err
	}
	if ok && indexed == string(data) {
		return nil // без изменений
	}
	return s.index.Upsert(ctx, path, string(data))
}

// ReindexAll — переиндексация при старте демона: глобальная память +
// локальные всех проектов (dirty-check по содержимому).
func (s *Service) ReindexAll(ctx context.Context, projectPaths []string) error {
	dirs := []string{s.globalDir}
	for _, p := range projectPaths {
		dirs = append(dirs, LocalDir(p))
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // каталога нет — памяти нет
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			if err := s.reindexFile(ctx, filepath.Join(dir, entry.Name())); err != nil {
				return fmt.Errorf("failed to reindex %s: %w", entry.Name(), err)
			}
		}
	}
	return nil
}

// ReindexDirs — переиндексация произвольных каталогов памяти
// (уроки T-29: ~/.glamor/lessons + <repo>/.glamor/lessons).
func (s *Service) ReindexDirs(ctx context.Context, dirs []string) error {
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			if err := s.reindexFile(ctx, filepath.Join(dir, entry.Name())); err != nil {
				return fmt.Errorf("failed to reindex %s: %w", entry.Name(), err)
			}
		}
	}
	return nil
}

// Search — FTS5-выдержки для промпта планировщика (T-23).
func (s *Service) Search(ctx context.Context, query string, limit int) ([]dtorep.VendorSearchHit, error) {
	return s.index.Search(ctx, query, limit)
}

// PromoteToGlobal — промоушн записи локальной памяти в глобальную
// (UI-кнопка «в глобальную», T-23).
func (s *Service) PromoteToGlobal(ctx context.Context, projectPath, vendor string) (string, error) {
	localPath := filepath.Join(LocalDir(projectPath), vendor+".md")
	data, err := os.ReadFile(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to read local memory %s: %w", localPath, err)
	}

	globalPath := filepath.Join(s.globalDir, vendor+".md")
	if err := appendAtomic(globalPath, data); err != nil {
		return "", err
	}
	if err := s.gitCommit(globalPath, fmt.Sprintf("promote %s from %s", vendor, filepath.Base(projectPath))); err != nil {
		return "", err
	}
	if err := s.reindexFile(ctx, globalPath); err != nil {
		return "", err
	}
	return globalPath, nil
}

// --- UI/API операции (T-23) --------------------------------------------------

// MemoryFile — файл памяти в дереве UI.
type MemoryFile struct {
	Path      string    `json:"path"` // абсолютный путь
	Vendor    string    `json:"vendor"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}

var vendorNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidVendorName — защита от path traversal (vendor = имя файла).
func ValidVendorName(vendor string) bool {
	return vendorNameRe.MatchString(vendor)
}

// TreeGlobal — глобальные файлы памяти.
func (s *Service) TreeGlobal() ([]MemoryFile, error) {
	return listMemoryDir(s.globalDir)
}

// TreeProject — файлы памяти проекта.
func (s *Service) TreeProject(projectPath string) ([]MemoryFile, error) {
	return listMemoryDir(LocalDir(projectPath))
}

func listMemoryDir(dir string) ([]MemoryFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // каталога нет — пусто
	}
	var out []MemoryFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, MemoryFile{
			Path:      filepath.Join(dir, entry.Name()),
			Vendor:    strings.TrimSuffix(entry.Name(), ".md"),
			Size:      info.Size(),
			UpdatedAt: info.ModTime(),
		})
	}
	return out, nil
}

// ReadFile — содержимое файла памяти (scope global|project).
func (s *Service) ReadFile(scope, projectPath, vendor string) (string, error) {
	if !ValidVendorName(vendor) {
		return "", fmt.Errorf("invalid vendor name %q", vendor)
	}
	path := s.globalDir
	if scope == "project" {
		path = LocalDir(projectPath)
	}
	data, err := os.ReadFile(filepath.Join(path, vendor+".md"))
	if err != nil {
		return "", fmt.Errorf("failed to read memory file: %w", err)
	}
	return string(data), nil
}

// WriteFile — ручная правка из UI (textarea + сохранить): атомарно,
// глобальная — с git-коммитом, переиндексация.
func (s *Service) WriteFile(ctx context.Context, scope, projectPath, vendor, content string) error {
	if !ValidVendorName(vendor) {
		return fmt.Errorf("invalid vendor name %q", vendor)
	}
	dir := s.globalDir
	if scope == "project" {
		dir = LocalDir(projectPath)
	}
	path := filepath.Join(dir, vendor+".md")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if scope != "project" {
		if err := s.gitCommit(path, fmt.Sprintf("manual edit %s", vendor)); err != nil {
			return err
		}
	}
	return s.reindexFile(ctx, path)
}

// HistoryEntry — запись истории памяти (git log глобального репо).
type HistoryEntry struct {
	Hash    string `json:"hash"`
	Date    string `json:"date"`
	Message string `json:"message"`
}

// History — история изменений файла глобальной памяти (git log).
// Локальная память не версионируется — пустой список.
func (s *Service) History(vendor string) ([]HistoryEntry, error) {
	if !ValidVendorName(vendor) {
		return nil, fmt.Errorf("invalid vendor name %q", vendor)
	}
	if _, err := os.Stat(filepath.Join(s.globalDir, ".git")); err != nil {
		return nil, nil
	}

	cmd := exec.Command("git", "-C", s.globalDir, "log",
		"--format=%H%x09%ad%x09%s", "--date=short", "--", vendor+".md")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git log memory repo: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	var entries []HistoryEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) == 3 {
			entries = append(entries, HistoryEntry{Hash: parts[0], Date: parts[1], Message: parts[2]})
		}
	}
	return entries, nil
}

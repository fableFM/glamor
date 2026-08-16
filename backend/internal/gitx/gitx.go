// Package gitx — git-контур glamor (D-30..34) через git CLI (не go-git:
// нужна полная совместимость с конфигами/hooks пользователя). Ран работает
// в основном чекауте; пайплайн НИЧЕГО не коммитит и не пушит (D-32).
package gitx

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const componentName = "gitx"

// gitTimeout — таймаут одной git-команды.
const gitTimeout = 30 * time.Second

// runGit выполняет git -C <path> <args...> с таймаутом.
func runGit(ctx context.Context, path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	fullArgs := append([]string{"-C", path}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		return outBuf.String(),
			fmt.Errorf("git %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return outBuf.String(), nil
}

// IsRepo проверяет, что path — валидный git-репозиторий.
func IsRepo(ctx context.Context, path string) bool {
	_, err := runGit(ctx, path, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// RefExists проверяет существование ref (ветка/тег/коммит).
func RefExists(ctx context.Context, path, ref string) bool {
	_, err := runGit(ctx, path, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// BranchExists проверяет существование локальной ветки.
func BranchExists(ctx context.Context, path, branch string) bool {
	return RefExists(ctx, path, "refs/heads/"+branch)
}

// CurrentBranch — текущая ветка чекаута ("" для detached HEAD).
func CurrentBranch(ctx context.Context, path string) (string, error) {
	out, err := runGit(ctx, path, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// DirtyFiles — список изменённых/untracked файлов чекаута (пустой = чисто).
func DirtyFiles(ctx context.Context, path string) ([]string, error) {
	out, err := runGit(ctx, path, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// формат: "XY path" (первые 2 символа — статус)
		if len(line) > 3 {
			files = append(files, strings.TrimSpace(line[2:]))
		} else {
			files = append(files, line)
		}
	}
	return files, nil
}

// Preflight — проверки перед стартом рана (D-34): репозиторий валиден,
// base_branch существует, чекаут чист (или force). Lock ветки — в БД
// (частичный UNIQUE-индекс, T-05).
func Preflight(ctx context.Context, projectPath, baseBranch, branch string, force bool) error {
	if !IsRepo(ctx, projectPath) {
		return fmt.Errorf("%s is not a git repository: %w", projectPath, ErrNotARepo)
	}
	if !RefExists(ctx, projectPath, baseBranch) {
		return fmt.Errorf("base branch %q does not exist: %w", baseBranch, ErrBaseBranchMissing)
	}
	if !force {
		dirty, err := DirtyFiles(ctx, projectPath)
		if err != nil {
			return err
		}
		if len(dirty) > 0 {
			return &DirtyCheckoutError{Files: dirty}
		}
	}
	return nil
}

// SuggestBranch — имя рабочей ветки glamor/<slug>; при коллизии с
// существующей веткой — суффикс -2, -3, ... (D-31).
func SuggestBranch(ctx context.Context, projectPath, slug string) (string, error) {
	branch := "glamor/" + slug
	if !BranchExists(ctx, projectPath, branch) {
		return branch, nil
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", branch, i)
		if !BranchExists(ctx, projectPath, candidate) {
			return candidate, nil
		}
	}
}

// PrepareBranch: ветка существует → checkout; нет → checkout -b от base (D-31).
func PrepareBranch(ctx context.Context, projectPath, baseBranch, branch string) error {
	if BranchExists(ctx, projectPath, branch) {
		if _, err := runGit(ctx, projectPath, "checkout", branch); err != nil {
			return fmt.Errorf("failed to checkout existing branch %s: %w", branch, err)
		}
		return nil
	}
	if _, err := runGit(ctx, projectPath, "checkout", "-b", branch, baseBranch); err != nil {
		return fmt.Errorf("failed to create branch %s from %s: %w", branch, baseBranch, err)
	}
	return nil
}

// CheckBranchMismatch — sanity-check перед этапом (T-10): пользователь мог
// переключить ветку руками → этап не стартует (защита от потери работы).
func CheckBranchMismatch(ctx context.Context, projectPath, wantBranch string) error {
	current, err := CurrentBranch(ctx, projectPath)
	if err != nil {
		return err
	}
	if current != wantBranch {
		return fmt.Errorf("checkout is on %q, run expects %q: %w",
			current, wantBranch, ErrBranchMismatch)
	}
	return nil
}

// DiffStat — статистика изменений рана для UI/summary (включая untracked).
type DiffStat struct {
	FilesChanged int
	Insertions   int
	Deletions    int
	Untracked    []string
}

// DiffStat считает diff рабочего дерева против base_branch
// (`git diff --stat` + untracked из status).
func GetDiffStat(ctx context.Context, projectPath, baseBranch string) (DiffStat, error) {
	var stat DiffStat

	out, err := runGit(ctx, projectPath, "diff", "--numstat", baseBranch)
	if err != nil {
		return stat, fmt.Errorf("failed to get diff numstat: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		var plus, minus int
		// бинарные файлы: "-\t-\tpath"
		_, _ = fmt.Sscanf(parts[0], "%d", &plus)
		_, _ = fmt.Sscanf(parts[1], "%d", &minus)
		stat.Insertions += plus
		stat.Deletions += minus
		stat.FilesChanged++
	}

	out, err = runGit(ctx, projectPath, "status", "--porcelain")
	if err != nil {
		return stat, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "?? ") {
			stat.Untracked = append(stat.Untracked, strings.TrimSpace(line[3:]))
		}
	}

	return stat, nil
}

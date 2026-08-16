package gitx_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/gitx"
)

// initRepo создаёт tmp-репозиторий с одним коммитом на main.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	git("init", "-b", "main")
	git("config", "user.email", "test@glamor.local")
	git("config", "user.name", "glamor test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0o644))
	git("add", ".")
	git("commit", "-m", "init")

	return dir
}

func TestPreflight_CleanAndDirty(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)

	// чистый чекаут — ок
	require.NoError(t, gitx.Preflight(ctx, dir, "main", "glamor/test", false))

	// грязный чекаут — ошибка со списком файлов
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0o644))
	err := gitx.Preflight(ctx, dir, "main", "glamor/test", false)
	require.Error(t, err)
	dirty, ok := gitx.IsDirtyCheckout(err)
	require.True(t, ok, "must be DirtyCheckoutError, got %v", err)
	assert.Contains(t, dirty.Files, "dirty.txt")

	// force — пропускаем грязный чекаут
	require.NoError(t, gitx.Preflight(ctx, dir, "main", "glamor/test", true))

	// несуществующая base — ошибка
	err = gitx.Preflight(ctx, dir, "no-such-branch", "glamor/test", true)
	require.ErrorIs(t, err, gitx.ErrBaseBranchMissing)

	// не репозиторий — ошибка
	err = gitx.Preflight(ctx, t.TempDir(), "main", "glamor/test", false)
	require.ErrorIs(t, err, gitx.ErrNotARepo)
}

func TestSuggestBranch_Collisions(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)

	branch, err := gitx.SuggestBranch(ctx, dir, "fix-parser")
	require.NoError(t, err)
	assert.Equal(t, "glamor/fix-parser", branch)

	// создаём ветку — следующее предложение с суффиксом -2
	require.NoError(t, gitx.PrepareBranch(ctx, dir, "main", branch))
	branch2, err := gitx.SuggestBranch(ctx, dir, "fix-parser")
	require.NoError(t, err)
	assert.Equal(t, "glamor/fix-parser-2", branch2)

	// и -3
	require.NoError(t, gitx.PrepareBranch(ctx, dir, "main", branch2))
	branch3, err := gitx.SuggestBranch(ctx, dir, "fix-parser")
	require.NoError(t, err)
	assert.Equal(t, "glamor/fix-parser-3", branch3)
}

func TestPrepareBranch_AndMismatch(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)

	// новая ветка от base
	require.NoError(t, gitx.PrepareBranch(ctx, dir, "main", "glamor/run-1"))
	current, err := gitx.CurrentBranch(ctx, dir)
	require.NoError(t, err)
	assert.Equal(t, "glamor/run-1", current)
	require.NoError(t, gitx.CheckBranchMismatch(ctx, dir, "glamor/run-1"))

	// существующая ветка — просто checkout
	require.NoError(t, gitx.PrepareBranch(ctx, dir, "main", "main"))
	require.NoError(t, gitx.PrepareBranch(ctx, dir, "main", "glamor/run-1"))

	// mismatch детектится
	require.NoError(t, gitx.PrepareBranch(ctx, dir, "main", "main"))
	err = gitx.CheckBranchMismatch(ctx, dir, "glamor/run-1")
	require.ErrorIs(t, err, gitx.ErrBranchMismatch)
}

func TestDiffStat(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)

	require.NoError(t, gitx.PrepareBranch(ctx, dir, "main", "glamor/diff"))

	// изменение отслеживаемого файла + untracked
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\nmore lines\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0o644))

	stat, err := gitx.GetDiffStat(ctx, dir, "main")
	require.NoError(t, err)
	assert.Equal(t, 1, stat.FilesChanged)
	// "init" (без перевода строки) → "init\nmore lines\n" = +2/-1
	assert.Equal(t, 2, stat.Insertions)
	assert.Equal(t, 1, stat.Deletions)
	assert.Equal(t, []string{"new.txt"}, stat.Untracked)
}

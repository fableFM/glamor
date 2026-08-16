package daemon_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/daemon"
)

func TestLock_SingleInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "glamord.lock")

	lock, err := daemon.AcquireLock(path)
	require.NoError(t, err)

	// второй инстанс — отказ
	_, err = daemon.AcquireLock(path)
	require.ErrorIs(t, err, daemon.ErrAlreadyRunning)

	// после release — можно снова
	lock.Release()
	lock2, err := daemon.AcquireLock(path)
	require.NoError(t, err)
	lock2.Release()
}

func TestLock_StaleLockRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "glamord.lock")

	// lock мёртвого процесса (pid гарантированно не существует)
	deadPID := 99999999
	require.NoError(t, os.WriteFile(path, []byte(strconv.Itoa(deadPID)), 0o600))

	lock, err := daemon.AcquireLock(path)
	require.NoError(t, err, "stale lock must be removed")
	defer lock.Release()
}

func TestToken_GenerateAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")

	token, err := daemon.LoadOrCreateToken(path)
	require.NoError(t, err)
	assert.Len(t, token, 64) // 32 байта hex

	// perms 600 (D-08)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// повторная загрузка — тот же токен
	token2, err := daemon.LoadOrCreateToken(path)
	require.NoError(t, err)
	assert.Equal(t, token, token2)
}

func TestRotateWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "glamord.log")

	w, err := daemon.NewRotateWriter(path, 100)
	require.NoError(t, err)

	_, err = w.Write([]byte(strings.Repeat("a", 120)))
	require.NoError(t, err)
	_, err = w.Write([]byte(strings.Repeat("b", 10)))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	// ротация сработала: бэкап .1 существует
	_, err = os.Stat(path + ".1")
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "bbb")
}

func TestInfo_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.json")

	require.NoError(t, daemon.WriteInfo(path, daemon.Info{
		PID: 123, Port: 7380, URL: "http://127.0.0.1:7380", Version: "test",
	}))

	info, err := daemon.ReadInfo(path)
	require.NoError(t, err)
	assert.Equal(t, 123, info.PID)
	assert.Equal(t, 7380, info.Port)

	daemon.RemoveInfo(path)
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

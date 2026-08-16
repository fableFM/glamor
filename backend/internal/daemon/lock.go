// Package daemon — жизненный цикл glamord (T-12): lock-файл единственного
// инстанса, токен localhost-API (D-08), daemon.json для клиентов,
// ротация лог-файла.
package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrAlreadyRunning — второй инстанс демона (lock-файл живого процесса).
var ErrAlreadyRunning = errors.New("glamord already running")

// Lock — pid-lock единственного инстанса (T-12).
type Lock struct {
	path string
}

// AcquireLock захватывает lock-файл. Если lock занят ЖИВЫМ процессом —
// ErrAlreadyRunning (с pid в сообщении); протухший lock (процесс мёртв)
// удаляется. Release снимает lock (вызывать при выходе).
func AcquireLock(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 && processAlive(pid) {
			return nil, fmt.Errorf("pid %d: %w", pid, ErrAlreadyRunning)
		}
		// протухший lock — демон умер без cleanup
		_ = os.Remove(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("failed to read lock file %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create lock dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return nil, fmt.Errorf("failed to write lock file %s: %w", path, err)
	}
	return &Lock{path: path}, nil
}

// Release снимает lock (идемпотентно).
func (l *Lock) Release() {
	_ = os.Remove(l.path)
}

// LockInfo — что известно о работающем инстансе (для сообщения «уже запущен»).
type LockInfo struct {
	PID  int
	Port int // из daemon.json, 0 если неизвестен
}

// InspectLock — информация о текущем владельце lock-файла.
func InspectLock(lockPath, daemonJSONPath string) LockInfo {
	var info LockInfo
	if data, err := os.ReadFile(lockPath); err == nil {
		info.PID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	if di, err := ReadInfo(daemonJSONPath); err == nil {
		info.Port = di.Port
	}
	return info
}

func processAlive(pid int) bool {
	// сигнал 0 — проверка существования без побочных эффектов
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RotateWriter — простая ротация лог-файла по размеру (T-12):
// при превышении maxBytes файл переименовывается в <name>.1 (один бэкап)
// и начинается заново.
type RotateWriter struct {
	path     string
	maxBytes int64

	mu   sync.Mutex
	file *os.File
	size int64
}

func NewRotateWriter(path string, maxBytes int64) (*RotateWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create log dir: %w", err)
	}
	w := &RotateWriter{path: path, maxBytes: maxBytes}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotateWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %w", w.path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to stat log file %s: %w", w.path, err)
	}
	w.file = f
	w.size = info.Size()
	return nil
}

func (w *RotateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.size+int64(len(p)) > w.maxBytes {
		_ = w.file.Close()
		_ = os.Remove(w.path + ".1")
		_ = os.Rename(w.path, w.path+".1")
		if err := w.open(); err != nil {
			return 0, err
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotateWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

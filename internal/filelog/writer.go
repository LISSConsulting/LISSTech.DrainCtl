//go:build windows

package filelog

import (
	"fmt"
	"os"
	"sync"
)

// Writer is a thread-safe, size-rotating file writer.
type Writer struct {
	mu       sync.Mutex
	path     string
	base     string // path without .log extension
	maxBytes int64
	keep     int
	file     *os.File
	size     int64
}

// New creates a Writer that rotates at maxBytes, keeping keep old files.
func New(path string, maxBytes int64, keep int) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("filelog: open %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("filelog: stat %s: %w", path, err)
	}
	base := path
	if len(path) > 4 && path[len(path)-4:] == ".log" {
		base = path[:len(path)-4]
	}
	return &Writer{
		path: path, base: base, maxBytes: maxBytes, keep: keep,
		file: f, size: info.Size(),
	}, nil
}

// Write implements io.Writer. Safe for concurrent use.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, fmt.Errorf("filelog: closed")
	}
	if w.size+int64(len(p)) > w.maxBytes && w.size > 0 {
		if err := w.rotate(); err != nil {
			return 0, fmt.Errorf("filelog: rotate: %w", err)
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the underlying file. Safe to call multiple times.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *Writer) rotate() error {
	_ = w.file.Close()
	oldest := fmt.Sprintf("%s.%d.log", w.base, w.keep)
	_ = os.Remove(oldest)
	for i := w.keep - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d.log", w.base, i)
		dst := fmt.Sprintf("%s.%d.log", w.base, i+1)
		_ = os.Rename(src, dst)
	}
	_ = os.Rename(w.path, fmt.Sprintf("%s.1.log", w.base))
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	w.file = f
	w.size = 0
	return nil
}

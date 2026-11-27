// Package dailylog writes logs to a date-stamped file and switches to a new
// file at local midnight.
package dailylog

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Writer is an io.WriteCloser that appends to <dir>/<prefix>-YYYY-MM-DD.log.
// Write failures (for example a read-only install directory) fall back to
// stderr so logging never breaks the caller.
type Writer struct {
	dir    string
	prefix string
	now    func() time.Time

	mu  sync.Mutex
	day string
	f   *os.File
}

// New returns a writer that appends to dir/prefix-YYYY-MM-DD.log.
func New(dir, prefix string) *Writer {
	return &Writer{dir: dir, prefix: prefix, now: time.Now}
}

// Ensure opens today's file and is useful to detect configuration problems
// before the first log line is written.
func (w *Writer) Ensure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ensureLocked(w.now())
}

// Current returns the path of the file Write would use right now.
func (w *Writer) Current() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		return w.f.Name()
	}
	return w.pathLocked(w.now())
}

// Write appends p to the current day's file, rotating at midnight.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureLocked(w.now()); err != nil {
		return os.Stderr.Write(p)
	}
	return w.f.Write(p)
}

// Close closes the current file, if any.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

func (w *Writer) pathLocked(t time.Time) string {
	return filepath.Join(w.dir, w.prefix+"-"+t.Format("2006-01-02")+".log")
}

func (w *Writer) ensureLocked(t time.Time) error {
	day := t.Format("2006-01-02")
	if w.f != nil && w.day == day {
		return nil
	}
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(w.pathLocked(t), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.f, w.day = f, day
	return nil
}

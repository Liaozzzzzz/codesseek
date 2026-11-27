package dailylog

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriterSwitchesFileAtMidnight(t *testing.T) {
	dir := t.TempDir()
	clock := time.Date(2026, 11, 27, 23, 59, 0, 0, time.Local)

	w := New(dir, "mcp")
	w.now = func() time.Time { return clock }
	if err := w.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("day one\n")); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(2 * time.Minute) // 00:01 next day
	if _, err := w.Write([]byte("day two\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}
	if got := read("mcp-2026-11-27.log"); got != "day one\n" {
		t.Fatalf("first file = %q", got)
	}
	if got := read("mcp-2026-11-28.log"); got != "day two\n" {
		t.Fatalf("second file = %q", got)
	}
}

func TestWriterAppendsWithinSameDay(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "mcp")
	if _, err := w.Write([]byte("a\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("b\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("files = %d, want 1", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "a\nb\n" {
		t.Fatalf("content = %q", got)
	}
	if !strings.HasPrefix(entries[0].Name(), "mcp-") {
		t.Fatalf("unexpected file name %q", entries[0].Name())
	}
}

func TestWriterFallsBackToStderr(t *testing.T) {
	// Use a regular file as the log directory so MkdirAll cannot succeed.
	base := t.TempDir()
	blocker := filepath.Join(base, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := New(filepath.Join(blocker, "logs"), "mcp")
	if err := w.Ensure(); err == nil {
		t.Fatal("Ensure succeeded on an unusable directory")
	}

	// Redirect stderr so the fallback line does not pollute test output.
	orig := os.Stderr
	r, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = pipeW
	defer func() { os.Stderr = orig; _ = r.Close() }()

	if _, err := w.Write([]byte("still visible\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if err := pipeW.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stderr = orig
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "still visible\n" {
		t.Fatalf("stderr = %q", got)
	}
}

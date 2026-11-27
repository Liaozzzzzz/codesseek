package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeLoggerDefaultsToDailyFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODESEEK_HOME", home)

	logger, closeLog, desc, err := serveLogger("")
	if err != nil {
		t.Fatal(err)
	}
	logger.Print("hello daily")
	closeLog()

	want := filepath.Join(home, "logs", "mcp-"+time.Now().Format("2006-01-02")+".log")
	if desc != want {
		t.Fatalf("log path = %q, want %q", desc, want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello daily") {
		t.Fatalf("log content = %q", data)
	}
}

func TestServeLoggerHonoursExplicitFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODESEEK_HOME", home)
	path := filepath.Join(t.TempDir(), "nested", "custom.log")

	logger, closeLog, desc, err := serveLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	logger.Print("explicit file")
	closeLog()

	if desc != path {
		t.Fatalf("log path = %q, want %q", desc, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "explicit file") {
		t.Fatalf("log content = %q", data)
	}
	// The default daily file must not be created when --log is given.
	entries, err := os.ReadDir(filepath.Join(home, "logs"))
	if err == nil && len(entries) != 0 {
		t.Fatalf("default log dir was used: %v", entries)
	}
}

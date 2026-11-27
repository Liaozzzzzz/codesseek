package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadModelWritesAllFiles(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("content for " + r.URL.Path))
	}))
	defer srv.Close()

	dest := t.TempDir()
	var got []string
	err := downloadModel(context.Background(), srv.URL, dest, false, func(name string, size int64) {
		got = append(got, name)
	})
	if err != nil {
		t.Fatal(err)
	}
	if hits != len(modelFiles) || len(got) != len(modelFiles) {
		t.Fatalf("hits=%d progress=%v, want %d", hits, got, len(modelFiles))
	}
	for _, remote := range modelFiles {
		local := filepath.Join(dest, filepath.FromSlash(filepath.Base(remote)))
		data, err := os.ReadFile(local)
		if err != nil {
			t.Fatalf("missing %s: %v", local, err)
		}
		if len(data) == 0 {
			t.Fatalf("%s is empty", local)
		}
	}
}

func TestDownloadModelSkipsExistingFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request for %s", r.URL.Path)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dest := t.TempDir()
	for _, remote := range modelFiles {
		if err := os.WriteFile(filepath.Join(dest, filepath.FromSlash(filepath.Base(remote))), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := downloadModel(context.Background(), srv.URL, dest, false, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadModelForceRefetches(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("fresh"))
	}))
	defer srv.Close()

	dest := t.TempDir()
	for _, remote := range modelFiles {
		if err := os.WriteFile(filepath.Join(dest, filepath.FromSlash(filepath.Base(remote))), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := downloadModel(context.Background(), srv.URL, dest, true, nil); err != nil {
		t.Fatal(err)
	}
	if hits != len(modelFiles) {
		t.Fatalf("hits = %d, want %d", hits, len(modelFiles))
	}
	data, _ := os.ReadFile(filepath.Join(dest, "config.json"))
	if string(data) != "fresh" {
		t.Fatalf("config.json = %q", data)
	}
}

func TestDownloadModelReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer srv.Close()
	if err := downloadModel(context.Background(), srv.URL, t.TempDir(), false, nil); err == nil {
		t.Fatal("want an error for HTTP 404")
	}
}

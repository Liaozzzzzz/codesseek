package main

import (
	"errors"
	"strings"
	"testing"

	"codesseek/internal/config"
	"codesseek/internal/embedder"
	"codesseek/internal/store"
)

func fakeBuild(calls *int, inner embedder.Embedder) func() (embedder.Embedder, func(), error) {
	return func() (embedder.Embedder, func(), error) {
		*calls++
		return inner, func() { *calls += 100 }, nil
	}
}

func TestLazyEmbedderDefersBuild(t *testing.T) {
	calls := 0
	lazy := newLazyEmbedder("fake-hash-8", 8, fakeBuild(&calls, embedder.NewFake(8)))
	if calls != 0 {
		t.Fatal("lazy embedder loaded the model too early")
	}
	if lazy.Model() != "fake-hash-8" || lazy.Dim() != 8 {
		t.Fatalf("lazy metadata = %s/%d", lazy.Model(), lazy.Dim())
	}
	if calls != 0 {
		t.Fatal("Model/Dim must not load the model")
	}
	if _, err := lazy.EmbedDocuments([]string{"login"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("build calls = %d, want 1", calls)
	}
	if _, err := lazy.EmbedQuery("login"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("build calls = %d, want still 1", calls)
	}
	if err := lazy.Close(); err != nil {
		t.Fatal(err)
	}
	if calls != 101 {
		t.Fatalf("close did not release the embedder: calls = %d", calls)
	}
}

func TestLazyEmbedderPropagatesBuildError(t *testing.T) {
	want := errors.New("no model")
	lazy := newLazyEmbedder("m", 4, func() (embedder.Embedder, func(), error) {
		return nil, nil, want
	})
	if _, err := lazy.EmbedDocuments([]string{"x"}); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestLazyEmbedderRejectsDimMismatch(t *testing.T) {
	lazy := newLazyEmbedder("fake-hash-8", 8, fakeBuild(new(int), embedder.NewFake(4)))
	if _, err := lazy.EmbedDocuments([]string{"x"}); err == nil {
		t.Fatal("want an error when the loaded dim differs from the index")
	}
}

func TestIndexEmbedderIsLazyForMatchingIndex(t *testing.T) {
	dataDir := t.TempDir()
	root := indexedRepo(t, dataDir)
	st, err := store.New(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Model = "fake-hash-8"
	emb, closer, err := indexEmbedder(cfg, st, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	lazy, ok := emb.(*lazyEmbedder)
	if !ok {
		t.Fatalf("embedder = %T, want *lazyEmbedder", emb)
	}
	if lazy.Dim() != 8 || lazy.Model() != "fake-hash-8" {
		t.Fatalf("lazy metadata = %s/%d", lazy.Model(), lazy.Dim())
	}
	if _, err := emb.EmbedDocuments([]string{"login"}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexEmbedderRejectsModelChangeWithoutFull(t *testing.T) {
	dataDir := t.TempDir()
	root := indexedRepo(t, dataDir)
	st, err := store.New(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Model = "fake-hash-4"
	_, _, err = indexEmbedder(cfg, st, false)
	if err == nil {
		t.Fatal("want an error when the model changes without --full")
	}
	if !strings.Contains(err.Error(), "--full") {
		t.Fatalf("error = %v, want a --full hint", err)
	}
}

func TestIndexEmbedderAllowsModelSwitchWithFull(t *testing.T) {
	dataDir := t.TempDir()
	root := indexedRepo(t, dataDir)
	st, err := store.New(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Model = "fake-hash-4"
	emb, closer, err := indexEmbedder(cfg, st, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	if emb.Model() != "fake-hash-4" || emb.Dim() != 4 {
		t.Fatalf("embedder = %s/%d, want fake-hash-4/4", emb.Model(), emb.Dim())
	}
}

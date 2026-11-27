package main

import (
	"strings"
	"testing"

	"codesseek/internal/config"
	"codesseek/internal/store"
)

func TestEmbedderForMetaRejectsModelMismatch(t *testing.T) {
	cfg := config.Defaults()
	meta := &store.Meta{Model: "someone/other-model", Dim: 512}
	_, _, err := embedderForMeta(cfg, meta)
	if err == nil {
		t.Fatal("want an error when the index model differs from the configured model")
	}
	if !strings.Contains(err.Error(), "other-model") || !strings.Contains(err.Error(), "--full") {
		t.Fatalf("error = %v, want the mismatched model and --full hint", err)
	}
}

func TestEmbedderForMetaMatchesFakeIndex(t *testing.T) {
	cfg := config.Defaults()
	cfg.Model = "fake-hash-8"
	meta := &store.Meta{Model: "fake-hash-8", Dim: 8}
	emb, closer, err := embedderForMeta(cfg, meta)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	if emb.Model() != "fake-hash-8" || emb.Dim() != 8 {
		t.Fatalf("embedder = %s/%d", emb.Model(), emb.Dim())
	}
}

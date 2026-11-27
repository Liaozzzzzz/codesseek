package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMergePriorityGlobalThenProject(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.json")
	writeFile(t, global, `{"modelDir":"G:/models","chunkSize":500,"nResult":5}`)
	projectDir := filepath.Join(dir, "proj")
	project := filepath.Join(projectDir, ".codesseek", "config.json")
	writeFile(t, project, `{"dataDir":"P:/data","chunkSize":700}`)

	cfg := Defaults()
	if err := cfg.MergeFile(global); err != nil {
		t.Fatal(err)
	}
	if err := cfg.MergeFile(project); err != nil {
		t.Fatal(err)
	}
	if cfg.ModelDir != "G:/models" {
		t.Fatalf("modelDir = %q, want G:/models", cfg.ModelDir)
	}
	if cfg.DataDir != "P:/data" {
		t.Fatalf("dataDir = %q, want P:/data", cfg.DataDir)
	}
	if cfg.ChunkSize != 700 {
		t.Fatalf("chunkSize = %d, want 700 (project wins)", cfg.ChunkSize)
	}
	if cfg.NResult != 5 {
		t.Fatalf("nResult = %d, want 5 (global retained)", cfg.NResult)
	}
	if cfg.Model != DefaultModel {
		t.Fatalf("model = %q, want default %q", cfg.Model, DefaultModel)
	}
}

func TestMissingFileIsNotAnError(t *testing.T) {
	cfg := Defaults()
	if err := cfg.MergeFile(filepath.Join(t.TempDir(), "nope.json")); err != nil {
		t.Fatalf("missing config returned error: %v", err)
	}
}

func TestInvalidJSONFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	writeFile(t, path, `{oops}`)
	cfg := Defaults()
	if err := cfg.MergeFile(path); err == nil {
		t.Fatal("want parse error")
	}
}

func TestEnvOverridesConfig(t *testing.T) {
	t.Setenv("CODESEEK_DATA_DIR", "E:/data")
	t.Setenv("CODESEEK_MODEL_DIR", "E:/models")
	t.Setenv("CODESEEK_MODEL", "other/model")

	cfg := Defaults()
	cfg.DataDir = "old"
	cfg.ApplyEnv()
	if cfg.DataDir != "E:/data" || cfg.ModelDir != "E:/models" || cfg.Model != "other/model" {
		t.Fatalf("env not applied: %+v", cfg)
	}
}

func TestModelPath(t *testing.T) {
	cfg := Defaults()
	cfg.Model = "BAAI/bge-small-zh-v1.5"
	cfg.ModelDir = filepath.Join("D:", "models")
	want := filepath.Join("D:", "models", "bge-small-zh-v1.5")
	if got := cfg.ModelPath(); got != want {
		t.Fatalf("ModelPath = %q, want %q", got, want)
	}
}

func TestDefaultsPopulated(t *testing.T) {
	cfg := Defaults()
	if cfg.ChunkSize != 1000 || cfg.OverlapRatio != 0.15 {
		t.Fatalf("chunk defaults = %d/%.2f", cfg.ChunkSize, cfg.OverlapRatio)
	}
	if cfg.MaxFileBytes != 1<<20 || cfg.NResult != 8 || cfg.Workers != 2 {
		t.Fatalf("defaults = %+v", cfg)
	}
	if len(cfg.Include) == 0 || len(cfg.ExcludeDirs) == 0 || len(cfg.ExcludeGlobs) == 0 {
		t.Fatalf("defaults lists empty: %+v", cfg)
	}
}

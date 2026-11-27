package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codesseek/internal/assets"
	"codesseek/internal/config"
)

func TestLocalRuntimeDLLDetection(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("ONNXRUNTIME_DLL", "")

	if hasLocalRuntimeDLL() {
		t.Fatal("no DLL should be found in an empty directory")
	}
	if err := os.WriteFile(filepath.Join(dir, "onnxruntime.dll"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasLocalRuntimeDLL() {
		t.Fatal("runtime in the working directory was not detected")
	}
}

func TestLocalRuntimeDLLUsesEnvPath(t *testing.T) {
	dir := t.TempDir()
	dll := filepath.Join(dir, "custom-runtime.dll")
	if err := os.WriteFile(dll, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONNXRUNTIME_DLL", dll)
	if !hasLocalRuntimeDLL() {
		t.Fatal("ONNXRUNTIME_DLL was not honored")
	}
}

func TestEnsureModelFilesKeepsExistingModel(t *testing.T) {
	cfg := config.Defaults()
	cfg.ModelDir = t.TempDir()
	target := cfg.ModelPath()
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(target, "model_quantized.onnx")
	if err := os.WriteFile(model, []byte("user-model"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureModelFiles(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(model)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "user-model" {
		t.Fatalf("existing model was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "tokenizer.json")); err == nil {
		t.Fatal("tokenizer should not be added when the model is already present")
	}
}

func TestEnsureModelFilesIgnoresCustomModel(t *testing.T) {
	cfg := config.Defaults()
	cfg.Model = "someone/other-model"
	cfg.ModelDir = t.TempDir()

	if err := ensureModelFiles(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.ModelPath()); err == nil {
		t.Fatal("a custom model must not trigger embedded extraction")
	}
}

func TestEnsureModelFilesExtractsEmbeddedModel(t *testing.T) {
	if !assets.HasModel() {
		t.Skip("this build has no embedded model")
	}
	cfg := config.Defaults()
	cfg.ModelDir = t.TempDir()

	if err := ensureModelFiles(cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"model_quantized.onnx", "tokenizer.json", "config.json"} {
		if _, err := os.Stat(filepath.Join(cfg.ModelPath(), name)); err != nil {
			t.Fatalf("%s not extracted: %v", name, err)
		}
	}
}

func TestEnsureRuntimeDLLExtractsEmbeddedRuntime(t *testing.T) {
	if !assets.HasRuntime() {
		t.Skip("this build has no embedded runtime")
	}
	home := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv("CODESEEK_HOME", home)
	t.Setenv("ONNXRUNTIME_DLL", "")

	dll, err := ensureRuntimeDLL()
	if err != nil {
		t.Fatal(err)
	}
	if dll == "" {
		t.Fatal("expected an extracted DLL path")
	}
	wantDir := filepath.Join(home, "runtime", "onnxruntime-"+assets.RuntimeVersion)
	if !strings.EqualFold(filepath.Dir(dll), wantDir) {
		t.Fatalf("dll dir = %s, want %s", filepath.Dir(dll), wantDir)
	}
	if _, err := os.Stat(dll); err != nil {
		t.Fatalf("extracted DLL missing: %v", err)
	}
}

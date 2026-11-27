package main

import (
	"fmt"
	"os"
	"path/filepath"

	"codesseek/internal/assets"
	"codesseek/internal/config"
)

// hasLocalRuntimeDLL mirrors the lookup order of embedder.resolveDLL. When a
// runtime is already on disk the embedded copy is skipped, so users can still
// upgrade or override it.
func hasLocalRuntimeDLL() bool {
	candidates := []string{os.Getenv("ONNXRUNTIME_DLL")}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "onnxruntime.dll"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "onnxruntime.dll"))
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// ensureRuntimeDLL returns an explicit DLL path when the runtime has to be
// extracted from the executable. An empty path means "resolve as usual"
// (ONNXRUNTIME_DLL, next to the exe, or the working directory).
func ensureRuntimeDLL() (string, error) {
	if hasLocalRuntimeDLL() || !assets.HasRuntime() {
		return "", nil
	}
	dir := filepath.Join(config.HomeDir(), "runtime", "onnxruntime-"+assets.RuntimeVersion)
	dll, err := assets.ExtractRuntime(dir)
	if err != nil {
		return "", fmt.Errorf("extract the embedded ONNX runtime to %s: %w (set CODESEEK_HOME to a writable directory)", dir, err)
	}
	return dll, nil
}

// ensureModelFiles extracts the embedded model when the configured model is
// missing. A model already on disk always wins, so custom downloads are never
// overwritten.
func ensureModelFiles(cfg config.Config) error {
	if cfg.Model != config.DefaultModel {
		return nil
	}
	target := cfg.ModelPath()
	if info, err := os.Stat(filepath.Join(target, "model_quantized.onnx")); err == nil && !info.IsDir() {
		return nil
	}
	if !assets.HasModel() {
		return nil
	}
	if err := assets.EnsureModel(target); err != nil {
		return fmt.Errorf("extract the embedded model to %s: %w (set CODESEEK_MODEL_DIR or CODESEEK_HOME to a writable directory)", target, err)
	}
	return nil
}

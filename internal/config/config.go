// Package config resolves codesseek settings.
//
// Priority: CLI flags > environment > project .codesseek/config.json > global
// ~/.config/codesseek/config.json > built-in defaults (docs/spec.md §4).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codesseek/internal/scanner"
)

// DefaultModel is the built-in model identifier.
const DefaultModel = "BAAI/bge-small-zh-v1.5"

// Config is the merged, effective configuration.
type Config struct {
	Model        string   `json:"model"`
	ModelDir     string   `json:"modelDir"`
	DataDir      string   `json:"dataDir"`
	ChunkSize    int      `json:"chunkSize"`
	OverlapRatio float64  `json:"overlapRatio"`
	MaxFileBytes int64    `json:"maxFileBytes"`
	Include      []string `json:"include"`
	ExcludeDirs  []string `json:"excludeDirs"`
	ExcludeGlobs []string `json:"excludeGlobs"`
	Workers      int      `json:"workers"`
	NResult      int      `json:"nResult"`
}

// Defaults returns the built-in configuration. Models and data live under
// CODESEEK_HOME, or next to the executable when it sits in a bin/ directory
// (the layout documented in docs/spec.md §3.1).
func Defaults() Config {
	home := homeDir()
	return Config{
		Model:        DefaultModel,
		ModelDir:     filepath.Join(home, "models"),
		DataDir:      filepath.Join(home, "data"),
		ChunkSize:    1000,
		OverlapRatio: 0.15,
		MaxFileBytes: 1 << 20,
		Include:      append([]string(nil), scanner.DefaultInclude...),
		ExcludeDirs:  append([]string(nil), scanner.DefaultExcludeDirs...),
		ExcludeGlobs: append([]string(nil), scanner.DefaultExcludeGlobs...),
		Workers:      2,
		NResult:      8,
	}
}

// HomeDir is the base directory for models, data and the runtime extracted
// from an embedded distribution.
func HomeDir() string { return homeDir() }

func homeDir() string {
	if h := os.Getenv("CODESEEK_HOME"); h != "" {
		return h
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if strings.EqualFold(filepath.Base(dir), "bin") {
			return filepath.Dir(dir)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codesseek")
	}
	return ".codesseek"
}

// GlobalPath returns the per-user config path:
// %USERPROFILE%\.config\codesseek\config.json.
func GlobalPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "codesseek", "config.json")
}

// Load merges global config, the project config under projectRoot and the
// environment. A missing config file is not an error.
func Load(projectRoot string) (Config, error) {
	cfg := Defaults()
	if path := GlobalPath(); path != "" {
		if err := cfg.MergeFile(path); err != nil {
			return cfg, err
		}
	}
	if projectRoot != "" {
		if err := cfg.MergeFile(filepath.Join(projectRoot, ".codesseek", "config.json")); err != nil {
			return cfg, err
		}
	}
	cfg.ApplyEnv()
	if err := cfg.Normalize(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// MergeFile overlays one JSON config file. Missing files are ignored; fields
// with zero values are treated as absent so lower-priority values survive.
func (c *Config) MergeFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var overlay Config
	if err := json.Unmarshal(data, &overlay); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	c.merge(overlay)
	return nil
}

func (c *Config) merge(o Config) {
	if o.Model != "" {
		c.Model = o.Model
	}
	if o.ModelDir != "" {
		c.ModelDir = o.ModelDir
	}
	if o.DataDir != "" {
		c.DataDir = o.DataDir
	}
	if o.ChunkSize > 0 {
		c.ChunkSize = o.ChunkSize
	}
	if o.OverlapRatio > 0 {
		c.OverlapRatio = o.OverlapRatio
	}
	if o.MaxFileBytes > 0 {
		c.MaxFileBytes = o.MaxFileBytes
	}
	if len(o.Include) > 0 {
		c.Include = append([]string(nil), o.Include...)
	}
	if len(o.ExcludeDirs) > 0 {
		c.ExcludeDirs = append([]string(nil), o.ExcludeDirs...)
	}
	if len(o.ExcludeGlobs) > 0 {
		c.ExcludeGlobs = append([]string(nil), o.ExcludeGlobs...)
	}
	if o.Workers > 0 {
		c.Workers = o.Workers
	}
	if o.NResult > 0 {
		c.NResult = o.NResult
	}
}

// ApplyEnv overlays CODESEEK_* environment variables.
func (c *Config) ApplyEnv() {
	if v := os.Getenv("CODESEEK_MODEL"); v != "" {
		c.Model = v
	}
	if v := os.Getenv("CODESEEK_MODEL_DIR"); v != "" {
		c.ModelDir = v
	}
	if v := os.Getenv("CODESEEK_DATA_DIR"); v != "" {
		c.DataDir = v
	}
}

// Normalize expands ~ and makes the directory settings absolute.
func (c *Config) Normalize() error {
	var err error
	if c.ModelDir, err = expandDir(c.ModelDir); err != nil {
		return fmt.Errorf("modelDir: %w", err)
	}
	if c.DataDir, err = expandDir(c.DataDir); err != nil {
		return fmt.Errorf("dataDir: %w", err)
	}
	return nil
}

func expandDir(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	path = os.ExpandEnv(path)
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimLeft(path[1:], `/\`))
		}
	}
	path = filepath.FromSlash(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// ModelPath returns the directory holding the ONNX model files, e.g.
// <ModelDir>/bge-small-zh-v1.5 for BAAI/bge-small-zh-v1.5.
func (c Config) ModelPath() string {
	return filepath.Join(c.ModelDir, filepath.Base(filepath.FromSlash(c.Model)))
}

// ScannerOptions maps the config onto the scanner options.
func (c Config) ScannerOptions() scanner.Options {
	return scanner.Options{
		Include:      c.Include,
		ExcludeDirs:  c.ExcludeDirs,
		ExcludeGlobs: c.ExcludeGlobs,
		MaxFileBytes: c.MaxFileBytes,
	}
}

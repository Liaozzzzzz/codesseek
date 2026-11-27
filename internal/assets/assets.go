// Package assets embeds the ONNX runtime and the default model so a
// distribution build is a single executable.
//
// The embedded files are only compiled in with `-tags embed` (build.ps1 does
// this). A plain `go build` or `go test` uses the empty filesystem, so
// HasRuntime and HasModel report false and callers fall back to files on disk.
// Keeping the staging directory out of default builds means a dev build is
// always the slim executable, even after a distribution build.
package assets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// RuntimeVersion is the ONNX Runtime build bundled into the executable.
	RuntimeVersion = "1.31.1"
	// ModelVersion identifies the bundled model. Bump it when the embedded
	// model changes so an already-extracted copy is refreshed.
	ModelVersion = "bge-small-zh-v1.5-int8-v1"

	runtimeSrc = "data/runtime"
	modelSrc   = "data/model/bge-small-zh-v1.5"
	markerName = ".codesseek-assets.json"
)

// ErrRuntimeNotEmbedded is returned by ExtractRuntime for a slim build.
var ErrRuntimeNotEmbedded = errors.New("assets: onnxruntime is not embedded in this build")

// ErrModelNotEmbedded is returned by EnsureModel for a slim build.
var ErrModelNotEmbedded = errors.New("assets: the default model is not embedded in this build")

// HasRuntime reports whether an ONNX runtime is embedded in this executable.
func HasRuntime() bool {
	return hasFile(runtimeSrc, "onnxruntime.dll")
}

// HasModel reports whether the default model is embedded in this executable.
func HasModel() bool {
	return hasFiles(modelSrc, "model_quantized.onnx", "tokenizer.json", "config.json")
}

func hasFile(dir, name string) bool {
	_, err := fs.Stat(embedded, path.Join(dir, name))
	return err == nil
}

func hasFiles(dir string, names ...string) bool {
	for _, name := range names {
		if !hasFile(dir, name) {
			return false
		}
	}
	return true
}

// ExtractRuntime writes the embedded ONNX runtime into destDir and returns the
// path to onnxruntime.dll. Existing files with the same size are left alone, so
// repeated calls are cheap.
func ExtractRuntime(destDir string) (string, error) {
	if !HasRuntime() {
		return "", ErrRuntimeNotEmbedded
	}
	if err := extractTree(embedded, runtimeSrc, destDir, RuntimeVersion); err != nil {
		return "", err
	}
	return filepath.Join(destDir, "onnxruntime.dll"), nil
}

// EnsureModel writes the embedded default model into destDir. The caller is
// expected to skip this when the user already has a model in place.
func EnsureModel(destDir string) error {
	if !HasModel() {
		return ErrModelNotEmbedded
	}
	return extractTree(embedded, modelSrc, destDir, ModelVersion)
}

// marker records which assets were extracted. It lets a later run skip the
// copy when the version and file sizes still match.
type marker struct {
	Version string           `json:"version"`
	Files   map[string]int64 `json:"files"`
}

func extractTree(fsys fs.FS, srcDir, destDir, version string) error {
	sizes := make(map[string]int64)
	var files []string
	err := fs.WalkDir(fsys, srcDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, p)
		sizes[p] = info.Size()
		return nil
	})
	if err != nil {
		return fmt.Errorf("assets: read %s: %w", srcDir, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("assets: %s contains no files", srcDir)
	}

	prev, hasPrev := readMarker(destDir)
	sameVersion := hasPrev && prev.Version == version

	if sameVersion && allPresent(destDir, srcDir, sizes) {
		return nil
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("assets: create %s: %w", destDir, err)
	}
	for _, src := range files {
		dest := filepath.Join(destDir, filepath.FromSlash(relPath(srcDir, src)))
		if sameVersion {
			if info, err := os.Stat(dest); err == nil && !info.IsDir() && info.Size() == sizes[src] {
				continue
			}
		}
		if err := copyFileAtomic(fsys, src, dest, sizes[src]); err != nil {
			return err
		}
	}
	return writeMarker(destDir, version, srcDir, sizes)
}

func allPresent(destDir, srcDir string, sizes map[string]int64) bool {
	for src, size := range sizes {
		dest := filepath.Join(destDir, filepath.FromSlash(relPath(srcDir, src)))
		info, err := os.Stat(dest)
		if err != nil || info.IsDir() || info.Size() != size {
			return false
		}
	}
	return true
}

func relPath(srcDir, p string) string {
	return strings.TrimPrefix(p, strings.TrimSuffix(srcDir, "/")+"/")
}

func readMarker(destDir string) (marker, bool) {
	data, err := os.ReadFile(filepath.Join(destDir, markerName))
	if err != nil {
		return marker{}, false
	}
	var m marker
	if err := json.Unmarshal(data, &m); err != nil || m.Version == "" {
		return marker{}, false
	}
	return m, true
}

func writeMarker(destDir, version, srcDir string, sizes map[string]int64) error {
	m := marker{Version: version, Files: make(map[string]int64, len(sizes))}
	for src, size := range sizes {
		m.Files[filepath.ToSlash(relPath(srcDir, src))] = size
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomic(filepath.Join(destDir, markerName), int64(len(data)), func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

func copyFileAtomic(fsys fs.FS, src, dest string, size int64) error {
	return writeAtomic(dest, size, func(w io.Writer) error {
		in, err := fsys.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
}

// writeAtomic writes via a temp file and rename so a crash or a second process
// never observes a half-written file. If another process wins the rename, an
// existing destination of the expected size is accepted.
func writeAtomic(dest string, size int64, write func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("assets: create %s: %w", filepath.Dir(dest), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".codesseek-assets-*")
	if err != nil {
		return fmt.Errorf("assets: create temp file next to %s: %w", dest, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if err := write(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("assets: write %s: %w", dest, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("assets: sync %s: %w", dest, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("assets: close %s: %w", dest, err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		if info, statErr := os.Stat(dest); statErr == nil && info.Size() == size {
			_ = os.Remove(tmpName)
			tmpName = ""
			return nil
		}
		return fmt.Errorf("assets: move %s into place: %w", dest, err)
	}
	tmpName = ""
	return nil
}

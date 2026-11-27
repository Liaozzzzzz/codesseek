package assets

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
)

func runtimeTree() fstest.MapFS {
	return fstest.MapFS{
		"runtime/onnxruntime.dll":                  {Data: []byte("dll-bytes")},
		"runtime/onnxruntime_providers_shared.dll": {Data: []byte("providers")},
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestExtractTreeWritesFilesAndMarker(t *testing.T) {
	dest := t.TempDir()
	if err := extractTree(runtimeTree(), "runtime", dest, "v1"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dest, "onnxruntime.dll")); got != "dll-bytes" {
		t.Fatalf("onnxruntime.dll = %q", got)
	}
	if got := readFile(t, filepath.Join(dest, "onnxruntime_providers_shared.dll")); got != "providers" {
		t.Fatalf("providers = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, markerName)); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
}

func TestExtractTreeSkipsWhenVersionAndSizesMatch(t *testing.T) {
	dest := t.TempDir()
	if err := extractTree(runtimeTree(), "runtime", dest, "v1"); err != nil {
		t.Fatal(err)
	}
	// Same size, different bytes: a matching marker must keep the local file.
	target := filepath.Join(dest, "onnxruntime.dll")
	if err := os.WriteFile(target, []byte("DLL-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractTree(runtimeTree(), "runtime", dest, "v1"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "DLL-BYTES" {
		t.Fatalf("matching marker re-extracted the file: %q", got)
	}
}

func TestExtractTreeRepairsMissingFile(t *testing.T) {
	dest := t.TempDir()
	if err := extractTree(runtimeTree(), "runtime", dest, "v1"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dest, "onnxruntime.dll")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := extractTree(runtimeTree(), "runtime", dest, "v1"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "dll-bytes" {
		t.Fatalf("missing file was not repaired: %q", got)
	}
}

func TestExtractTreeRewritesOnVersionChange(t *testing.T) {
	dest := t.TempDir()
	if err := extractTree(runtimeTree(), "runtime", dest, "v1"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dest, "onnxruntime.dll")
	if err := os.WriteFile(target, []byte("DLL-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractTree(runtimeTree(), "runtime", dest, "v2"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "dll-bytes" {
		t.Fatalf("version change did not refresh the file: %q", got)
	}
}

func TestExtractTreeConcurrent(t *testing.T) {
	dest := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- extractTree(runtimeTree(), "runtime", dest, "v1")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := readFile(t, filepath.Join(dest, "onnxruntime.dll")); got != "dll-bytes" {
		t.Fatalf("concurrent extraction produced %q", got)
	}
}

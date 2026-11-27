package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func paths(files []File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestScanFiltersExtensionsAndDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src/a.ts", "export const a = 1")
	writeFile(t, root, "src/readme.txt", "hello")
	writeFile(t, root, "node_modules/dep/index.ts", "dep")
	writeFile(t, root, "dist/bundle.ts", "bundle")
	writeFile(t, root, ".git/hooks/hook.ts", "hook")

	got, err := Scan(root, Options{
		Include:      []string{".ts"},
		ExcludeDirs:  []string{"node_modules", "dist", ".git"},
		MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"src/a.ts"}
	if !equalStrings(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
}

func TestScanAppliesGitignoreWithNegation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "*.log\n!keep.log\nbuild/\n")
	writeFile(t, root, "keep.log", "keep")
	writeFile(t, root, "drop.log", "drop")
	writeFile(t, root, "build/out.ts", "out")
	writeFile(t, root, "src/a.ts", "a")

	got, err := Scan(root, Options{
		Include:      []string{".log", ".ts"},
		MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"keep.log", "src/a.ts"}
	if !equalStrings(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
}

func TestScanAppliesExcludeGlobs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src/app.js", "app")
	writeFile(t, root, "src/app.min.js", "min")
	writeFile(t, root, "src/app.js.map", "map")

	got, err := Scan(root, Options{
		Include:      []string{".js"},
		ExcludeGlobs: []string{"*.min.js", "*.map"},
		MaxFileBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"src/app.js"}
	if !equalStrings(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
}

func TestScanSkipsLargeAndBinaryFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "ok.ts", "const ok = 1")
	writeFile(t, root, "big.ts", string(make([]byte, 200)))
	writeFile(t, root, "binary.ts", "hello\x00world")

	got, err := Scan(root, Options{
		Include:      []string{".ts"},
		MaxFileBytes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ok.ts"}
	if !equalStrings(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
}

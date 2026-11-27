package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codesseek/internal/chunker"
	"codesseek/internal/embedder"
	"codesseek/internal/scanner"
	"codesseek/internal/searcher"
	"codesseek/internal/store"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// TestIndexDecodesGBKSource guards against Chinese Windows repositories whose
// source files are GBK rather than UTF-8.
func TestIndexDecodesGBKSource(t *testing.T) {
	utf8Source := `// 用户登录失败处理：提示用户名或密码错误
export function handleLoginError(error: Error) {
  return '登录失败，请重试'
}
`
	gbk, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte(utf8Source))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "src", "auth.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, gbk, 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	emb := embedder.NewFake(8)
	if _, err := Run(context.Background(), Options{
		Root:     root,
		DataDir:  dataDir,
		Scan:     scanner.Options{Include: scanner.DefaultInclude, ExcludeDirs: scanner.DefaultExcludeDirs, MaxFileBytes: 1 << 20},
		Chunk:    chunker.Config{ChunkSize: 1000, OverlapRatio: 0.15},
		Embedder: emb,
		Workers:  1,
	}); err != nil {
		t.Fatal(err)
	}

	st, err := store.New(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := searcher.New(st, emb)
	if err != nil {
		t.Fatal(err)
	}
	results, err := s.Search("用户登录失败处理", searcher.Options{TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].Path != "src/auth.ts" {
		t.Fatalf("results = %+v", results)
	}
	if !strings.Contains(results[0].Snippet, "登录失败") {
		t.Fatalf("snippet was not decoded to UTF-8: %q", results[0].Snippet)
	}
	if !strings.Contains(results[0].Snippet, "handleLoginError") {
		t.Fatalf("snippet missing code: %q", results[0].Snippet)
	}
}

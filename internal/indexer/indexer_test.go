package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"codesseek/internal/chunker"
	"codesseek/internal/embedder"
	"codesseek/internal/scanner"
	"codesseek/internal/searcher"
	"codesseek/internal/store"
)

type countingEmbedder struct {
	embedder.Embedder
	mu   sync.Mutex
	docs int
}

func (c *countingEmbedder) EmbedDocuments(texts []string) ([][]float32, error) {
	c.mu.Lock()
	c.docs += len(texts)
	c.mu.Unlock()
	return c.Embedder.EmbedDocuments(texts)
}

func (c *countingEmbedder) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.docs
}

func (c *countingEmbedder) reset() {
	c.mu.Lock()
	c.docs = 0
	c.mu.Unlock()
}

func fixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/login.vue", `<template>
  <div class="login">
    <span>登录失败，请检查用户名或密码</span>
  </div>
</template>

<script setup lang="ts">
function handleLoginError(error: Error) {
  console.error(error)
  return '登录失败，请重试'
}
</script>

<style scoped>
.login { color: red; }
</style>
`)
	write("src/payment.ts", `export function payOrder(amount: number) {
  return { amount, status: 'paid' }
}
`)
	write("README.md", `# Demo

## 登录失败处理
捕获异常并提示错误。
`)
	return root
}

func opts(root, dataDir string, emb embedder.Embedder) Options {
	return Options{
		Root:     root,
		DataDir:  dataDir,
		Scan:     scanner.Options{Include: scanner.DefaultInclude, ExcludeDirs: scanner.DefaultExcludeDirs, MaxFileBytes: 1 << 20},
		Chunk:    chunker.Config{ChunkSize: 1000, OverlapRatio: 0.15},
		Embedder: emb,
	}
}

func TestIndexSearchIncrementalAndDelete(t *testing.T) {
	ctx := context.Background()
	root := fixtureRepo(t)
	dataDir := t.TempDir()
	emb := &countingEmbedder{Embedder: embedder.NewFake(8)}
	opt := opts(root, dataDir, emb)

	first, err := Run(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	if first.Added != 3 || first.Skipped != 0 || first.Files != 3 || first.Chunks == 0 {
		t.Fatalf("first run = %+v", first)
	}
	if emb.count() != first.Chunks {
		t.Fatalf("embedded %d docs, want %d", emb.count(), first.Chunks)
	}

	// Search finds login.vue through the fake embedding.
	st, err := store.New(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := searcher.New(st, emb)
	if err != nil {
		t.Fatal(err)
	}
	results, err := s.Search("登录失败处理", searcher.Options{TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || (results[0].Path != "src/login.vue" && results[0].Path != "README.md") {
		t.Fatalf("search results = %+v", results)
	}
	if !strings.Contains(results[0].Snippet, "登录失败") {
		t.Fatalf("snippet = %q", results[0].Snippet)
	}
	foundLogin := false
	for _, r := range results {
		if r.Path == "src/login.vue" {
			foundLogin = true
		}
	}
	if !foundLogin {
		t.Fatalf("login.vue missing from top results: %+v", results)
	}

	// A second run must reuse every chunk: zero embeddings, all skipped.
	emb.reset()
	second, err := Run(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	if second.Skipped != 3 || second.Added != 0 || second.Updated != 0 || second.Removed != 0 {
		t.Fatalf("second run = %+v", second)
	}
	if emb.count() != 0 {
		t.Fatalf("second run embedded %d docs, want 0", emb.count())
	}

	// Changing one file re-embeds only that file.
	writeFile(t, root, "src/payment.ts", `export function payOrder(amount: number) {
  return { amount, status: 'paid', method: 'wechat' }
}
`)
	emb.reset()
	third, err := Run(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	if third.Updated != 1 || third.Skipped != 2 || third.Removed != 0 {
		t.Fatalf("third run = %+v", third)
	}
	if emb.count() == 0 || emb.count() >= third.Chunks {
		t.Fatalf("third run embedded %d docs, want 0 < docs < %d", emb.count(), third.Chunks)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("index invalid after update: %v", err)
	}

	// Deleting a file removes its chunks from the index.
	if err := os.Remove(filepath.Join(root, "src", "login.vue")); err != nil {
		t.Fatal(err)
	}
	emb.reset()
	fourth, err := Run(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Removed != 1 || fourth.Skipped != 2 {
		t.Fatalf("fourth run = %+v", fourth)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("index invalid after delete: %v", err)
	}
	results, err = s.Search("登录失败处理", searcher.Options{TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Path == "src/login.vue" {
			t.Fatalf("deleted file still returned: %+v", results)
		}
	}
}

func TestIndexRequiresFullWhenChunkConfigChanges(t *testing.T) {
	ctx := context.Background()
	root := fixtureRepo(t)
	dataDir := t.TempDir()
	emb := embedder.NewFake(8)
	opt := opts(root, dataDir, emb)
	if _, err := Run(ctx, opt); err != nil {
		t.Fatal(err)
	}

	changed := opt
	changed.Chunk = chunker.Config{ChunkSize: 300, OverlapRatio: 0.15}
	if _, err := Run(ctx, changed); !errors.Is(err, ErrNeedsFull) {
		t.Fatalf("incremental with new chunk config = %v, want ErrNeedsFull", err)
	}

	changed.Full = true
	got, err := Run(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Skipped != 0 || got.Chunks == 0 {
		t.Fatalf("full rebuild = %+v", got)
	}
	st, err := store.New(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("full rebuild invalid: %v", err)
	}
}

func TestIndexRejectsConcurrentRun(t *testing.T) {
	ctx := context.Background()
	root := fixtureRepo(t)
	dataDir := t.TempDir()
	emb := embedder.NewFake(8)
	st, err := store.New(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := st.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	_, err = Run(ctx, opts(root, dataDir, emb))
	if !errors.Is(err, store.ErrLocked) {
		t.Fatalf("Run = %v, want ErrLocked", err)
	}
}

func TestIndexWorkersPreserveOrderAndStats(t *testing.T) {
	ctx := context.Background()
	root := fixtureRepo(t)
	dataDir := t.TempDir()
	emb := embedder.NewFake(8)
	opt := opts(root, dataDir, emb)
	opt.Workers = 4
	got, err := Run(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Files != 3 || got.Added != 3 {
		t.Fatalf("workers run = %+v", got)
	}
	st, err := store.New(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("index invalid: %v", err)
	}
}

func TestConfigHashCoversVectorInputs(t *testing.T) {
	base := chunker.Config{ChunkSize: 1000, OverlapRatio: 0.15}
	want := ConfigHash(base, "model-a", 512)
	if got := ConfigHash(base, "model-a", 512); got != want {
		t.Fatalf("hash not stable: %q vs %q", got, want)
	}
	cases := map[string]string{
		"chunk size": ConfigHash(chunker.Config{ChunkSize: 800, OverlapRatio: 0.15}, "model-a", 512),
		"overlap":    ConfigHash(chunker.Config{ChunkSize: 1000, OverlapRatio: 0.10}, "model-a", 512),
		"model":      ConfigHash(base, "model-b", 512),
		"dim":        ConfigHash(base, "model-a", 768),
	}
	for name, got := range cases {
		if got == want {
			t.Fatalf("%s change did not change the hash", name)
		}
	}
}

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

func TestNeedsRefreshDetectsTreeChanges(t *testing.T) {
	ctx := context.Background()
	root := fixtureRepo(t)
	dataDir := t.TempDir()
	scanOpt := scanner.Options{Include: scanner.DefaultInclude, ExcludeDirs: scanner.DefaultExcludeDirs, MaxFileBytes: 1 << 20}
	if _, err := Run(ctx, opts(root, dataDir, embedder.NewFake(8))); err != nil {
		t.Fatal(err)
	}
	if stale, err := NeedsRefresh(root, dataDir, scanOpt); err != nil || stale {
		t.Fatalf("fresh index reported stale=%v err=%v", stale, err)
	}

	writeFile(t, root, "src/refund.ts", "export const refund = 1\n")
	if stale, err := NeedsRefresh(root, dataDir, scanOpt); err != nil || !stale {
		t.Fatalf("added file: stale=%v err=%v", stale, err)
	}
	if _, err := Run(ctx, opts(root, dataDir, embedder.NewFake(8))); err != nil {
		t.Fatal(err)
	}
	if stale, err := NeedsRefresh(root, dataDir, scanOpt); err != nil || stale {
		t.Fatalf("after reindex: stale=%v err=%v", stale, err)
	}

	// Rewrite an existing file; the stat check must notice it.
	writeFile(t, root, "src/login.vue", "<template><span>登录失败次数过多</span></template>\n")
	if stale, err := NeedsRefresh(root, dataDir, scanOpt); err != nil || !stale {
		t.Fatalf("edited file: stale=%v err=%v", stale, err)
	}
}

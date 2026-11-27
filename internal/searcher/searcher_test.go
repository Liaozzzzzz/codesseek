package searcher

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"codesseek/internal/embedder"
	"codesseek/internal/store"
)

type doc struct {
	path      string
	text      string
	startLine int
	symbol    string
}

func writeIndex(t *testing.T, emb embedder.Embedder, indexedAt time.Time, docs []doc) *store.Store {
	t.Helper()
	root := t.TempDir()

	byPath := map[string][]doc{}
	for _, d := range docs {
		if d.startLine < 1 {
			d.startLine = 1
		}
		byPath[d.path] = append(byPath[d.path], d)
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var fileRecords []store.FileRecord
	var chunks []store.ChunkRecord
	var texts []string
	chunkStart := 0
	for _, p := range paths {
		group := byPath[p]
		maxLine := 0
		for _, d := range group {
			textLines := strings.Split(d.text, "\n")
			if end := d.startLine + len(textLines) - 1; end > maxLine {
				maxLine = end
			}
		}
		lines := make([]string, maxLine)
		for _, d := range group {
			textLines := strings.Split(d.text, "\n")
			for i, line := range textLines {
				idx := d.startLine - 1 + i
				if idx < len(lines) {
					lines[idx] = line
				}
			}
		}
		content := strings.Join(lines, "\n")
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			t.Fatal(err)
		}
		fileRecords = append(fileRecords, store.FileRecord{
			Path:       p,
			Size:       info.Size(),
			MTime:      info.ModTime().Unix(),
			SHA256:     "x",
			ChunkStart: chunkStart,
			ChunkCount: len(group),
		})
		chunkStart += len(group)
		for _, d := range group {
			lines := strings.Split(d.text, "\n")
			chunks = append(chunks, store.ChunkRecord{
				Path:      d.path,
				StartLine: d.startLine,
				EndLine:   d.startLine + len(lines) - 1,
				Kind:      "test",
				Symbol:    d.symbol,
				SHA256:    "x",
			})
			texts = append(texts, d.text)
		}
	}

	vectors, err := emb.EmbedDocuments(texts)
	if err != nil {
		t.Fatal(err)
	}
	flat := make([]float32, 0, len(vectors)*emb.Dim())
	for _, v := range vectors {
		flat = append(flat, v...)
	}

	st, err := store.New(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	meta := store.Meta{
		Root:            root,
		Model:           emb.Model(),
		Dim:             emb.Dim(),
		ChunkConfigHash: "test",
		IndexedAt:       indexedAt,
	}
	if err := st.Write(meta, fileRecords, chunks, flat); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSearchOrdersByCosine(t *testing.T) {
	emb := embedder.NewFake(64)
	st := writeIndex(t, emb, time.Now(), []doc{
		{path: "src/login.vue", text: "用户登录失败处理：提示错误", startLine: 1},
		{path: "src/payment.vue", text: "订单支付结算流程", startLine: 1},
	})
	s, err := New(st, emb)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Search("登录失败", Options{TopK: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("results = %d, want 2", len(got))
	}
	if got[0].Path != "src/login.vue" {
		t.Fatalf("top result = %s, want src/login.vue", got[0].Path)
	}
	if !strings.Contains(got[0].Snippet, "登录失败") {
		t.Fatalf("snippet = %q", got[0].Snippet)
	}
}

func TestSearchDownranksExtraChunksFromSameFile(t *testing.T) {
	emb := embedder.NewFake(64)
	st := writeIndex(t, emb, time.Now(), []doc{
		{path: "src/a.ts", text: "登录失败处理", startLine: 1},
		{path: "src/a.ts", text: "登录失败处理", startLine: 2},
		{path: "src/a.ts", text: "登录失败处理", startLine: 3},
		{path: "src/b.ts", text: "登录失败处理", startLine: 1},
	})
	s, err := New(st, emb)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Search("登录失败处理", Options{TopK: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("results = %d, want 4", len(got))
	}
	bIndex := -1
	thirdAIndex := -1
	for i, r := range got {
		if r.Path == "src/b.ts" && bIndex < 0 {
			bIndex = i
		}
		if r.Path == "src/a.ts" && r.StartLine == 3 {
			thirdAIndex = i
		}
	}
	if bIndex < 0 || thirdAIndex < 0 || bIndex > thirdAIndex {
		t.Fatalf("want b.ts before a.ts#3, got %+v", got)
	}
}

func TestSearchReranksOnPathHit(t *testing.T) {
	emb := embedder.NewFake(64)
	// Both chunks have identical text, so cosine ties; only the path differs.
	st := writeIndex(t, emb, time.Now(), []doc{
		{path: "src/login/handler.ts", text: "相同的代码内容", startLine: 1},
		{path: "src/other/handler.ts", text: "相同的代码内容", startLine: 1},
	})
	s, err := New(st, emb)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Search("login", Options{TopK: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Path != "src/login/handler.ts" {
		t.Fatalf("path hit not applied: %+v", got)
	}
}

func TestSearchPathPrefix(t *testing.T) {
	emb := embedder.NewFake(64)
	st := writeIndex(t, emb, time.Now(), []doc{
		{path: "src/login/a.ts", text: "登录失败处理", startLine: 1},
		{path: "src/pay/b.ts", text: "登录失败处理", startLine: 1},
	})
	s, err := New(st, emb)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Search("登录失败", Options{TopK: 5, PathPrefix: "src/pay"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "src/pay/b.ts" {
		t.Fatalf("results = %+v", got)
	}
}

func TestSearchMarksStaleIndex(t *testing.T) {
	emb := embedder.NewFake(64)
	st := writeIndex(t, emb, time.Now().Add(-25*time.Hour), []doc{
		{path: "src/a.ts", text: "登录失败处理", startLine: 1},
	})
	s, err := New(st, emb)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Search("登录失败", Options{TopK: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Stale {
		t.Fatalf("want stale result, got %+v", got)
	}
}

func TestSearchRejectsCorruptVectors(t *testing.T) {
	emb := embedder.NewFake(64)
	st := writeIndex(t, emb, time.Now(), []doc{
		{path: "src/a.ts", text: "登录失败处理", startLine: 1},
	})
	path := filepath.Join(st.Dir(), "vectors.bin")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-4); err != nil {
		t.Fatal(err)
	}
	s, err := New(st, emb)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search("登录失败", Options{TopK: 1}); !errors.Is(err, store.ErrCorrupt) {
		t.Fatalf("Search = %v, want ErrCorrupt", err)
	}
}

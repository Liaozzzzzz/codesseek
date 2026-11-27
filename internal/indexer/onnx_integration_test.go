//go:build modeltest

package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"codesseek/internal/chunker"
	"codesseek/internal/embedder"
	"codesseek/internal/scanner"
	"codesseek/internal/searcher"
	"codesseek/internal/store"
)

func realEmbedder(t *testing.T) *embedder.ONNX {
	t.Helper()
	modelsRoot := os.Getenv("CODESEEK_MODEL_DIR")
	if modelsRoot == "" {
		modelsRoot = filepath.Join("..", "..", "models")
	}
	modelDir := filepath.Join(modelsRoot, "bge-small-zh-v1.5")
	if _, err := os.Stat(filepath.Join(modelDir, "model_quantized.onnx")); err != nil {
		t.Skipf("model not available (set CODESEEK_MODEL_DIR): %v", err)
	}
	emb, err := embedder.NewONNX(embedder.ONNXOptions{
		ModelDir: modelDir,
		DLLPath:  os.Getenv("ONNXRUNTIME_DLL"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = emb.Close() })
	return emb
}

func searchFixture(t *testing.T, root string, emb embedder.Embedder, query string) []searcher.Result {
	t.Helper()
	dataDir := t.TempDir()
	res, err := Run(context.Background(), Options{
		Root:     root,
		DataDir:  dataDir,
		Scan:     scanner.Options{Include: scanner.DefaultInclude, ExcludeDirs: scanner.DefaultExcludeDirs, MaxFileBytes: 1 << 20},
		Chunk:    chunker.Config{ChunkSize: 1000, OverlapRatio: 0.15},
		Embedder: emb,
		Workers:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Chunks == 0 {
		t.Fatalf("index result = %+v", res)
	}
	st, err := store.New(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := searcher.New(st, emb)
	if err != nil {
		t.Fatal(err)
	}
	results, err := s.Search(query, searcher.Options{TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	return results
}

// TestRealModelIndexAndChineseSearch is the step-3 gate: index a fixture with
// the real bge-small-zh-v1.5 model and check that a Chinese query finds the
// login code.
func TestRealModelIndexAndChineseSearch(t *testing.T) {
	emb := realEmbedder(t)
	results := searchFixture(t, fixtureRepo(t), emb, "用户登录失败的处理")
	hit := false
	for _, r := range results[:min(2, len(results))] {
		if r.Path == "src/login.vue" || r.Path == "README.md" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("login-related chunk not in top 2: %+v", results)
	}
}

// TestRealModelChineseQueryEnglishOnlyCode guards the Chinese keyword hints:
// the login code here has no Chinese text at all, so only the identifier
// glossary can connect it to a Chinese question.
func TestRealModelChineseQueryEnglishOnlyCode(t *testing.T) {
	emb := realEmbedder(t)
	root := t.TempDir()
	writeFile(t, root, "src/session.ts", `import userApi from '../services/user'

export function useLogin(username: string, password: string) {
  return userApi.login({ username, password }).catch((error) => {
    console.error(error)
    return null
  })
}
`)
	writeFile(t, root, "src/cart.ts", `export function addToCart(productId: number, quantity: number) {
  return { productId, quantity }
}
`)
	writeFile(t, root, "docs/policy.md", `# 隐私政策

本平台可能会通过小型数据文件识别您的身份，目的是为您省去重复输入注册信息的步骤，
或帮助判断您的账号安全。这些数据文件可能是 Cookie，或您的浏览器提供的其他本地存储。
`)

	results := searchFixture(t, root, emb, "用户登录失败的处理")
	if results[0].Path != "src/session.ts" {
		t.Fatalf("top result = %s, want src/session.ts: %+v", results[0].Path, results)
	}
}

//go:build modeltest

package embedder

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// modelDirForTest locates the bge-small-zh-v1.5 model directory. The test is
// skipped when the model is not present, so `go test -tags modeltest ./...`
// works on machines without the downloaded weights.
func modelDirForTest(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("CODESEEK_MODEL_DIR")
	if dir == "" {
		dir = filepath.Join("..", "..", "models")
	}
	// CODESEEK_MODEL_DIR may point at either the models root or the model
	// directory itself; both are used across the test suite.
	if _, err := os.Stat(filepath.Join(dir, "model_quantized.onnx")); err != nil {
		if alt := filepath.Join(dir, "bge-small-zh-v1.5"); !fileExists(filepath.Join(alt, "model_quantized.onnx")) {
			t.Skipf("model not available (set CODESEEK_MODEL_DIR): %v", err)
		} else {
			dir = alt
		}
	}
	return dir
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func newTestONNX(t *testing.T) *ONNX {
	t.Helper()
	e, err := NewONNX(ONNXOptions{
		ModelDir: modelDirForTest(t),
		DLLPath:  os.Getenv("ONNXRUNTIME_DLL"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestONNXMetadata(t *testing.T) {
	e := newTestONNX(t)
	if e.Dim() != 512 {
		t.Fatalf("Dim = %d, want 512", e.Dim())
	}
	if e.Model() != "BAAI/bge-small-zh-v1.5" {
		t.Fatalf("Model = %q", e.Model())
	}
}

func TestONNXChineseRanking(t *testing.T) {
	e := newTestONNX(t)
	qv, err := e.EmbedQuery("用户登录失败的处理")
	if err != nil {
		t.Fatal(err)
	}
	docs := []string{
		"src/pages/login.vue\nfunction handleLoginError\n登录失败，请检查用户名或密码，记录登录日志",
		"src/pages/order/payment.vue\n订单支付结算流程：计算应付金额、调用支付网关、生成支付流水号",
		"src/components/chart/bar.ts\n柱状图配置：设置坐标轴、图例、数据系列颜色与提示框",
	}
	vecs, err := e.EmbedDocuments(docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(docs) {
		t.Fatalf("got %d vectors for %d docs", len(vecs), len(docs))
	}
	login := dot(qv, vecs[0])
	payment := dot(qv, vecs[1])
	chart := dot(qv, vecs[2])
	if login <= payment || login <= chart {
		t.Fatalf("login %.4f should beat payment %.4f and chart %.4f", login, payment, chart)
	}
	if login < 0.5 {
		t.Fatalf("login similarity %.4f is too low for a related doc", login)
	}
}

func TestONNXEnglishQuery(t *testing.T) {
	e := newTestONNX(t)
	qv, err := e.EmbedQuery("handle login error")
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := e.EmbedDocuments([]string{
		"src/utils/auth.ts\nexport function handleLoginError(error: unknown) { return 'bad credentials' }",
		"src/utils/image.js\ncompress and upload images to object storage",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dot(qv, vecs[0]) <= dot(qv, vecs[1]) {
		t.Fatalf("english login score %.4f <= unrelated %.4f", dot(qv, vecs[0]), dot(qv, vecs[1]))
	}
}

func TestONNXVectorsAreNormalizedAndDeterministic(t *testing.T) {
	e := newTestONNX(t)
	text := "用户登录失败时提示错误并限制重试次数"
	a, err := e.EmbedQuery(text)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.EmbedQuery(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 512 {
		t.Fatalf("vector length = %d, want 512", len(a))
	}
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(a[i])
	}
	if math.Abs(math.Sqrt(sum)-1) > 1e-4 {
		t.Fatalf("norm = %f, want 1", math.Sqrt(sum))
	}
	if dot(a, b) < 0.9999 {
		t.Fatalf("same query produced different vectors: %f", dot(a, b))
	}
}

func TestONNXBatchesLargeInput(t *testing.T) {
	e := newTestONNX(t)
	texts := make([]string, 0, 24)
	for i := 0; i < 24; i++ {
		texts = append(texts, strings.Repeat("登录失败处理", i%5+1))
	}
	vecs, err := e.EmbedDocuments(texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(vecs), len(texts))
	}
	for i, v := range vecs {
		if len(v) != 512 {
			t.Fatalf("vector %d dim = %d, want 512", i, len(v))
		}
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		if math.Abs(math.Sqrt(sum)-1) > 1e-4 {
			t.Fatalf("vector %d norm = %f", i, math.Sqrt(sum))
		}
	}
}

func TestONNXEmptyInput(t *testing.T) {
	e := newTestONNX(t)
	vecs, err := e.EmbedDocuments(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 0 {
		t.Fatalf("got %d vectors for empty input", len(vecs))
	}
}

package embedder

import (
	"math"
	"testing"
)

// The ONNX default should scale with the machine but stay within a sane range.
func TestDefaultThreadCountRange(t *testing.T) {
	n := defaultThreadCount()
	if n < 2 || n > 8 {
		t.Fatalf("defaultThreadCount() = %d, want 2..8", n)
	}
}

func dot(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

func TestFakeIsDeterministicAndNormalized(t *testing.T) {
	e := NewFake(16)
	if e.Dim() != 16 {
		t.Fatalf("dim = %d, want 16", e.Dim())
	}
	a, err := e.EmbedQuery("登录失败处理")
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.EmbedQuery("登录失败处理")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 16 {
		t.Fatalf("vector len = %d, want 16", len(a))
	}
	if dot(a, b) < 0.9999 {
		t.Fatalf("same text produced different vectors: %v vs %v", a, b)
	}
	var sum float64
	for _, x := range a {
		sum += float64(x) * float64(x)
	}
	if math.Abs(math.Sqrt(sum)-1) > 1e-6 {
		t.Fatalf("norm = %f, want 1", math.Sqrt(sum))
	}
}

func TestFakeSimilarityPrefersOverlappingText(t *testing.T) {
	e := NewFake(128)
	q, err := e.EmbedQuery("用户登录失败处理")
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := e.EmbedDocuments([]string{
		"登录失败：提示用户名或密码错误",
		"订单支付结算流程",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dot(q, vecs[0]) <= dot(q, vecs[1]) {
		t.Fatalf("overlapping text score %f <= unrelated %f", dot(q, vecs[0]), dot(q, vecs[1]))
	}
}

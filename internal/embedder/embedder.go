// Package embedder turns text into normalized vectors.
//
// Production uses the bge-small-zh-v1.5 ONNX model; Fake is a deterministic
// bag-of-tokens embedding used by tests and by the offline index/search tests.
package embedder

import (
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Embedder is the interface consumed by the indexer and searcher.
type Embedder interface {
	Dim() int
	Model() string
	EmbedDocuments(texts []string) ([][]float32, error)
	EmbedQuery(text string) ([]float32, error)
}

// Fake is a deterministic hash embedding. It is not semantically strong; it
// only guarantees that shared tokens produce similar vectors, which is enough
// to exercise the indexing/search pipeline without the ONNX model.
type Fake struct {
	dim   int
	model string
}

// NewFake returns a fake embedder with the given dimension (default 8).
func NewFake(dim int) *Fake {
	if dim <= 0 {
		dim = 8
	}
	return &Fake{dim: dim, model: fmt.Sprintf("fake-hash-%d", dim)}
}

func (f *Fake) Dim() int      { return f.dim }
func (f *Fake) Model() string { return f.model }

func (f *Fake) EmbedDocuments(texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = f.embed(text)
	}
	return out, nil
}

func (f *Fake) EmbedQuery(text string) ([]float32, error) {
	return f.embed(text), nil
}

func (f *Fake) embed(text string) []float32 {
	vec := make([]float32, f.dim)
	for _, token := range tokens(text) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(token))
		sum := h.Sum64()
		idx := int(sum % uint64(f.dim))
		weight := float32(1)
		if sum&(1<<63) != 0 {
			weight = -1
		}
		vec[idx] += weight
	}
	return l2Normalize(vec)
}

// tokens splits text into lowercase ASCII words and CJK unigrams/bigrams.
func tokens(text string) []string {
	text = strings.ToLower(text)
	var out []string
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) > 0 {
			out = append(out, string(word))
			word = word[:0]
		}
	}
	flushCJK := func() {
		for i, r := range cjk {
			out = append(out, string(r))
			if i+1 < len(cjk) {
				out = append(out, string(cjk[i:i+2]))
			}
		}
		cjk = cjk[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			cjk = append(cjk, r)
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_':
			flushCJK()
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return out
}

func l2Normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		if len(v) > 0 {
			v[0] = 1
		}
		return v
	}
	n := math.Sqrt(sum)
	for i := range v {
		v[i] = float32(float64(v[i]) / n)
	}
	return v
}

package indexer

import (
	"strings"
	"testing"

	"codesseek/internal/chunker"
)

func TestEmbedTextAddsChineseKeywordsForIdentifiers(t *testing.T) {
	c := chunker.Chunk{
		Path:   "app/composables/useLogin.ts",
		Kind:   "const",
		Symbol: "useLogin",
		Text:   "export const useLogin = () => userApi.getToken()",
	}
	got := embedText(c)
	if !strings.Contains(got, "keywords:") {
		t.Fatalf("embedText missing keyword line: %q", got)
	}
	for _, want := range []string{"登录", "用户", "令牌"} {
		if !strings.Contains(got, want) {
			t.Fatalf("embedText = %q, missing %q", got, want)
		}
	}
	if idx := strings.Index(got, "keywords:"); idx < 0 || idx > strings.Index(got, c.Text) {
		t.Fatalf("keywords must precede the code for truncation safety: %q", got)
	}
}

func TestEmbedTextWithoutKnownIdentifiersKeepsTemplate(t *testing.T) {
	c := chunker.Chunk{Path: "src/a.ts", Kind: "k", Symbol: "qqqq", Text: "x = 1"}
	got := embedText(c)
	if got != "src/a.ts\nk qqqq\nx = 1" {
		t.Fatalf("embedText = %q", got)
	}
}

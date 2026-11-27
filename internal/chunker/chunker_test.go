package chunker

import (
	"strings"
	"testing"
)

func findChunk(chunks []Chunk, kind, symbol string) (Chunk, bool) {
	for _, c := range chunks {
		if c.Kind == kind && c.Symbol == symbol {
			return c, true
		}
	}
	return Chunk{}, false
}

func hasKind(chunks []Chunk, kind string) bool {
	for _, c := range chunks {
		if c.Kind == kind {
			return true
		}
	}
	return false
}

func TestChunkVueSplitsTemplateScriptStyle(t *testing.T) {
	content := `<template>
  <div class="login">
    <span>登录失败</span>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'

function handleLoginError(e: Error) {
  console.error(e)
  return '登录失败'
}
</script>

<style scoped>
.login { color: red; }
</style>
`
	chunks := Split("src/login.vue", content, Config{ChunkSize: 1000, OverlapRatio: 0.15})
	if !hasKind(chunks, "template") {
		t.Fatalf("missing template chunk: %+v", chunks)
	}
	if !hasKind(chunks, "style") {
		t.Fatalf("missing style chunk: %+v", chunks)
	}
	fn, ok := findChunk(chunks, "function", "handleLoginError")
	if !ok {
		t.Fatalf("missing handleLoginError chunk: %+v", chunks)
	}
	if fn.StartLine != 10 || fn.EndLine != 13 {
		t.Fatalf("function lines = %d-%d, want 10-13", fn.StartLine, fn.EndLine)
	}
	if !strings.Contains(fn.Text, "console.error") {
		t.Fatalf("function text missing body: %q", fn.Text)
	}
}

func TestChunkTSFunctionsClassesAndConsts(t *testing.T) {
	content := `import x from 'y'

export function alpha() {
  return 1
}

export class Beta {
  run() {
    return 2
  }
}

const gamma = () => {
  return 3
}
`
	chunks := Split("src/service.ts", content, Config{ChunkSize: 1000, OverlapRatio: 0.15})

	alpha, ok := findChunk(chunks, "function", "alpha")
	if !ok {
		t.Fatalf("missing alpha: %+v", chunks)
	}
	if alpha.StartLine != 3 || alpha.EndLine != 5 {
		t.Fatalf("alpha lines = %d-%d, want 3-5", alpha.StartLine, alpha.EndLine)
	}

	beta, ok := findChunk(chunks, "class", "Beta")
	if !ok {
		t.Fatalf("missing Beta class: %+v", chunks)
	}
	if beta.StartLine != 7 || beta.EndLine != 11 {
		t.Fatalf("Beta lines = %d-%d, want 7-11", beta.StartLine, beta.EndLine)
	}
	if !strings.Contains(beta.Text, "run()") {
		t.Fatalf("Beta text missing method: %q", beta.Text)
	}

	gamma, ok := findChunk(chunks, "const", "gamma")
	if !ok {
		t.Fatalf("missing gamma: %+v", chunks)
	}
	if gamma.StartLine != 13 || gamma.EndLine != 15 {
		t.Fatalf("gamma lines = %d-%d, want 13-15", gamma.StartLine, gamma.EndLine)
	}
}

func TestChunkPythonClassKeepsMethods(t *testing.T) {
	content := `import os

class UserService:
    def login(self):
        return 'ok'

    def logout(self):
        return 'bye'

def helper():
    return 1
`
	chunks := Split("app/service.py", content, Config{ChunkSize: 1000, OverlapRatio: 0.15})

	class, ok := findChunk(chunks, "class", "UserService")
	if !ok {
		t.Fatalf("missing UserService class: %+v", chunks)
	}
	if class.StartLine != 3 || class.EndLine != 8 {
		t.Fatalf("class lines = %d-%d, want 3-8", class.StartLine, class.EndLine)
	}
	if !strings.Contains(class.Text, "def login") || !strings.Contains(class.Text, "def logout") {
		t.Fatalf("class text should contain both methods: %q", class.Text)
	}

	if _, ok := findChunk(chunks, "function", "helper"); !ok {
		t.Fatalf("missing helper function: %+v", chunks)
	}
}

func TestChunkMarkdownSections(t *testing.T) {
	content := `# Title
intro

## 登录失败
some text
### 细节
more
`
	chunks := Split("docs/guide.md", content, Config{ChunkSize: 1000, OverlapRatio: 0.15})
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3: %+v", len(chunks), chunks)
	}
	title, ok := findChunk(chunks, "section", "Title")
	if !ok || title.StartLine != 1 || title.EndLine != 2 {
		t.Fatalf("title chunk = %+v", title)
	}
	login, ok := findChunk(chunks, "section", "登录失败")
	if !ok || login.StartLine != 4 || login.EndLine != 5 {
		t.Fatalf("login chunk = %+v", login)
	}
	detail, ok := findChunk(chunks, "section", "细节")
	if !ok || detail.StartLine != 6 || detail.EndLine != 7 {
		t.Fatalf("detail chunk = %+v", detail)
	}
}

func TestChunkLongFunctionSplitsUnderHardLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("function big() {\n")
	for i := 0; i < 20; i++ {
		b.WriteString("  const value = '0123456789abcdef';\n")
	}
	b.WriteString("}\n")
	content := b.String()

	cfg := Config{ChunkSize: 80, OverlapRatio: 0.15}
	chunks := Split("src/big.ts", content, cfg)
	if len(chunks) < 2 {
		t.Fatalf("want multiple chunks, got %d", len(chunks))
	}
	hard := cfg.ChunkSize * 3 / 2
	for _, c := range chunks {
		if c.Symbol != "big" {
			t.Fatalf("symbol = %q, want big", c.Symbol)
		}
		if n := len([]rune(c.Text)); n > hard {
			t.Fatalf("chunk has %d runes, hard limit %d: %q", n, hard, c.Text)
		}
	}
	if chunks[0].StartLine != 1 {
		t.Fatalf("first start line = %d, want 1", chunks[0].StartLine)
	}
	if last := chunks[len(chunks)-1]; last.EndLine != 22 {
		t.Fatalf("last end line = %d, want 22", last.EndLine)
	}
}

func TestChunkFallbackWindowOverlap(t *testing.T) {
	var lines []string
	for i := 1; i <= 130; i++ {
		lines = append(lines, "line content")
	}
	chunks := Split("notes.txt", strings.Join(lines, "\n"), Config{ChunkSize: 100000, OverlapRatio: 0.15})
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3: %+v", len(chunks), chunks)
	}
	want := [][2]int{{1, 60}, {52, 111}, {103, 130}}
	for i, c := range chunks {
		if c.StartLine != want[i][0] || c.EndLine != want[i][1] {
			t.Fatalf("chunk %d = %d-%d, want %d-%d", i, c.StartLine, c.EndLine, want[i][0], want[i][1])
		}
	}
}

func TestChunkSQLStatements(t *testing.T) {
	content := `SELECT * FROM users;
SELECT * FROM orders
WHERE id = 1;
`
	chunks := Split("db/query.sql", content, Config{ChunkSize: 1000, OverlapRatio: 0.15})
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2: %+v", len(chunks), chunks)
	}
	if chunks[0].StartLine != 1 || chunks[0].EndLine != 1 {
		t.Fatalf("first statement = %d-%d, want 1-1", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[1].StartLine != 2 || chunks[1].EndLine != 3 {
		t.Fatalf("second statement = %d-%d, want 2-3", chunks[1].StartLine, chunks[1].EndLine)
	}
}

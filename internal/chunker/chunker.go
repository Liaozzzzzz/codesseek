// Package chunker splits source files into language-aware chunks.
//
// The chunker is heuristic on purpose: it avoids tree-sitter/CGO while still
// keeping declarations, Vue SFC blocks, Python classes and Markdown sections
// intact. See docs/spec.md §6.
package chunker

import (
	"path/filepath"
	"strings"
)

const (
	defaultChunkSize    = 1000
	defaultOverlapRatio = 0.15
	fallbackWindowLines = 60
)

// Config controls chunk sizes. Zero values fall back to the spec defaults.
type Config struct {
	ChunkSize    int     // soft limit in runes
	OverlapRatio float64 // used by line-window splitting
}

// Normalized returns cfg with defaults applied.
func (c Config) Normalized() Config {
	if c.ChunkSize <= 0 {
		c.ChunkSize = defaultChunkSize
	}
	if c.OverlapRatio <= 0 || c.OverlapRatio >= 1 {
		c.OverlapRatio = defaultOverlapRatio
	}
	return c
}

// HardSize is the absolute per-chunk limit: 1.5 × ChunkSize.
func (c Config) HardSize() int {
	return c.Normalized().ChunkSize * 3 / 2
}

// Chunk is one indexable unit of a file. Line numbers are 1-based and EndLine
// is inclusive.
type Chunk struct {
	Path      string
	StartLine int
	EndLine   int
	Kind      string
	Symbol    string
	Text      string
}

// Split splits content into chunks according to path's extension.
func Split(path, content string, cfg Config) []Chunk {
	cfg = cfg.Normalized()
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")

	var chunks []Chunk
	switch strings.ToLower(filepath.Ext(path)) {
	case ".vue":
		chunks = chunkVue(path, lines, cfg)
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
		".java", ".kt", ".cs", ".go", ".rs":
		chunks = chunkBraces(path, lines, 1, cfg)
	case ".py":
		chunks = chunkPython(path, lines, cfg)
	case ".md":
		chunks = chunkMarkdown(path, lines, cfg)
	case ".sql":
		chunks = chunkSQL(path, lines, cfg)
	default:
		chunks = windowChunks(path, lines, 1, len(lines), "block", "", cfg)
	}

	if len(chunks) == 0 {
		chunks = windowChunks(path, lines, 1, len(lines), "block", "", cfg)
	}
	out := chunks[:0]
	for _, c := range chunks {
		if strings.TrimSpace(c.Text) == "" {
			continue
		}
		c.Path = path
		if c.StartLine < 1 {
			c.StartLine = 1
		}
		if c.EndLine < c.StartLine {
			c.EndLine = c.StartLine
		}
		out = append(out, c)
	}
	return out
}

// joinLines returns the text of lines[from:to] (0-based, end exclusive) and the
// rune count including separators.
func joinLines(lines []string, from, to int) (string, int) {
	if from < 0 {
		from = 0
	}
	if to > len(lines) {
		to = len(lines)
	}
	if from >= to {
		return "", 0
	}
	text := strings.Join(lines[from:to], "\n")
	return text, len([]rune(text))
}

// splitByLimit breaks lines into chunks of at most cfg.HardSize runes,
// preferring line boundaries. baseLine is the 1-based line number of lines[0].
func splitByLimit(path string, lines []string, baseLine int, kind, symbol string, cfg Config) []Chunk {
	hard := cfg.HardSize()
	if hard < 1 {
		hard = 1
	}
	var chunks []Chunk
	start := 0
	runes := 0
	flush := func(end int) {
		if end <= start {
			return
		}
		text, _ := joinLines(lines, start, end)
		if strings.TrimSpace(text) == "" {
			start = end
			runes = 0
			return
		}
		chunks = append(chunks, Chunk{
			Path:      path,
			StartLine: baseLine + start,
			EndLine:   baseLine + end - 1,
			Kind:      kind,
			Symbol:    symbol,
			Text:      text,
		})
		start = end
		runes = 0
	}
	for i, line := range lines {
		n := len([]rune(line))
		if runes > 0 && runes+1+n > hard {
			flush(i)
		}
		if n > hard {
			// A single pathological line: split it by runes while keeping the
			// same line number.
			flush(i)
			rs := []rune(line)
			for from := 0; from < len(rs); from += hard {
				to := from + hard
				if to > len(rs) {
					to = len(rs)
				}
				chunks = append(chunks, Chunk{
					Path:      path,
					StartLine: baseLine + i,
					EndLine:   baseLine + i,
					Kind:      kind,
					Symbol:    symbol,
					Text:      string(rs[from:to]),
				})
			}
			start = i + 1
			runes = 0
			continue
		}
		if runes > 0 {
			runes++
		}
		runes += n
	}
	if start < len(lines) {
		flush(len(lines))
	}
	return chunks
}

// windowChunks applies the fallback 60-line window with overlap.
func windowChunks(path string, lines []string, startLine, endLine int, kind, symbol string, cfg Config) []Chunk {
	if startLine < 1 {
		startLine = 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if endLine < startLine {
		return nil
	}
	overlap := int(float64(fallbackWindowLines) * cfg.OverlapRatio)
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= fallbackWindowLines {
		overlap = fallbackWindowLines - 1
	}
	step := fallbackWindowLines - overlap
	if step < 1 {
		step = 1
	}

	var chunks []Chunk
	for from := startLine; from <= endLine; from += step {
		to := from + fallbackWindowLines - 1
		if to > endLine {
			to = endLine
		}
		part := lines[from-1 : to]
		chunks = append(chunks, splitByLimit(path, part, from, kind, symbol, cfg)...)
		if to == endLine {
			break
		}
	}
	return chunks
}

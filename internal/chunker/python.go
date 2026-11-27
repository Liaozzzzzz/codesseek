package chunker

import (
	"regexp"
	"strings"
)

var pyDeclRe = regexp.MustCompile(`^(?:async\s+)?(class|def)\s+([A-Za-z_]\w*)`)

// chunkPython uses indentation to find top-level classes/functions. Methods are
// kept inside their class chunk unless the class is too large.
func chunkPython(path string, lines []string, cfg Config) []Chunk {
	var chunks []Chunk
	for i := 0; i < len(lines); i++ {
		if indentOf(lines[i]) != 0 {
			continue
		}
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		m := pyDeclRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		keyword, name := m[1], m[2]
		end := i
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if indentOf(lines[j]) <= 0 {
				break
			}
			end = j
		}
		end = trimBlankEnd(lines, i, end)

		block := lines[i : end+1]
		_, runes := joinLines(block, 0, len(block))
		if keyword == "class" && runes > cfg.HardSize() {
			chunks = append(chunks, splitPythonClass(path, lines, i, end, name, cfg)...)
		} else {
			kind := "function"
			if keyword == "class" {
				kind = "class"
			}
			chunks = append(chunks, splitByLimit(path, block, i+1, kind, name, cfg)...)
		}
		i = end
	}
	return chunks
}

func splitPythonClass(path string, lines []string, start, end int, className string, cfg Config) []Chunk {
	var methods []int
	for i := start + 1; i <= end; i++ {
		t := strings.TrimSpace(lines[i])
		if indentOf(lines[i]) > 0 && pyDeclRe.MatchString(t) {
			methods = append(methods, i)
		}
	}
	var chunks []Chunk
	if len(methods) == 0 {
		return splitByLimit(path, lines[start:end+1], start+1, "class", className, cfg)
	}
	headerEnd := trimBlankEnd(lines, start, methods[0]-1)
	chunks = append(chunks, splitByLimit(path, lines[start:headerEnd+1], start+1, "class", className, cfg)...)
	for idx, m := range methods {
		mEnd := end
		if idx+1 < len(methods) {
			mEnd = methods[idx+1] - 1
		}
		mEnd = trimBlankEnd(lines, m, mEnd)
		name := className
		if sub := pyDeclRe.FindStringSubmatch(strings.TrimSpace(lines[m])); sub != nil {
			name += "." + sub[2]
		}
		chunks = append(chunks, splitByLimit(path, lines[m:mEnd+1], m+1, "method", name, cfg)...)
	}
	return chunks
}

func indentOf(line string) int {
	indent := 0
	for _, r := range line {
		switch r {
		case ' ':
			indent++
		case '\t':
			indent += 4
		default:
			return indent
		}
	}
	return indent
}

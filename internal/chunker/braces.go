package chunker

import (
	"regexp"
	"strings"
)

var (
	declRe     = regexp.MustCompile(`(?i)^\s*(?:(?:export|default|declare|pub|public|private|protected|internal|static|final|abstract|async|override|open|sealed|data|readonly|unsafe)\s+)*(function|func|fn|fun|class|interface|enum|struct|type|const|let|var|def)\s+([A-Za-z_$][\w$]*)`)
	goMethodRe = regexp.MustCompile(`^\s*func\s+\([^)]*\)\s*([A-Za-z_]\w*)`)
)

// chunkBraces handles C-like languages by tracking brace depth and cutting at
// top-level declarations. baseLine is the 1-based line number of lines[0].
func chunkBraces(path string, lines []string, baseLine int, cfg Config) []Chunk {
	code, deltas, depthBefore := analyzeLines(lines)
	var chunks []Chunk
	for i := 0; i < len(lines); i++ {
		if depthBefore[i] != 0 {
			continue
		}
		keyword, symbol := declarationOn(code[i])
		if symbol == "" {
			continue
		}
		end := findBlockEnd(code, deltas, i)
		chunks = append(chunks, splitByLimit(path, lines[i:end+1], baseLine+i, kindOf(keyword), symbol, cfg)...)
		i = end
	}
	return chunks
}

func declarationOn(line string) (keyword, symbol string) {
	if m := goMethodRe.FindStringSubmatch(line); m != nil {
		return "func", m[1]
	}
	if m := declRe.FindStringSubmatch(line); m != nil {
		return strings.ToLower(m[1]), m[2]
	}
	return "", ""
}

func kindOf(keyword string) string {
	switch keyword {
	case "func", "fn", "fun", "function", "def":
		return "function"
	case "const", "let", "var":
		return "const"
	default:
		return keyword
	}
}

func findBlockEnd(code []string, deltas []int, start int) int {
	depth := 0
	opened := false
	for j := start; j < len(code); j++ {
		depth += deltas[j]
		if depth > 0 {
			opened = true
		}
		if opened {
			if depth <= 0 {
				return j
			}
			continue
		}
		// No block opened yet: decide whether this is a one-line declaration
		// or a signature continuing on the following lines.
		if j == start {
			trimmed := strings.TrimSpace(code[j])
			if strings.Contains(trimmed, "{") && strings.Contains(trimmed, "}") {
				return j
			}
			if !strings.Contains(trimmed, "{") &&
				!strings.HasSuffix(trimmed, "(") &&
				!strings.HasSuffix(trimmed, ",") {
				return j
			}
		} else if strings.TrimSpace(code[j]) == "" {
			return j - 1
		}
	}
	return len(code) - 1
}

// analyzeLines strips strings/comments and returns brace deltas plus the depth
// at the start of each line.
func analyzeLines(lines []string) (code []string, deltas []int, depthBefore []int) {
	code = make([]string, len(lines))
	deltas = make([]int, len(lines))
	depthBefore = make([]int, len(lines))
	state := lineState{}
	depth := 0
	for i, line := range lines {
		c := stripCode(line, &state)
		code[i] = c
		d := strings.Count(c, "{") - strings.Count(c, "}")
		deltas[i] = d
		depthBefore[i] = depth
		depth += d
	}
	return code, deltas, depthBefore
}

type lineState struct {
	blockComment bool
}

// stripCode removes comments and string contents so braces inside them do not
// affect the depth heuristic.
func stripCode(line string, st *lineState) string {
	var b strings.Builder
	rs := []rune(line)
	var quote rune
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if st.blockComment {
			if c == '*' && i+1 < len(rs) && rs[i+1] == '/' {
				st.blockComment = false
				i++
				b.WriteString("  ")
			} else {
				b.WriteRune(' ')
			}
			continue
		}
		if quote != 0 {
			if c == '\\' && i+1 < len(rs) {
				i++
				b.WriteString("  ")
				continue
			}
			if c == quote {
				quote = 0
			}
			b.WriteRune(' ')
			continue
		}
		if c == '/' && i+1 < len(rs) {
			switch rs[i+1] {
			case '/':
				return b.String()
			case '*':
				st.blockComment = true
				i++
				b.WriteString("  ")
				continue
			}
		}
		switch c {
		case '\'', '"', '`':
			quote = c
			b.WriteRune(' ')
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

func trimBlankEnd(lines []string, start, end int) int {
	if end >= len(lines) {
		end = len(lines) - 1
	}
	for end >= start && strings.TrimSpace(lines[end]) == "" {
		end--
	}
	return end
}

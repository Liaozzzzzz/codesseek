package chunker

import (
	"strings"
)

// chunkSQL splits at semicolons that are outside single-quoted strings.
func chunkSQL(path string, lines []string, cfg Config) []Chunk {
	var chunks []Chunk
	start := 0
	for i, line := range lines {
		if !hasSQLTerminator(line) {
			continue
		}
		end := trimBlankEnd(lines, start, i)
		if end >= start {
			text, _ := joinLines(lines, start, i+1)
			chunks = append(chunks, splitByLimit(path, lines[start:i+1], start+1, "statement", sqlKeyword(text), cfg)...)
		}
		start = i + 1
	}
	if start < len(lines) {
		end := trimBlankEnd(lines, start, len(lines)-1)
		if end >= start {
			text, _ := joinLines(lines, start, end+1)
			chunks = append(chunks, splitByLimit(path, lines[start:end+1], start+1, "statement", sqlKeyword(text), cfg)...)
		}
	}
	return chunks
}

func hasSQLTerminator(line string) bool {
	inQuote := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\'':
			if inQuote && i+1 < len(line) && line[i+1] == '\'' {
				i++
				continue
			}
			inQuote = !inQuote
		case ';':
			if !inQuote {
				return true
			}
		}
	}
	return false
}

func sqlKeyword(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToUpper(strings.Trim(fields[0], "();"))
}

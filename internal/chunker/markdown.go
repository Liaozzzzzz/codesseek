package chunker

import (
	"regexp"
	"strings"
)

var mdHeadingRe = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

// chunkMarkdown splits at headings, one chunk per section.
func chunkMarkdown(path string, lines []string, cfg Config) []Chunk {
	var headings []int
	for i, line := range lines {
		if mdHeadingRe.MatchString(line) {
			headings = append(headings, i)
		}
	}
	if len(headings) == 0 {
		return windowChunks(path, lines, 1, len(lines), "section", "", cfg)
	}

	var chunks []Chunk
	if headings[0] > 0 {
		end := trimBlankEnd(lines, 0, headings[0]-1)
		if end >= 0 {
			chunks = append(chunks, splitByLimit(path, lines[:end+1], 1, "section", "", cfg)...)
		}
	}
	for i, h := range headings {
		end := len(lines) - 1
		if i+1 < len(headings) {
			end = headings[i+1] - 1
		}
		end = trimBlankEnd(lines, h, end)
		if end < h {
			continue
		}
		symbol := strings.TrimSpace(mdHeadingRe.FindStringSubmatch(lines[h])[2])
		chunks = append(chunks, splitByLimit(path, lines[h:end+1], h+1, "section", symbol, cfg)...)
	}
	return chunks
}

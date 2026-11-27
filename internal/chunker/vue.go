package chunker

import "strings"

// chunkVue splits a single-file component into template/script/style blocks.
// Script blocks are chunked with the brace-aware splitter.
func chunkVue(path string, lines []string, cfg Config) []Chunk {
	type block struct {
		kind  string
		start int
		end   int
	}
	var blocks []block
	for i := 0; i < len(lines); i++ {
		kind, ok := vueOpenTag(lines[i])
		if !ok {
			continue
		}
		end := len(lines) - 1
		for j := i + 1; j < len(lines); j++ {
			if vueCloseTag(lines[j], kind) {
				end = j
				break
			}
		}
		blocks = append(blocks, block{kind: kind, start: i, end: end})
		i = end
	}
	if len(blocks) == 0 {
		return windowChunks(path, lines, 1, len(lines), "block", "", cfg)
	}

	var chunks []Chunk
	for _, b := range blocks {
		if b.kind == "script" {
			innerStart := b.start + 1
			innerEnd := b.end - 1
			if innerEnd < innerStart {
				continue
			}
			inner := lines[innerStart : innerEnd+1]
			sub := chunkBraces(path, inner, innerStart+1, cfg)
			if len(sub) == 0 {
				sub = windowChunks(path, inner, 1, len(inner), "script", "", cfg)
				for i := range sub {
					sub[i].StartLine += innerStart
					sub[i].EndLine += innerStart
				}
			}
			chunks = append(chunks, sub...)
			continue
		}
		chunks = append(chunks, splitByLimit(path, lines[b.start:b.end+1], b.start+1, b.kind, "", cfg)...)
	}
	return chunks
}

func vueOpenTag(line string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(line))
	for _, kind := range []string{"template", "script", "style"} {
		prefix := "<" + kind
		if !strings.HasPrefix(t, prefix) {
			continue
		}
		if len(t) == len(prefix) {
			return kind, true
		}
		switch t[len(prefix)] {
		case ' ', '\t', '>', '/':
			return kind, true
		}
	}
	return "", false
}

func vueCloseTag(line, kind string) bool {
	t := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(t, "</"+kind)
}

// Package searcher implements cosine Top-K plus the reranking rules from
// docs/spec.md §8.
package searcher

import (
	"container/heap"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"codesseek/internal/embedder"
	"codesseek/internal/store"
	"codesseek/internal/terms"
	"codesseek/internal/textenc"
)

const (
	defaultTopK          = 8
	defaultSameFileLimit = 2
	staleAfter           = 24 * time.Hour
)

// Options controls one search.
type Options struct {
	TopK          int
	PathPrefix    string
	SameFileLimit int
}

// Result is one ranked code location.
type Result struct {
	Path      string  `json:"path"`
	StartLine int     `json:"startLine"`
	EndLine   int     `json:"endLine"`
	Score     float64 `json:"score"`
	Symbol    string  `json:"symbol,omitempty"`
	Snippet   string  `json:"snippet"`
	Stale     bool    `json:"stale"`
}

// Searcher ranks chunks for a query.
type Searcher struct {
	store *store.Store
	emb   embedder.Embedder
}

// New creates a searcher over an existing index.
func New(st *store.Store, emb embedder.Embedder) (*Searcher, error) {
	if st == nil || emb == nil {
		return nil, fmt.Errorf("searcher: nil store or embedder")
	}
	return &Searcher{store: st, emb: emb}, nil
}

// Search embeds query, scans vectors.bin and reranks the best candidates.
func (s *Searcher) Search(query string, opt Options) ([]Result, error) {
	topK := opt.TopK
	if topK <= 0 {
		topK = defaultTopK
	}
	limit := opt.SameFileLimit
	if limit <= 0 {
		limit = defaultSameFileLimit
	}
	// Chinese questions rarely share characters with English identifiers, so
	// expand both directions before embedding and reranking.
	query = terms.ExpandQuery(query)

	meta, err := s.store.LoadMeta()
	if err != nil {
		return nil, err
	}
	if meta.Model != s.emb.Model() || meta.Dim != s.emb.Dim() {
		return nil, fmt.Errorf("index model %s/%d does not match embedder %s/%d; reindex with --full",
			meta.Model, meta.Dim, s.emb.Model(), s.emb.Dim())
	}
	files, err := s.store.LoadFiles()
	if err != nil {
		return nil, err
	}
	chunks, err := s.store.LoadChunks()
	if err != nil {
		return nil, err
	}
	if len(files) != meta.FileCount || len(chunks) != meta.ChunkCount {
		return nil, fmt.Errorf("%w: meta/files/chunks disagree", store.ErrCorrupt)
	}
	if err := store.ValidateLoaded(meta, files, chunks); err != nil {
		return nil, err
	}

	qv, err := s.emb.EmbedQuery(query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// chunkIndex -> file index, for fast file lookup.
	chunkFile := make([]int, len(chunks))
	for i := range files {
		for j := files[i].ChunkStart; j < files[i].ChunkStart+files[i].ChunkCount; j++ {
			if j >= 0 && j < len(chunkFile) {
				chunkFile[j] = i
			}
		}
	}

	prefix := normalizePrefix(opt.PathPrefix)
	keep := 4 * topK
	if keep < 40 {
		keep = 40
	}
	candidates, err := scanTop(s.store.VectorsPath(), chunks, chunkFile, files, qv, meta.Dim, prefix, keep)
	if err != nil {
		return nil, err
	}

	tokens := queryTokens(query)
	indexedAtStale := time.Since(meta.IndexedAt) > staleAfter

	type candidate struct {
		chunk store.ChunkRecord
		file  store.FileRecord
		score float64
		stale bool
	}
	ranked := make([]candidate, 0, len(candidates))
	lineCache := map[string][]string{}
	for _, c := range candidates {
		chunk := chunks[c.index]
		file := files[chunkFile[c.index]]
		abs := filepath.Join(meta.Root, filepath.FromSlash(chunk.Path))
		lines, readOK := lineCache[abs]
		if lines == nil && !readOK {
			lines, _ = readLines(abs)
			lineCache[abs] = lines
		}
		chunkText := chunkLines(lines, chunk.StartLine, chunk.EndLine)
		pathHit := hitRatio(tokens, strings.ToLower(chunk.Path))
		identifierHit := hitRatio(tokens, strings.ToLower(chunkText))
		score := 0.75*c.cosine + 0.15*pathHit + 0.10*identifierHit

		stale := indexedAtStale
		if info, err := os.Stat(abs); err != nil {
			stale = true
		} else if info.ModTime().Unix() > file.MTime {
			stale = true
		}
		ranked = append(ranked, candidate{chunk: chunk, file: file, score: score, stale: stale})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].chunk.Path != ranked[j].chunk.Path {
			return ranked[i].chunk.Path < ranked[j].chunk.Path
		}
		return ranked[i].chunk.StartLine < ranked[j].chunk.StartLine
	})

	// More than `limit` chunks from one file are downranked, not dropped.
	counts := map[string]int{}
	for i := range ranked {
		counts[ranked[i].chunk.Path]++
		if counts[ranked[i].chunk.Path] > limit {
			ranked[i].score *= 0.85
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].chunk.Path != ranked[j].chunk.Path {
			return ranked[i].chunk.Path < ranked[j].chunk.Path
		}
		return ranked[i].chunk.StartLine < ranked[j].chunk.StartLine
	})
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}

	out := make([]Result, 0, len(ranked))
	for _, c := range ranked {
		abs := filepath.Join(meta.Root, filepath.FromSlash(c.chunk.Path))
		lines := lineCache[abs]
		out = append(out, Result{
			Path:      c.chunk.Path,
			StartLine: c.chunk.StartLine,
			EndLine:   c.chunk.EndLine,
			Score:     c.score,
			Symbol:    c.chunk.Symbol,
			Snippet:   snippet(lines, c.chunk.StartLine, c.chunk.EndLine),
			Stale:     c.stale,
		})
	}
	return out, nil
}

type hit struct {
	index  int
	cosine float64
}

type minHeap []hit

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i].cosine < h[j].cosine }
func (h minHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minHeap) Push(x any)        { *h = append(*h, x.(hit)) }
func (h *minHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// scanTop streams vectors.bin in ~4MB blocks and keeps the best `keep` rows.
func scanTop(path string, chunks []store.ChunkRecord, chunkFile []int, files []store.FileRecord,
	qv []float32, dim int, prefix string, keep int) ([]hit, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("%w: dim %d", store.ErrCorrupt, dim)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open vectors.bin: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	wantSize := int64(len(chunks)) * int64(dim) * 4
	if info.Size() != wantSize {
		return nil, fmt.Errorf("%w: vectors.bin size %d, want %d", store.ErrCorrupt, info.Size(), wantSize)
	}

	rowsPerBlock := (4 << 20) / (dim * 4)
	if rowsPerBlock < 1 {
		rowsPerBlock = 1
	}
	buf := make([]byte, rowsPerBlock*dim*4)
	h := &minHeap{}
	heap.Init(h)

	for start := 0; start < len(chunks); start += rowsPerBlock {
		nRows := rowsPerBlock
		if start+nRows > len(chunks) {
			nRows = len(chunks) - start
		}
		block := buf[:nRows*dim*4]
		if _, err := f.ReadAt(block, int64(start)*int64(dim)*4); err != nil {
			return nil, fmt.Errorf("read vectors.bin: %w", err)
		}
		for r := 0; r < nRows; r++ {
			idx := start + r
			if prefix != "" {
				fi := chunkFile[idx]
				if fi < 0 || fi >= len(files) || !pathMatches(chunks[idx].Path, prefix) {
					continue
				}
			}
			var dot float64
			base := r * dim * 4
			for d := 0; d < dim; d++ {
				v := math.Float32frombits(binary.LittleEndian.Uint32(block[base+d*4:]))
				dot += float64(v) * float64(qv[d])
			}
			if h.Len() < keep {
				heap.Push(h, hit{index: idx, cosine: dot})
				continue
			}
			if dot > (*h)[0].cosine {
				(*h)[0] = hit{index: idx, cosine: dot}
				heap.Fix(h, 0)
			}
		}
	}

	out := make([]hit, h.Len())
	for i := h.Len() - 1; i >= 0; i-- {
		out[i] = heap.Pop(h).(hit)
	}
	return out, nil
}

// queryTokens returns unique lowercase ASCII identifiers plus CJK bigrams.
// Used only for the rerank heuristics; unigrams are too noisy on Chinese prose.
func queryTokens(q string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(tok string) {
		if tok == "" || seen[tok] {
			return
		}
		seen[tok] = true
		out = append(out, tok)
	}
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) > 0 {
			add(string(word))
			word = word[:0]
		}
	}
	flushCJK := func() {
		for i := 0; i+1 < len(cjk); i++ {
			add(string(cjk[i : i+2]))
		}
		cjk = cjk[:0]
	}
	for _, r := range strings.ToLower(q) {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			cjk = append(cjk, r)
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '$':
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

func hitRatio(tokens []string, haystack string) float64 {
	if len(tokens) == 0 {
		return 0
	}
	matched := 0
	for _, tok := range tokens {
		if strings.Contains(haystack, tok) {
			matched++
		}
	}
	return float64(matched) / float64(len(tokens))
}

func normalizePrefix(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	p = strings.TrimPrefix(p, "./")
	return strings.Trim(p, "/")
}

func pathMatches(p, prefix string) bool {
	p = strings.TrimPrefix(filepath.ToSlash(p), "./")
	if !strings.HasPrefix(p, prefix) {
		return false
	}
	return len(p) == len(prefix) || p[len(prefix)] == '/'
}

func chunkLines(lines []string, startLine, endLine int) string {
	if len(lines) == 0 {
		return ""
	}
	from := startLine - 1
	to := endLine
	if from < 0 {
		from = 0
	}
	if to > len(lines) {
		to = len(lines)
	}
	if from >= to {
		return ""
	}
	return strings.Join(lines[from:to], "\n")
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(textenc.Decode(data), "\r\n", "\n")
	return strings.Split(text, "\n"), nil
}

func snippet(lines []string, startLine, endLine int) string {
	if len(lines) == 0 {
		return ""
	}
	from := startLine - 2
	if from < 1 {
		from = 1
	}
	to := endLine + 2
	if to > len(lines) {
		to = len(lines)
	}
	if to-from+1 > 40 {
		to = from + 39
	}
	if to < from || from > len(lines) {
		return ""
	}
	return strings.Join(lines[from-1:to], "\n")
}

// Package indexer scans a repository, chunks changed files, embeds them and
// atomically replaces the project index. See docs/spec.md §5.
package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"codesseek/internal/chunker"
	"codesseek/internal/embedder"
	"codesseek/internal/scanner"
	"codesseek/internal/store"
	"codesseek/internal/terms"
	"codesseek/internal/textenc"
)

// ErrNeedsFull means the chunking parameters, model or dimension changed and
// the existing vectors must not be reused.
var ErrNeedsFull = errors.New("chunk config or model changed; run index --full")

const (
	defaultMaxFileBytes = 1 << 20
	defaultBatchSize    = 16
	defaultWorkers      = 2
)

// Options controls one index run.
type Options struct {
	Root      string
	DataDir   string
	Scan      scanner.Options
	Chunk     chunker.Config
	Embedder  embedder.Embedder // must be safe for concurrent use when Workers > 1
	Full      bool
	BatchSize int
	Workers   int // parallel file workers, default 2 (bounds memory)
	Progress  func(Progress)
}

// Progress is reported while indexing.
type Progress struct {
	Phase     string
	Processed int
	Total     int
	Path      string
}

// Result summarizes one run.
type Result struct {
	Files    int           `json:"files"`
	Added    int           `json:"added"`
	Updated  int           `json:"updated"`
	Removed  int           `json:"removed"`
	Skipped  int           `json:"skipped"`
	Chunks   int           `json:"chunks"`
	Duration time.Duration `json:"-"`
}

// Run indexes opt.Root into opt.DataDir. Concurrent runs for the same project
// are rejected with store.ErrLocked.
func Run(ctx context.Context, opt Options) (Result, error) {
	var res Result
	start := time.Now()
	if opt.Root == "" || opt.DataDir == "" {
		return res, fmt.Errorf("indexer: root and data dir are required")
	}
	if opt.Embedder == nil {
		return res, fmt.Errorf("indexer: embedder is required")
	}
	scanOpt := withScanDefaults(opt.Scan)
	chunkCfg := opt.Chunk.Normalized()
	emb := opt.Embedder
	dim := emb.Dim()
	if dim <= 0 {
		return res, fmt.Errorf("indexer: embedder dim %d", dim)
	}
	batchSize := opt.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	report := func(p Progress) {
		if opt.Progress != nil {
			opt.Progress(p)
		}
	}

	st, err := store.New(opt.DataDir, opt.Root)
	if err != nil {
		return res, err
	}
	unlock, err := st.Lock()
	if err != nil {
		return res, err
	}
	defer func() { _ = unlock() }()

	hash := ConfigHash(chunkCfg, emb.Model(), dim)
	var oldFiles []store.FileRecord
	var oldChunks []store.ChunkRecord

	if st.Exists() {
		if opt.Full {
			// A full rebuild also has to recover from a damaged index, so the
			// previous records are best-effort only (used for stats).
			oldFiles, _ = st.LoadFiles()
		} else {
			oldMeta, err := st.LoadMeta()
			if err != nil {
				return res, err
			}
			if oldMeta.Model != emb.Model() || oldMeta.Dim != dim || oldMeta.ChunkConfigHash != hash {
				return res, ErrNeedsFull
			}
			if err := st.Validate(); err != nil {
				return res, err
			}
			oldFiles, err = st.LoadFiles()
			if err != nil {
				return res, err
			}
			oldChunks, err = st.LoadChunks()
			if err != nil {
				return res, err
			}
		}
	}
	oldByPath := make(map[string]store.FileRecord, len(oldFiles))
	for _, f := range oldFiles {
		oldByPath[f.Path] = f
	}

	report(Progress{Phase: "scan"})
	files, err := scanner.Scan(opt.Root, scanOpt)
	if err != nil {
		return res, err
	}
	report(Progress{Phase: "index", Total: len(files)})

	var fileRecords []store.FileRecord
	var chunks []store.ChunkRecord
	var vectors []float32
	seen := make(map[string]bool, len(files))

	workers := opt.Workers
	if workers <= 0 {
		workers = defaultWorkers
	}
	if workers > len(files) {
		workers = len(files)
	}
	if workers < 1 {
		workers = 1
	}

	outcomes := make([]fileOutcome, len(files))
	errs := make([]error, len(files))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var processed int64
	for i, f := range files {
		wg.Add(1)
		go func(i int, f scanner.File) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := ctx.Err(); err != nil {
				errs[i] = err
			} else {
				outcomes[i], errs[i] = processFile(f, st, oldByPath, oldChunks, dim, chunkCfg, emb, batchSize, opt.Full)
			}
			n := atomic.AddInt64(&processed, 1)
			report(Progress{Phase: "index", Processed: int(n), Total: len(files), Path: f.Path})
		}(i, f)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return res, err
		}
	}
	for _, out := range outcomes {
		if out.status == "" {
			continue // unreadable or empty file: skip silently
		}
		out.record.ChunkStart = len(chunks)
		fileRecords = append(fileRecords, out.record)
		chunks = append(chunks, out.chunks...)
		vectors = append(vectors, out.vectors...)
		seen[out.record.Path] = true
		switch out.status {
		case "added":
			res.Added++
		case "updated":
			res.Updated++
		case "skipped":
			res.Skipped++
		}
	}
	for _, old := range oldFiles {
		if !seen[old.Path] {
			res.Removed++
		}
	}

	meta := store.Meta{
		Version:         1,
		Root:            st.Root(),
		Model:           emb.Model(),
		Dim:             dim,
		ChunkConfigHash: hash,
		FileCount:       len(fileRecords),
		ChunkCount:      len(chunks),
		IndexedAt:       time.Now().UTC(),
		DurationMs:      time.Since(start).Milliseconds(),
	}
	if err := st.Write(meta, fileRecords, chunks, vectors); err != nil {
		return res, err
	}
	res.Files = len(fileRecords)
	res.Chunks = len(chunks)
	res.Duration = time.Since(start)
	report(Progress{Phase: "done", Processed: len(files), Total: len(files)})
	return res, nil
}

// fileOutcome is the per-file result of one worker. ChunkStart is assigned
// later, when outcomes are assembled in scan order.
type fileOutcome struct {
	record  store.FileRecord
	chunks  []store.ChunkRecord
	vectors []float32
	status  string // added | updated | skipped | "" (ignored)
}

func processFile(f scanner.File, st *store.Store, oldByPath map[string]store.FileRecord,
	oldChunks []store.ChunkRecord, dim int, cfg chunker.Config, emb embedder.Embedder,
	batchSize int, full bool) (fileOutcome, error) {
	var out fileOutcome

	data, err := os.ReadFile(f.AbsPath)
	if err != nil {
		// Single unreadable file: skip, do not abort the batch.
		return out, nil
	}
	sum := sha256.Sum256(data)
	fileSHA := hex.EncodeToString(sum[:])
	out.record = store.FileRecord{
		Path:   f.Path,
		Size:   f.Size,
		MTime:  f.MTime,
		SHA256: fileSHA,
	}

	if !full {
		if old, ok := oldByPath[f.Path]; ok && old.SHA256 == fileSHA && validRange(old, len(oldChunks)) {
			vecs, err := st.ReadVectors(old.ChunkStart, old.ChunkCount, dim)
			if err != nil {
				return out, fmt.Errorf("reuse vectors for %s: %w", f.Path, err)
			}
			out.chunks = append(out.chunks, oldChunks[old.ChunkStart:old.ChunkStart+old.ChunkCount]...)
			out.vectors = vecs
			out.record.ChunkCount = old.ChunkCount
			out.status = "skipped"
			return out, nil
		}
	}

	parts := chunker.Split(f.Path, textenc.Decode(data), cfg)
	if len(parts) == 0 {
		return out, nil
	}
	texts := make([]string, len(parts))
	for j, c := range parts {
		texts[j] = embedText(c)
	}
	vecs, err := embedBatched(emb, texts, batchSize)
	if err != nil {
		return out, fmt.Errorf("embed %s: %w", f.Path, err)
	}
	if len(vecs) != len(parts) {
		return out, fmt.Errorf("embed %s: got %d vectors for %d chunks", f.Path, len(vecs), len(parts))
	}
	out.chunks = make([]store.ChunkRecord, len(parts))
	out.vectors = make([]float32, 0, len(parts)*dim)
	for j, c := range parts {
		if len(vecs[j]) != dim {
			return out, fmt.Errorf("embed %s: vector dim %d, want %d", f.Path, len(vecs[j]), dim)
		}
		chunkSum := sha256.Sum256([]byte(c.Text))
		out.chunks[j] = store.ChunkRecord{
			Path:      c.Path,
			StartLine: c.StartLine,
			EndLine:   c.EndLine,
			Kind:      c.Kind,
			Symbol:    c.Symbol,
			SHA256:    hex.EncodeToString(chunkSum[:]),
		}
		out.vectors = append(out.vectors, vecs[j]...)
	}
	out.record.ChunkCount = len(parts)
	if _, ok := oldByPath[f.Path]; ok {
		out.status = "updated"
	} else {
		out.status = "added"
	}
	return out, nil
}

// ConfigHash covers every input that changes the meaning of stored vectors.
// Bump the leading version when the chunker or text normalization changes.
func ConfigHash(cfg chunker.Config, model string, dim int) string {
	c := cfg.Normalized()
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "v4|chunkSize=%d|overlap=%.6f|model=%s|dim=%d|encoding=gb18030|template=path\\nkind symbol\\nkeywords\\ncode",
		c.ChunkSize, c.OverlapRatio, model, dim)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func embedText(c chunker.Chunk) string {
	head := c.Kind
	if c.Symbol != "" {
		head += " " + c.Symbol
	}
	base := c.Path + "\n" + head + "\n"
	// Chinese hints let the zh model connect English identifiers to a Chinese
	// query. They go before the code so truncation cannot drop them.
	if kws := terms.DocKeywords(c.Path + " " + c.Symbol + " " + c.Text); len(kws) > 0 {
		base += "keywords: " + strings.Join(kws, " ") + "\n"
	}
	return base + c.Text
}

func embedBatched(emb embedder.Embedder, texts []string, batchSize int) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		end := start + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := emb.EmbedDocuments(texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func validRange(f store.FileRecord, chunkCount int) bool {
	return f.ChunkStart >= 0 && f.ChunkCount >= 0 && f.ChunkStart+f.ChunkCount <= chunkCount
}

func withScanDefaults(opt scanner.Options) scanner.Options {
	if opt.Include == nil {
		opt.Include = scanner.DefaultInclude
	}
	if opt.ExcludeDirs == nil {
		opt.ExcludeDirs = scanner.DefaultExcludeDirs
	}
	if opt.ExcludeGlobs == nil {
		opt.ExcludeGlobs = scanner.DefaultExcludeGlobs
	}
	if opt.MaxFileBytes <= 0 {
		opt.MaxFileBytes = defaultMaxFileBytes
	}
	return opt
}

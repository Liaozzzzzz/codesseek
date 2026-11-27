// Package store owns the on-disk index for one project.
//
// Layout (docs/spec.md §3):
//
//	<dataDir>/projects/<sha1(root)>/
//	    meta.json
//	    files.jsonl
//	    chunks.jsonl
//	    vectors.bin
//	    lock
package store

import (
	"bufio"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotFound means the project has not been indexed yet.
var ErrNotFound = errors.New("index not found")

// ErrLocked means another index run holds the project lock.
var ErrLocked = errors.New("index locked by another process")

// ErrCorrupt means the on-disk index is internally inconsistent.
var ErrCorrupt = errors.New("index corrupt")

// Meta is meta.json.
type Meta struct {
	Version         int       `json:"version"`
	Root            string    `json:"root"`
	Model           string    `json:"model"`
	Dim             int       `json:"dim"`
	ChunkConfigHash string    `json:"chunkConfigHash"`
	FileCount       int       `json:"fileCount"`
	ChunkCount      int       `json:"chunkCount"`
	IndexedAt       time.Time `json:"indexedAt"`
	DurationMs      int64     `json:"durationMs"`
}

// FileRecord is one line of files.jsonl.
type FileRecord struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	MTime      int64  `json:"mtime"`
	SHA256     string `json:"sha256"`
	ChunkStart int    `json:"chunkStart"`
	ChunkCount int    `json:"chunkCount"`
}

// ChunkRecord is one line of chunks.jsonl. Source text is not stored.
type ChunkRecord struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Kind      string `json:"kind"`
	Symbol    string `json:"symbol,omitempty"`
	SHA256    string `json:"sha256"`
}

// Store is a handle to one project's index directory.
type Store struct {
	dir    string
	root   string
	rootID string
}

// New resolves the project index directory for root under dataDir. It does not
// create or read anything.
func New(dataDir, root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	id := ProjectID(abs)
	return &Store{
		dir:    filepath.Join(dataDir, "projects", id),
		root:   abs,
		rootID: id,
	}, nil
}

// ProjectID returns sha1 of the case-folded absolute path.
func ProjectID(absRoot string) string {
	sum := sha1.Sum([]byte(strings.ToLower(filepath.Clean(absRoot))))
	return hex.EncodeToString(sum[:])
}

func (s *Store) Dir() string         { return s.dir }
func (s *Store) Root() string        { return s.root }
func (s *Store) ID() string          { return s.rootID }
func (s *Store) metaPath() string    { return filepath.Join(s.dir, "meta.json") }
func (s *Store) filesPath() string   { return filepath.Join(s.dir, "files.jsonl") }
func (s *Store) chunksPath() string  { return filepath.Join(s.dir, "chunks.jsonl") }
func (s *Store) vectorsPath() string { return filepath.Join(s.dir, "vectors.bin") }
func (s *Store) lockPath() string    { return filepath.Join(s.dir, "lock") }

// Exists reports whether meta.json is present.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.metaPath())
	return err == nil
}

// LoadMeta reads meta.json.
func (s *Store) LoadMeta() (*Meta, error) {
	data, err := os.ReadFile(s.metaPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read meta: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("%w: meta.json: %v", ErrCorrupt, err)
	}
	if meta.Version != 1 {
		return nil, fmt.Errorf("%w: unsupported meta version %d", ErrCorrupt, meta.Version)
	}
	return &meta, nil
}

// LoadFiles reads files.jsonl.
func (s *Store) LoadFiles() ([]FileRecord, error) {
	file, err := os.Open(s.filesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("open files.jsonl: %w", err)
	}
	defer file.Close()
	var out []FileRecord
	if err := decodeJSONL(file, &out); err != nil {
		return nil, fmt.Errorf("%w: files.jsonl: %v", ErrCorrupt, err)
	}
	return out, nil
}

// LoadChunks reads chunks.jsonl.
func (s *Store) LoadChunks() ([]ChunkRecord, error) {
	file, err := os.Open(s.chunksPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("open chunks.jsonl: %w", err)
	}
	defer file.Close()
	var out []ChunkRecord
	if err := decodeJSONL(file, &out); err != nil {
		return nil, fmt.Errorf("%w: chunks.jsonl: %v", ErrCorrupt, err)
	}
	return out, nil
}

func decodeJSONL(r io.Reader, out any) error {
	br := bufio.NewReader(r)
	dec := json.NewDecoder(br)
	for {
		var raw json.RawMessage
		err := dec.Decode(&raw)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch dst := out.(type) {
		case *[]FileRecord:
			var rec FileRecord
			if err := json.Unmarshal(raw, &rec); err != nil {
				return err
			}
			*dst = append(*dst, rec)
		case *[]ChunkRecord:
			var rec ChunkRecord
			if err := json.Unmarshal(raw, &rec); err != nil {
				return err
			}
			*dst = append(*dst, rec)
		default:
			return fmt.Errorf("unsupported type %T", out)
		}
	}
}

// ReadVectors returns count vectors starting at start, each of length dim.
func (s *Store) ReadVectors(start, count, dim int) ([]float32, error) {
	if start < 0 || count < 0 || dim <= 0 {
		return nil, fmt.Errorf("%w: bad vector range start=%d count=%d dim=%d", ErrCorrupt, start, count, dim)
	}
	file, err := os.Open(s.vectorsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("open vectors.bin: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := int64(start) * int64(dim) * 4
	size := int64(count) * int64(dim) * 4
	if offset+size > info.Size() {
		return nil, fmt.Errorf("%w: vectors.bin size %d, need [%d,%d)", ErrCorrupt, info.Size(), offset, offset+size)
	}
	buf := make([]byte, size)
	if _, err := file.ReadAt(buf, offset); err != nil {
		return nil, fmt.Errorf("read vectors.bin: %w", err)
	}
	out := make([]float32, count*dim)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return out, nil
}

// VectorsPath exposes vectors.bin for streaming readers (the searcher).
func (s *Store) VectorsPath() string { return s.vectorsPath() }

// Validate checks meta/files/chunks/vectors consistency.
func (s *Store) Validate() error {
	meta, err := s.LoadMeta()
	if err != nil {
		return err
	}
	files, err := s.LoadFiles()
	if err != nil {
		return err
	}
	chunks, err := s.LoadChunks()
	if err != nil {
		return err
	}
	if err := ValidateLoaded(meta, files, chunks); err != nil {
		return err
	}
	info, err := os.Stat(s.vectorsPath())
	if err != nil {
		return fmt.Errorf("%w: vectors.bin: %v", ErrCorrupt, err)
	}
	wantSize := int64(len(chunks)) * int64(meta.Dim) * 4
	if info.Size() != wantSize {
		return fmt.Errorf("%w: vectors.bin size %d, want %d", ErrCorrupt, info.Size(), wantSize)
	}
	return nil
}

// ValidateLoaded checks the already-loaded records. It does not touch disk.
func ValidateLoaded(meta *Meta, files []FileRecord, chunks []ChunkRecord) error {
	if meta.Dim <= 0 {
		return fmt.Errorf("%w: dim %d", ErrCorrupt, meta.Dim)
	}
	if len(files) != meta.FileCount {
		return fmt.Errorf("%w: fileCount meta=%d files=%d", ErrCorrupt, meta.FileCount, len(files))
	}
	if len(chunks) != meta.ChunkCount {
		return fmt.Errorf("%w: chunkCount meta=%d chunks=%d", ErrCorrupt, meta.ChunkCount, len(chunks))
	}

	next := 0
	for _, f := range files {
		if f.ChunkStart != next {
			return fmt.Errorf("%w: file %s chunkStart %d, want %d", ErrCorrupt, f.Path, f.ChunkStart, next)
		}
		if f.ChunkCount < 0 || f.ChunkStart+f.ChunkCount > len(chunks) {
			return fmt.Errorf("%w: file %s chunk range [%d,%d)", ErrCorrupt, f.Path, f.ChunkStart, f.ChunkStart+f.ChunkCount)
		}
		for j := f.ChunkStart; j < f.ChunkStart+f.ChunkCount; j++ {
			c := chunks[j]
			if c.Path != f.Path {
				return fmt.Errorf("%w: chunk %d path %s, want %s", ErrCorrupt, j, c.Path, f.Path)
			}
			if c.StartLine < 1 || c.EndLine < c.StartLine {
				return fmt.Errorf("%w: chunk %d lines %d-%d", ErrCorrupt, j, c.StartLine, c.EndLine)
			}
		}
		next += f.ChunkCount
	}
	if next != len(chunks) {
		return fmt.Errorf("%w: files cover %d chunks, chunks.jsonl has %d", ErrCorrupt, next, len(chunks))
	}
	return nil
}

// Write replaces meta/files/chunks/vectors atomically (meta last).
func (s *Store) Write(meta Meta, files []FileRecord, chunks []ChunkRecord, vectors []float32) error {
	if meta.Dim <= 0 {
		return fmt.Errorf("%w: dim %d", ErrCorrupt, meta.Dim)
	}
	if len(vectors) != len(chunks)*meta.Dim {
		return fmt.Errorf("%w: %d vectors for %d chunks x dim %d", ErrCorrupt, len(vectors), len(chunks), meta.Dim)
	}
	meta.Version = 1
	meta.FileCount = len(files)
	meta.ChunkCount = len(chunks)
	if meta.IndexedAt.IsZero() {
		meta.IndexedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create index dir: %w", err)
	}

	if err := writeJSONLAtomic(s.filesPath(), files); err != nil {
		return err
	}
	if err := writeJSONLAtomic(s.chunksPath(), chunks); err != nil {
		return err
	}
	if err := writeVectorsAtomic(s.vectorsPath(), vectors); err != nil {
		return err
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomic(s.metaPath(), data); err != nil {
		return err
	}
	return nil
}

// Lock creates the project lock file. The returned function releases it.
func (s *Store) Lock() (func() error, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create index dir: %w", err)
	}
	f, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("create lock: %w", err)
	}
	_, _ = fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	_ = f.Close()
	return func() error {
		err := os.Remove(s.lockPath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}, nil
}

func writeJSONLAtomic(path string, rows any) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	enc := json.NewEncoder(f)
	failed := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	switch rows := rows.(type) {
	case []FileRecord:
		for _, rec := range rows {
			if err := enc.Encode(rec); err != nil {
				return failed(fmt.Errorf("write %s: %w", tmp, err))
			}
		}
	case []ChunkRecord:
		for _, rec := range rows {
			if err := enc.Encode(rec); err != nil {
				return failed(fmt.Errorf("write %s: %w", tmp, err))
			}
		}
	default:
		return failed(fmt.Errorf("unsupported rows type %T", rows))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func writeVectorsAtomic(path string, vectors []float32) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	buf := make([]byte, 1<<20)
	used := 0
	flush := func() error {
		if used == 0 {
			return nil
		}
		_, err := f.Write(buf[:used])
		used = 0
		return err
	}
	for _, v := range vectors {
		if used+4 > len(buf) {
			if err := flush(); err != nil {
				_ = f.Close()
				_ = os.Remove(tmp)
				return fmt.Errorf("write %s: %w", tmp, err)
			}
		}
		binary.LittleEndian.PutUint32(buf[used:], math.Float32bits(v))
		used += 4
	}
	if err := flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

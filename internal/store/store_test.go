package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	s, err := New(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func sampleIndex() (Meta, []FileRecord, []ChunkRecord, []float32) {
	meta := Meta{
		Version:         1,
		Model:           "fake-hash-4",
		Dim:             4,
		ChunkConfigHash: "sha256:test",
		IndexedAt:       time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC),
	}
	files := []FileRecord{
		{Path: "a.ts", Size: 10, MTime: 100, SHA256: "aa", ChunkStart: 0, ChunkCount: 2},
		{Path: "b.md", Size: 20, MTime: 200, SHA256: "bb", ChunkStart: 2, ChunkCount: 1},
	}
	chunks := []ChunkRecord{
		{Path: "a.ts", StartLine: 1, EndLine: 2, Kind: "function", Symbol: "a", SHA256: "c1"},
		{Path: "a.ts", StartLine: 3, EndLine: 4, Kind: "function", Symbol: "b", SHA256: "c2"},
		{Path: "b.md", StartLine: 1, EndLine: 1, Kind: "section", Symbol: "B", SHA256: "c3"},
	}
	vectors := []float32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	return meta, files, chunks, vectors
}

func TestWriteReadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	meta, files, chunks, vectors := sampleIndex()
	meta.Root = s.Root()

	if s.Exists() {
		t.Fatal("store should not exist before Write")
	}
	if err := s.Write(meta, files, chunks, vectors); err != nil {
		t.Fatal(err)
	}
	if !s.Exists() {
		t.Fatal("store should exist after Write")
	}

	gotMeta, err := s.LoadMeta()
	if err != nil {
		t.Fatal(err)
	}
	if gotMeta.FileCount != 2 || gotMeta.ChunkCount != 3 {
		t.Fatalf("meta counts = %d/%d, want 2/3", gotMeta.FileCount, gotMeta.ChunkCount)
	}
	gotFiles, err := s.LoadFiles()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotFiles, files) {
		t.Fatalf("files = %+v, want %+v", gotFiles, files)
	}
	gotChunks, err := s.LoadChunks()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotChunks, chunks) {
		t.Fatalf("chunks = %+v, want %+v", gotChunks, chunks)
	}
	gotVec, err := s.ReadVectors(1, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []float32{5, 6, 7, 8, 9, 10, 11, 12}
	if !reflect.DeepEqual(gotVec, want) {
		t.Fatalf("vectors = %v, want %v", gotVec, want)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateDetectsCorruptVectors(t *testing.T) {
	s := newTestStore(t)
	meta, files, chunks, vectors := sampleIndex()
	meta.Root = s.Root()
	if err := s.Write(meta, files, chunks, vectors); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir(), "vectors.bin")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-4); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Validate = %v, want ErrCorrupt", err)
	}
}

func TestWriteReplacesPreviousIndex(t *testing.T) {
	s := newTestStore(t)
	meta, files, chunks, vectors := sampleIndex()
	meta.Root = "old"
	if err := s.Write(meta, files, chunks, vectors); err != nil {
		t.Fatal(err)
	}

	meta.Root = "new"
	meta.FileCount = 99 // should be overwritten from len(files)
	small := []FileRecord{files[0]}
	small[0].ChunkCount = 1
	if err := s.Write(meta, small, chunks[:1], vectors[:4]); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadMeta()
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != "new" || got.FileCount != 1 || got.ChunkCount != 1 {
		t.Fatalf("meta = %+v", got)
	}
	tmp, err := filepath.Glob(filepath.Join(s.Dir(), "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmp) != 0 {
		t.Fatalf("temp files left behind: %v", tmp)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestLockRejectsConcurrentWriter(t *testing.T) {
	s := newTestStore(t)
	unlock, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Lock = %v, want ErrLocked", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	unlock2, err := s.Lock()
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	if err := unlock2(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDetectsChunkRangeCorruption(t *testing.T) {
	s := newTestStore(t)
	meta, files, chunks, vectors := sampleIndex()
	meta.Root = s.Root()
	files[1].ChunkStart = 1 // overlaps a.ts's [0,2)
	if err := s.Write(meta, files, chunks, vectors); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Validate = %v, want ErrCorrupt", err)
	}
}

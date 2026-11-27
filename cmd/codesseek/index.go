package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"codesseek/internal/chunker"
	"codesseek/internal/config"
	"codesseek/internal/indexer"
	"codesseek/internal/store"
)

func runIndex(args []string) int {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	full := fs.Bool("full", false, "rebuild all vectors (required after chunk/model changes)")
	workers := fs.Int("workers", 0, "parallel file workers (default from config)")
	quiet := fs.Bool("quiet", false, "suppress progress output")
	positional, err := parseInterspersed(fs, args, map[string]bool{"workers": true})
	if err != nil {
		return exitUsage
	}
	if len(positional) > 1 {
		fmt.Fprintln(os.Stderr, "codesseek: index takes at most one path")
		return exitUsage
	}
	path := ""
	if len(positional) == 1 {
		path = positional[0]
	}
	root, err := resolveDir(path)
	if err != nil {
		return fail(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		return fail(err)
	}
	if *workers > 0 {
		cfg.Workers = *workers
	}

	st, err := store.New(cfg.DataDir, root)
	if err != nil {
		return fail(err)
	}
	emb, closeEmb, err := indexEmbedder(cfg, st, *full)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codesseek: cannot load the embedding model\n  %v\n", err)
		fmt.Fprintf(os.Stderr, "  model dir: %s\n", cfg.ModelPath())
		fmt.Fprintf(os.Stderr, "  hint: run `codesseek model download`, or put onnxruntime.dll next to codesseek.exe / set ONNXRUNTIME_DLL\n")
		return exitError
	}
	defer closeEmb()

	fmt.Fprintf(os.Stderr, "indexing %s\n", root)
	res, err := indexer.Run(context.Background(), indexer.Options{
		Root:     root,
		DataDir:  cfg.DataDir,
		Scan:     cfg.ScannerOptions(),
		Chunk:    chunker.Config{ChunkSize: cfg.ChunkSize, OverlapRatio: cfg.OverlapRatio},
		Embedder: emb,
		Full:     *full,
		Workers:  cfg.Workers,
		Progress: makeProgress(!*quiet),
	})
	switch {
	case errors.Is(err, indexer.ErrNeedsFull):
		fmt.Fprintln(os.Stderr, "codesseek: chunk config or embedding model changed since the last index")
		fmt.Fprintln(os.Stderr, "          rerun with --full to rebuild all vectors")
		return exitError
	case errors.Is(err, store.ErrLocked):
		fmt.Fprintln(os.Stderr, "codesseek: another index run is already active for this project")
		return exitError
	case err != nil:
		return fail(err)
	}

	fmt.Fprintf(stdout, "%s\n", root)
	fmt.Fprintf(stdout, "  files     %d (added %d, updated %d, removed %d, skipped %d)\n",
		res.Files, res.Added, res.Updated, res.Removed, res.Skipped)
	fmt.Fprintf(stdout, "  chunks    %d\n", res.Chunks)
	fmt.Fprintf(stdout, "  duration  %s\n", res.Duration.Round(time.Millisecond))
	fmt.Fprintf(stdout, "  index     %s\n", st.Dir())
	return exitOK
}

// makeProgress returns a thread-safe stderr reporter; indexer workers may call
// it concurrently. It prints one line per ~100 files instead of a spinner so
// redirected logs stay readable.
func makeProgress(enabled bool) func(indexer.Progress) {
	if !enabled {
		return nil
	}
	var mu sync.Mutex
	last := 0
	return func(p indexer.Progress) {
		mu.Lock()
		defer mu.Unlock()
		switch p.Phase {
		case "scan":
			fmt.Fprintln(os.Stderr, "scanning files...")
		case "index":
			if p.Total == 0 {
				return
			}
			if p.Processed-last >= 100 || (p.Processed == p.Total && p.Processed != last) {
				last = p.Processed
				fmt.Fprintf(os.Stderr, "  %d/%d files\n", p.Processed, p.Total)
			}
		case "done":
			fmt.Fprintln(os.Stderr, "index complete")
		}
	}
}

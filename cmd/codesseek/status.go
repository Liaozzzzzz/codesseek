package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"codesseek/internal/config"
	"codesseek/internal/store"
)

func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	positional, err := parseInterspersed(fs, args, nil)
	if err != nil {
		return exitUsage
	}
	if len(positional) > 1 {
		fmt.Fprintln(os.Stderr, "codesseek: status takes at most one path")
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
	st, err := store.New(cfg.DataDir, root)
	if err != nil {
		return fail(err)
	}
	meta, err := st.LoadMeta()
	if errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "codesseek: project not indexed: %s\n", root)
		return exitNotIndexed
	}
	if err != nil {
		return fail(err)
	}

	fmt.Fprintf(stdout, "root:      %s\n", meta.Root)
	fmt.Fprintf(stdout, "index:     %s\n", st.Dir())
	fmt.Fprintf(stdout, "model:     %s (%d dims)\n", meta.Model, meta.Dim)
	fmt.Fprintf(stdout, "files:     %d\n", meta.FileCount)
	fmt.Fprintf(stdout, "chunks:    %d\n", meta.ChunkCount)
	fmt.Fprintf(stdout, "indexedAt: %s\n", meta.IndexedAt.Local().Format(time.RFC3339))
	if meta.DurationMs > 0 {
		fmt.Fprintf(stdout, "duration:  %s\n", time.Duration(meta.DurationMs)*time.Millisecond)
	}
	if time.Since(meta.IndexedAt) > 24*time.Hour {
		fmt.Fprintln(stdout, "warning:   index is older than 24h; search results are marked stale")
	}
	return exitOK
}

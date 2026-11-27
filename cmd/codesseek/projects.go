package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"codesseek/internal/config"
	"codesseek/internal/store"
)

func runProjects(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "codesseek: projects takes no arguments")
		return exitUsage
	}
	cfg, err := config.Load("")
	if err != nil {
		return fail(err)
	}
	metas, err := listProjectMetas(cfg.DataDir)
	if err != nil {
		return fail(err)
	}
	if len(metas) == 0 {
		fmt.Fprintf(stdout, "no indexed projects under %s\n", cfg.DataDir)
		return exitOK
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].IndexedAt.After(metas[j].IndexedAt) })

	fmt.Fprintf(stdout, "%-8s %-9s %-5s %-22s %s\n", "FILES", "CHUNKS", "DIM", "INDEXED", "ROOT")
	for _, m := range metas {
		fmt.Fprintf(stdout, "%-8d %-9d %-5d %-22s %s\n",
			m.FileCount, m.ChunkCount, m.Dim,
			m.IndexedAt.Local().Format("2006-01-02 15:04:05"), m.Root)
	}
	return exitOK
}

// listProjectMetas reads every project index under dataDir. Broken entries are
// skipped: one corrupt project must not hide the rest.
func listProjectMetas(dataDir string) ([]store.Meta, error) {
	matches, err := filepath.Glob(filepath.Join(dataDir, "projects", "*", "meta.json"))
	if err != nil {
		return nil, err
	}
	var metas []store.Meta
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var meta store.Meta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		metas = append(metas, meta)
	}
	return metas, nil
}

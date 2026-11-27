package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"codesseek/internal/config"
	"codesseek/internal/searcher"
	"codesseek/internal/store"
)

func runSearch(args []string) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	pathFlag := fs.String("path", "", "project root (default: current directory)")
	top := fs.Int("top", 0, "number of results (default: nResult)")
	asJSON := fs.Bool("json", false, "output JSON")
	prefix := fs.String("prefix", "", "restrict results to a path prefix")
	positional, err := parseInterspersed(fs, args, map[string]bool{"path": true, "top": true, "prefix": true})
	if err != nil {
		return exitUsage
	}
	if len(positional) == 0 {
		fmt.Fprintln(os.Stderr, "codesseek: search needs a query")
		return exitUsage
	}
	query := strings.Join(positional, " ")

	root, err := resolveDir(*pathFlag)
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
		fmt.Fprintf(os.Stderr, "          run `codesseek index %s` first\n", root)
		return exitNotIndexed
	}
	if err != nil {
		return fail(err)
	}

	emb, closeEmb, err := embedderForMeta(cfg, meta)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codesseek: cannot load the embedding model for this index\n  %v\n", err)
		return exitError
	}
	defer closeEmb()

	s, err := searcher.New(st, emb)
	if err != nil {
		return fail(err)
	}
	topK := *top
	if topK <= 0 {
		topK = cfg.NResult
	}
	results, err := s.Search(query, searcher.Options{TopK: topK, PathPrefix: *prefix})
	if errors.Is(err, store.ErrCorrupt) {
		fmt.Fprintln(os.Stderr, "codesseek: indexed files are inconsistent; rerun `codesseek index --full`")
		return exitError
	}
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		return printJSON(results)
	}
	printHuman(query, results)
	return exitOK
}

func printHuman(query string, results []searcher.Result) {
	if len(results) == 0 {
		fmt.Fprintf(stdout, "no results for %q\n", query)
		return
	}
	for _, r := range results {
		header := fmt.Sprintf("%s:%d-%d  %.3f", r.Path, r.StartLine, r.EndLine, r.Score)
		if r.Symbol != "" {
			header += "  " + r.Symbol
		}
		if r.Stale {
			header += "  [stale]"
		}
		fmt.Fprintln(stdout, header)
		snippet := strings.TrimRight(r.Snippet, "\n")
		if snippet != "" {
			for _, line := range strings.Split(snippet, "\n") {
				fmt.Fprintln(stdout, "    "+line)
			}
		}
		fmt.Fprintln(stdout)
	}
}

func printJSON(results []searcher.Result) int {
	if results == nil {
		results = []searcher.Result{}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		return fail(err)
	}
	return exitOK
}

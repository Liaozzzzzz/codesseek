package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codesseek/internal/assets"
	"codesseek/internal/config"
)

// The Xenova conversion keeps the BAAI weights but ships ready-made ONNX.
const (
	defaultHFEndpoint = "https://huggingface.co"
	modelRepo         = "Xenova/bge-small-zh-v1.5"
)

var modelFiles = []string{
	"onnx/model_quantized.onnx",
	"tokenizer.json",
	"config.json",
}

func runModel(args []string) int {
	if len(args) == 0 || args[0] != "download" {
		fmt.Fprintln(os.Stderr, "usage: codesseek model download [--force] [--model DIR]")
		return exitUsage
	}
	fs := flag.NewFlagSet("model download", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "re-download even if files exist")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "codesseek: model download takes no positional arguments")
		return exitUsage
	}

	cfg, err := config.Load("")
	if err != nil {
		return fail(err)
	}
	dest := cfg.ModelPath()
	// A single-file build carries the default model. Prefer extracting it over
	// hitting the network when the model is missing and no force was requested.
	if !*force && cfg.Model == config.DefaultModel && assets.HasModel() {
		if _, err := os.Stat(filepath.Join(dest, "model_quantized.onnx")); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "extracting embedded model\n  to %s\n", dest)
			if err := assets.EnsureModel(dest); err != nil {
				return fail(err)
			}
			fmt.Fprintf(stdout, "model ready: %s\n", dest)
			return exitOK
		}
	}
	fmt.Fprintf(os.Stderr, "downloading %s\n  to %s\n", cfg.Model, dest)
	if err := downloadModel(context.Background(), os.Getenv("HF_ENDPOINT"), dest, *force, func(name string, size int64) {
		fmt.Fprintf(os.Stderr, "  %-28s %s\n", name, humanSize(size))
	}); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "model ready: %s\n", dest)
	return exitOK
}

// downloadModel fetches the ONNX file, tokenizer and config into destDir.
// Existing files are kept unless force is set. endpoint overrides the default
// Hugging Face host (HF_ENDPOINT, e.g. https://hf-mirror.com).
func downloadModel(ctx context.Context, endpoint, destDir string, force bool, progress func(name string, size int64)) error {
	if endpoint == "" {
		endpoint = defaultHFEndpoint
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	for _, remote := range modelFiles {
		local := filepath.Join(destDir, filepath.FromSlash(filepath.Base(remote)))
		if !force {
			if info, err := os.Stat(local); err == nil && info.Size() > 0 {
				if progress != nil {
					progress(filepath.Base(local), info.Size())
				}
				continue
			}
		}
		url := endpoint + "/" + modelRepo + "/resolve/main/" + remote
		n, err := fetchToFile(ctx, client, url, local)
		if err != nil {
			return fmt.Errorf("download %s: %w", url, err)
		}
		if progress != nil {
			progress(filepath.Base(local), n)
		}
	}
	return nil
}

func fetchToFile(ctx context.Context, client *http.Client, url, dest string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %s", resp.Status)
	}
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		if copyErr != nil {
			return 0, copyErr
		}
		return 0, closeErr
	}
	if n == 0 {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("server returned an empty file")
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return n, nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

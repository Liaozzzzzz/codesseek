// Command codesseek is a local semantic code-search tool.
//
// It indexes a repository into quantized bge-small-zh-v1.5 vectors and answers
// natural-language queries without any network access at runtime.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "0.0.0-dev"

// stdout is os.Stdout, optionally wrapped to the console code page on Windows
// (see console_windows.go). Everything user-facing must write here.
var stdout io.Writer = os.Stdout

const (
	exitOK         = 0
	exitError      = 1
	exitUsage      = 2
	exitNotIndexed = 3
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "index":
		return runIndex(rest)
	case "search":
		return runSearch(rest)
	case "status":
		return runStatus(rest)
	case "projects":
		return runProjects(rest)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "codesseek "+version)
		return exitOK
	case "help", "--help", "-h":
		usage(stdout)
		return exitOK
	case "serve":
		return runServe(rest)
	case "model":
		return runModel(rest)
	default:
		fmt.Fprintf(os.Stderr, "codesseek: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		return exitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `codesseek - semantic code search

usage:
  codesseek index [path] [--full] [--workers N] [--quiet]
  codesseek search "<query>" [--path DIR] [--top N] [--json] [--prefix PATH]
  codesseek status [path]
  codesseek projects
  codesseek model download [--force]
  codesseek serve [--log FILE]
  codesseek version

exit codes: 0 ok, 1 runtime error, 2 usage error, 3 project not indexed
`)
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "codesseek:", err)
	return exitError
}

// resolveDir turns an optional CLI path into an absolute, existing directory.
func resolveDir(arg string) (string, error) {
	if arg == "" {
		arg = "."
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return filepath.Clean(abs), nil
}

// parseInterspersed accepts flags before or after positional arguments, e.g.
// `index PATH --full` (the stdlib flag package stops at the first positional).
// valueFlags names the flags that consume the following argument.
func parseInterspersed(fs *flag.FlagSet, args []string, valueFlags map[string]bool) ([]string, error) {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) > 1 && arg[0] == '-' {
			flags = append(flags, arg)
			name := strings.TrimLeft(arg, "-")
			if strings.ContainsRune(name, '=') {
				continue
			}
			if valueFlags[name] {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("flag %s needs a value", arg)
				}
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, arg)
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return positional, nil
}

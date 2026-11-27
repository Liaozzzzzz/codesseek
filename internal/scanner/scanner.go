// Package scanner walks a repository and returns the files worth indexing.
//
// It applies the extension whitelist, built-in exclude directories,
// codesseek.ignore/.gitignore rules (including "!" negation) and a
// size/binary filter.
package scanner

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Options controls which files are returned by Scan.
type Options struct {
	Include      []string // extensions including the dot, case-insensitive
	ExcludeDirs  []string // directory names skipped at any depth
	ExcludeGlobs []string // globs matched against the slash path or basename
	MaxFileBytes int64
}

// File is one scanned source file.
type File struct {
	Path    string // slash-separated path relative to the scan root
	AbsPath string
	Size    int64
	MTime   int64 // Unix seconds
}

// DefaultInclude is the extension whitelist from docs/spec.md §4.
var DefaultInclude = []string{
	".vue", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".java", ".kt",
	".py", ".go", ".rs", ".cs", ".sql", ".md", ".json", ".yaml", ".yml",
}

// DefaultExcludeDirs is the built-in directory blacklist from docs/spec.md §4.
var DefaultExcludeDirs = []string{
	"node_modules", "dist", "build", ".nuxt", ".output", ".git", "vendor",
	"target", "__pycache__", ".venv",
}

// DefaultExcludeGlobs is the built-in file blacklist from docs/spec.md §4.
var DefaultExcludeGlobs = []string{
	"*.min.js", "*.map", "*.lock", "pnpm-lock.yaml", "package-lock.json", "*.snap",
}

const binarySniffBytes = 512

// Scan walks root and returns matching files sorted by relative path.
func Scan(root string, opt Options) ([]File, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root %s is not a directory", absRoot)
	}

	include := make(map[string]bool, len(opt.Include))
	for _, ext := range opt.Include {
		include[strings.ToLower(ext)] = true
	}
	excludeDirs := make(map[string]bool, len(opt.ExcludeDirs))
	for _, d := range opt.ExcludeDirs {
		excludeDirs[d] = true
	}
	globs := make([]*regexp.Regexp, 0, len(opt.ExcludeGlobs))
	for _, g := range opt.ExcludeGlobs {
		re, err := globToRegexp(g)
		if err != nil {
			return nil, fmt.Errorf("exclude glob %q: %w", g, err)
		}
		globs = append(globs, re)
	}

	rules, err := loadIgnoreRules(absRoot)
	if err != nil {
		return nil, err
	}
	hasNegation := false
	for _, r := range rules {
		if r.negate {
			hasNegation = true
		}
	}

	var files []File
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A single unreadable directory or file must not abort the scan.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relOS, err := filepath.Rel(absRoot, path)
		if err != nil {
			return nil
		}
		if relOS == "." {
			return nil
		}
		rel := filepath.ToSlash(relOS)

		if d.IsDir() {
			if excludeDirs[d.Name()] {
				return filepath.SkipDir
			}
			if ignored(rules, rel, true) && !hasNegation {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() {
			return nil
		}
		if !include[strings.ToLower(filepath.Ext(rel))] {
			return nil
		}
		if ignored(rules, rel, false) {
			return nil
		}
		for _, re := range globs {
			if re.MatchString(rel) {
				return nil
			}
			base := rel
			if i := strings.LastIndexByte(rel, '/'); i >= 0 {
				base = rel[i+1:]
			}
			if re.MatchString(base) {
				return nil
			}
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if opt.MaxFileBytes > 0 && info.Size() > opt.MaxFileBytes {
			return nil
		}
		if info.Size() == 0 {
			return nil
		}
		binary, err := isBinary(path)
		if err != nil {
			return nil
		}
		if binary {
			return nil
		}

		files = append(files, File{
			Path:    rel,
			AbsPath: path,
			Size:    info.Size(),
			MTime:   info.ModTime().Unix(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", absRoot, err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func isBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, binarySniffBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

// ---------------------------------------------------------------------------
// .gitignore / codesseek.ignore

type ignoreRule struct {
	re       *regexp.Regexp
	negate   bool
	dirOnly  bool
	anchored bool
	hasSlash bool
	pattern  string
}

func loadIgnoreRules(root string) ([]ignoreRule, error) {
	var rules []ignoreRule
	for _, name := range []string{".gitignore", "codesseek.ignore"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		rules = append(rules, parseIgnoreFile(string(data))...)
	}
	return rules, nil
}

func parseIgnoreFile(content string) []ignoreRule {
	var rules []ignoreRule
	for _, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negate := false
		if strings.HasPrefix(line, "!") {
			negate = true
			line = strings.TrimSpace(line[1:])
		}
		if line == "" {
			continue
		}
		// Trailing spaces are ignored unless escaped; keep it simple.
		dirOnly := strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		anchored := strings.HasPrefix(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		re, err := globToRegexp(line)
		if err != nil {
			continue
		}
		rules = append(rules, ignoreRule{
			re:       re,
			negate:   negate,
			dirOnly:  dirOnly,
			anchored: anchored,
			hasSlash: strings.Contains(line, "/"),
			pattern:  line,
		})
	}
	return rules
}

// ignored reports whether rel is ignored by the last matching rule.
func ignored(rules []ignoreRule, rel string, isDir bool) bool {
	ignored := false
	base := rel
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		base = rel[i+1:]
	}
	for _, r := range rules {
		if r.dirOnly && !isDir {
			// A directory rule also covers files below it, but the directory
			// itself is normally pruned while walking.
			if !matchesAncestor(r, rel) {
				continue
			}
		} else {
			target := rel
			if !r.anchored && !r.hasSlash {
				target = base
			}
			if !r.re.MatchString(target) {
				continue
			}
		}
		ignored = !r.negate
	}
	return ignored
}

func matchesAncestor(r ignoreRule, rel string) bool {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		dir := strings.Join(parts[:i], "/")
		target := dir
		if !r.anchored && !r.hasSlash {
			target = parts[i-1]
		}
		if r.re.MatchString(target) {
			return true
		}
	}
	return false
}

// globToRegexp converts a gitignore-style glob into a regexp. Patterns
// without a slash are matched against the basename by the caller.
func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			j := i + 1
			for j < len(glob) && glob[j] != ']' {
				j++
			}
			if j >= len(glob) {
				b.WriteString(`\[`)
			} else {
				class := glob[i : j+1]
				if strings.HasPrefix(class, "[!") {
					class = "[^" + class[2:]
				}
				b.WriteString(class)
				i = j
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

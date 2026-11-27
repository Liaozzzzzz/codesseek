package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"codesseek/internal/chunker"
	"codesseek/internal/config"
	"codesseek/internal/dailylog"
	"codesseek/internal/embedder"
	"codesseek/internal/indexer"
	"codesseek/internal/searcher"
	"codesseek/internal/store"
)

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	logPath := fs.String("log", "", "append to FILE instead of the default daily log under <home>/logs")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "codesseek: serve takes no positional arguments")
		return exitUsage
	}

	logger, closeLog, logDesc, err := serveLogger(*logPath)
	if err != nil {
		return fail(err)
	}
	defer closeLog()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	s := newMCPServer(logger)
	defer s.close()
	logger.Printf("codesseek %s serving MCP over stdio (log: %s)", version, logDesc)
	if err := s.rpc.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		logger.Printf("serve: %v", err)
		return exitError
	}
	return exitOK
}

// serveLogger picks the log sink. An explicit --log FILE wins; otherwise logs
// go to <home>/logs/mcp-YYYY-MM-DD.log and rotate at local midnight. A failed
// default open (read-only install dir) falls back to stderr instead of blocking
// the MCP server.
func serveLogger(explicit string) (*log.Logger, func(), string, error) {
	prefix := fmt.Sprintf("[pid %d] ", os.Getpid())
	if explicit != "" {
		if dir := filepath.Dir(explicit); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, nil, "", err
			}
		}
		f, err := os.OpenFile(explicit, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, nil, "", err
		}
		return log.New(f, prefix, log.LstdFlags), func() { _ = f.Close() }, explicit, nil
	}

	dir := filepath.Join(config.HomeDir(), "logs")
	w := dailylog.New(dir, "mcp")
	if err := w.Ensure(); err != nil {
		l := log.New(os.Stderr, prefix, log.LstdFlags)
		l.Printf("cannot open log dir %s: %v; logging to stderr", dir, err)
		return l, func() {}, "stderr", nil
	}
	return log.New(w, prefix, log.LstdFlags), func() { _ = w.Close() }, w.Current(), nil
}

// mcpServer exposes the four tools from docs/spec.md §10. One process serves
// many projects; the ONNX session is created on the first search and cached.
type mcpServer struct {
	rpc  *mcp.Server
	log  *log.Logger
	fake bool // tests may force the hash embedder

	embMu    sync.Mutex
	emb      embedder.Embedder
	closeEmb func()

	mu     sync.Mutex
	stores map[string]*store.Store
	jobs   map[string]*indexJob
	byRoot map[string][]*indexJob
	jobSeq atomic.Int64

	auto    autoIndexPolicy
	staleAt map[string]time.Time // last cheap staleness check per root
}

func newMCPServer(logger *log.Logger) *mcpServer {
	s := &mcpServer{
		log:     logger,
		stores:  map[string]*store.Store{},
		jobs:    map[string]*indexJob{},
		byRoot:  map[string][]*indexJob{},
		auto:    autoIndexFromEnv(),
		staleAt: map[string]time.Time{},
	}
	s.rpc = mcp.NewServer(&mcp.Implementation{Name: "codesseek", Version: version}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})
	s.addTools()
	return s
}

// serverInstructions are added to the client's system prompt by OpenCode and
// similar hosts, so the agent knows when semantic search is the right tool.
const serverInstructions = `This server searches local code by meaning, not by exact text.
- Use search_code with a natural-language query (Chinese or English) when you are looking for behavior, intent, or an implementation you cannot name exactly.
- search_code indexes the project automatically on first use and refreshes stale indexes. If it returns an index.action of "indexing" or "refreshing", poll index_status with that index.jobId until state is "done", then call search_code again.
- Pass root explicitly when the user names a specific repository or directory.
- Use index_project only for an explicit rebuild (full=true after model/chunk changes).
- Use grep/ripgrep for exact identifiers or string literals; use search_code for concepts and Chinese descriptions.`

// autoIndexPolicy controls whether search_code builds or refreshes an index on
// demand. CODESEEK_AUTO_INDEX=off restores the old "index_project first"
// behaviour; CODESEEK_AUTO_INDEX_WAIT sets the synchronous wait in seconds.
type autoIndexPolicy struct {
	enabled bool
	wait    time.Duration
}

func autoIndexFromEnv() autoIndexPolicy {
	p := autoIndexPolicy{enabled: true, wait: 30 * time.Second}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODESEEK_AUTO_INDEX"))) {
	case "off", "0", "false", "never":
		p.enabled = false
	}
	if v := strings.TrimSpace(os.Getenv("CODESEEK_AUTO_INDEX_WAIT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.wait = time.Duration(n) * time.Second
		}
	}
	return p
}

// logCall records one tool invocation. It includes the OpenCode session ID from
// _meta so a log line can be traced back to the session that triggered it.
func (s *mcpServer) logCall(req *mcp.CallToolRequest, tool string, start time.Time, err error, format string, args ...any) {
	if s.log == nil {
		return
	}
	session := ""
	if req != nil && req.Params != nil {
		if v, ok := req.Params.Meta["ai.opencode/sessionID"].(string); ok {
			session = v
		}
	}
	detail := fmt.Sprintf(format, args...)
	if err != nil {
		detail += fmt.Sprintf(" error=%q", err.Error())
	}
	s.log.Printf("tool=%s session=%q elapsed=%s %s", tool, session, time.Since(start).Round(time.Millisecond), detail)
}

func (s *mcpServer) close() {
	s.embMu.Lock()
	defer s.embMu.Unlock()
	if s.closeEmb != nil {
		s.closeEmb()
		s.closeEmb = nil
		s.emb = nil
	}
}

type searchInput struct {
	Query      string `json:"query" jsonschema:"natural-language query, Chinese or English"`
	Root       string `json:"root,omitempty" jsonschema:"project root; defaults to the project containing the working directory, and is indexed automatically if needed"`
	TopK       int    `json:"topK,omitempty" jsonschema:"number of results, default from config (8)"`
	PathPrefix string `json:"pathPrefix,omitempty" jsonschema:"only return chunks whose path starts with this prefix"`
}

type indexInput struct {
	Root string `json:"root,omitempty" jsonschema:"project root; defaults to the working directory"`
	Full bool   `json:"full,omitempty" jsonschema:"rebuild every vector instead of reusing unchanged files"`
}

type indexStarted struct {
	JobID     string `json:"jobId"`
	Root      string `json:"root"`
	StartedAt string `json:"startedAt"`
}

type statusInput struct {
	Root  string `json:"root,omitempty" jsonschema:"project root"`
	JobID string `json:"jobId,omitempty" jsonschema:"job id returned by index_project"`
}

type statusOutput struct {
	State     string `json:"state"`
	Processed int64  `json:"processed"`
	Total     int64  `json:"total"`
	Files     int64  `json:"files"`
	Chunks    int64  `json:"chunks"`
	ElapsedMs int64  `json:"elapsedMs"`
	Error     string `json:"error,omitempty"`
}

type projectInfo struct {
	Root      string `json:"root"`
	Files     int    `json:"files"`
	Chunks    int    `json:"chunks"`
	IndexedAt string `json:"indexedAt"`
	Model     string `json:"model"`
	Dim       int    `json:"dim"`
}

// MCP structuredContent must be a JSON object, so list results are wrapped.
type searchOutput struct {
	Results []searcher.Result `json:"results"`
	// Index explains what search_code did to the index itself: it is nil when
	// the index was already fresh, and set when it was built, refreshed, or is
	// still running in the background.
	Index *indexNote `json:"index,omitempty"`
}

// indexNote is the self-describing part of a search response: the agent can
// tell whether results were produced from a fresh index, and what to poll when
// the index is still being built.
type indexNote struct {
	Action    string `json:"action"` // indexed | refreshed | indexing | refreshing
	JobID     string `json:"jobId,omitempty"`
	State     string `json:"state,omitempty"`
	Processed int64  `json:"processed,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Files     int64  `json:"files,omitempty"`
	Chunks    int64  `json:"chunks,omitempty"`
	ElapsedMs int64  `json:"elapsedMs,omitempty"`
	Message   string `json:"message,omitempty"`
}

type projectsOutput struct {
	Projects []projectInfo `json:"projects"`
}

func (s *mcpServer) addTools() {
	mcp.AddTool(s.rpc, &mcp.Tool{
		Name:        "search_code",
		Description: "Search a repository by meaning and return code chunks with path, line range, score and snippet. Builds the index automatically on first use and refreshes it when files changed; if the response says the index is still running, poll index_status and retry.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in searchInput) (res *mcp.CallToolResult, out searchOutput, err error) {
		start := time.Now()
		indexAction := ""
		defer func() {
			if out.Index != nil {
				indexAction = out.Index.Action
			}
			s.logCall(req, "search_code", start, err, "query=%q root=%q topK=%d results=%d index=%s", in.Query, in.Root, in.TopK, len(out.Results), indexAction)
		}()
		if strings.TrimSpace(in.Query) == "" {
			return nil, searchOutput{}, errors.New("query is required")
		}
		root, err := s.searchRoot(in.Root)
		if err != nil {
			return nil, searchOutput{}, err
		}
		note, ready, err := s.ensureIndexed(ctx, root)
		if err != nil {
			return nil, searchOutput{}, err
		}
		if !ready {
			return nil, searchOutput{Results: []searcher.Result{}, Index: note}, nil
		}
		results, err := s.search(root, in)
		if err != nil {
			return nil, searchOutput{}, err
		}
		return nil, searchOutput{Results: results, Index: note}, nil
	})

	mcp.AddTool(s.rpc, &mcp.Tool{
		Name:        "index_project",
		Description: "Start indexing a project in the background and return its job id immediately.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in indexInput) (res *mcp.CallToolResult, out indexStarted, err error) {
		start := time.Now()
		defer func() {
			s.logCall(req, "index_project", start, err, "root=%q full=%v job=%q", in.Root, in.Full, out.JobID)
		}()
		root := in.Root
		if root == "" {
			wd, err := os.Getwd()
			if err != nil {
				return nil, indexStarted{}, err
			}
			root = wd
		}
		root, err = resolveDir(root)
		if err != nil {
			return nil, indexStarted{}, err
		}
		job := s.startIndex(root, in.Full)
		return nil, indexStarted{JobID: job.ID, Root: job.Root, StartedAt: job.StartedAt.Format(time.RFC3339)}, nil
	})

	mcp.AddTool(s.rpc, &mcp.Tool{
		Name:        "index_status",
		Description: "Report progress of an index job, or the state of an already indexed project.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in statusInput) (res *mcp.CallToolResult, out statusOutput, err error) {
		start := time.Now()
		defer func() {
			s.logCall(req, "index_status", start, err, "root=%q jobID=%q state=%s files=%d", in.Root, in.JobID, out.State, out.Files)
		}()
		return nil, s.indexStatus(in), nil
	})

	mcp.AddTool(s.rpc, &mcp.Tool{
		Name:        "list_projects",
		Description: "List every indexed project with file/chunk counts and model information.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (res *mcp.CallToolResult, out projectsOutput, err error) {
		start := time.Now()
		defer func() {
			s.logCall(req, "list_projects", start, err, "projects=%d", len(out.Projects))
		}()
		infos, err := s.listProjects()
		if err != nil {
			return nil, projectsOutput{}, err
		}
		return nil, projectsOutput{Projects: infos}, nil
	})
}

func (s *mcpServer) search(root string, in searchInput) ([]searcher.Result, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	st, err := s.storeFor(cfg, root)
	if err != nil {
		return nil, err
	}
	meta, err := st.LoadMeta()
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("project not indexed: %s; call index_project first", root)
	}
	if err != nil {
		return nil, err
	}
	if meta.Model != cfg.Model {
		return nil, fmt.Errorf("index was built with %s but current model is %s; set the model back or reindex with --full",
			meta.Model, cfg.Model)
	}
	emb, err := s.embedderFor(cfg, meta)
	if err != nil {
		return nil, err
	}
	srch, err := searcher.New(st, emb)
	if err != nil {
		return nil, err
	}
	topK := in.TopK
	if topK <= 0 {
		topK = cfg.NResult
	}
	return srch.Search(in.Query, searcher.Options{TopK: topK, PathPrefix: in.PathPrefix})
}

func (s *mcpServer) embedderFor(cfg config.Config, meta *store.Meta) (embedder.Embedder, error) {
	s.embMu.Lock()
	defer s.embMu.Unlock()
	if s.emb != nil && s.emb.Model() == meta.Model && s.emb.Dim() == meta.Dim {
		return s.emb, nil
	}
	if s.closeEmb != nil {
		s.closeEmb()
		s.emb, s.closeEmb = nil, nil
	}
	emb, closer, err := buildEmbedder(cfg, meta.Model)
	if err != nil {
		return nil, fmt.Errorf("load embedding model: %w; run `codesseek model download`", err)
	}
	s.emb, s.closeEmb = emb, closer
	return emb, nil
}

func (s *mcpServer) storeFor(cfg config.Config, root string) (*store.Store, error) {
	st, err := store.New(cfg.DataDir, root)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.stores[st.Dir()]; ok {
		return cached, nil
	}
	s.stores[st.Dir()] = st
	return st, nil
}

// searchRoot applies the order from docs/spec.md §10: explicit argument,
// nearest indexed ancestor of the working directory, a project marker in the
// working directory, then the only indexed project. Unlike a pure lookup it may
// return a directory that is not indexed yet: search_code indexes on demand.
func (s *mcpServer) searchRoot(arg string) (string, error) {
	if arg != "" {
		return resolveDir(arg)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if s.indexed(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	if hasProjectMarker(wd) {
		return wd, nil
	}

	cfg, err := config.Load("")
	if err != nil {
		return "", err
	}
	metas, err := listProjectMetas(cfg.DataDir)
	if err != nil {
		return "", err
	}
	switch len(metas) {
	case 0:
		return wd, nil
	case 1:
		return metas[0].Root, nil
	default:
		var roots []string
		for _, m := range metas {
			roots = append(roots, m.Root)
		}
		return "", fmt.Errorf("several projects are indexed and %s is not one of them; pass root explicitly: %s", wd, strings.Join(roots, ", "))
	}
}

// indexed reports whether root already has a usable on-disk index.
func (s *mcpServer) indexed(root string) bool {
	cfg, err := config.Load(root)
	if err != nil {
		return false
	}
	st, err := store.New(cfg.DataDir, root)
	return err == nil && st.Exists()
}

// projectMarkers justify indexing a directory the caller did not name
// explicitly. Without one, an arbitrary working directory never triggers a
// large accidental index.
var projectMarkers = []string{
	".git", ".hg", ".svn", "go.mod", "package.json", "pyproject.toml",
	"requirements.txt", "Cargo.toml", "pom.xml", "build.gradle",
	"build.gradle.kts", "composer.json", "Gemfile", "CMakeLists.txt",
}

func hasProjectMarker(dir string) bool {
	for _, name := range projectMarkers {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.sln"))
	return len(matches) > 0
}

// guardedRoot rejects filesystem roots and the home directory: indexing those
// by accident would scan far more than the user meant.
func guardedRoot(dir string) error {
	clean := filepath.Clean(dir)
	if clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return fmt.Errorf("refusing to index filesystem root %s; pass the project root explicitly", clean)
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == clean {
		return fmt.Errorf("refusing to index home directory %s; pass the project root explicitly", clean)
	}
	return nil
}

// staleCheckInterval bounds how often a single root is re-scanned for changes.
const staleCheckInterval = 10 * time.Second

// ensureIndexed makes root searchable before the query runs: it builds a
// missing index or refreshes a stale one and waits up to auto.wait, so the
// common case finishes in one call. When the budget runs out it returns
// ready=false plus a job id the agent can poll with index_status.
func (s *mcpServer) ensureIndexed(ctx context.Context, root string) (*indexNote, bool, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, false, err
	}
	st, err := store.New(cfg.DataDir, root)
	if err != nil {
		return nil, false, err
	}

	var action string
	switch {
	case !st.Exists():
		action = "indexed"
	case s.auto.enabled && s.stale(root, cfg):
		action = "refreshed"
	default:
		return nil, true, nil
	}
	if !s.auto.enabled {
		return nil, false, fmt.Errorf("project not indexed: %s; call index_project first", root)
	}
	if err := guardedRoot(root); err != nil {
		return nil, false, err
	}

	job := s.startOrJoinIndex(root)
	note := &indexNote{JobID: job.ID}
	if !job.wait(ctx, s.auto.wait) {
		snap := job.snapshot()
		note.Action = "indexing"
		if action == "refreshed" {
			note.Action = "refreshing"
		}
		note.State = snap.State
		note.Processed, note.Total = snap.Processed, snap.Total
		note.Message = fmt.Sprintf("index build is still running (%s, job %s); poll index_status with this jobId, then call search_code again", progressText(snap), job.ID)
		return note, false, nil
	}

	snap := job.snapshot()
	if snap.State == "error" {
		return nil, false, fmt.Errorf("indexing %s failed: %s", root, snap.Error)
	}
	note.Action = action
	note.State = snap.State
	note.Files, note.Chunks = snap.Files, snap.Chunks
	note.ElapsedMs = snap.ElapsedMs
	note.Message = fmt.Sprintf("index %s: %d files, %d chunks in %s",
		action, snap.Files, snap.Chunks, time.Duration(snap.ElapsedMs)*time.Millisecond)
	return note, true, nil
}

func progressText(snap statusOutput) string {
	if snap.Total > 0 {
		return fmt.Sprintf("%d/%d files", snap.Processed, snap.Total)
	}
	return snap.State
}

// stale answers "does the index need a refresh?" at most once per
// staleCheckInterval per root.
func (s *mcpServer) stale(root string, cfg config.Config) bool {
	s.mu.Lock()
	if t, ok := s.staleAt[root]; ok && time.Since(t) < staleCheckInterval {
		s.mu.Unlock()
		return false
	}
	s.staleAt[root] = time.Now()
	s.mu.Unlock()

	stale, err := indexer.NeedsRefresh(root, cfg.DataDir, cfg.ScannerOptions())
	return err == nil && stale
}

func (s *mcpServer) listProjects() ([]projectInfo, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	metas, err := listProjectMetas(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].IndexedAt.After(metas[j].IndexedAt) })
	infos := make([]projectInfo, 0, len(metas))
	for _, m := range metas {
		infos = append(infos, projectInfo{
			Root:      m.Root,
			Files:     m.FileCount,
			Chunks:    m.ChunkCount,
			IndexedAt: m.IndexedAt.Local().Format(time.RFC3339),
			Model:     m.Model,
			Dim:       m.Dim,
		})
	}
	return infos, nil
}

type indexJob struct {
	ID        string
	Root      string
	StartedAt time.Time
	done      chan struct{}

	state     atomic.Value // string
	processed atomic.Int64
	total     atomic.Int64
	files     atomic.Int64
	chunks    atomic.Int64

	errMu  sync.Mutex
	errMsg string
}

func newIndexJob(id, root string) *indexJob {
	j := &indexJob{ID: id, Root: root, StartedAt: time.Now(), done: make(chan struct{})}
	j.state.Store("queued")
	return j
}

func (j *indexJob) setState(state string) { j.state.Store(state) }

func (j *indexJob) fail(err error) {
	j.errMu.Lock()
	j.errMsg = err.Error()
	j.errMu.Unlock()
	j.setState("error")
}

func (j *indexJob) snapshot() statusOutput {
	out := statusOutput{
		State:     j.state.Load().(string),
		Processed: j.processed.Load(),
		Total:     j.total.Load(),
		Files:     j.files.Load(),
		Chunks:    j.chunks.Load(),
		ElapsedMs: time.Since(j.StartedAt).Milliseconds(),
	}
	j.errMu.Lock()
	out.Error = j.errMsg
	j.errMu.Unlock()
	return out
}

// active reports whether the job is still queued or running.
func (j *indexJob) active() bool {
	switch j.state.Load().(string) {
	case "queued", "running":
		return true
	}
	return false
}

// wait blocks until the job finishes, the budget expires, or ctx is cancelled.
// It reports whether the job finished.
func (j *indexJob) wait(ctx context.Context, budget time.Duration) bool {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-j.done:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// startOrJoinIndex starts an incremental index for root unless one is already
// running, in which case concurrent searches share that job instead of racing
// for the store lock.
func (s *mcpServer) startOrJoinIndex(root string) *indexJob {
	s.mu.Lock()
	if list := s.byRoot[root]; len(list) > 0 {
		if last := list[len(list)-1]; last.active() {
			s.mu.Unlock()
			return last
		}
	}
	id := fmt.Sprintf("idx-%d", s.jobSeq.Add(1))
	job := newIndexJob(id, root)
	s.jobs[id] = job
	s.byRoot[root] = append(s.byRoot[root], job)
	s.mu.Unlock()

	go s.runIndex(job, false)
	return job
}

func (s *mcpServer) startIndex(root string, full bool) *indexJob {
	id := fmt.Sprintf("idx-%d", s.jobSeq.Add(1))
	job := newIndexJob(id, root)
	s.mu.Lock()
	s.jobs[id] = job
	s.byRoot[root] = append(s.byRoot[root], job)
	s.mu.Unlock()
	go s.runIndex(job, full)
	return job
}

func (s *mcpServer) runIndex(job *indexJob, full bool) {
	defer close(job.done)
	job.setState("running")
	cfg, err := config.Load(job.Root)
	if err != nil {
		job.fail(err)
		return
	}
	st, err := store.New(cfg.DataDir, job.Root)
	if err != nil {
		job.fail(err)
		return
	}
	emb, closeEmb, err := indexEmbedder(cfg, st, full)
	if err != nil {
		job.fail(fmt.Errorf("load embedding model: %w; run `codesseek model download`", err))
		return
	}
	defer closeEmb()

	res, err := indexer.Run(context.Background(), indexer.Options{
		Root:     job.Root,
		DataDir:  cfg.DataDir,
		Scan:     cfg.ScannerOptions(),
		Chunk:    chunker.Config{ChunkSize: cfg.ChunkSize, OverlapRatio: cfg.OverlapRatio},
		Embedder: emb,
		Full:     full,
		Workers:  cfg.Workers,
		Progress: func(p indexer.Progress) {
			if p.Phase != "index" {
				return
			}
			job.processed.Store(int64(p.Processed))
			job.total.Store(int64(p.Total))
		},
	})
	if errors.Is(err, indexer.ErrNeedsFull) {
		job.fail(fmt.Errorf("chunk config or embedding model changed; rerun index_project with full=true"))
		return
	}
	if err != nil {
		job.fail(err)
		return
	}
	job.files.Store(int64(res.Files))
	job.chunks.Store(int64(res.Chunks))
	job.processed.Store(int64(res.Files))
	job.total.Store(int64(res.Files))
	job.setState("done")
}

func (s *mcpServer) indexStatus(in statusInput) statusOutput {
	s.mu.Lock()
	defer s.mu.Unlock()
	var job *indexJob
	if in.JobID != "" {
		job = s.jobs[in.JobID]
		if job == nil {
			return statusOutput{State: "unknown", Error: "job not found: " + in.JobID}
		}
	} else if in.Root != "" {
		if root, err := resolveDir(in.Root); err == nil {
			if list := s.byRoot[root]; len(list) > 0 {
				job = list[len(list)-1]
			}
		}
	}
	if job != nil {
		return job.snapshot()
	}

	// No job in this process: report the on-disk index, if any.
	root := in.Root
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}
	if root != "" {
		if cfg, err := config.Load(root); err == nil {
			if st, err := store.New(cfg.DataDir, root); err == nil {
				if meta, err := st.LoadMeta(); err == nil {
					return statusOutput{State: "idle", Files: int64(meta.FileCount), Chunks: int64(meta.ChunkCount)}
				}
			}
		}
	}
	return statusOutput{State: "absent"}
}

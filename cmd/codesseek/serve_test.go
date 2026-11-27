package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"codesseek/internal/chunker"
	"codesseek/internal/embedder"
	"codesseek/internal/indexer"
	"codesseek/internal/scanner"
	"codesseek/internal/store"
)

func writeTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/login.vue", `<template><span>登录失败，请检查用户名或密码</span></template>
<script setup lang="ts">
function handleLoginError(error: Error) { return '登录失败' }
</script>
`)
	write("src/payment.ts", "export function payOrder(amount: number) { return amount }\n")
	return root
}

func indexedRepo(t *testing.T, dataDir string) string {
	t.Helper()
	root := writeTestRepo(t)
	if _, err := indexer.Run(context.Background(), indexer.Options{
		Root:     root,
		DataDir:  dataDir,
		Scan:     scanner.Options{Include: scanner.DefaultInclude, ExcludeDirs: scanner.DefaultExcludeDirs, MaxFileBytes: 1 << 20},
		Chunk:    chunker.Config{ChunkSize: 1000, OverlapRatio: 0.15},
		Embedder: embedder.NewFake(8),
	}); err != nil {
		t.Fatal(err)
	}
	return root
}

func connectMCP(t *testing.T, s *mcpServer) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := s.rpc.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func decodeText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("tool returned no content")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	return text.Text
}

// Some MCP clients (OpenCode) require structuredContent to be a JSON object.
func assertStructuredObject(t *testing.T, res *mcp.CallToolResult, key string) {
	t.Helper()
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("structuredContent is not an object: %s", data)
	}
	if _, ok := obj[key]; !ok {
		t.Fatalf("structuredContent %s has no %q key", data, key)
	}
}

func TestMCPSearchAndListProjects(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CODESEEK_DATA_DIR", dataDir)
	t.Setenv("CODESEEK_MODEL", "fake-hash-8")
	root := indexedRepo(t, dataDir)

	s := newMCPServer(log.New(io.Discard, "", 0))
	defer s.close()
	session := connectMCP(t, s)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search_code",
		Arguments: map[string]any{"query": "登录失败处理", "root": root, "topK": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	var searchOut struct {
		Results []struct {
			Path    string  `json:"path"`
			Score   float64 `json:"score"`
			Snippet string  `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(decodeText(t, res)), &searchOut); err != nil {
		t.Fatal(err)
	}
	results := searchOut.Results
	if len(results) == 0 || results[0].Path != "src/login.vue" {
		t.Fatalf("results = %+v", results)
	}
	assertStructuredObject(t, res, "results")

	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "list_projects"})
	if err != nil {
		t.Fatal(err)
	}
	var projectList projectsOutput
	if err := json.Unmarshal([]byte(decodeText(t, res)), &projectList); err != nil {
		t.Fatal(err)
	}
	projects := projectList.Projects
	if len(projects) != 1 || projects[0].Root != root || projects[0].Files != 2 {
		t.Fatalf("projects = %+v", projects)
	}
	assertStructuredObject(t, res, "projects")

	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "index_status",
		Arguments: map[string]any{"root": root},
	})
	if err != nil {
		t.Fatal(err)
	}
	var status statusOutput
	if err := json.Unmarshal([]byte(decodeText(t, res)), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "idle" || status.Files != 2 {
		t.Fatalf("status = %+v", status)
	}
}

func TestMCPIndexProjectRunsInBackground(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CODESEEK_DATA_DIR", dataDir)
	t.Setenv("CODESEEK_MODEL", "fake-hash-8")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("# 登录\n\n登录失败处理。\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newMCPServer(log.New(io.Discard, "", 0))
	defer s.close()
	session := connectMCP(t, s)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "index_project",
		Arguments: map[string]any{"root": root},
	})
	if err != nil {
		t.Fatal(err)
	}
	var started indexStarted
	if err := json.Unmarshal([]byte(decodeText(t, res)), &started); err != nil {
		t.Fatal(err)
	}
	if started.JobID == "" || started.Root != root {
		t.Fatalf("started = %+v", started)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err = session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "index_status",
			Arguments: map[string]any{"jobId": started.JobID},
		})
		if err != nil {
			t.Fatal(err)
		}
		var status statusOutput
		if err := json.Unmarshal([]byte(decodeText(t, res)), &status); err != nil {
			t.Fatal(err)
		}
		if status.State == "done" {
			if status.Files != 1 || status.Chunks == 0 {
				t.Fatalf("done status = %+v", status)
			}
			break
		}
		if status.State == "error" {
			t.Fatalf("index job failed: %+v", status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("index job did not finish: %+v", status)
		}
		time.Sleep(50 * time.Millisecond)
	}

	st, err := store.New(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists() {
		t.Fatal("index not written")
	}
}

// Every tool call must leave a trace so users can tell whether an agent
// actually used semantic search in a session.
func TestToolCallsAreLogged(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CODESEEK_DATA_DIR", dataDir)
	t.Setenv("CODESEEK_MODEL", "fake-hash-8")
	indexedRepo(t, dataDir)

	var buf bytes.Buffer
	s := newMCPServer(log.New(&buf, "", 0))
	defer s.close()
	session := connectMCP(t, s)

	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_projects",
		Arguments: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "tool=list_projects") {
		t.Fatalf("tool call was not logged: %q", logged)
	}
	if !strings.Contains(logged, "elapsed=") {
		t.Fatalf("log line has no elapsed time: %q", logged)
	}
	if strings.Contains(logged, "error=") {
		t.Fatalf("successful call logged an error: %q", logged)
	}
}

func callSearch(t *testing.T, session *mcp.ClientSession, args map[string]any) (*mcp.CallToolResult, searchOutput) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_code", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var out searchOutput
	if !res.IsError {
		if err := json.Unmarshal([]byte(decodeText(t, res)), &out); err != nil {
			t.Fatal(err)
		}
	}
	return res, out
}

// A first search must index the project instead of telling the user to run a
// manual command.
func TestSearchAutoIndexesProject(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CODESEEK_DATA_DIR", dataDir)
	t.Setenv("CODESEEK_MODEL", "fake-hash-8")

	root := writeTestRepo(t)
	s := newMCPServer(log.New(io.Discard, "", 0))
	defer s.close()
	session := connectMCP(t, s)

	res, out := callSearch(t, session, map[string]any{"query": "登录失败", "root": root, "topK": 5})
	if out.Index == nil {
		t.Fatal("first search did not report an index action")
	}
	if out.Index.Action != "indexed" || out.Index.State != "done" {
		t.Fatalf("index note = %+v, want indexed/done", out.Index)
	}
	if out.Index.Files != 2 {
		t.Fatalf("indexed files = %d, want 2", out.Index.Files)
	}
	if len(out.Results) == 0 {
		t.Fatal("search returned no results after auto-indexing")
	}
	assertStructuredObject(t, res, "results")

	if _, again := callSearch(t, session, map[string]any{"query": "登录失败", "root": root}); again.Index != nil {
		t.Fatalf("fresh index was rebuilt again: %+v", again.Index)
	}
}

// Adding a file must refresh the index before the query is answered.
func TestSearchRefreshesStaleIndex(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CODESEEK_DATA_DIR", dataDir)
	t.Setenv("CODESEEK_MODEL", "fake-hash-8")

	root := indexedRepo(t, dataDir)
	added := filepath.Join(root, "src", "refund.ts")
	if err := os.WriteFile(added, []byte("export function refund(orderId: string) { return orderId }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newMCPServer(log.New(io.Discard, "", 0))
	defer s.close()
	session := connectMCP(t, s)

	_, out := callSearch(t, session, map[string]any{"query": "退款流程", "root": root, "topK": 20})
	if out.Index == nil || out.Index.Action != "refreshed" {
		t.Fatalf("index note = %+v, want refreshed", out.Index)
	}
	if out.Index.Files != 3 {
		t.Fatalf("refreshed files = %d, want 3", out.Index.Files)
	}
	if len(out.Results) == 0 {
		t.Fatal("search returned no results after refresh")
	}
}

// CODESEEK_AUTO_INDEX=off keeps the explicit index_project contract.
func TestSearchAutoIndexCanBeDisabled(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CODESEEK_DATA_DIR", dataDir)
	t.Setenv("CODESEEK_MODEL", "fake-hash-8")
	t.Setenv("CODESEEK_AUTO_INDEX", "off")

	root := writeTestRepo(t)
	s := newMCPServer(log.New(io.Discard, "", 0))
	defer s.close()
	session := connectMCP(t, s)

	res, _ := callSearch(t, session, map[string]any{"query": "登录失败", "root": root})
	if !res.IsError {
		t.Fatal("search succeeded although auto indexing is off")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "index_project") {
		t.Fatalf("error %q does not point at index_project", res.Content)
	}
	st, err := store.New(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Exists() {
		t.Fatal("index was written although auto indexing is off")
	}
}

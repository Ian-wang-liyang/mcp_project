// Package harness holds the helpers every milestone's acceptance tests share.
// You don't need to read it to do the project, but it's ordinary MCP client code, so it's worth a look.
package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"devmcp/server"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ModuleRoot returns the absolute path of the devmcp module (the directory holding go.mod).
func ModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("harness: cannot locate the module root")
	}
	// file is <root>/acceptance/internal/harness/harness.go
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// Workspace copies acceptance/testdata/sampleproj into a fresh temp dir and returns its absolute path.
// Each test gets its own copy, so tests may create or change files freely.
func Workspace(t *testing.T) string {
	t.Helper()
	src := filepath.Join(ModuleRoot(t), "acceptance", "testdata", "sampleproj")
	dst := filepath.Join(t.TempDir(), "sampleproj")
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("harness: copying the sample workspace: %v", err)
	}
	// macOS puts temp dirs behind a symlink (/var → /private/var); resolve it so paths compare cleanly.
	if real, err := filepath.EvalSymlinks(dst); err == nil {
		dst = real
	}
	return dst
}

// NewServer calls your server.New and fails the test with a hint if it returns an error or nil.
func NewServer(t *testing.T, cfg server.Config) *mcp.Server {
	t.Helper()
	s, err := server.New(cfg)
	if err != nil {
		t.Fatalf("server.New(%+v) returned error: %v", cfg, err)
	}
	if s == nil {
		t.Fatal("server.New returned a nil *mcp.Server")
	}
	return s
}

// Connect builds your server with cfg and connects a fresh client to it over in-memory transports.
// Both sessions are closed when the test ends. opts may be nil.
func Connect(t *testing.T, cfg server.Config, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	return ConnectServer(t, NewServer(t, cfg), opts)
}

// ConnectServer connects a fresh client to an already-built server.
func ConnectServer(t *testing.T, s *mcp.Server, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "v0.0.1"}, opts)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// CallTool calls a tool and fails the test if the call itself fails (unknown tool, protocol error).
// A result with IsError set is returned normally; check res.IsError yourself.
func CallTool(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%q): %v", name, err)
	}
	return res
}

// Text joins the text of every TextContent block in a tool result.
func Text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// Structured decodes a tool result's StructuredContent into a T. On the client side StructuredContent
// arrives as generic JSON (map[string]any), so this re-marshals it into your struct.
func Structured[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	if res.StructuredContent == nil {
		t.Fatalf("the tool result has no StructuredContent (did you return a typed Out value?); text was %q", Text(res))
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("re-marshaling StructuredContent: %v", err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("StructuredContent %s doesn't decode into %T: %v", data, out, err)
	}
	return out
}

// FindTool lists the server's tools and returns the one named name, or fails the test.
func FindTool(t *testing.T, cs *mcp.ClientSession, name string) *mcp.Tool {
	t.Helper()
	var names []string
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("listing tools: %v", err)
		}
		if tool.Name == name {
			return tool
		}
		names = append(names, tool.Name)
	}
	t.Fatalf("no tool named %q (the server has: %q)", name, names)
	return nil
}

// ToolNames returns the names of every tool the server lists, sorted.
func ToolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	var names []string
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("listing tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// Within fails the test if f takes longer than d (turns "blocked forever" into a clear failure).
// Assert on results AFTER Within returns; don't call t.Fatal inside f.
func Within(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("timed out after %v: something is blocked (a handler that never returns?)", d)
	}
}

// BuildBinary builds ./cmd/devmcp into a temp dir and returns the binary's path.
func BuildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "devmcp")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/devmcp")
	cmd.Dir = ModuleRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/devmcp failed (write cmd/devmcp/main.go in milestone 1):\n%s", out)
	}
	return bin
}

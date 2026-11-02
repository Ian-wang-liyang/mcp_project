// Milestone 1 acceptance tests: a server with one tool, served over stdio.
// Brief: milestones/01_hello.md
package m01

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"devmcp/acceptance/internal/harness"
	"devmcp/server"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type workspaceInfo struct {
	Root      string `json:"root"`
	Module    string `json:"module"`
	GoVersion string `json:"go_version"`
}

// The server introduces itself in the handshake: name "devmcp", a version, and instructions.
func TestServerIdentity(t *testing.T) {
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, nil)
	res := cs.InitializeResult()
	if res.ServerInfo == nil || res.ServerInfo.Name != "devmcp" {
		t.Errorf("serverInfo = %+v, want Name \"devmcp\"", res.ServerInfo)
	}
	if res.ServerInfo != nil && res.ServerInfo.Version == "" {
		t.Error("serverInfo.Version is empty")
	}
	if res.Instructions == "" {
		t.Error("no instructions: set ServerOptions.Instructions so the model knows how to use your tools")
	}
}

// New must refuse a Root that doesn't exist or isn't a directory, instead of failing later on every call.
func TestNewRejectsBadRoot(t *testing.T) {
	ws := harness.Workspace(t)
	if _, err := server.New(server.Config{Root: ws}); err != nil {
		t.Fatalf("server.New with a valid Root returned error: %v", err)
	}
	if _, err := server.New(server.Config{Root: filepath.Join(ws, "no-such-dir")}); err == nil {
		t.Error("server.New with a missing Root returned nil error")
	}
	if _, err := server.New(server.Config{Root: filepath.Join(ws, "go.mod")}); err == nil {
		t.Error("server.New with a Root that is a file returned nil error")
	}
}

// workspace_info is read-only, documented, and returns structured output read from go.mod.
func TestWorkspaceInfo(t *testing.T) {
	ws := harness.Workspace(t)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	tool := harness.FindTool(t, cs, "workspace_info")
	if tool.Description == "" {
		t.Error("workspace_info has no Description; the model reads it to decide when to call the tool")
	}
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
		t.Error("workspace_info should be annotated ReadOnlyHint: true (it changes nothing)")
	}
	if tool.OutputSchema == nil {
		t.Error("workspace_info has no OutputSchema: return a typed struct as the Out value of mcp.AddTool")
	}

	res := harness.CallTool(t, cs, "workspace_info", map[string]any{})
	if res.IsError {
		t.Fatalf("workspace_info returned an error result: %s", harness.Text(res))
	}
	got := harness.Structured[workspaceInfo](t, res)
	want := workspaceInfo{Root: ws, Module: "example.com/sampleproj", GoVersion: "1.26"}
	if got != want {
		t.Errorf("workspace_info = %+v\n                     want %+v", got, want)
	}
}

// A directory without go.mod is still a valid workspace: module and go_version are just empty.
func TestWorkspaceInfoWithoutGoMod(t *testing.T) {
	ws := harness.Workspace(t)
	if err := os.Remove(filepath.Join(ws, "go.mod")); err != nil {
		t.Fatal(err)
	}
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	res := harness.CallTool(t, cs, "workspace_info", map[string]any{})
	if res.IsError {
		t.Fatalf("workspace_info failed without a go.mod: %s", harness.Text(res))
	}
	got := harness.Structured[workspaceInfo](t, res)
	if got.Root != ws || got.Module != "" || got.GoVersion != "" {
		t.Errorf("workspace_info = %+v, want root %q and empty module/go_version", got, ws)
	}
}

// workspace_info must not read a go.mod symlink that leaves the workspace.
func TestWorkspaceInfoSymlink(t *testing.T) {
	for _, outside := range []bool{false, true} {
		t.Run(fmt.Sprintf("outside=%v", outside), func(t *testing.T) {
			ws := harness.Workspace(t)
			target := filepath.Join(ws, "module.txt")
			if outside {
				target = filepath.Join(t.TempDir(), "module.txt")
			}
			if err := os.WriteFile(target, []byte("module example.com/linked\ngo 1.26\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(ws, "go.mod")); err != nil {
				t.Fatal(err)
			}
			link, err := filepath.Rel(ws, target)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(link, filepath.Join(ws, "go.mod")); err != nil {
				t.Fatal(err)
			}
			cs := harness.Connect(t, server.Config{Root: ws}, nil)
			res := harness.CallTool(t, cs, "workspace_info", map[string]any{})
			if outside {
				if !res.IsError {
					t.Fatal("workspace_info read go.mod outside the workspace through a symlink")
				}
				return
			}
			if res.IsError {
				t.Fatalf("workspace_info rejected an internal symlink: %s", harness.Text(res))
			}
			if got := harness.Structured[workspaceInfo](t, res); got.Module != "example.com/linked" {
				t.Errorf("module = %q, want example.com/linked", got.Module)
			}
		})
	}
}

// The real binary speaks MCP over stdin/stdout, the way Claude Code will launch it.
func TestBinaryOverStdio(t *testing.T) {
	bin := harness.BuildBinary(t)
	ws := harness.Workspace(t)

	cmd := exec.Command(bin, "-root", ws)
	cmd.Stderr = &testLogWriter{t: t}
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "v0.0.1"}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connecting to `devmcp -root %s` over stdio: %v\n(does main serve with mcp.StdioTransport? anything printed to stdout corrupts the protocol)", ws, err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool(workspace_info) over stdio: %v", err)
	}
	if got := harness.Structured[workspaceInfo](t, res); got.Root != ws {
		t.Errorf("over stdio, workspace_info root = %q, want %q (is the -root flag wired to Config.Root?)", got.Root, ws)
	}
}

// testLogWriter forwards the server's stderr into the test log, so its log lines show up with -v.
type testLogWriter struct{ t *testing.T }

func (w *testLogWriter) Write(p []byte) (int, error) {
	w.t.Logf("devmcp stderr: %s", p)
	return len(p), nil
}

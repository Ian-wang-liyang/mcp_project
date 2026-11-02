// Milestone 6 acceptance tests: a write_file tool that asks the user (elicitation) before it writes.
// Brief: milestones/06_safe_writes.md
//
// Most tests run on both protocol eras: 2026-07-28, where your handler returns InputRequests and is
// called again with the answer, and 2025-11-25, where the SDK asks the client on your behalf.
package m06

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"devmcp/acceptance/internal/harness"
	"devmcp/server"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// eras are the two protocol generations; an MRTR-style handler works with both.
var eras = []string{"2026-07-28", "2025-11-25"}

type writeResult struct {
	Written *bool  `json:"written"` // a pointer, so a missing field is caught
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
}

// fakeUser answers every elicitation the same way. It records each question, and checks the
// workspace at the moment it is asked: by then the server must not have written anything.
type fakeUser struct {
	action  string         // "accept", "decline" or "cancel"
	content map[string]any // the form values sent with "accept"
	ws      string         // the workspace root
	notYet  []string       // paths relative to ws that must not exist while the user is being asked

	mu       sync.Mutex
	asked    []*mcp.ElicitParams
	tooEarly []string // paths from notYet that already existed when the user was asked
}

func (u *fakeUser) handle(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.asked = append(u.asked, req.Params)
	for _, p := range u.notYet {
		if _, err := os.Lstat(filepath.Join(u.ws, filepath.FromSlash(p))); err == nil {
			u.tooEarly = append(u.tooEarly, p)
		}
	}
	return &mcp.ElicitResult{Action: u.action, Content: u.content}, nil
}

func (u *fakeUser) questions() []*mcp.ElicitParams {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.asked)
}

// checkNothingEarly fails if anything was on disk before the user answered.
func (u *fakeUser) checkNothingEarly(t *testing.T) {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.tooEarly) > 0 {
		t.Errorf("%q already existed while the user was still being asked.\n"+
			"Your handler runs twice per call (on 2025-11-25 too: the SDK asks the client for you, then calls it again).\n"+
			"Round 1 must ONLY return InputRequests: no MkdirAll, no write. Act in round 2, once req.Params.InputResponses holds the answer", u.tooEarly)
	}
}

// connect builds a server with cfg and connects a client on the given protocol version.
// user may be nil: then the client has no ElicitationHandler, and so no elicitation capability.
func connect(t *testing.T, cfg server.Config, user *fakeUser, version string) *mcp.ClientSession {
	t.Helper()
	opts := &mcp.ClientOptions{}
	if user != nil {
		opts.ElicitationHandler = user.handle
	}
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := harness.NewServer(t, cfg).Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "v0.0.1"}, opts)
	cs, err := client.Connect(ctx, clientT, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	if got := cs.InitializeResult().ProtocolVersion; got != version {
		t.Fatalf("negotiated protocol %q, want %q", got, version)
	}
	return cs
}

// callWrite calls write_file, failing the test on a protocol error (with a hint for the usual causes).
func callWrite(t *testing.T, cs *mcp.ClientSession, path, content string) *mcp.CallToolResult {
	t.Helper()
	var (
		res *mcp.CallToolResult
		err error
	)
	harness.Within(t, 15*time.Second, func() {
		res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "write_file",
			Arguments: map[string]any{"path": path, "content": content},
		})
	})
	if err != nil {
		t.Fatalf("CallTool(write_file %q): %v%s", path, err, hint(err.Error()))
	}
	return res
}

func hint(msg string) string {
	switch {
	case strings.Contains(msg, "cannot be sent while serving a request"):
		return "\n(you called req.Session.Elicit: on 2026-07-28 return a CallToolResult with InputRequests instead, and read req.Params.InputResponses on the retry)"
	case strings.Contains(msg, "both content and inputRequests"):
		return "\n(round 1 must return ONLY InputRequests, with no Content)"
	case strings.Contains(msg, "does not support elicitation"):
		return "\n(the client has no elicitation support: check req.Session.InitializeParams().Capabilities.Elicitation BEFORE asking, and return an IsError result)"
	case strings.Contains(msg, "unknown tool"):
		return "\n(register write_file in server.New, unless cfg.ReadOnly)"
	}
	return ""
}

// mustStructured decodes write_file's structured result, failing on an error result.
func mustStructured(t *testing.T, res *mcp.CallToolResult) writeResult {
	t.Helper()
	if res.IsError {
		t.Fatalf("write_file returned an error result: %s%s", harness.Text(res), hint(harness.Text(res)))
	}
	out := harness.Structured[writeResult](t, res)
	if out.Written == nil {
		t.Fatalf("write_file's structured output has no \"written\" field: %+v", res.StructuredContent)
	}
	return out
}

func readFile(t *testing.T, path string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data), true
}

// write_file is listed with a description, the two required inputs and honest annotations.
func TestWriteFileTool(t *testing.T) {
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, nil)
	tool := harness.FindTool(t, cs, "write_file")
	if tool.Description == "" {
		t.Error("write_file has no Description")
	}
	if tool.OutputSchema == nil {
		t.Error("write_file has no OutputSchema: return a typed struct as the Out value of mcp.AddTool")
	}
	schema, _ := json.Marshal(tool.InputSchema)
	var in struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	_ = json.Unmarshal(schema, &in)
	for _, p := range []string{"path", "content"} {
		if _, ok := in.Properties[p]; !ok {
			t.Errorf("write_file input schema has no %q property: %s", p, schema)
		} else if !slices.Contains(in.Required, p) {
			t.Errorf("write_file input %q is optional, want required (no omitempty): %s", p, schema)
		}
	}
	a := tool.Annotations
	switch {
	case a == nil:
		t.Fatal("write_file has no Annotations: clients use them to decide how carefully to treat a tool")
	case a.ReadOnlyHint:
		t.Error("write_file is annotated ReadOnlyHint: true, but it changes files")
	}
	if a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Errorf("write_file DestructiveHint = %v, want true (it can overwrite a file; set DestructiveHint: new(true))", ptrStr(a.DestructiveHint))
	}
	if !a.IdempotentHint {
		t.Error("write_file IdempotentHint = false, want true (writing the same content twice changes nothing more)")
	}
}

func ptrStr(b *bool) string {
	if b == nil {
		return "nil (which clients read as true, but say it explicitly)"
	}
	return strconv.FormatBool(*b)
}

// Read-only mode leaves write_file out, like run_tests.
func TestWriteFileReadOnly(t *testing.T) {
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t), ReadOnly: true}, nil)
	names := harness.ToolNames(t, cs)
	for _, banned := range []string{"write_file", "run_tests"} {
		if slices.Contains(names, banned) {
			t.Errorf("with ReadOnly the server lists %q; %s must not be registered", names, banned)
		}
	}
}

// The user is asked once, with the path and size and a required boolean "confirm"; on yes the file
// is written (parent directories too) and the result says so.
func TestWriteFileConfirmed(t *testing.T) {
	for _, version := range eras {
		t.Run(version, func(t *testing.T) {
			ws := harness.Workspace(t)
			const rel, content = "docs/notes/todo.md", "# TODO\n\n- ship devmcp\n"
			target := filepath.Join(ws, filepath.FromSlash(rel))
			user := &fakeUser{
				action:  "accept",
				content: map[string]any{"confirm": true},
				ws:      ws,
				notYet:  []string{rel, "docs"},
			}
			cs := connect(t, server.Config{Root: ws}, user, version)

			out := mustStructured(t, callWrite(t, cs, rel, content))
			user.checkNothingEarly(t)
			qs := user.questions()
			if len(qs) != 1 {
				t.Fatalf("the user was asked %d times for one write_file call, want 1", len(qs))
			}
			checkQuestion(t, qs[0], rel, len(content))
			if !*out.Written || out.Path != rel || out.Bytes != len(content) {
				t.Errorf("write_file result = {written:%v path:%q bytes:%d}, want {written:true path:%q bytes:%d}",
					*out.Written, out.Path, out.Bytes, rel, len(content))
			}
			if got, ok := readFile(t, target); !ok {
				t.Errorf("the user confirmed, but %s doesn't exist (create the parent directories with root.MkdirAll)", rel)
			} else if got != content {
				t.Errorf("%s = %q, want %q", rel, got, content)
			}
		})
	}
}

// Overwriting an existing file replaces its contents, after asking.
func TestWriteFileOverwrite(t *testing.T) {
	ws := harness.Workspace(t)
	const rel, content = "calc/calc.go", "package calc\n\n// rewritten\n"
	user := &fakeUser{action: "accept", content: map[string]any{"confirm": true}}
	cs := connect(t, server.Config{Root: ws}, user, eras[0])
	out := mustStructured(t, callWrite(t, cs, rel, content))
	if !*out.Written {
		t.Errorf("written = false after the user confirmed overwriting %s", rel)
	}
	if got, _ := readFile(t, filepath.Join(ws, "calc", "calc.go")); got != content {
		t.Errorf("%s = %q after overwrite, want %q", rel, got, content)
	}
	if n := len(user.questions()); n != 1 {
		t.Errorf("the user was asked %d times, want 1 (an overwrite needs confirmation too)", n)
	}
}

// checkQuestion verifies the elicitation request names the path and the byte count, and asks for a
// required boolean "confirm".
func checkQuestion(t *testing.T, q *mcp.ElicitParams, rel string, size int) {
	t.Helper()
	if !strings.Contains(q.Message, rel) {
		t.Errorf("elicitation Message %q doesn't mention %q: the user can't tell what they're confirming", q.Message, rel)
	}
	if !strings.Contains(q.Message, strconv.Itoa(size)) {
		t.Errorf("elicitation Message %q doesn't mention the size, %d bytes", q.Message, size)
	}
	raw, err := json.Marshal(q.RequestedSchema)
	if err != nil {
		t.Fatalf("marshal RequestedSchema: %v", err)
	}
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("RequestedSchema %s isn't a JSON object: %v", raw, err)
	}
	if schema.Properties["confirm"].Type != "boolean" {
		t.Errorf("RequestedSchema %s has no boolean property \"confirm\"", raw)
	}
	if !slices.Contains(schema.Required, "confirm") {
		t.Errorf("RequestedSchema %s doesn't list \"confirm\" as required", raw)
	}
}

// Decline, cancel, or accept without confirm: nothing is written (not even a directory), and that is
// a normal result with written=false, not an error.
func TestWriteFileNotConfirmed(t *testing.T) {
	answers := []struct {
		name    string
		action  string
		content map[string]any
	}{
		{"decline", "decline", nil},
		{"cancel", "cancel", nil},
		{"accept-confirm-false", "accept", map[string]any{"confirm": false}},
	}
	for _, version := range eras {
		for _, a := range answers {
			t.Run(version+"/"+a.name, func(t *testing.T) {
				ws := harness.Workspace(t)
				const rel, content = "newdir/plan.txt", "do the thing\n"
				target := filepath.Join(ws, "newdir", "plan.txt")
				user := &fakeUser{action: a.action, content: a.content, ws: ws, notYet: []string{"newdir"}}
				cs := connect(t, server.Config{Root: ws}, user, version)

				res := callWrite(t, cs, rel, content)
				if res.IsError {
					t.Fatalf("user answered %s: write_file returned an error result (%q), want a normal result with written=false: "+
						"the tool worked, the user just said no", a.name, harness.Text(res))
				}
				out := mustStructured(t, res)
				user.checkNothingEarly(t)
				if *out.Written {
					t.Errorf("user answered %s: written = true, want false", a.name)
				}
				if _, err := os.Lstat(target); err == nil {
					t.Errorf("user answered %s, but %s was written anyway (only an \"accept\" with confirm=true may write)", a.name, rel)
				}
				if _, err := os.Lstat(filepath.Join(ws, "newdir")); err == nil {
					t.Errorf("user answered %s, but newdir/ was created: create directories only once the user has said yes", a.name)
				}
				if text := harness.Text(res); !strings.Contains(strings.ToLower(text), "declined") {
					t.Errorf("result text = %q, want it to tell the model the user declined (containing \"declined\")", text)
				}
			})
		}
	}
}

// A client that can't ask the user gets an error result, and nothing is written.
func TestWriteFileWithoutElicitation(t *testing.T) {
	for _, version := range eras {
		t.Run(version, func(t *testing.T) {
			ws := harness.Workspace(t)
			cs := connect(t, server.Config{Root: ws}, nil, version)
			res := callWrite(t, cs, "notes.txt", "hello\n")
			if !res.IsError {
				t.Errorf("the client has no elicitation capability, yet write_file returned a normal result (%s); want IsError "+
					"explaining the client can't confirm", clip(harness.Text(res)))
			}
			if harness.Text(res) == "" {
				t.Error("the error result has no text: tell the model why nothing was written")
			}
			if _, ok := readFile(t, filepath.Join(ws, "notes.txt")); ok {
				t.Error("notes.txt was written without anyone confirming")
			}
		})
	}
}

// Config.AllowUnconfirmedWrites lets a client without elicitation write; a client that CAN ask still asks.
func TestWriteFileAllowUnconfirmed(t *testing.T) {
	for _, version := range eras {
		t.Run(version, func(t *testing.T) {
			ws := harness.Workspace(t)
			cs := connect(t, server.Config{Root: ws, AllowUnconfirmedWrites: true}, nil, version)
			res := callWrite(t, cs, "a/b.txt", "unconfirmed\n")
			if res.IsError {
				t.Fatalf("with AllowUnconfirmedWrites, a client without elicitation got an error result (%s);\n"+
					"want the file written without asking: that's what the flag is for", clip(harness.Text(res)))
			}
			out := mustStructured(t, res)
			if !*out.Written {
				t.Error("with AllowUnconfirmedWrites and a client that can't ask, written = false, want true")
			}
			if got, _ := readFile(t, filepath.Join(ws, "a", "b.txt")); got != "unconfirmed\n" {
				t.Errorf("a/b.txt = %q, want %q", got, "unconfirmed\n")
			}
		})
	}
	t.Run("client can ask", func(t *testing.T) {
		ws := harness.Workspace(t)
		user := &fakeUser{action: "decline"}
		cs := connect(t, server.Config{Root: ws, AllowUnconfirmedWrites: true}, user, eras[0])
		res := callWrite(t, cs, "c.txt", "should not land\n")
		if n := len(user.questions()); n != 1 {
			t.Errorf("AllowUnconfirmedWrites with a client that supports elicitation: the user was asked %d times, want 1 "+
				"(the flag is only a fallback for clients that can't ask)", n)
		}
		if _, ok := readFile(t, filepath.Join(ws, "c.txt")); ok || res.IsError {
			t.Errorf("the user declined: want no file and a normal result; file written=%v, IsError=%v", ok, res.IsError)
		}
	})
}

// Paths outside the workspace are refused before the user is even asked, however they're spelled.
func TestWriteFileUnsafePaths(t *testing.T) {
	ws := harness.Workspace(t)
	outside := t.TempDir()
	if real, err := filepath.EvalSymlinks(outside); err == nil {
		outside = real
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinks := true
	if err := os.Symlink(outside, filepath.Join(ws, "escape")); err != nil {
		symlinks = false
	}
	if err := os.Symlink(secret, filepath.Join(ws, "leak.txt")); err != nil {
		symlinks = false
	}

	cases := []struct{ path, why string }{
		{"../outside.txt", "a ../ path"},
		{"calc/../../outside.txt", "a ../ path in the middle"},
		{filepath.Join(outside, "abs.txt"), "an absolute path"},
		{"", "an empty path"},
		{"calc", "an existing directory"},
	}
	if symlinks {
		cases = append(cases,
			struct{ path, why string }{"escape/new.txt", "a symlinked directory that points outside (use os.Root)"},
			struct{ path, why string }{"leak.txt", "a symlink to a file outside (use os.Root)"},
		)
	} else {
		t.Log("cannot create symlinks here; the symlink cases are skipped")
	}

	user := &fakeUser{action: "accept", content: map[string]any{"confirm": true}}
	cs := connect(t, server.Config{Root: ws}, user, eras[0])
	for _, tc := range cases {
		before := len(user.questions())
		res := callWrite(t, cs, tc.path, "pwned\n")
		if !res.IsError {
			t.Errorf("write_file %q (%s) returned a normal result (%s), want IsError", tc.path, tc.why, clip(harness.Text(res)))
		}
		if len(user.questions()) != before {
			t.Errorf("write_file %q (%s): the user was asked to confirm a write that must be refused; validate the path before asking", tc.path, tc.why)
		}
	}
	if got, _ := readFile(t, secret); got != "original" {
		t.Errorf("a file outside the workspace was overwritten: %s = %q", secret, got)
	}
	for _, name := range []string{"outside.txt", "abs.txt", "new.txt"} {
		if _, ok := readFile(t, filepath.Join(outside, name)); ok {
			t.Errorf("write_file created %s outside the workspace", filepath.Join(outside, name))
		}
	}
	if _, ok := readFile(t, filepath.Join(filepath.Dir(ws), "outside.txt")); ok {
		t.Errorf("write_file created %s outside the workspace", filepath.Join(filepath.Dir(ws), "outside.txt"))
	}
}

// The binary's -allow-unconfirmed-writes flag reaches Config.AllowUnconfirmedWrites (over stdio,
// with a client that has no elicitation support).
func TestBinaryAllowUnconfirmedWritesFlag(t *testing.T) {
	bin := harness.BuildBinary(t)
	for _, tc := range []struct {
		args  []string
		write bool
	}{
		{nil, false},
		{[]string{"-allow-unconfirmed-writes"}, true},
	} {
		t.Run(fmt.Sprintf("flags=%q", tc.args), func(t *testing.T) {
			ws := harness.Workspace(t)
			cmd := exec.Command(bin, append(tc.args, "-root", ws)...)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "v0.0.1"}, nil)
			cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
			if err != nil {
				t.Fatalf("connecting to devmcp %q over stdio: %v", tc.args, err)
			}
			defer cs.Close()
			res := callWrite(t, cs, "flag.txt", "via flag\n")
			_, written := readFile(t, filepath.Join(ws, "flag.txt"))
			if written != tc.write || res.IsError == tc.write {
				t.Errorf("devmcp %q, client without elicitation: file written=%v IsError=%v, want written=%v IsError=%v (%s)",
					tc.args, written, res.IsError, tc.write, !tc.write, clip(harness.Text(res)))
			}
		})
	}
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

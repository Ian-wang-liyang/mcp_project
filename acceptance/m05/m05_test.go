// Milestone 5 acceptance tests: an audit-log middleware, read-only mode, and the binary's
// Streamable HTTP mode. Brief: milestones/05_ship.md
//
// The binary tests start real devmcp processes and send them signals. They are Unix-only.
package m05

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"devmcp/acceptance/internal/harness"
	"devmcp/server"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ─── read-only mode ───

// readTools must exist in every mode; runTools only when the server may run code or change files.
var readTools = []string{"workspace_info", "list_files", "search_code"}

// Config.ReadOnly leaves run_tests out entirely; the read tools stay.
func TestReadOnly(t *testing.T) {
	ws := harness.Workspace(t)

	normal := harness.ToolNames(t, harness.Connect(t, server.Config{Root: ws}, nil))
	if !slices.Contains(normal, "run_tests") {
		t.Fatalf("without ReadOnly the server lists %q, want run_tests among them", normal)
	}

	cs := harness.Connect(t, server.Config{Root: ws, ReadOnly: true}, nil)
	names := harness.ToolNames(t, cs)
	for _, want := range readTools {
		if !slices.Contains(names, want) {
			t.Errorf("with ReadOnly the server lists %q: %s is missing (read tools stay in read-only mode)", names, want)
		}
	}
	if slices.Contains(names, "run_tests") {
		t.Errorf("with ReadOnly the server still lists run_tests (%q): don't register tools that run code", names)
	}

	// Not registered means not callable either, not just hidden from tools/list.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "run_tests", Arguments: map[string]any{"packages": "./calc/"}})
	if err == nil && !res.IsError {
		t.Errorf("with ReadOnly, calling run_tests anyway succeeded (%s); hiding a tool from the list is not enough", clip(harness.Text(res)))
	}
}

// ─── the audit log ───

// auditLine is one line of the audit log, as the brief specifies it. Pointers catch missing fields.
type auditLine struct {
	Time       *string  `json:"time"`
	Tool       *string  `json:"tool"`
	DurationMS *float64 `json:"duration_ms"`
	IsError    *bool    `json:"is_error"`
}

// checkedWriter is the io.Writer the audit tests hand to Config.AuditLog. Like a bytes.Buffer, it is
// NOT safe for concurrent use. Unlike one, it notices: each Write takes a few milliseconds, and if
// two Writes ever overlap it records that, so a missing mutex fails reliably instead of only
// sometimes under -race.
type checkedWriter struct {
	inFlight    atomic.Int32
	maxInFlight atomic.Int32 // the most Writes ever running at the same time
	slow        time.Duration

	mu  sync.Mutex // guards buf for the test's reads only; it is not what keeps Write calls apart
	buf bytes.Buffer
}

func (w *checkedWriter) Write(p []byte) (int, error) {
	n := w.inFlight.Add(1)
	defer w.inFlight.Add(-1)
	for {
		m := w.maxInFlight.Load()
		if n <= m || w.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(w.slow) // widen the window, as a slow disk would
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *checkedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// lines waits up to 2s for the log to hold at least n complete lines, then parses all of them.
func (w *checkedWriter) lines(t *testing.T, n int) []auditLine {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for strings.Count(w.String(), "\n") < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return parseAudit(t, w.String())
}

// parseAudit parses a JSON-lines audit log and checks every line has the four fields, well-formed.
func parseAudit(t *testing.T, log string) []auditLine {
	t.Helper()
	if log != "" && !strings.HasSuffix(log, "\n") {
		t.Errorf("the audit log doesn't end with a newline: every entry is one line ending in \"\\n\"\nlog: %q", clip(log))
	}
	var out []auditLine
	for i, raw := range strings.Split(strings.TrimSuffix(log, "\n"), "\n") {
		if raw == "" && log == "" {
			break
		}
		var l auditLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("audit line %d is not one JSON object: %q (%v)\n(write each entry as a single line; under concurrency, unserialized writes interleave)", i+1, clip(raw), err)
		}
		switch {
		case l.Time == nil || l.Tool == nil || l.DurationMS == nil || l.IsError == nil:
			t.Fatalf("audit line %d = %s, want all of \"time\", \"tool\", \"duration_ms\" (a number) and \"is_error\" (a bool)", i+1, raw)
		case *l.DurationMS < 0:
			t.Errorf("audit line %d: duration_ms = %v, want >= 0", i+1, *l.DurationMS)
		}
		ts, err := time.Parse(time.RFC3339, *l.Time)
		if err != nil {
			t.Errorf("audit line %d: time %q is not RFC 3339 (use t.Format(time.RFC3339)): %v", i+1, *l.Time, err)
		} else if d := time.Since(ts); d < -time.Minute || d > time.Minute {
			t.Errorf("audit line %d: time %q is %v away from now", i+1, *l.Time, d.Round(time.Second))
		}
		out = append(out, l)
	}
	return out
}

func describe(lines []auditLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, fmt.Sprintf("%s is_error=%v", *l.Tool, *l.IsError))
	}
	return out
}

// One line per tools/call, in order, with the tool name and whether it failed; nothing for other methods.
func TestAuditLog(t *testing.T) {
	w := &checkedWriter{}
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t), AuditLog: w}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Requests that are not tool calls must not be logged.
	if _, err := cs.ListTools(ctx, nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "workspace:///go.mod"}); err != nil {
		t.Fatalf("ReadResource: %v", err)
	}

	if res := harness.CallTool(t, cs, "workspace_info", map[string]any{}); res.IsError {
		t.Fatalf("workspace_info returned an error result: %s", harness.Text(res))
	}
	// A tool error: an IsError result.
	if res := harness.CallTool(t, cs, "list_files", map[string]any{"dir": "no-such-dir"}); !res.IsError {
		t.Fatalf("list_files on a missing directory: want an IsError result, got %s", clip(harness.Text(res)))
	}
	// A protocol error: there is no result at all, only err.
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "no_such_tool", Arguments: map[string]any{}}); err == nil {
		t.Fatal("calling an unknown tool succeeded; want a protocol error from the SDK")
	}
	if res := harness.CallTool(t, cs, "search_code", map[string]any{"query": "func Add"}); res.IsError {
		t.Fatalf("search_code returned an error result: %s", harness.Text(res))
	}

	got := describe(w.lines(t, 4))
	want := []string{
		"workspace_info is_error=false",
		"list_files is_error=true",
		"no_such_tool is_error=true",
		"search_code is_error=false",
	}
	if !slices.Equal(got, want) {
		hint := ""
		switch {
		case len(got) == 0:
			hint = "\n(nothing was logged: add the middleware with s.AddReceivingMiddleware when cfg.AuditLog != nil)"
		case len(got) > len(want):
			hint = "\n(log only method \"tools/call\": tools/list and resources/read must not appear)"
		case len(got) == 3:
			hint = "\n(a call that fails with a protocol error must be logged too: log after next returns, whatever it returned)"
		default:
			hint = "\n(is_error is true when next returns an error OR a *mcp.CallToolResult with IsError set)"
		}
		t.Errorf("audit log entries (tool, is_error):\n  got  %q\n  want %q%s\nlog:\n%s", got, want, hint, w.String())
	}
}

// duration_ms is in milliseconds and measures the call.
func TestAuditLogDuration(t *testing.T) {
	w := &checkedWriter{}
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t), AuditLog: w}, nil)

	start := time.Now()
	res := harness.CallTool(t, cs, "run_tests", map[string]any{"packages": "./calc/"}) // compiles and runs: hundreds of ms
	wall := float64(time.Since(start).Microseconds()) / 1000
	if res.IsError {
		t.Fatalf("run_tests returned an error result: %s", harness.Text(res))
	}
	lines := w.lines(t, 1)
	if len(lines) != 1 {
		t.Fatalf("got %d audit lines for one call, want 1:\n%s", len(lines), w.String())
	}
	if got := *lines[0].DurationMS; got < wall/2 || got > wall+50 {
		t.Errorf("run_tests took %.0fms as the client saw it, but duration_ms = %v.\n"+
			"Measure time.Since(start) around next(...) and convert to milliseconds (not seconds, not nanoseconds)", wall, got)
	}
}

// Many clients calling at once: every call gets one intact line, and Writes never overlap.
func TestAuditLogConcurrent(t *testing.T) {
	w := &checkedWriter{slow: 2 * time.Millisecond}
	s := harness.NewServer(t, server.Config{Root: harness.Workspace(t), AuditLog: w})

	const clients, calls = 8, 10
	sessions := make([]*mcp.ClientSession, clients)
	for i := range sessions {
		sessions[i] = harness.ConnectServer(t, s, nil) // separate sessions really run in parallel
	}
	var (
		wg   sync.WaitGroup
		errs = make(chan error, clients*calls)
	)
	harness.Within(t, 30*time.Second, func() {
		for _, cs := range sessions {
			wg.Go(func() {
				for j := range calls {
					name, args := "workspace_info", map[string]any{}
					if j%2 == 1 {
						name, args = "list_files", map[string]any{"pattern": "*.go"}
					}
					res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
					if err == nil && res.IsError {
						err = fmt.Errorf("%s: error result %s", name, harness.Text(res))
					}
					if err != nil {
						errs <- err
					}
				}
			})
		}
		wg.Wait()
	})
	close(errs)
	for err := range errs {
		t.Fatalf("a concurrent call failed: %v", err)
	}

	if n := w.maxInFlight.Load(); n > 1 {
		t.Errorf("up to %d Write calls on the audit writer ran at the same time.\n"+
			"Requests run concurrently, and an io.Writer like *os.File or bytes.Buffer is not safe for that:\n"+
			"guard the Write with a sync.Mutex (one per middleware, created outside the per-request closure)", n)
	}
	lines := w.lines(t, clients*calls)
	if len(lines) != clients*calls {
		t.Errorf("got %d audit lines for %d calls, want one per call", len(lines), clients*calls)
	}
	counts := map[string]int{}
	for _, l := range lines {
		counts[*l.Tool]++
	}
	if counts["workspace_info"] != clients*calls/2 || counts["list_files"] != clients*calls/2 {
		t.Errorf("audit lines per tool = %v, want %d workspace_info and %d list_files", counts, clients*calls/2, clients*calls/2)
	}
}

// ─── the binary ───

// proc is a devmcp process started by a test. It is killed when the test ends.
type proc struct {
	cmd    *exec.Cmd
	stderr *syncBuffer
	done   chan struct{} // closed once the process has exited
	err    error         // cmd.Wait's result, valid after done
}

// start runs the devmcp binary with args, collecting its stderr.
func start(t *testing.T, bin string, args ...string) *proc {
	t.Helper()
	p := &proc{cmd: exec.Command(bin, args...), stderr: &syncBuffer{}, done: make(chan struct{})}
	p.cmd.Stderr = p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("starting devmcp %s: %v", strings.Join(args, " "), err)
	}
	go func() {
		p.err = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		p.cmd.Process.Kill()
		<-p.done
		if t.Failed() {
			t.Logf("devmcp stderr:\n%s", p.stderr.String())
		}
	})
	return p
}

var listenRE = regexp.MustCompile(`listening on (http://\S+/mcp)`)

// startHTTP starts `devmcp -http 127.0.0.1:0 -root ws extra...` and returns it with its MCP endpoint URL.
func startHTTP(t *testing.T, ws string, extra ...string) (*proc, string) {
	t.Helper()
	bin := harness.BuildBinary(t)
	args := append([]string{"-http", "127.0.0.1:0", "-root", ws}, extra...)
	p := start(t, bin, args...)
	deadline := time.After(15 * time.Second)
	for {
		if m := listenRE.FindStringSubmatch(p.stderr.String()); m != nil {
			return p, m[1]
		}
		select {
		case <-p.done:
			t.Fatalf("devmcp %s exited (%v) before printing \"listening on http://HOST:PORT/mcp\".\nstderr:\n%s",
				strings.Join(args, " "), p.err, p.stderr.String())
		case <-deadline:
			t.Fatalf("devmcp %s didn't print \"listening on http://HOST:PORT/mcp\" to stderr within 15s.\n"+
				"net.Listen first, then log the listener's real address (with port 0 the OS picks it).\nstderr so far:\n%s",
				strings.Join(args, " "), p.stderr.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// connectHTTP connects a new client to the Streamable HTTP endpoint url.
func connectHTTP(t *testing.T, url string, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "v0.0.1"}, opts)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url}, nil)
	if err != nil {
		t.Fatalf("connecting to %s with mcp.StreamableClientTransport: %v\n(serve mcp.NewStreamableHTTPHandler at the path /mcp)", url, err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

type workspaceInfo struct {
	Root string `json:"root"`
}

// workspaceRoot calls workspace_info and returns its root.
func workspaceRoot(t *testing.T, cs *mcp.ClientSession) string {
	t.Helper()
	res := harness.CallTool(t, cs, "workspace_info", map[string]any{})
	if res.IsError {
		t.Fatalf("workspace_info returned an error result: %s", harness.Text(res))
	}
	return harness.Structured[workspaceInfo](t, res).Root
}

// `devmcp -http 127.0.0.1:0` serves MCP over Streamable HTTP at /mcp and reports its address.
func TestHTTPServe(t *testing.T) {
	ws := harness.Workspace(t)
	_, url := startHTTP(t, ws)
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Errorf("devmcp reported %s, want http://127.0.0.1:PORT/mcp (the address it actually bound)", url)
	}
	if strings.HasSuffix(url, ":0/mcp") {
		t.Fatalf("devmcp reported %s: that's the -http flag, not the bound address. Print ln.Addr() after net.Listen", url)
	}
	cs := connectHTTP(t, url, nil)
	if got := workspaceRoot(t, cs); got != ws {
		t.Errorf("over HTTP, workspace_info root = %q, want %q (is -root wired up in HTTP mode too?)", got, ws)
	}
	t.Logf("negotiated protocol %s", cs.InitializeResult().ProtocolVersion)
}

// One HTTP server, two clients at once: each gets its own session and both work.
func TestHTTPTwoClients(t *testing.T) {
	ws := harness.Workspace(t)
	_, url := startHTTP(t, ws)
	a := connectHTTP(t, url, nil)
	b := connectHTTP(t, url, nil)
	if a.ID() != "" && a.ID() == b.ID() {
		t.Errorf("both clients got session id %q, want one session per client", a.ID())
	}

	var wg sync.WaitGroup
	roots := make([]string, 2)
	errs := make([]error, 2)
	harness.Within(t, 20*time.Second, func() {
		for i, cs := range []*mcp.ClientSession{a, b} {
			wg.Go(func() {
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "workspace_info", Arguments: map[string]any{}})
				if err != nil {
					errs[i] = err
					return
				}
				var info workspaceInfo
				data, _ := json.Marshal(res.StructuredContent)
				_ = json.Unmarshal(data, &info)
				roots[i] = info.Root
			})
		}
		wg.Wait()
	})
	for i := range 2 {
		if errs[i] != nil {
			t.Errorf("client %d: workspace_info: %v", i+1, errs[i])
		} else if roots[i] != ws {
			t.Errorf("client %d: workspace_info root = %q, want %q", i+1, roots[i], ws)
		}
	}
	// Closing one client ends its session only.
	a.Close()
	if got := workspaceRoot(t, b); got != ws {
		t.Errorf("after the first client closed, the second got root %q, want %q", got, ws)
	}
}

// A request whose Host header isn't localhost is refused (DNS-rebinding protection), while the
// same request with the real Host works.
func TestHTTPRejectsForeignHost(t *testing.T) {
	_, url := startHTTP(t, harness.Workspace(t))
	post := func(host string) (int, string) {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25",` +
			`"capabilities":{},"clientInfo":{"name":"raw","version":"v0.0.1"}}}`
		req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			req.Host = host // what a browser sends after DNS rebinding: the attacker's name, your IP
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("POST %s (Host %q): %v", url, host, err)
		}
		defer resp.Body.Close()
		var b strings.Builder
		sc := bufio.NewScanner(resp.Body)
		for i := 0; i < 3 && sc.Scan(); i++ {
			b.WriteString(sc.Text() + "\n")
		}
		return resp.StatusCode, b.String()
	}

	if code, body := post(""); code != http.StatusOK {
		t.Fatalf("a plain initialize POST to %s got HTTP %d (%q), want 200", url, code, clip(body))
	}
	if code, body := post("evil.example"); code != http.StatusForbidden {
		t.Errorf("a POST with Host: evil.example got HTTP %d (%q), want 403 Forbidden.\n"+
			"The SDK's handler refuses it by default; did you set DisableLocalhostProtection?", code, clip(body))
	}
}

// -readonly reaches Config.ReadOnly (tested over stdio, the way Claude Code starts devmcp).
func TestBinaryReadOnlyFlag(t *testing.T) {
	bin := harness.BuildBinary(t)
	ws := harness.Workspace(t)
	cmd := exec.Command(bin, "-readonly", "-root", ws)
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "v0.0.1"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connecting to `devmcp -readonly -root %s` over stdio: %v\nstderr:\n%s", ws, err, stderr.String())
	}
	defer cs.Close()
	names := harness.ToolNames(t, cs)
	if slices.Contains(names, "run_tests") {
		t.Errorf("devmcp -readonly lists %q; run_tests should be gone (wire -readonly to Config.ReadOnly)", names)
	}
	if !slices.Contains(names, "workspace_info") {
		t.Errorf("devmcp -readonly lists %q; workspace_info should still be there", names)
	}
}

// -audit PATH appends JSON lines to a file; -audit - writes them to stderr (never stdout).
func TestBinaryAuditFlag(t *testing.T) {
	t.Run("file over HTTP", func(t *testing.T) {
		ws := harness.Workspace(t)
		logFile := filepath.Join(t.TempDir(), "audit.jsonl")
		earlier := fmt.Sprintf(`{"time":%q,"tool":"earlier","duration_ms":1,"is_error":false}`+"\n", time.Now().UTC().Format(time.RFC3339))
		if err := os.WriteFile(logFile, []byte(earlier), 0o644); err != nil {
			t.Fatal(err)
		}
		_, url := startHTTP(t, ws, "-audit", logFile)
		workspaceRoot(t, connectHTTP(t, url, nil))

		var lines []auditLine
		deadline := time.Now().Add(2 * time.Second)
		for {
			data, _ := os.ReadFile(logFile)
			if strings.Count(string(data), "\n") >= 2 || time.Now().After(deadline) {
				if len(data) == 0 {
					t.Fatalf("%s is empty: -audit must APPEND (os.O_APPEND), keeping the earlier line", logFile)
				}
				lines = parseAuditLoose(t, string(data))
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		got := describe(lines)
		if want := []string{"earlier is_error=false", "workspace_info is_error=false"}; !slices.Equal(got, want) {
			t.Errorf("audit file entries = %q, want %q\n(-audit PATH opens the file with O_APPEND|O_CREATE|O_WRONLY and passes it as Config.AuditLog)", got, want)
		}
	})

	t.Run("stderr over stdio", func(t *testing.T) {
		bin := harness.BuildBinary(t)
		ws := harness.Workspace(t)
		cmd := exec.Command(bin, "-audit", "-", "-root", ws)
		stderr := &syncBuffer{}
		cmd.Stderr = stderr
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "v0.0.1"}, nil)
		cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			t.Fatalf("connecting to `devmcp -audit - -root %s` over stdio: %v\n(audit lines must go to stderr: stdout is the protocol)\nstderr:\n%s", ws, err, stderr.String())
		}
		defer cs.Close()
		if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_info", Arguments: map[string]any{}}); err != nil {
			t.Fatalf("with -audit -, CallTool(workspace_info) over stdio failed: %v\n"+
				"(did the audit line go to stdout? \"-\" means stderr: stdout carries the protocol)", err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(stderr.String(), `"workspace_info"`) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if !strings.Contains(stderr.String(), `"tool":"workspace_info"`) {
			t.Errorf("with -audit -, stderr has no audit line for workspace_info (want {\"tool\":\"workspace_info\",...}).\nstderr:\n%s", stderr.String())
		}
	})
}

// parseAuditLoose parses only the lines that look like audit entries (a file may hold other output).
func parseAuditLoose(t *testing.T, log string) []auditLine {
	t.Helper()
	var keep []string
	for _, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "{") {
			keep = append(keep, l)
		}
	}
	if len(keep) == 0 {
		return nil
	}
	return parseAudit(t, strings.Join(keep, "\n")+"\n")
}

// BONUS: SIGINT shuts the HTTP server down and the process exits 0, even with a client connected.
func TestBonusGracefulShutdown(t *testing.T) {
	p, url := startHTTP(t, harness.Workspace(t))
	cs := connectHTTP(t, url, nil)
	workspaceRoot(t, cs)

	sent := time.Now()
	if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT: %v", err)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("devmcp was still running 5s after SIGINT, with a client connected.\n" +
			"Catch the signal with signal.NotifyContext, then call srv.Shutdown with a timeout ctx and srv.Close after it:\n" +
			"the client's open GET stream never finishes by itself, so Shutdown(context.Background()) waits forever")
	}
	var exitErr *exec.ExitError
	if errors.As(p.err, &exitErr) {
		t.Errorf("after SIGINT devmcp exited with %v, want status 0 (a signal is a normal way to stop; http.ErrServerClosed is not an error)", p.err)
	}
	t.Logf("exited %v after SIGINT", time.Since(sent).Round(time.Millisecond))
}

// ─── helpers ───

// syncBuffer is a bytes.Buffer that is safe for concurrent use (the process writes, the test reads).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// Milestone 4 acceptance tests: a long-running run_tests tool with progress, logging, cancellation
// and a timeout. Brief: milestones/04_test_runner.md
//
// These tests start real `go test` processes. They are Unix-only, like the milestone.
package m04

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"devmcp/acceptance/internal/harness"
	"devmcp/server"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testResult struct {
	Package string `json:"package"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Output  string `json:"output"`
}

type runResult struct {
	Passed  bool          `json:"passed"`
	Summary string        `json:"summary"`
	Tests   *[]testResult `json:"tests"` // a pointer, so a missing or null "tests" is caught
	Output  string        `json:"output"`
}

const pkgPrefix = "example.com/sampleproj/"

// runTests calls run_tests and decodes its structured result, failing on an error result.
func runTests(t *testing.T, cs *mcp.ClientSession, args map[string]any) runResult {
	t.Helper()
	res := harness.CallTool(t, cs, "run_tests", args)
	if res.IsError {
		t.Fatalf("run_tests %v returned an error result: %s\n(failing tests are not a tool error: return passed=false)", args, harness.Text(res))
	}
	out := harness.Structured[runResult](t, res)
	if out.Tests == nil {
		t.Fatalf("run_tests %v: structured output has no \"tests\" array (null or missing): %+v", args, out)
	}
	return out
}

// brief renders tests as "pkg Name status" lines, sorted, for comparing and printing.
func brief(tests []testResult) []string {
	var out []string
	for _, tr := range tests {
		out = append(out, fmt.Sprintf("%s %s %s", strings.TrimPrefix(tr.Package, pkgPrefix), tr.Name, tr.Status))
	}
	slices.Sort(out)
	return out
}

func findTest(tests []testResult, name string) (testResult, bool) {
	for _, tr := range tests {
		if tr.Name == name {
			return tr, true
		}
	}
	return testResult{}, false
}

// writeFile creates ws/rel (and its directory) with content.
func writeFile(t *testing.T, ws, rel, content string) {
	t.Helper()
	path := filepath.Join(ws, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// run_tests is listed with a description, the two optional inputs, and an output schema.
func TestRunTestsTool(t *testing.T) {
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, nil)
	tool := harness.FindTool(t, cs, "run_tests")
	if tool.Description == "" {
		t.Error("run_tests has no Description")
	}
	if tool.OutputSchema == nil {
		t.Error("run_tests has no OutputSchema: return a typed struct as the Out value of mcp.AddTool")
	}
	schema, _ := json.Marshal(tool.InputSchema)
	var in struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	_ = json.Unmarshal(schema, &in)
	for _, p := range []string{"packages", "run"} {
		if _, ok := in.Properties[p]; !ok {
			t.Errorf("run_tests input schema has no %q property: %s", p, schema)
		}
		if slices.Contains(in.Required, p) {
			t.Errorf("run_tests input %q is required, want optional (tag it json:\"%s,omitempty\")", p, p)
		}
	}
}

// With no arguments run_tests runs ./...: failing tests give passed=false (not a tool error) and their output.
func TestRunTestsAll(t *testing.T) {
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, nil)
	out := runTests(t, cs, map[string]any{})

	if out.Passed {
		t.Error("passed = true, but broken/ has failing tests")
	}
	if want := "2 passed, 2 failed, 0 skipped"; out.Summary != want {
		t.Errorf("summary = %q, want %q", out.Summary, want)
	}
	want := []string{"broken TestDouble fail", "broken TestDoubleZero fail", "calc TestAdd pass", "calc TestDiv pass"}
	if got := brief(*out.Tests); !slices.Equal(got, want) {
		t.Fatalf("tests (package minus %q, name, status):\n  got  %q\n  want %q\n(package is the import path from go test -json; count only events that have a Test)", pkgPrefix, got, want)
	}
	for name, msg := range map[string]string{"TestDouble": "Double(5) = 7, want 10", "TestDoubleZero": "Double(0) = 2, want 0"} {
		if tr, _ := findTest(*out.Tests, name); !strings.Contains(tr.Output, msg) {
			t.Errorf("%s output = %q, want it to contain %q (collect the test's \"output\" events)", name, tr.Output, msg)
		}
	}
	for _, name := range []string{"TestAdd", "TestDiv"} {
		if tr, _ := findTest(*out.Tests, name); tr.Output != "" {
			t.Errorf("%s passed, so its output should be empty, got %q", name, tr.Output)
		}
	}
}

// packages narrows the run to a pattern, run filters tests by regexp, and skipped tests are counted.
func TestRunTestsPackagesAndRun(t *testing.T) {
	ws := harness.Workspace(t)
	writeFile(t, ws, "calc/skip_test.go", `package calc

import "testing"

func TestSkipped(t *testing.T) { t.Skip("not today") }
`)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	out := runTests(t, cs, map[string]any{"packages": "./calc/"})
	if !out.Passed {
		t.Errorf("packages ./calc/: passed = false, want true (only calc ran, and it has no failures)\noutput: %s", out.Output)
	}
	if want := "2 passed, 0 failed, 1 skipped"; out.Summary != want {
		t.Errorf("packages ./calc/: summary = %q, want %q", out.Summary, want)
	}
	if got, want := brief(*out.Tests), []string{"calc TestAdd pass", "calc TestDiv pass", "calc TestSkipped skip"}; !slices.Equal(got, want) {
		t.Errorf("packages ./calc/: tests = %q, want %q", got, want)
	}

	out = runTests(t, cs, map[string]any{"packages": "./calc/", "run": "^TestAdd$"})
	if got, want := brief(*out.Tests), []string{"calc TestAdd pass"}; !slices.Equal(got, want) {
		t.Errorf("run ^TestAdd$: tests = %q, want %q (pass run to go test as -run)", got, want)
	}
	if want := "1 passed, 0 failed, 0 skipped"; out.Summary != want {
		t.Errorf("run ^TestAdd$: summary = %q, want %q", out.Summary, want)
	}
}

// A package that doesn't compile gives passed=false and the compiler's message, not a tool error.
func TestRunTestsBuildFailure(t *testing.T) {
	ws := harness.Workspace(t)
	writeFile(t, ws, "bad/bad.go", "package bad\n\nfunc F() int { return \"not an int\" }\n")
	writeFile(t, ws, "bad/bad_test.go", "package bad\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { F() }\n")
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	out := runTests(t, cs, map[string]any{"packages": "./bad/"})
	if out.Passed {
		t.Error("passed = true for a package that doesn't compile")
	}
	if !strings.Contains(out.Output, "cannot use") {
		t.Errorf("top-level output = %q, want the compiler error (\"cannot use ...\"): keep \"build-output\" events and stderr", out.Output)
	}
	if len(*out.Tests) != 0 {
		t.Errorf("tests = %q, want none: nothing ran", brief(*out.Tests))
	}
}

// packages must not smuggle flags into go test (e.g. -exec runs any program you like).
func TestRunTestsRejectsFlags(t *testing.T) {
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, nil)
	for _, pkgs := range []string{"-exec=/bin/echo", "-v", "--count=2"} {
		res := harness.CallTool(t, cs, "run_tests", map[string]any{"packages": pkgs})
		if !res.IsError {
			t.Errorf("packages %q: got a normal result, want IsError (reject values starting with \"-\")", pkgs)
		}
	}
}

// Every call really runs the tests: a cached "ok" would hide a flaky test or an outside change.
func TestRunTestsDoesNotCache(t *testing.T) {
	ws := harness.Workspace(t)
	counter := filepath.Join(t.TempDir(), "runs")
	// The test appends a line per run. It uses raw syscalls so go's test cache can't see the file.
	writeFile(t, ws, "counter/counter_test.go", fmt.Sprintf(`package counter

import (
	"syscall"
	"testing"
)

func TestCount(t *testing.T) {
	fd, err := syscall.Open(%q, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	syscall.Write(fd, []byte("run\n"))
}
`, counter))
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	for range 2 {
		if out := runTests(t, cs, map[string]any{"packages": "./counter/"}); !out.Passed {
			t.Fatalf("./counter/ failed: %+v", out)
		}
	}
	data, _ := os.ReadFile(counter)
	if n := strings.Count(string(data), "run\n"); n != 2 {
		t.Errorf("two run_tests calls ran TestCount %d time(s), want 2: go test reused a cached result (pass -count=1)", n)
	}
}

// progressRecorder collects progress notifications on a channel.
func progressRecorder() (chan *mcp.ProgressNotificationParams, *mcp.ClientOptions) {
	ch := make(chan *mcp.ProgressNotificationParams, 100)
	return ch, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			ch <- req.Params
		},
	}
}

// With a progress token, each finished test sends a notification with increasing Progress; without one, none.
func TestRunTestsProgress(t *testing.T) {
	ch, opts := progressRecorder()
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, opts)

	// First a call WITHOUT a token: it must send nothing. Notifications arrive in order, so any it
	// sent will show up before those of the next call.
	runTests(t, cs, map[string]any{"packages": "./calc/"})

	params := &mcp.CallToolParams{Name: "run_tests", Arguments: map[string]any{}}
	params.SetProgressToken("run-1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, params)
	if err != nil {
		t.Fatalf("CallTool(run_tests) with a progress token: %v", err)
	}
	if res.IsError {
		t.Fatalf("run_tests returned an error result: %s", harness.Text(res))
	}
	out := harness.Structured[runResult](t, res)
	if out.Tests == nil {
		t.Fatal("run_tests result has no tests array")
	}
	want := len(*out.Tests)

	// Notifications can arrive after CallTool returns: wait for them.
	var got []*mcp.ProgressNotificationParams
	timeout := time.After(5 * time.Second)
	for len(got) < want {
		select {
		case p := <-ch:
			if p.ProgressToken != "run-1" {
				t.Fatalf("got a progress notification with token %v (%q), want only token \"run-1\": send progress only when req.Params.GetProgressToken() != nil, and echo it", p.ProgressToken, p.Message)
			}
			got = append(got, p)
		case <-timeout:
			t.Fatalf("got %d progress notifications within 5s, want one per finished test (%d): call req.Session.NotifyProgress after each pass/fail/skip event", len(got), want)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i].Progress <= got[i-1].Progress {
			t.Errorf("Progress went %v → %v; it must increase with every notification", got[i-1].Progress, got[i].Progress)
		}
	}
	var msgs []string
	for _, p := range got {
		msgs = append(msgs, p.Message)
	}
	for name, status := range map[string]string{"TestAdd": "PASS", "TestDiv": "PASS", "TestDouble": "FAIL", "TestDoubleZero": "FAIL"} {
		if !slices.ContainsFunc(msgs, func(m string) bool { return slices.Contains(words(m), name) && slices.Contains(words(m), status) }) {
			t.Errorf("no progress Message has both %s and %q; messages were %q (want e.g. \"PASS calc TestAdd\")", name, status, msgs)
		}
	}
}

// words splits s into identifier-like words ("PASS calc/x TestAdd" → PASS, calc, x, TestAdd).
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	})
}

// connectVersion is harness.Connect, but negotiates a specific MCP protocol version.
func connectVersion(t *testing.T, cfg server.Config, opts *mcp.ClientOptions, version string) *mcp.ClientSession {
	t.Helper()
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

// run_tests logs at "info" when it starts, naming the go test command, on both protocol versions.
func TestRunTestsLogs(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			logs := make(chan *mcp.LoggingMessageParams, 100)
			opts := &mcp.ClientOptions{LoggingMessageHandler: func(_ context.Context, req *mcp.LoggingMessageRequest) {
				logs <- req.Params
			}}
			cs := connectVersion(t, server.Config{Root: harness.Workspace(t)}, opts, version)

			// Each version reads the client's log level from a different place; set only that one.
			params := &mcp.CallToolParams{Name: "run_tests", Arguments: map[string]any{"packages": "./calc/"}}
			if version < "2026-07-28" {
				if err := cs.SetLoggingLevel(context.Background(), &mcp.SetLoggingLevelParams{Level: "info"}); err != nil {
					t.Fatalf("SetLoggingLevel: %v", err)
				}
			} else {
				params.Meta = mcp.Meta{mcp.MetaKeyLogLevel: "info"} // per request: only the handler's ctx knows it
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := cs.CallTool(ctx, params); err != nil {
				t.Fatalf("CallTool(run_tests): %v", err)
			}

			timeout := time.After(3 * time.Second)
			var seen []string
			for {
				select {
				case m := <-logs:
					data, _ := json.Marshal(m.Data)
					if m.Level == "info" && strings.Contains(string(data), "go test") {
						return
					}
					seen = append(seen, fmt.Sprintf("%s %s", m.Level, data))
				case <-timeout:
					t.Fatalf("no \"info\" log message mentioning \"go test\" within 3s (got %q).\nLog with req.Session.Log(ctx, ...) using the handler's ctx: on 2026-07-28 the level travels in that ctx, so context.Background() logs nothing", seen)
				}
			}
		})
	}
}

// writeSlowTest adds slow/slow_test.go, whose test writes its PID to the returned file and sleeps 60s.
// It registers a cleanup that kills that process if it is still around, so a failing run leaves no orphans.
// It sets GOTMPDIR (so it can't be used with t.Parallel).
func writeSlowTest(t *testing.T, ws string) (pidFile string) {
	t.Helper()
	// A killed `go` can't delete its work directory: give it a temp dir that the test cleans up.
	t.Setenv("GOTMPDIR", t.TempDir())
	pidFile = filepath.Join(t.TempDir(), "slow.pid")
	writeFile(t, ws, "slow/slow_test.go", fmt.Sprintf(`package slow

import (
	"os"
	"strconv"
	"testing"
	"time"
)

func TestSlow(t *testing.T) {
	tmp := %q + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, %q); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Second)
}
`, pidFile, pidFile))
	t.Cleanup(func() {
		if pid := readPID(pidFile); pid > 0 && alive(pid) {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return pidFile
}

func readPID(pidFile string) int {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(string(data))
	return pid
}

func alive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// assertGone fails unless process pid exits within d (polling: an exited process lingers briefly as a zombie).
func assertGone(t *testing.T, pid int, d time.Duration, after string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for alive(pid) {
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the test binary (pid %d) was still running %v after %s.\n"+
				"Killing `go` is not enough: go test runs the compiled test binary as a child process, which keeps running.\n"+
				"Start go in its own process group (SysProcAttr{Setpgid: true}) and make cmd.Cancel kill the group: syscall.Kill(-pid, SIGKILL)", pid, d, after)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Cancelling the client's request stops go test AND the test binary it started.
func TestRunTestsCancel(t *testing.T) {
	ws := harness.Workspace(t)
	pidFile := writeSlowTest(t, ws)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	callErr := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "run_tests", Arguments: map[string]any{"packages": "./slow/"}})
		callErr <- err
	}()

	// Wait for the slow test to start (it writes its PID), unless the call ends first.
	var pid int
	deadline := time.After(30 * time.Second)
	for pid == 0 {
		select {
		case err := <-callErr:
			t.Fatalf("run_tests packages ./slow/ returned (err %v) before the slow test started: does it run go test in the workspace root?", err)
		case <-deadline:
			t.Fatal("the slow test didn't start within 30s: does run_tests run go test in the workspace root with the packages pattern?")
		case <-time.After(20 * time.Millisecond):
			pid = readPID(pidFile)
		}
	}
	cancel()
	select {
	case <-callErr:
	case <-time.After(5 * time.Second):
		t.Fatal("CallTool didn't return within 5s of cancelling its ctx")
	}
	assertGone(t, pid, 5*time.Second, "the client cancelled the call")
}

// Config.TestTimeout bounds a run: the result is an error saying "timed out", and the test binary is killed.
func TestRunTestsTimeout(t *testing.T) {
	ws := harness.Workspace(t)
	pidFile := writeSlowTest(t, ws)
	cs := harness.Connect(t, server.Config{Root: ws, TestTimeout: 2 * time.Second}, nil)

	type reply struct {
		res *mcp.CallToolResult
		err error
	}
	done := make(chan reply, 1)
	start := time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "run_tests", Arguments: map[string]any{"packages": "./slow/"}})
		done <- reply{res, err}
	}()
	var r reply
	select {
	case r = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("run_tests with TestTimeout 2s hadn't returned after 20s (the slow test sleeps 60s).\n" +
			"Apply cfg.TestTimeout with context.WithTimeout and run go with exec.CommandContext on that ctx.\n" +
			"If you do, something is still blocking: a process that holds your stdout/stderr pipes? (set cmd.WaitDelay)")
	}
	if r.err != nil {
		t.Fatalf("CallTool(run_tests): %v (a timeout should be an IsError result, not a protocol error)", r.err)
	}
	if !r.res.IsError || !strings.Contains(harness.Text(r.res), "timed out") {
		t.Fatalf("after %v: result IsError=%v text %q, want IsError with a message containing \"timed out\"",
			time.Since(start).Round(time.Millisecond), r.res.IsError, harness.Text(r.res))
	}
	if pid := readPID(pidFile); pid > 0 {
		assertGone(t, pid, 5*time.Second, "run_tests timed out")
	} else {
		t.Log("the slow test never started (compiling took longer than the timeout?), so the process check was skipped")
	}
}

// When go can't be found, run_tests is an error result (and server.New still works).
func TestRunTestsWithoutGo(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty directory: no go here
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, nil)
	res := harness.CallTool(t, cs, "run_tests", map[string]any{})
	if !res.IsError {
		t.Errorf("with no go on PATH, run_tests returned a normal result (%s), want IsError", harness.Text(res))
	}
}

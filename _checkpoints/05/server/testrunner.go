package server

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"devmcp/internal/gotest"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultTestTimeout = 2 * time.Minute

type runTestsInput struct {
	Packages string `json:"packages,omitempty" jsonschema:"go package pattern relative to the workspace root, e.g. ./... or ./calc (default ./...)"`
	Run      string `json:"run,omitempty" jsonschema:"only run tests matching this regular expression, as in go test -run"`
}

type testResult struct {
	Package string `json:"package" jsonschema:"import path of the test's package"`
	Name    string `json:"name" jsonschema:"test name, e.g. TestAdd or TestAdd/subtest"`
	Status  string `json:"status" jsonschema:"pass, fail or skip"`
	Output  string `json:"output,omitempty" jsonschema:"what the test printed; only set for failures"`
}

type runTestsOutput struct {
	Passed  bool         `json:"passed" jsonschema:"true if go test succeeded: everything built and no test failed"`
	Summary string       `json:"summary" jsonschema:"counts, e.g. 2 passed, 1 failed, 0 skipped"`
	Command string       `json:"command" jsonschema:"the go test command that ran"`
	Tests   []testResult `json:"tests" jsonschema:"every test that finished, sorted by package and name"`
	Output  string       `json:"output,omitempty" jsonschema:"output not tied to one test: build errors, package failures, stderr"`
}

func addTestTool(s *mcp.Server, ws *workspace, timeout time.Duration) {
	if timeout <= 0 {
		timeout = defaultTestTimeout
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "run_tests",
		Description: "Run `go test` in the workspace and report each test's result. Failing tests are not an error: " +
			"check passed, and read the output of the failed tests. Narrow a slow run with packages and run.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: new(false)},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in runTestsInput) (*mcp.CallToolResult, runTestsOutput, error) {
		pkgs := cmp.Or(in.Packages, "./...")
		if strings.HasPrefix(pkgs, "-") {
			return nil, runTestsOutput{}, fmt.Errorf("packages %q looks like a flag; pass a package pattern such as ./... or ./calc", pkgs)
		}
		// -count=1 disables the test cache: a rerun must really run, or a flaky test looks fixed.
		args := []string{"test", "-json", "-count=1"}
		if in.Run != "" {
			args = append(args, "-run="+in.Run) // one argument, so the value can't become a flag
		}
		args = append(args, pkgs)
		command := "go " + strings.Join(args, " ")

		tctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		_ = req.Session.Log(ctx, &mcp.LoggingMessageParams{
			Level: "info", Logger: "run_tests", Data: fmt.Sprintf("running %s in %s", command, ws.root),
		})
		rep := newTestReport()
		token := req.Params.GetProgressToken()
		stderr, err := gotest.Run(tctx, ws.root, args, func(line []byte) {
			if tr, ok := rep.add(line); ok && token != nil {
				// Best effort: once the client is gone this fails, and that's fine.
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: token,
					Progress:      float64(len(rep.tests)),
					Message:       fmt.Sprintf("%s %s %s", strings.ToUpper(tr.Status), tr.Package, tr.Name),
				})
			}
		})

		switch {
		case ctx.Err() != nil: // the client cancelled; nobody will read the result
			return nil, runTestsOutput{}, ctx.Err()
		case tctx.Err() != nil:
			return nil, runTestsOutput{}, fmt.Errorf("go test timed out after %v (%d tests had finished); narrow packages or run",
				timeout, len(rep.tests))
		}
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			return nil, runTestsOutput{}, fmt.Errorf("cannot run go: %w", err)
		}
		rep.extra.Write(stderr)
		return nil, rep.result(err == nil, command), nil
	})
}

// testEvent is one line of `go test -json` output (see `go doc test2json`).
type testEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// testReport turns test2json events into per-test results.
type testReport struct {
	tests   []testResult
	testOut map[[2]string]*strings.Builder // [package, test] → output so far
	pkgOut  map[string]*strings.Builder    // package → output not inside a test
	extra   strings.Builder                // build errors, failed packages' output, non-JSON lines
}

func newTestReport() *testReport {
	return &testReport{testOut: map[[2]string]*strings.Builder{}, pkgOut: map[string]*strings.Builder{}}
}

// add records one output line. It returns the finished test when the line ends a test.
func (r *testReport) add(line []byte) (testResult, bool) {
	if len(bytes.TrimSpace(line)) == 0 {
		return testResult{}, false
	}
	var ev testEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Action == "" {
		r.extra.Write(line)
		return testResult{}, false
	}
	if ev.Action == "build-output" {
		r.extra.WriteString(ev.Output)
		return testResult{}, false
	}
	if ev.Test == "" { // a package-level event
		switch ev.Action {
		case "output":
			builder(r.pkgOut, ev.Package).WriteString(ev.Output)
		case "fail":
			r.extra.WriteString(builder(r.pkgOut, ev.Package).String())
		}
		return testResult{}, false
	}
	key := [2]string{ev.Package, ev.Test}
	switch ev.Action {
	case "output":
		builder(r.testOut, key).WriteString(ev.Output)
	case "pass", "fail", "skip":
		tr := testResult{Package: ev.Package, Name: ev.Test, Status: ev.Action}
		if ev.Action == "fail" {
			tr.Output = builder(r.testOut, key).String()
		}
		delete(r.testOut, key)
		r.tests = append(r.tests, tr)
		return tr, true
	}
	return testResult{}, false
}

func builder[K comparable](m map[K]*strings.Builder, k K) *strings.Builder {
	b, ok := m[k]
	if !ok {
		b = &strings.Builder{}
		m[k] = b
	}
	return b
}

func (r *testReport) result(passed bool, command string) runTestsOutput {
	counts := map[string]int{}
	for _, t := range r.tests {
		counts[t.Status]++
	}
	tests := slices.Clone(r.tests)
	slices.SortFunc(tests, func(a, b testResult) int {
		return cmp.Or(cmp.Compare(a.Package, b.Package), cmp.Compare(a.Name, b.Name))
	})
	if tests == nil {
		tests = []testResult{} // JSON [] rather than null
	}
	return runTestsOutput{
		Passed:  passed,
		Summary: fmt.Sprintf("%d passed, %d failed, %d skipped", counts["pass"], counts["fail"], counts["skip"]),
		Command: command,
		Tests:   tests,
		Output:  r.extra.String(),
	}
}

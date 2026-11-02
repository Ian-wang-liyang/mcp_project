// Milestone 2 acceptance tests: list_files and search_code, with schemas, tool errors and path safety.
// Brief: milestones/02_search.md
package m02

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"devmcp/acceptance/internal/harness"
	"devmcp/server"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listFilesOut struct {
	Files []string `json:"files"`
}

type match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func (m match) String() string { return fmt.Sprintf("%s:%d: %q", m.Path, m.Line, m.Text) }

type searchOut struct {
	Matches   []match `json:"matches"`
	Truncated bool    `json:"truncated"`
}

// Every file in sampleproj, in sorted order (dot-named entries such as .git are never listed).
var allFiles = []string{
	"README.md",
	"assets/logo.png",
	"broken/broken.go",
	"broken/broken_test.go",
	"calc/calc.go",
	"calc/calc_test.go",
	"go.mod",
	"internal/strutil/reverse.go",
}

// Both tools are registered, documented, read-only, and have an output schema.
func TestSearchToolsRegistered(t *testing.T) {
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, nil)
	for _, name := range []string{"list_files", "search_code"} {
		tool := harness.FindTool(t, cs, name)
		if tool.Description == "" {
			t.Errorf("%s has no Description; the model reads it to decide when to call the tool", name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s should be annotated ReadOnlyHint: true (it only reads files)", name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s has no OutputSchema: return a typed struct as the Out value of mcp.AddTool", name)
		}
	}
}

// The input schemas, inferred from your argument structs, have the right properties, descriptions and required list.
func TestSearchInputSchemas(t *testing.T) {
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, nil)
	cases := []struct {
		tool     string
		props    []string
		required []string
	}{
		{"list_files", []string{"dir", "pattern"}, nil},
		{"search_code", []string{"glob", "max_results", "query"}, []string{"query"}},
	}
	for _, c := range cases {
		schema := inputSchema(t, harness.FindTool(t, cs, c.tool))
		var props []string
		for name, p := range schema.Properties {
			props = append(props, name)
			if p.Description == "" {
				t.Errorf("%s: property %q has no description: add a `jsonschema:\"...\"` tag to its field", c.tool, name)
			}
		}
		slices.Sort(props)
		if !slices.Equal(props, c.props) {
			t.Errorf("%s: input properties = %q, want %q (check the json tags on your argument struct)", c.tool, props, c.props)
		}
		got := slices.Sorted(slices.Values(schema.Required))
		if !slices.Equal(got, c.required) {
			t.Errorf("%s: required = %q, want %q\n(a field is required unless its json tag has omitempty or omitzero)", c.tool, got, c.required)
		}
	}
}

// With no arguments, list_files returns every file in the workspace: sorted, relative, slash-separated, no hidden entries.
func TestListFilesAll(t *testing.T) {
	ws := harness.Workspace(t)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	got := listFiles(t, cs, map[string]any{})
	switch {
	case len(got) == 0:
		t.Fatalf("list_files returned no files, want %q\n(if you skip names starting with \".\", don't skip the directory the walk starts from: fs.WalkDir(os.DirFS(dir), \".\") names it \".\")", allFiles)
	case slices.ContainsFunc(got, filepath.IsAbs):
		t.Fatalf("list_files returned absolute paths %q; paths must be relative to the workspace root (filepath.Rel, then filepath.ToSlash)", got)
	case !slices.Equal(got, allFiles):
		t.Errorf("list_files = %q\n          want %q\n(files only, no directories; skip dot-named entries like .git; sort the result)", got, allFiles)
	}
}

// dir narrows the listing but paths stay relative to the root; pattern filters by base name.
func TestListFilesDirAndPattern(t *testing.T) {
	ws := harness.Workspace(t)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	cases := []struct {
		args map[string]any
		want []string
		hint string
	}{
		{map[string]any{"dir": "calc"}, []string{"calc/calc.go", "calc/calc_test.go"},
			"paths are relative to the workspace root, not to dir"},
		{map[string]any{"dir": "./calc/"}, []string{"calc/calc.go", "calc/calc_test.go"},
			"\"./calc/\" means the same directory as \"calc\""},
		{map[string]any{"pattern": "*_test.go"}, []string{"broken/broken_test.go", "calc/calc_test.go"},
			"match the pattern against the base name with path.Match"},
		{map[string]any{"dir": "internal", "pattern": "*.go"}, []string{"internal/strutil/reverse.go"},
			"dir and pattern combine, and the walk is recursive"},
		{map[string]any{"pattern": "*.rs"}, nil,
			"no file matches, so files should be empty"},
	}
	for _, c := range cases {
		got := listFiles(t, cs, c.args)
		if !slices.Equal(got, c.want) {
			t.Errorf("list_files(%v) = %q, want %q\n(%s)", c.args, got, c.want, c.hint)
		}
	}
}

// Sorting means sorting the full paths: WalkDir visits a/ before a-b/, but "a-b/x.go" < "a/x.go".
func TestListFilesSortedByFullPath(t *testing.T) {
	ws := harness.Workspace(t)
	writeFile(t, ws, "a/x.go", "package a\n")
	writeFile(t, ws, "a-b/x.go", "package ab\n")
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	got := listFiles(t, cs, map[string]any{"pattern": "x.go"})
	want := []string{"a-b/x.go", "a/x.go"}
	if !slices.Equal(got, want) {
		t.Errorf("list_files(pattern x.go) = %q, want %q\n(walk order is not sorted order: slices.Sort the paths at the end)", got, want)
	}
}

// Dot-named files and directories are skipped, except the directory you explicitly asked to list.
func TestListFilesSkipsHidden(t *testing.T) {
	ws := harness.Workspace(t)
	writeFile(t, ws, ".secret/notes.txt", "hidden\n")
	writeFile(t, ws, "calc/.cache/build.go", "package cache\n")
	writeFile(t, ws, ".env", "TOKEN=x\n")
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	if got := listFiles(t, cs, map[string]any{}); !slices.Equal(got, allFiles) {
		t.Errorf("list_files = %q\n          want %q\n(skip every entry whose name starts with \".\": return filepath.SkipDir for directories)", got, allFiles)
	}
	if got, want := listFiles(t, cs, map[string]any{"dir": ".secret"}), []string{".secret/notes.txt"}; !slices.Equal(got, want) {
		t.Errorf("list_files(dir .secret) = %q, want %q\n(the directory the caller asked for is never skipped, even if its name starts with \".\")", got, want)
	}
}

// list_files refuses paths outside the workspace, missing directories, files and bad globs with tool errors.
func TestListFilesRejectsBadInput(t *testing.T) {
	ws := harness.Workspace(t)
	// Something worth stealing right next to the workspace.
	if err := os.WriteFile(filepath.Join(filepath.Dir(ws), "secret.txt"), []byte("s3cret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	for _, dir := range []string{"..", "../", "calc/../..", "../sampleproj/../..", filepath.Dir(ws), "/etc"} {
		res := expectToolError(t, cs, "list_files", map[string]any{"dir": dir}, "outside the workspace",
			"validate dir with your workspace's resolve helper before touching the filesystem")
		if res != nil && strings.Contains(harness.Text(res), "secret.txt") {
			t.Errorf("list_files(dir %q) leaked a file outside the workspace: %s", dir, harness.Text(res))
		}
	}
	expectToolError(t, cs, "list_files", map[string]any{"dir": "no-such-dir"}, "",
		"os.Stat the directory and return an error if it doesn't exist")
	expectToolError(t, cs, "list_files", map[string]any{"dir": "go.mod"}, "",
		"dir names a file, not a directory: return an error")
	expectToolError(t, cs, "list_files", map[string]any{"pattern": "[a-"}, "",
		"check the glob up front: path.Match(pattern, \"\") returns path.ErrBadPattern")
}

// list_files refuses a dir that leads out of the workspace through a symlink, even though the path itself looks local.
func TestListFilesSymlinkOutside(t *testing.T) {
	ws := harness.Workspace(t)
	outside := filepath.Join(filepath.Dir(ws), "outside")
	writeFile(t, outside, "sub/secret.txt", "s3cret\n")
	if err := os.Symlink(outside, filepath.Join(ws, "link")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	for _, dir := range []string{"link", "link/sub"} {
		res := expectToolError(t, cs, "list_files", map[string]any{"dir": dir}, "outside the workspace",
			"link is a symlink to a directory outside the root, which a lexical path check can't see; "+
				"filepath.EvalSymlinks the dir and check the result is still under the root")
		if res != nil && strings.Contains(harness.Text(res), "secret.txt") {
			t.Errorf("list_files(dir %q) leaked a file outside the workspace: %s", dir, harness.Text(res))
		}
	}
}

// search_code returns path, 1-based line and the line's text for every matching line.
func TestSearchCode(t *testing.T) {
	ws := harness.Workspace(t)
	writeFile(t, ws, "win/crlf.txt", "one\r\ntwo zebra\r\nthree\r\n")
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	got := searchCode(t, cs, map[string]any{"query": `^func (Add|Div)\(`})
	want := searchOut{Matches: []match{
		{"calc/calc.go", 10, "func Add(a, b int) int { return a + b }"},
		{"calc/calc.go", 13, "func Div(a, b int) (int, error) {"},
	}}
	checkSearch(t, "search_code(query `^func (Add|Div)\\(`)", got, want,
		"lines are numbered from 1; text is the whole line without its newline")

	got = searchCode(t, cs, map[string]any{"query": "zebra"})
	want = searchOut{Matches: []match{{"win/crlf.txt", 2, "two zebra"}}}
	checkSearch(t, "search_code(query zebra) on a CRLF file", got, want,
		"strip the trailing \\r from Windows line endings")
}

// Matches are ordered by path, then line, across directories.
func TestSearchCodeOrdering(t *testing.T) {
	ws := harness.Workspace(t)
	writeFile(t, ws, "a/x.go", "package a\n")
	writeFile(t, ws, "a-b/x.go", "// x\npackage ab\n")
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	got := searchCode(t, cs, map[string]any{"query": "^package "})
	want := searchOut{Matches: []match{
		{"a-b/x.go", 2, "package ab"},
		{"a/x.go", 1, "package a"},
		{"broken/broken.go", 2, "package broken"},
		{"broken/broken_test.go", 1, "package broken"},
		{"calc/calc.go", 2, "package calc"},
		{"calc/calc_test.go", 1, "package calc"},
		{"internal/strutil/reverse.go", 2, "package strutil"},
	}}
	checkSearch(t, "search_code(query ^package)", got, want,
		"search files in sorted path order (the same order list_files uses), and lines top to bottom")
}

// glob limits which files are searched, by base name.
func TestSearchCodeGlob(t *testing.T) {
	ws := harness.Workspace(t)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	all := searchCode(t, cs, map[string]any{"query": "Double"})
	if len(all.Matches) != 8 {
		t.Errorf("search_code(query Double) returned %d matches, want 8 (2 in broken/broken.go, 6 in broken/broken_test.go): %v",
			len(all.Matches), all.Matches)
	}
	tests := searchCode(t, cs, map[string]any{"query": "Double", "glob": "*_test.go"})
	if len(tests.Matches) != 6 || slices.ContainsFunc(tests.Matches, func(m match) bool { return m.Path != "broken/broken_test.go" }) {
		t.Errorf("search_code(query Double, glob *_test.go) = %v\nwant the 6 matches in broken/broken_test.go only (match glob against the base name)", tests.Matches)
	}
	md := searchCode(t, cs, map[string]any{"query": "calc", "glob": "*.md"})
	want := searchOut{Matches: []match{{"README.md", 3, "A tiny Go project for devmcp's acceptance tests. `calc` works, `broken` has a failing test on purpose."}}}
	checkSearch(t, "search_code(query calc, glob *.md)", md, want, "glob *.md should search README.md only")
}

// max_results caps the matches, and truncated says whether any match was left out.
func TestSearchCodeTruncated(t *testing.T) {
	ws := harness.Workspace(t)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	full := searchCode(t, cs, map[string]any{"query": "Double"})
	if full.Truncated || len(full.Matches) != 8 {
		t.Fatalf("search_code(query Double) = %d matches, truncated %v; want 8, false", len(full.Matches), full.Truncated)
	}
	three := searchCode(t, cs, map[string]any{"query": "Double", "max_results": 3})
	if !three.Truncated || !slices.Equal(three.Matches, full.Matches[:3]) {
		t.Errorf("search_code(query Double, max_results 3) = %v, truncated %v\nwant the first 3 matches %v and truncated true",
			three.Matches, three.Truncated, full.Matches[:3])
	}
	exact := searchCode(t, cs, map[string]any{"query": "Double", "max_results": 8})
	if exact.Truncated || len(exact.Matches) != 8 {
		t.Errorf("search_code(query Double, max_results 8) = %d matches, truncated %v; want 8, false\n(there are exactly 8 matches, so nothing was left out: set truncated only when you find match number max_results+1)",
			len(exact.Matches), exact.Truncated)
	}
}

// Without max_results, search_code returns at most 50 matches.
func TestSearchCodeDefaultLimit(t *testing.T) {
	ws := harness.Workspace(t)
	var b strings.Builder
	for i := range 60 {
		fmt.Fprintf(&b, "needle %d\n", i+1)
	}
	writeFile(t, ws, "many.txt", b.String())
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	got := searchCode(t, cs, map[string]any{"query": "needle"})
	if len(got.Matches) != 50 || !got.Truncated {
		t.Errorf("search_code(query needle) over 60 matching lines = %d matches, truncated %v; want 50, true\n(when max_results is omitted it arrives as 0: use 50)",
			len(got.Matches), got.Truncated)
	}
}

// search_code skips dot-named files and directories and binary files (a NUL byte, or not valid UTF-8).
func TestSearchCodeSkipsHiddenAndBinary(t *testing.T) {
	ws := harness.Workspace(t)
	writeFile(t, ws, "notes/ok.txt", "zebra-token\n")
	writeFile(t, ws, ".secret/notes.txt", "zebra-token\n")
	writeFile(t, ws, "calc/.cache/build.go", "// zebra-token\n")
	writeFile(t, ws, ".env", "zebra-token\n")
	writeFile(t, ws, "bin/data.bin", "zebra-token\x00\x01\x02\n")
	writeFile(t, ws, "bin/latin1.txt", "caf\xe9 zebra-token\n")
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	got := searchCode(t, cs, map[string]any{"query": "zebra-token"})
	want := searchOut{Matches: []match{{"notes/ok.txt", 1, "zebra-token"}}}
	checkSearch(t, "search_code(query zebra-token)", got, want,
		"skip dot-named entries; skip files containing a NUL byte (bytes.IndexByte) or invalid UTF-8 (utf8.Valid)")

	if png := searchCode(t, cs, map[string]any{"query": "PNG|IHDR"}); len(png.Matches) != 0 {
		t.Errorf("search_code(query PNG|IHDR) = %v, want no matches: assets/logo.png is binary and must be skipped", png.Matches)
	}
}

// Bad input to search_code comes back as a tool error the model can read, not a protocol error.
func TestSearchCodeRejectsBadInput(t *testing.T) {
	ws := harness.Workspace(t)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	expectToolError(t, cs, "search_code", map[string]any{"query": "func ("}, "missing closing )",
		"return the regexp.Compile error (wrapped is fine) so the model can see what's wrong with its pattern")
	expectToolError(t, cs, "search_code", map[string]any{"query": ""}, "",
		"an empty query would match every line: reject it (\"required\" in the schema only means present)")
	expectToolError(t, cs, "search_code", map[string]any{"glob": "*.go"}, "",
		"query is missing: make it a required field (no omitempty) and the SDK rejects the call for you")
	expectToolError(t, cs, "search_code", map[string]any{"query": "x", "glob": "[a-"}, "",
		"check the glob up front: path.Match(glob, \"\") returns path.ErrBadPattern")
}

// ---- helpers ----

type schemaShape struct {
	Properties map[string]struct {
		Description string `json:"description"`
	} `json:"properties"`
	Required []string `json:"required"`
}

// inputSchema decodes a tool's input schema (generic JSON on the client side) into schemaShape.
func inputSchema(t *testing.T, tool *mcp.Tool) schemaShape {
	t.Helper()
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("%s: marshaling input schema: %v", tool.Name, err)
	}
	var s schemaShape
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("%s: input schema %s is not a JSON object schema: %v", tool.Name, data, err)
	}
	return s
}

// listFiles calls list_files and returns its files, failing the test on any kind of error.
func listFiles(t *testing.T, cs *mcp.ClientSession, args map[string]any) []string {
	t.Helper()
	res := harness.CallTool(t, cs, "list_files", args)
	if res.IsError {
		t.Fatalf("list_files(%v) returned a tool error: %s", args, harness.Text(res))
	}
	return harness.Structured[listFilesOut](t, res).Files
}

// searchCode calls search_code and decodes its output, failing the test on any kind of error.
func searchCode(t *testing.T, cs *mcp.ClientSession, args map[string]any) searchOut {
	t.Helper()
	res := harness.CallTool(t, cs, "search_code", args)
	if res.IsError {
		t.Fatalf("search_code(%v) returned a tool error: %s", args, harness.Text(res))
	}
	return harness.Structured[searchOut](t, res)
}

// checkSearch compares a search_code result with want; an empty matches list and null are the same.
func checkSearch(t *testing.T, call string, got, want searchOut, hint string) {
	t.Helper()
	if got.Truncated != want.Truncated || !slices.Equal(got.Matches, want.Matches) {
		t.Errorf("%s:\n got %v (truncated %v)\nwant %v (truncated %v)\n(%s)",
			call, got.Matches, got.Truncated, want.Matches, want.Truncated, hint)
	}
}

// expectToolError calls a tool that should refuse args, and checks that it refused with an IsError
// result (not a protocol error) whose text contains want. It returns the result, or nil on failure.
func expectToolError(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, want, hint string) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	switch {
	case err != nil:
		t.Errorf("%s(%v) failed with a protocol error: %v\nwant a tool error (IsError result): return a plain Go error from the handler, not a *jsonrpc.Error", name, args, err)
		return nil
	case !res.IsError:
		t.Errorf("%s(%v) succeeded with %s\nwant a tool error (%s)", name, args, harness.Text(res), hint)
		return nil
	case !strings.Contains(harness.Text(res), want):
		t.Errorf("%s(%v) error text = %q, want it to mention %q (%s)", name, args, harness.Text(res), want, hint)
	}
	return res
}

// writeFile creates a file (and its parent directories) inside the test's workspace copy.
func writeFile(t *testing.T, ws, rel, content string) {
	t.Helper()
	p := filepath.Join(ws, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

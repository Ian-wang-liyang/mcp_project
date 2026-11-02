// Milestone 3 acceptance tests: workspace files as resources, and a review_file prompt.
// Brief: milestones/03_files_prompts.md
package m03

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"devmcp/acceptance/internal/harness"
	"devmcp/server"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const secretText = "TOP SECRET: this file is outside the workspace"

// The workspace-file template is advertised, so a client can discover the workspace:/// URI family.
func TestResourceTemplateListed(t *testing.T) {
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, nil)
	var got []string
	for tmpl, err := range cs.ResourceTemplates(context.Background(), nil) {
		if err != nil {
			t.Fatalf("listing resource templates: %v (did you register any with s.AddResourceTemplate?)", err)
		}
		if tmpl.URITemplate == "workspace:///{+path}" {
			if tmpl.Name != "workspace-file" {
				t.Errorf("template workspace:///{+path} has Name %q, want \"workspace-file\"", tmpl.Name)
			}
			return
		}
		got = append(got, tmpl.URITemplate)
	}
	t.Fatalf("no resource template \"workspace:///{+path}\" (the server lists %q); register it with s.AddResourceTemplate", got)
}

// Text files come back as Text, byte for byte, with a MIME type chosen from the file name.
func TestReadTextFiles(t *testing.T) {
	ws := harness.Workspace(t)
	writeFile(t, filepath.Join(ws, "docs", "notes.txt"), "plain notes\n")
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	for _, tc := range []struct {
		rel, mimePrefix string
	}{
		{"internal/strutil/reverse.go", "text/x-go"}, // nested: needs {+path}, not {path}
		{"README.md", "text/markdown"},
		{"go.mod", "text/plain"},
		{"docs/notes.txt", "text/plain"}, // mime.TypeByExtension may add "; charset=utf-8"
	} {
		uri := "workspace:///" + tc.rel
		c, err := readOne(t, cs, uri)
		if err != nil {
			hint := ""
			if strings.Contains(tc.rel, "/") {
				hint = " (for nested paths the template must be workspace:///{+path}; a plain {path} doesn't match slashes)"
			}
			t.Errorf("ReadResource(%s): %v%s", uri, err, hint)
			continue
		}
		want := readDisk(t, ws, tc.rel)
		if c.Text != string(want) {
			t.Errorf("%s: Text = %q, want the file's contents %q", uri, clip(c.Text), clip(string(want)))
		}
		if len(c.Blob) != 0 {
			t.Errorf("%s: Blob has %d bytes; a UTF-8 text file belongs in Text only", uri, len(c.Blob))
		}
		if c.URI != uri {
			t.Errorf("%s: contents URI = %q, want the URI that was read", uri, c.URI)
		}
		if !strings.HasPrefix(c.MIMEType, tc.mimePrefix) {
			t.Errorf("%s: MIMEType = %q, want %q (see the MIME table in the brief)", uri, c.MIMEType, tc.mimePrefix)
		}
	}
}

// A binary file comes back as Blob with its exact bytes, never as (corrupted) Text.
func TestReadBinaryFile(t *testing.T) {
	ws := harness.Workspace(t)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	const uri = "workspace:///assets/logo.png"
	c, err := readOne(t, cs, uri)
	if err != nil {
		t.Fatalf("ReadResource(%s): %v", uri, err)
	}
	want := readDisk(t, ws, "assets/logo.png")
	if c.Text != "" {
		t.Errorf("%s came back as Text (%d bytes). Bytes that aren't UTF-8 get mangled in JSON text: "+
			"check utf8.Valid and put binary data in Blob", uri, len(c.Text))
	}
	if !bytes.Equal(c.Blob, want) {
		t.Errorf("%s: Blob is %d bytes, want the file's exact %d bytes", uri, len(c.Blob), len(want))
	}
	if c.MIMEType != "image/png" {
		t.Errorf("%s: MIMEType = %q, want \"image/png\" (http.DetectContentType sniffs it from the bytes)", uri, c.MIMEType)
	}
}

// Missing files and directories are "resource not found", not a generic failure.
func TestReadNotFound(t *testing.T) {
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, nil)
	for _, uri := range []string{
		"workspace:///no/such/file.go",
		"workspace:///calc", // a directory
		"workspace:///",     // the root directory itself
	} {
		wantNotFound(t, cs, uri, "", "")
	}
}

// No URI may read a file outside the workspace root, however it's spelled.
func TestReadOutsideWorkspace(t *testing.T) {
	ws := harness.Workspace(t)
	secret := filepath.Join(filepath.Dir(ws), "secret.txt") // a sibling of the workspace copy
	writeFile(t, secret, secretText)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	for _, uri := range []string{
		"workspace:///../secret.txt",
		"workspace:///calc/../../secret.txt",
		"workspace:///..%2Fsecret.txt",             // percent-encoded slash
		"workspace:///" + filepath.ToSlash(secret), // workspace:////abs/path
	} {
		wantNotFound(t, cs, uri, secretText, "check that every path stays under the root, after decoding %XX escapes")
	}
}

// A symlink inside the workspace that points outside it must not leak the target.
func TestReadSymlinkOutside(t *testing.T) {
	ws := harness.Workspace(t)
	secret := filepath.Join(filepath.Dir(ws), "secret.txt")
	writeFile(t, secret, secretText)
	if err := os.Symlink(secret, filepath.Join(ws, "leak.txt")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	wantNotFound(t, cs, "workspace:///leak.txt", secretText,
		"leak.txt is a symlink to a file outside the root, which a lexical path check can't see; open files through os.OpenRoot")
}

// go.mod and README.md show up in resources/list when they exist, and only then.
func TestStaticResources(t *testing.T) {
	ws := harness.Workspace(t)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	listed := listResources(t, cs)
	for _, uri := range []string{"workspace:///go.mod", "workspace:///README.md"} {
		r, ok := listed[uri]
		if !ok {
			t.Errorf("resources/list has no %s (it lists %q); register it with s.AddResource in New", uri, slices.Sorted(maps.Keys(listed)))
			continue
		}
		if r.Name == "" {
			t.Errorf("resource %s has an empty Name; the protocol requires one", uri)
		}
		if _, err := readOne(t, cs, uri); err != nil {
			t.Errorf("%s is listed but reading it fails: %v", uri, err)
		}
	}

	// Without a README.md, New must not advertise one.
	ws2 := harness.Workspace(t)
	if err := os.Remove(filepath.Join(ws2, "README.md")); err != nil {
		t.Fatal(err)
	}
	listed2 := listResources(t, harness.Connect(t, server.Config{Root: ws2}, nil))
	if _, ok := listed2["workspace:///README.md"]; ok {
		t.Error("README.md was deleted before New, but resources/list still offers workspace:///README.md; " +
			"only register the static resources whose files exist")
	}
	if _, ok := listed2["workspace:///go.mod"]; !ok {
		t.Error("without README.md, resources/list should still offer workspace:///go.mod")
	}

	// An external symlink must not advertise an unreadable resource or leak its size.
	outside := filepath.Join(t.TempDir(), "README.md")
	writeFile(t, outside, secretText)
	if err := os.Symlink(outside, filepath.Join(ws2, "README.md")); err != nil {
		t.Fatal(err)
	}
	listed3 := listResources(t, harness.Connect(t, server.Config{Root: ws2}, nil))
	if _, ok := listed3["workspace:///README.md"]; ok {
		t.Error("resources/list advertised README.md outside the workspace through a symlink")
	}
}

// review_file is listed with a description and its two arguments, path (required) and focus (optional).
func TestReviewFilePromptListed(t *testing.T) {
	cs := harness.Connect(t, server.Config{Root: harness.Workspace(t)}, nil)
	p := findPrompt(t, cs, "review_file")
	if p.Description == "" {
		t.Error("review_file has no Description; clients show it in their prompt menu")
	}
	args := map[string]*mcp.PromptArgument{}
	for _, a := range p.Arguments {
		args[a.Name] = a
	}
	if a := args["path"]; a == nil || !a.Required {
		t.Errorf("review_file needs an argument \"path\" with Required: true; got %v", describeArgs(p.Arguments))
	}
	if a := args["focus"]; a == nil || a.Required {
		t.Errorf("review_file needs an optional argument \"focus\" (Required: false); got %v", describeArgs(p.Arguments))
	}
}

// review_file expands into user messages: an instruction (mentioning the focus) and the file as an embedded resource.
func TestReviewFilePrompt(t *testing.T) {
	ws := harness.Workspace(t)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	const focus = "integer overflow"
	res, err := getPrompt(cs, "review_file", map[string]string{"path": "calc/calc.go", "focus": focus})
	if err != nil {
		t.Fatalf("GetPrompt(review_file, path=calc/calc.go): %v", err)
	}
	text, embedded := splitMessages(t, res)
	if !strings.Contains(text, focus) {
		t.Errorf("the instruction text should mention the focus %q; got %q", focus, clip(text))
	}
	if embedded == nil {
		t.Fatalf("no message carries an *mcp.EmbeddedResource with the file; messages were %s", describeMessages(res))
	}
	if embedded.URI != "workspace:///calc/calc.go" {
		t.Errorf("embedded resource URI = %q, want \"workspace:///calc/calc.go\"", embedded.URI)
	}
	if want := readDisk(t, ws, "calc/calc.go"); embedded.Text != string(want) {
		t.Errorf("embedded resource Text = %q, want the contents of calc/calc.go", clip(embedded.Text))
	}
	if embedded.MIMEType != "text/x-go" {
		t.Errorf("embedded resource MIMEType = %q, want \"text/x-go\" (reuse your resource reader)", embedded.MIMEType)
	}

	// focus is optional.
	res, err = getPrompt(cs, "review_file", map[string]string{"path": "internal/strutil/reverse.go"})
	if err != nil {
		t.Fatalf("GetPrompt(review_file) without focus: %v (focus is optional)", err)
	}
	if text, embedded := splitMessages(t, res); text == "" || embedded == nil {
		t.Errorf("without focus, review_file should still return an instruction and the file; messages were %s", describeMessages(res))
	}
}

// review_file refuses a missing path argument (with invalid-params) and files it can't read.
func TestReviewFilePromptErrors(t *testing.T) {
	ws := harness.Workspace(t)
	writeFile(t, filepath.Join(filepath.Dir(ws), "secret.txt"), secretText)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)

	_, err := getPrompt(cs, "review_file", map[string]string{"focus": "style"})
	var rpcErr *jsonrpc.Error
	switch {
	case err == nil:
		t.Error("GetPrompt(review_file) without path succeeded; the SDK doesn't enforce Required, so check it in your handler")
	case !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams || !strings.Contains(rpcErr.Message, "path"):
		t.Errorf("GetPrompt(review_file) without path: got %v; want a *jsonrpc.Error with Code jsonrpc.CodeInvalidParams (%d) "+
			"whose Message names the missing argument \"path\"", err, jsonrpc.CodeInvalidParams)
	}

	for _, p := range []string{"no/such/file.go", "calc", "../secret.txt"} {
		res, err := getPrompt(cs, "review_file", map[string]string{"path": p})
		if err == nil {
			t.Errorf("GetPrompt(review_file, path=%q) succeeded; want an error (missing, a directory, or outside the workspace)", p)
		}
		if res != nil && strings.Contains(describeMessages(res), "TOP SECRET") {
			t.Errorf("GetPrompt(review_file, path=%q) leaked a file outside the workspace", p)
		}
	}
}

// A prompt's embedded URI must identify the same file when read back as a resource.
func TestReviewFilePromptEscapedPath(t *testing.T) {
	ws := harness.Workspace(t)
	name := "notes/100%20ready #?.md"
	const contents = "# A file with URI-reserved characters\n"
	writeFile(t, filepath.Join(ws, name), contents)
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	res, err := getPrompt(cs, "review_file", map[string]string{"path": name})
	if err != nil {
		t.Fatal(err)
	}
	_, embedded := splitMessages(t, res)
	if embedded == nil {
		t.Fatal("review_file returned no embedded resource")
	}
	if want := "workspace:///notes/100%2520ready%20%23%3F.md"; embedded.URI != want {
		t.Errorf("embedded URI = %q, want %q", embedded.URI, want)
	}
	read, err := readOne(t, cs, embedded.URI)
	if err != nil {
		t.Fatalf("reading the embedded resource URI: %v", err)
	}
	if read.Text != contents || embedded.Text != contents {
		t.Error("the prompt and its resource URI must return the same file contents")
	}
}

// BONUS: completion suggests workspace file paths for review_file's path argument.
func TestBonusPathCompletion(t *testing.T) {
	ws := harness.Workspace(t)
	for i := range 150 {
		writeFile(t, filepath.Join(ws, "many", fmt.Sprintf("f%03d.txt", i)), "x\n")
	}
	cs := harness.Connect(t, server.Config{Root: ws}, nil)
	if cs.InitializeResult().Capabilities.Completions == nil {
		t.Fatal("the server doesn't advertise the completions capability; set ServerOptions.CompletionHandler")
	}

	got, err := completePath(cs, "review_file", "path", "calc/")
	if err != nil {
		t.Fatalf("Complete(review_file.path, %q): %v", "calc/", err)
	}
	if want := []string{"calc/calc.go", "calc/calc_test.go"}; !slices.Equal(got.Values, want) {
		t.Errorf("completing %q gave %q, want %q", "calc/", got.Values, want)
	}

	got, err = completePath(cs, "review_file", "path", "")
	if err != nil {
		t.Fatalf("Complete(review_file.path, \"\"): %v", err)
	}
	if !slices.IsSorted(got.Values) {
		t.Errorf("completion values should be sorted; got %q", got.Values)
	}
	if !slices.Contains(got.Values, "go.mod") {
		t.Errorf("completing \"\" should offer go.mod; got %q", got.Values)
	}
	if slices.Contains(got.Values, ".git/HEAD") {
		t.Error("completion offered .git/HEAD; skip hidden entries (names starting with \".\")")
	}
	if len(got.Values) > 100 || !got.HasMore {
		t.Errorf("with 150+ files, completion returned %d values (HasMore=%v); want at most 100 with HasMore: true",
			len(got.Values), got.HasMore)
	}

	got, err = completePath(cs, "review_file", "path", "many/f14")
	if err != nil {
		t.Fatalf("Complete(review_file.path, %q): %v", "many/f14", err)
	}
	if len(got.Values) != 10 || got.HasMore {
		t.Errorf("completing \"many/f14\" gave %d values (HasMore=%v), want the 10 files many/f140.txt..many/f149.txt",
			len(got.Values), got.HasMore)
	}

	got, err = completePath(cs, "review_file", "path", "zzz")
	if err != nil || got.Values == nil || len(got.Values) != 0 {
		t.Errorf("completing \"zzz\" = %#v, %v; want a non-nil empty Values slice and no error", got, err)
	}
	got, err = completePath(cs, "review_file", "focus", "")
	if err != nil || len(got.Values) != 0 {
		t.Errorf("completing review_file's focus = %v, %v; want no values and no error (only path is completed)", got, err)
	}
}

// --- helpers ---

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// readOne reads uri and returns its single content block. A protocol error is returned as err;
// a malformed success (no contents, several) fails the test.
func readOne(t *testing.T, cs *mcp.ClientSession, uri string) (*mcp.ResourceContents, error) {
	t.Helper()
	res, err := cs.ReadResource(ctx(t), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, err
	}
	if len(res.Contents) != 1 {
		t.Fatalf("ReadResource(%s) returned %d content blocks, want exactly 1", uri, len(res.Contents))
	}
	return res.Contents[0], nil
}

// wantNotFound checks that reading uri fails with mcp.CodeResourceNotFound. If leak is non-empty,
// it also checks that the leak text appears nowhere in a successful result, and reports leakHint if it does.
func wantNotFound(t *testing.T, cs *mcp.ClientSession, uri, leak, leakHint string) {
	t.Helper()
	res, err := cs.ReadResource(ctx(t), &mcp.ReadResourceParams{URI: uri})
	if err == nil {
		for _, c := range res.Contents {
			if leak != "" && (strings.Contains(c.Text, leak) || bytes.Contains(c.Blob, []byte(leak))) {
				t.Errorf("ReadResource(%s) LEAKED a file outside the workspace: %s", uri, leakHint)
				return
			}
		}
		t.Errorf("ReadResource(%s) succeeded; want mcp.ResourceNotFoundError", uri)
		return
	}
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != mcp.CodeResourceNotFound {
		t.Errorf("ReadResource(%s) error = %v; want code mcp.CodeResourceNotFound (%d). "+
			"Return mcp.ResourceNotFoundError(uri); a plain error reaches the client with code 0",
			uri, err, mcp.CodeResourceNotFound)
	}
}

func listResources(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Resource {
	t.Helper()
	out := map[string]*mcp.Resource{}
	for r, err := range cs.Resources(ctx(t), nil) {
		if err != nil {
			t.Fatalf("listing resources: %v (did you register any with s.AddResource?)", err)
		}
		out[r.URI] = r
	}
	return out
}

func findPrompt(t *testing.T, cs *mcp.ClientSession, name string) *mcp.Prompt {
	t.Helper()
	var names []string
	for p, err := range cs.Prompts(ctx(t), nil) {
		if err != nil {
			t.Fatalf("listing prompts: %v (did you register any with s.AddPrompt?)", err)
		}
		if p.Name == name {
			return p
		}
		names = append(names, p.Name)
	}
	t.Fatalf("no prompt named %q (the server has: %q)", name, names)
	return nil
}

func getPrompt(cs *mcp.ClientSession, name string, args map[string]string) (*mcp.GetPromptResult, error) {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return cs.GetPrompt(c, &mcp.GetPromptParams{Name: name, Arguments: args})
}

func completePath(cs *mcp.ClientSession, prompt, arg, value string) (mcp.CompletionResultDetails, error) {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := cs.Complete(c, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: prompt},
		Argument: mcp.CompleteParamsArgument{Name: arg, Value: value},
	})
	if err != nil {
		return mcp.CompletionResultDetails{}, err
	}
	return res.Completion, nil
}

// splitMessages checks every message is from the user, and returns the joined text of the
// TextContent messages and the first embedded resource (nil if none).
func splitMessages(t *testing.T, res *mcp.GetPromptResult) (string, *mcp.ResourceContents) {
	t.Helper()
	var text strings.Builder
	var embedded *mcp.ResourceContents
	for i, m := range res.Messages {
		if m.Role != "user" {
			t.Errorf("message %d has Role %q; review_file should only send \"user\" messages", i, m.Role)
		}
		switch c := m.Content.(type) {
		case *mcp.TextContent:
			text.WriteString(c.Text)
		case *mcp.EmbeddedResource:
			if embedded == nil {
				embedded = c.Resource
			}
		}
	}
	if embedded == nil && text.Len() == 0 {
		t.Errorf("review_file returned no text and no embedded resource; messages were %s", describeMessages(res))
	}
	return text.String(), embedded
}

func describeMessages(res *mcp.GetPromptResult) string {
	var parts []string
	for _, m := range res.Messages {
		switch c := m.Content.(type) {
		case *mcp.TextContent:
			parts = append(parts, fmt.Sprintf("%s: text %q", m.Role, clip(c.Text)))
		case *mcp.EmbeddedResource:
			if c.Resource == nil {
				parts = append(parts, fmt.Sprintf("%s: embedded resource <nil>", m.Role))
			} else {
				parts = append(parts, fmt.Sprintf("%s: embedded resource %s %q", m.Role, c.Resource.URI, clip(c.Resource.Text)))
			}
		default:
			parts = append(parts, fmt.Sprintf("%s: %T", m.Role, m.Content))
		}
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

func describeArgs(args []*mcp.PromptArgument) string {
	var parts []string
	for _, a := range args {
		parts = append(parts, fmt.Sprintf("%s(required=%v)", a.Name, a.Required))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func readDisk(t *testing.T, ws, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func clip(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

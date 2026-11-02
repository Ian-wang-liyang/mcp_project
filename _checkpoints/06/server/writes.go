package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type writeFileInput struct {
	Path    string `json:"path" jsonschema:"file to write, as a slash-separated path relative to the workspace root; parent directories are created"`
	Content string `json:"content" jsonschema:"the complete new contents of the file"`
}

type writeFileOutput struct {
	Written bool   `json:"written" jsonschema:"true if the file was written; false if the user declined"`
	Path    string `json:"path" jsonschema:"the file, relative to the workspace root"`
	Bytes   int    `json:"bytes" jsonschema:"size of content in bytes"`
}

// confirmKey labels the one question write_file asks. The answer comes back under the same key.
const confirmKey = "confirm"

// confirmSchema is the form the user fills in: one required yes/no field. RequestedSchema is typed
// any, so a map shaped like JSON Schema works (so does *jsonschema.Schema from jsonschema-go).
var confirmSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"confirm": map[string]any{"type": "boolean", "title": "Write the file", "description": "Yes, write it"},
	},
	"required": []string{"confirm"},
}

// addWriteTool registers write_file, which asks the user before it writes anything.
// allowUnconfirmed lets it write without asking when the client can't ask (no elicitation).
func addWriteTool(s *mcp.Server, ws *workspace, allowUnconfirmed bool) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "write_file",
		Description: "Create or overwrite a file in the workspace with the given content. The user is asked to confirm " +
			"first; if they decline, nothing is written and written is false. Prefer small, targeted files.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: new(true), // it can overwrite a file
			IdempotentHint:  true,      // writing the same content twice leaves the same result
			OpenWorldHint:   new(false),
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in writeFileInput) (*mcp.CallToolResult, writeFileOutput, error) {
		// Validate first: never ask the user about a write that can't happen.
		rel, local, exists, err := checkWritable(ws, in.Path)
		if err != nil {
			return nil, writeFileOutput{}, err
		}
		out := writeFileOutput{Path: rel, Bytes: len(in.Content)}

		if !canElicit(req) {
			if !allowUnconfirmed {
				return nil, writeFileOutput{}, fmt.Errorf("cannot write %s: this client can't ask the user to confirm "+
					"(it doesn't support elicitation), so nothing was written. "+
					"To allow writes without confirmation, restart devmcp with -allow-unconfirmed-writes", rel)
			}
			return write(ws, local, in.Content, out)
		}

		// The handler runs twice per call. Round 1 only asks; nothing irreversible happens here.
		resp, answered := req.Params.InputResponses[confirmKey]
		if !answered {
			verb := "Create"
			if exists {
				verb = "Overwrite"
			}
			return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{confirmKey: &mcp.ElicitParams{
				Message:         fmt.Sprintf("%s %s with %d bytes?", verb, rel, len(in.Content)),
				RequestedSchema: confirmSchema,
			}}}, writeFileOutput{}, nil
		}

		// Round 2: act on the answer. Anything but an explicit yes is a no.
		answer, ok := resp.(*mcp.ElicitResult)
		if !ok {
			return nil, writeFileOutput{}, fmt.Errorf("unexpected answer type %T", resp)
		}
		if confirmed, _ := answer.Content["confirm"].(bool); answer.Action != "accept" || !confirmed {
			// Not an error: the tool did its job, and the model should hear "no" and not retry.
			text := fmt.Sprintf("The user declined (%s): %s was not written.", answer.Action, rel)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
		}
		return write(ws, local, in.Content, out)
	})
}

// canElicit reports whether the client declared the elicitation capability. On 2026-07-28 there is
// no initialize request; the SDK fills InitializeParams from each request's metadata instead.
func canElicit(req *mcp.CallToolRequest) bool {
	p := req.Session.InitializeParams()
	return p != nil && p.Capabilities != nil && p.Capabilities.Elicitation != nil
}

// checkWritable validates a model-supplied path for writing, without changing anything on disk.
// It returns the cleaned slash path, its OS form, and whether a file already exists there.
func checkWritable(ws *workspace, p string) (rel, local string, exists bool, err error) {
	if p == "" {
		return "", "", false, errors.New("path is required")
	}
	// resolve rejects "../x" and absolute paths lexically.
	if _, err := ws.resolve(p); err != nil {
		return "", "", false, err
	}
	rel = path.Clean(p)
	if rel == "." {
		return "", "", false, fmt.Errorf("%q is the workspace root, not a file", p)
	}
	local = filepath.FromSlash(rel)
	// os.Root also refuses symlinks that lead outside the workspace, for the file or any parent.
	root, err := os.OpenRoot(ws.root)
	if err != nil {
		return "", "", false, err
	}
	defer root.Close()
	info, err := root.Stat(local)
	switch {
	case err == nil && !info.Mode().IsRegular():
		return "", "", false, fmt.Errorf("%s exists and is not a regular file", rel)
	case err == nil:
		return rel, local, true, nil
	case errors.Is(err, fs.ErrNotExist):
		return rel, local, false, nil
	default: // escapes the root, a parent is a file, permission denied, ...
		return "", "", false, fmt.Errorf("cannot write %s: %w", rel, err)
	}
}

// write creates local's parent directories and writes content, all through os.Root.
func write(ws *workspace, local, content string, out writeFileOutput) (*mcp.CallToolResult, writeFileOutput, error) {
	root, err := os.OpenRoot(ws.root)
	if err != nil {
		return nil, writeFileOutput{}, err
	}
	defer root.Close()
	if dir := filepath.Dir(local); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return nil, writeFileOutput{}, fmt.Errorf("cannot create %s: %w", filepath.ToSlash(dir), err)
		}
	}
	if err := root.WriteFile(local, []byte(content), 0o644); err != nil {
		return nil, writeFileOutput{}, fmt.Errorf("cannot write %s: %w", out.Path, err)
	}
	out.Written = true
	return nil, out, nil
}

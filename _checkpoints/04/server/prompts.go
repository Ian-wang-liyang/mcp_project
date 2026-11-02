package server

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addPrompts registers the review_file prompt.
func addPrompts(s *mcp.Server, ws *workspace) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "review_file",
		Title:       "Review a file",
		Description: "Ask for a code review of one workspace file, with the file attached.",
		Arguments: []*mcp.PromptArgument{
			{Name: "path", Description: "file to review, relative to the workspace root (e.g. calc/calc.go)", Required: true},
			{Name: "focus", Description: `what to concentrate on, e.g. "error handling" or "tests"`},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		// The SDK does not enforce Required: check it here, with the code the spec recommends.
		p := req.Params.Arguments["path"]
		if p == "" {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: `missing required argument "path"`}
		}
		p = path.Clean(p)
		uri := workspaceURIPrefix + (&url.URL{Path: p}).EscapedPath()
		contents, err := readWorkspaceFile(ws, p, uri)
		if err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("cannot review %q: %v", p, err)}
		}

		text := fmt.Sprintf("Please review %s from this Go workspace (attached below). "+
			"Point out bugs, unclear code and missing tests, and quote the lines you mean.", p)
		if focus := req.Params.Arguments["focus"]; focus != "" {
			text += fmt.Sprintf(" Focus especially on: %s.", focus)
		}
		// A PromptMessage holds ONE Content, so the instruction and the file are two messages.
		return &mcp.GetPromptResult{
			Description: "Code review of " + p,
			Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: text}},
				{Role: "user", Content: &mcp.EmbeddedResource{Resource: contents}},
			},
		}, nil
	})
}

// maxCompletions is the most values one completion/complete answer may carry (MCP spec).
const maxCompletions = 100

// completionHandler suggests workspace file paths for review_file's "path" argument (and for the
// workspace-file template's {path}).
func completionHandler(ws *workspace) func(context.Context, *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
	return func(ctx context.Context, req *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
		// Values must be a non-nil slice: nil marshals to null, which clients reject.
		res := &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{Values: []string{}}}
		ref, arg := req.Params.Ref, req.Params.Argument
		isPath := ref != nil && arg.Name == "path" &&
			(ref.Type == "ref/prompt" && ref.Name == "review_file" ||
				ref.Type == "ref/resource" && ref.URI == workspaceURIPrefix+"{+path}")
		if !isPath {
			return res, nil
		}
		matches, err := filePathsWithPrefix(ctx, ws, arg.Value)
		if err != nil {
			return nil, err
		}
		res.Completion.Total = len(matches)
		if len(matches) > maxCompletions {
			matches, res.Completion.HasMore = matches[:maxCompletions], true
		}
		res.Completion.Values = matches
		return res, nil
	}
}

// filePathsWithPrefix returns the slash-separated paths of the workspace's regular files that start with
// prefix, sorted. Hidden entries (names starting with ".", like .git) are skipped.
func filePathsWithPrefix(ctx context.Context, ws *workspace, prefix string) ([]string, error) {
	out := []string{}
	err := filepath.WalkDir(ws.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it, keep going
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p == ws.root {
			return nil
		}
		rel, err := filepath.Rel(ws.root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// Only descend where a match is possible: "calc/" fits prefix "ca" and prefix "calc/x".
			if !strings.HasPrefix(rel+"/", prefix) && !strings.HasPrefix(prefix, rel+"/") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && strings.HasPrefix(rel, prefix) {
			out = append(out, rel)
		}
		return nil
	})
	slices.Sort(out)
	return out, err
}

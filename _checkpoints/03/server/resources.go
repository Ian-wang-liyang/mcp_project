package server

import (
	"context"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// workspaceURIPrefix starts every workspace resource URI: workspace:///calc/calc.go names calc/calc.go.
const workspaceURIPrefix = "workspace:///"

// staticResourceFiles are listed in resources/list (when they exist) so a client can offer them without
// knowing the template.
var staticResourceFiles = []string{"go.mod", "README.md"}

// addResources registers the workspace-file template and the static resources.
func addResources(s *mcp.Server, ws *workspace) {
	h := workspaceFileHandler(ws)
	// {+path} is a "reserved" expansion: unlike {path}, it matches slashes, so nested files work.
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: workspaceURIPrefix + "{+path}",
		Name:        "workspace-file",
		Title:       "Workspace file",
		Description: "Any file in the workspace, by its slash-separated path relative to the root.",
	}, h)
	for _, name := range staticResourceFiles {
		info, err := os.Stat(filepath.Join(ws.root, name))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		s.AddResource(&mcp.Resource{
			URI:      workspaceURIPrefix + name,
			Name:     name,
			MIMEType: textMIMEType(name),
			Size:     info.Size(),
		}, h)
	}
}

// workspaceFileHandler serves workspace:///<path> for both the template and the static resources.
func workspaceFileHandler(ws *workspace) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		rest, ok := strings.CutPrefix(uri, workspaceURIPrefix)
		if !ok {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		// Decode %XX escapes BEFORE the safety check, so "..%2Fsecret" is checked as "../secret".
		rel, err := url.PathUnescape(rest)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		c, err := readWorkspaceFile(ws, rel, uri)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{c}}, nil
	}
}

// readWorkspaceFile reads the regular file at the slash-separated path rel and returns it as resource
// contents with the given URI: Text for UTF-8, Blob otherwise. Anything that isn't a regular file inside
// the workspace (missing, a directory, outside the root) is mcp.ResourceNotFoundError(uri), so a client
// learns nothing about what exists outside.
func readWorkspaceFile(ws *workspace, rel, uri string) (*mcp.ResourceContents, error) {
	// resolve rejects "../x" and absolute paths lexically.
	if rel == "" {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	if _, err := ws.resolve(rel); err != nil {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	// os.Root additionally refuses symlinks that lead out of the workspace.
	root, err := os.OpenRoot(ws.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	local := filepath.FromSlash(rel)
	info, err := root.Stat(local)
	if err != nil || !info.Mode().IsRegular() {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	data, err := root.ReadFile(local)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		// JSON strings must be UTF-8: binary goes in Blob, which the SDK base64-encodes.
		return &mcp.ResourceContents{URI: uri, MIMEType: http.DetectContentType(data), Blob: data}, nil
	}
	return &mcp.ResourceContents{URI: uri, MIMEType: textMIMEType(rel), Text: string(data)}, nil
}

// textMIMEType picks a MIME type for a text file from its name.
func textMIMEType(name string) string {
	switch path.Base(name) {
	case "go.mod", "go.sum", "go.work":
		return "text/plain" // the OS tables may know ".mod" as a music format
	}
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".go":
		return "text/x-go"
	case ".md":
		return "text/markdown"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "text/plain"
}

package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// searchDefaultMax is how many matches search_code returns when max_results is omitted.
const searchDefaultMax = 50

type listFilesArgs struct {
	Dir     string `json:"dir,omitempty" jsonschema:"directory to list, as a slash-separated path relative to the workspace root; default is the root"`
	Pattern string `json:"pattern,omitempty" jsonschema:"glob matched against each file's base name, e.g. *.go; default matches every file"`
}

type listFilesResult struct {
	Files []string `json:"files" jsonschema:"matching files, as sorted slash-separated paths relative to the workspace root"`
}

type searchCodeArgs struct {
	Query      string `json:"query" jsonschema:"RE2 regular expression to look for, matched against each line"`
	Glob       string `json:"glob,omitempty" jsonschema:"only search files whose base name matches this glob, e.g. *.go"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"maximum number of matches to return; default 50"`
}

type searchMatch struct {
	Path string `json:"path" jsonschema:"file path relative to the workspace root"`
	Line int    `json:"line" jsonschema:"1-based line number"`
	Text string `json:"text" jsonschema:"the matching line, without its line ending"`
}

type searchCodeResult struct {
	Matches   []searchMatch `json:"matches" jsonschema:"matches ordered by path, then line"`
	Truncated bool          `json:"truncated" jsonschema:"true if more matches exist than were returned"`
}

func addSearchTools(s *mcp.Server, ws *workspace) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_files",
		Description: "List files in the workspace (recursively), optionally under a subdirectory and filtered by a base-name glob. Hidden files and directories are skipped.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listFilesArgs) (*mcp.CallToolResult, listFilesResult, error) {
		if err := searchCheckGlob(in.Pattern); err != nil {
			return nil, listFilesResult{}, err
		}
		dir, err := ws.resolve(in.Dir)
		if err != nil {
			return nil, listFilesResult{}, err
		}
		info, err := os.Stat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, listFilesResult{}, fmt.Errorf("directory %q does not exist", in.Dir)
		}
		if err != nil {
			return nil, listFilesResult{}, err
		}
		if !info.IsDir() {
			return nil, listFilesResult{}, fmt.Errorf("%q is not a directory", in.Dir)
		}
		files, err := searchWalk(ctx, ws.root, dir, in.Pattern)
		if err != nil {
			return nil, listFilesResult{}, err
		}
		return nil, listFilesResult{Files: files}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_code",
		Description: "Search the workspace's text files for lines matching an RE2 regular expression (like grep -rn). Hidden directories and binary files are skipped.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in searchCodeArgs) (*mcp.CallToolResult, searchCodeResult, error) {
		if in.Query == "" {
			return nil, searchCodeResult{}, errors.New("query must not be empty")
		}
		re, err := regexp.Compile(in.Query)
		if err != nil {
			return nil, searchCodeResult{}, fmt.Errorf("invalid query: %w", err)
		}
		if err := searchCheckGlob(in.Glob); err != nil {
			return nil, searchCodeResult{}, err
		}
		limit := in.MaxResults
		if limit <= 0 {
			limit = searchDefaultMax
		}
		files, err := searchWalk(ctx, ws.root, ws.root, in.Glob)
		if err != nil {
			return nil, searchCodeResult{}, err
		}
		out := searchCodeResult{Matches: []searchMatch{}}
		for _, rel := range files {
			data, err := os.ReadFile(filepath.Join(ws.root, filepath.FromSlash(rel)))
			if err != nil {
				return nil, searchCodeResult{}, err
			}
			if searchLooksBinary(data) {
				continue
			}
			lines := strings.Split(string(data), "\n")
			if lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1] // the text after the final newline is not a line
			}
			for i, line := range lines {
				line = strings.TrimSuffix(line, "\r")
				if !re.MatchString(line) {
					continue
				}
				if len(out.Matches) == limit {
					out.Truncated = true // there is at least one match we are not returning
					return nil, out, nil
				}
				out.Matches = append(out.Matches, searchMatch{Path: rel, Line: i + 1, Text: line})
			}
		}
		return nil, out, nil
	})
}

// searchCheckGlob reports a malformed glob up front, so it fails even when nothing would be matched.
func searchCheckGlob(glob string) error {
	if glob == "" {
		return nil
	}
	if _, err := path.Match(glob, ""); err != nil {
		return fmt.Errorf("bad glob pattern %q: %w", glob, err)
	}
	return nil
}

// searchWalk returns the regular files under dir (absolute, inside root) whose base name matches glob
// (empty matches all), as sorted slash-separated paths relative to root. It skips every entry whose
// name starts with "." except dir itself, and anything that isn't a regular file (symlinks included,
// so the walk can never be led outside the workspace).
func searchWalk(ctx context.Context, root, dir, glob string) ([]string, error) {
	files := []string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if glob != "" {
			if ok, _ := path.Match(glob, d.Name()); !ok {
				return nil
			}
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	// WalkDir's order is per directory ("a/" before "a-b/"), which is not the same as sorting full paths.
	slices.Sort(files)
	return files, nil
}

// searchLooksBinary treats a file as binary if it has a NUL byte or isn't valid UTF-8.
func searchLooksBinary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
}

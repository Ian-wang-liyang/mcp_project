package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// workspace is the directory tree devmcp is allowed to see.
type workspace struct {
	root string // absolute, symlinks resolved
}

func newWorkspace(root string) (*workspace, error) {
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root %s is not a directory", abs)
	}
	return &workspace{root: abs}, nil
}

// errOutside is returned when a path points outside the workspace.
var errOutside = errors.New("path is outside the workspace")

// resolve turns a slash-separated path relative to the workspace root into an absolute path,
// refusing anything that would escape the root ("../x", "/etc/passwd").
func (w *workspace) resolve(rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: %q is absolute; use a path relative to the workspace root", errOutside, rel)
	}
	local := filepath.FromSlash(rel)
	if !filepath.IsLocal(local) && filepath.Clean(local) != "." {
		return "", fmt.Errorf("%w: %q", errOutside, rel)
	}
	return filepath.Join(w.root, local), nil
}

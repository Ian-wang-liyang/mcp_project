// Package server is the devmcp MCP server.
package server

import (
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config configures a devmcp server.
type Config struct {
	// Root is the workspace directory the server works in.
	Root string
	// TestTimeout bounds one run_tests call. Zero means 2 minutes.
	TestTimeout time.Duration
}

const instructions = `devmcp gives you tools for the Go workspace it was started in.
Call workspace_info first to learn the module path and Go version.`

// New builds the devmcp server for cfg.
func New(cfg Config) (*mcp.Server, error) {
	ws, err := newWorkspace(cfg.Root)
	if err != nil {
		return nil, err
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "devmcp", Version: "v0.1.0"}, &mcp.ServerOptions{
		Instructions:      instructions,
		CompletionHandler: completionHandler(ws),
	})
	addInfoTool(s, ws)
	addSearchTools(s, ws)
	addResources(s, ws)
	addPrompts(s, ws)
	addTestTool(s, ws, cfg.TestTimeout)
	return s, nil
}

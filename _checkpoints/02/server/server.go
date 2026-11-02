// Package server is the devmcp MCP server.
package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config configures a devmcp server.
type Config struct {
	// Root is the workspace directory the server works in.
	Root string
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
		Instructions: instructions,
	})
	addInfoTool(s, ws)
	addSearchTools(s, ws)
	return s, nil
}

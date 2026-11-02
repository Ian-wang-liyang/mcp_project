// Package server is your devmcp MCP server. Everything in this package is yours to design;
// the acceptance tests only call New and talk to the result over the MCP protocol.
package server

import (
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config configures a devmcp server. Later milestones ask you to add fields.
type Config struct {
	// Root is the workspace directory the server works in.
	Root string
}

// New builds the devmcp server for cfg.
func New(cfg Config) (*mcp.Server, error) {
	return nil, errors.New("not implemented yet: start with milestones/01_hello.md")
}

// Package server is the devmcp MCP server.
package server

import (
	"io"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config configures a devmcp server.
type Config struct {
	// Root is the workspace directory the server works in.
	Root string
	// TestTimeout bounds one run_tests call. Zero means 2 minutes.
	TestTimeout time.Duration
	// ReadOnly leaves out every tool that runs code or changes files (run_tests).
	ReadOnly bool
	// AuditLog, when non-nil, receives one JSON line per tools/call. It need not be safe for
	// concurrent use: the server serializes its writes.
	AuditLog io.Writer
}

const instructions = `devmcp gives you tools for the Go workspace it was started in.
Call workspace_info first to learn the module path and Go version.`

const readOnlyInstructions = `
This server runs in read-only mode: it cannot run tests or change files.`

// New builds the devmcp server for cfg.
func New(cfg Config) (*mcp.Server, error) {
	ws, err := newWorkspace(cfg.Root)
	if err != nil {
		return nil, err
	}
	instr := instructions
	if cfg.ReadOnly {
		instr += readOnlyInstructions
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "devmcp", Version: "v0.1.0"}, &mcp.ServerOptions{
		Instructions:      instr,
		CompletionHandler: completionHandler(ws),
	})
	if cfg.AuditLog != nil {
		// Added before any session connects, so it sees every request.
		s.AddReceivingMiddleware(auditMiddleware(cfg.AuditLog))
	}
	addInfoTool(s, ws)
	addSearchTools(s, ws)
	addResources(s, ws)
	addPrompts(s, ws)
	if !cfg.ReadOnly {
		// Tools that execute code or modify files exist only when they are allowed. Not registering
		// them is stronger than refusing their calls: the model never even sees them.
		addTestTool(s, ws, cfg.TestTimeout)
	}
	return s, nil
}

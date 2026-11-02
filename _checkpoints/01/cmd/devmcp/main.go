// Command devmcp serves the devmcp MCP server over stdio.
//
//	devmcp -root /path/to/a/go/repo
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"

	"devmcp/server"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	root := flag.String("root", ".", "workspace directory to serve")
	flag.Parse()

	// stdout carries the MCP protocol, so every log line must go to stderr.
	log.SetOutput(os.Stderr)
	log.SetPrefix("devmcp: ")

	s, err := server.New(server.Config{Root: *root})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

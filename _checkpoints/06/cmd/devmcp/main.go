// Command devmcp serves the devmcp MCP server over stdio (the default) or Streamable HTTP.
//
//	devmcp -root /path/to/a/go/repo                        # stdio, for `claude mcp add devmcp -- devmcp ...`
//	devmcp -root . -http 127.0.0.1:8080                    # HTTP at http://127.0.0.1:8080/mcp
//	devmcp -root . -readonly -audit audit.jsonl            # no run_tests/write_file; log every tool call
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"devmcp/server"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// shutdownGrace is how long in-flight HTTP requests get to finish after SIGINT/SIGTERM.
const shutdownGrace = 2 * time.Second

func main() {
	// In stdio mode stdout carries the MCP protocol, so every log line must go to stderr.
	log.SetOutput(os.Stderr)
	log.SetPrefix("devmcp: ")
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// run does the work of main. Returning an error instead of calling log.Fatal lets the deferred
// calls (closing the audit file, stopping the signal handler) run on every path.
func run() error {
	root := flag.String("root", ".", "workspace directory to serve")
	readOnly := flag.Bool("readonly", false, "leave out tools that run code or change files")
	auditPath := flag.String("audit", "", `append one JSON line per tool call to this file ("-" means stderr)`)
	httpAddr := flag.String("http", "", "serve Streamable HTTP on this address (e.g. 127.0.0.1:8080) instead of stdio")
	allowUnconfirmed := flag.Bool("allow-unconfirmed-writes", false, "let write_file write without asking when the client can't ask the user")
	flag.Parse()

	cfg := server.Config{Root: *root, ReadOnly: *readOnly, AllowUnconfirmedWrites: *allowUnconfirmed}
	if *auditPath != "" {
		w, closeAudit, err := openAudit(*auditPath)
		if err != nil {
			return err
		}
		defer closeAudit()
		cfg.AuditLog = w
	}
	s, err := server.New(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *httpAddr != "" {
		return serveHTTP(ctx, s, *httpAddr)
	}
	if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		return err
	}
	return nil // the client hung up, or a signal asked us to stop: both are normal
}

// openAudit opens the audit log for appending. "-" is stderr, which must not be closed.
func openAudit(path string) (io.Writer, func(), error) {
	if path == "-" {
		return os.Stderr, func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { f.Close() }, nil
}

// serveHTTP serves s over Streamable HTTP at /mcp until ctx is cancelled, then shuts down gracefully.
func serveHTTP(ctx context.Context, s *mcp.Server, addr string) error {
	// Listen first, then report the real address: with port 0 the OS picks a free one.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("listening on http://%s/mcp", ln.Addr())

	// One *mcp.Server shared by every client: the handler gives each client its own session.
	// nil options: stateful sessions, and DNS-rebinding (localhost) protection on.
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	select {
	case err := <-serveErr:
		return err // Serve failed on its own
	case <-ctx.Done():
	}

	log.Print("shutting down")
	// Shutdown waits for in-flight requests. A client's open SSE stream (the GET that carries
	// server-to-client messages) never finishes on its own, so give it a deadline, then Close.
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		srv.Close()
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

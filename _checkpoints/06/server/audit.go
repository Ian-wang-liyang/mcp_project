package server

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// auditEntry is one line of the audit log.
type auditEntry struct {
	Time       string  `json:"time"`        // when the call started, RFC 3339
	Tool       string  `json:"tool"`        // the tool the client asked for (even an unknown one)
	DurationMS float64 `json:"duration_ms"` // how long the server took to answer
	IsError    bool    `json:"is_error"`    // an IsError result or a protocol error
}

// auditMiddleware returns a receiving middleware that writes one JSON line to w for every tools/call.
//
// The SDK runs requests concurrently (and on HTTP, from many clients at once), so this function's
// inner closure runs on many goroutines. w may be a bytes.Buffer or another writer that is not
// safe for concurrent use. The mutex makes each line one uninterrupted Write.
func auditMiddleware(w io.Writer) mcp.Middleware {
	var mu sync.Mutex
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			ctr, ok := req.(*mcp.CallToolRequest)
			if !ok {
				return next(ctx, method, req) // tools/list, resources/read, ...: not audited
			}
			start := time.Now()
			res, err := next(ctx, method, req)
			entry := auditEntry{
				Time:       start.UTC().Format(time.RFC3339),
				Tool:       ctr.Params.Name,
				DurationMS: float64(time.Since(start).Microseconds()) / 1000,
				IsError:    err != nil,
			}
			if r, ok := res.(*mcp.CallToolResult); ok && r != nil && r.IsError {
				entry.IsError = true
			}
			line, jerr := json.Marshal(entry)
			if jerr == nil {
				line = append(line, '\n')
				mu.Lock()
				_, _ = w.Write(line) // auditing must never break the call itself
				mu.Unlock()
			}
			return res, err
		}
	}
}

# Milestone 5: Harden and ship

**Goal:** make devmcp safe to leave running. Every tool call gets audited, a read-only mode removes the tools that run
code, and the same binary can serve Streamable HTTP so several clients can share one long-running server.
**Time:** about 60 minutes. **Check:** `make check M=05`

> **Before you start:** add two fields to `server.Config`, `ReadOnly bool` and `AuditLog io.Writer`. The m05 tests set
> them, so they won't compile until both exist.
>
> **Unix only**, like milestone 4: the binary tests send the process `SIGINT`.

## What you'll learn

- **Receiving middleware.** One function sees every request before your handlers do, so it's the place for auditing,
  metrics and policy. You'll also see what it sees: a tool call that fails with a protocol error never reaches a handler.
- **Concurrency in a real server.** The SDK handles requests concurrently. Over HTTP they come from many clients at once,
  so anything shared, like a log file, needs a mutex.
- **Read-only mode,** and why *not registering* a tool is stronger than hiding or refusing it.
- **The Streamable HTTP transport.** Your tools don't change at all, only `main.go` does. You'll learn about sessions,
  listening on port 0, and which protocol version an HTTP client ends up with.
- **DNS-rebinding protection:** what the SDK does for you by default, and where that protection ends.
- **Graceful shutdown** of an HTTP server that has long-lived streams open.
- **Deployment:** installing the binary and registering an HTTP server with Claude Code.

The lessons for this milestone:
- Middleware: lessons 8.1 to 8.3 in [`lessons/08_mcp_middleware.md`](../lessons/08_mcp_middleware.md). Read its primer first.
  8.1 is a stateful middleware with a mutex. 8.2 rejects a tool and 8.3 hides one. Compare both with read-only mode below.
- HTTP: the primer of [`lessons/09_mcp_capstone.md`](../lessons/09_mcp_capstone.md), especially "Before you deploy over HTTP"
  and the security checklist. Then lesson 9.1.
- Mutexes: lesson 1.1 in [`lessons/01_sync_context.md`](../lessons/01_sync_context.md).

The SDK's own guides are `docs/protocol.md` (sections Streamable Transport, Stateless Mode, Security) and
`examples/server/middleware` and `examples/http`.

### Stdio or HTTP?

With **stdio**, the host starts devmcp as a child process. There's one process per client, and it dies with the host.
Nothing listens on the network. For a local tool that's ideal, and it stays the default.

With **Streamable HTTP**, devmcp is a web service you start yourself. It runs until you stop it. Clients POST JSON-RPC to
one URL (`http://127.0.0.1:8080/mcp`) and read the replies as JSON or as a Server-Sent Events stream. Several clients
can share one server: a Claude Code session, the Inspector, a teammate's editor. Each one gets its own **session**,
tracked with an `Mcp-Session-Id` header. It's also the first step toward a remote server, and that's where the
security questions below start to matter.

### DNS rebinding, in one paragraph

A web page you visit can't read `http://127.0.0.1:8080` directly, because the browser's same-origin policy blocks it.
But the attacker controls DNS for their own domain. They can make `evil.example` resolve to their server first and to
`127.0.0.1` a moment later. The page then talks to "its own" origin, which is now your devmcp, and can call
`run_tests` in your repo. One thing gives this away: the request still says `Host: evil.example`.

The SDK's Streamable HTTP handler checks for exactly that, and it's **on by default**. If a request reached the server
on a loopback address (`127.0.0.1`, `[::1]`) and its `Host` header isn't `localhost` or a loopback IP, the handler
answers `403 Forbidden` before your server sees anything. Know where this protection ends:

- It looks at the address the connection **arrived on**, not the one you bound. If you bind `0.0.0.0` and someone on
  your network connects to your LAN IP, the check doesn't apply, and it couldn't help anyway. Anyone who can reach that
  port can call your tools. So don't bind beyond loopback without authentication (see the end of this brief).
- It's not a defense against cross-origin requests in general. For that, wrap the handler with Go's
  `http.NewCrossOriginProtection().Handler(h)` (Go 1.25+).
- `StreamableHTTPOptions.DisableLocalhostProtection` turns it off. Don't.

## Requirements

The acceptance tests check exactly these.

1. **`Config.ReadOnly`.** When true, `run_tests` is **not registered**. It's absent from `tools/list`, and calling it
   fails. `workspace_info`, `list_files`, `search_code`, the resources and the prompt are unchanged.
   Milestone 6's `write_file` will follow the same rule: anything that runs code or changes files.
2. **`Config.AuditLog`.** When non-nil, a receiving middleware writes **one line per `tools/call` request**. Each line is
   a JSON object followed by `"\n"`, with these fields:

   | field | value |
   |---|---|
   | `time` | when the call started, as an RFC 3339 string (`time.RFC3339`) |
   | `tool` | the tool name the client asked for, even if no such tool exists |
   | `duration_ms` | a number: how long the call took, in milliseconds (fractions are fine) |
   | `is_error` | `true` if the result has `IsError` set **or** the call failed with a protocol error |

   - Other requests (`tools/list`, `resources/read`, ...) are not logged.
   - The writer doesn't have to be safe for concurrent use. A `bytes.Buffer` or an `*os.File` isn't. Your middleware
     must never call `Write` from two goroutines at once. The test drives many concurrent clients through a writer
     that notices overlapping writes.
   - A nil `AuditLog` means no auditing (and no crash).
3. **`cmd/devmcp` flags**, in addition to `-root`:

   | flag | meaning |
   |---|---|
   | `-readonly` | sets `Config.ReadOnly` |
   | `-audit PATH` | appends audit lines to the file `PATH`, creating it if needed. `-audit -` means stderr. Never stdout: in stdio mode that's the protocol. |
   | `-http ADDR` | serves Streamable HTTP on `ADDR` (e.g. `127.0.0.1:8080`) instead of stdio |

4. **HTTP mode** (`-http ADDR`):
   - Serves the MCP endpoint at the path **`/mcp`**, using `mcp.NewStreamableHTTPHandler`.
   - Once the listener is bound, prints a line to **stderr** that contains `listening on http://HOST:PORT/mcp`, using
     the real bound address. `-http 127.0.0.1:0` must report the port the OS picked, not `:0`.
   - Serves several clients at once, each in its own session, from one `*mcp.Server`.
   - Keeps DNS-rebinding protection on: a request with `Host: evil.example` gets `403`.
5. **Without `-http`,** devmcp serves stdio exactly as before. (The m01 stdio test still runs.)
6. **Bonus (`TestBonusGracefulShutdown`):** on `SIGINT` or `SIGTERM` in HTTP mode, shut down and **exit with status
   0** within 5 seconds, even while a client is connected.

## Suggested steps

1. **Read-only.** Add `Config.ReadOnly` and register `run_tests` only when it's false. Run `TestReadOnly`.
2. **The audit middleware.** Add `Config.AuditLog`. Write a function that takes an `io.Writer` and returns an
   `mcp.Middleware`. In `New`, install it with `s.AddReceivingMiddleware` when the writer is non-nil. Get `TestAuditLog`
   and `TestAuditLogDuration` passing. Then run `TestAuditLogConcurrent` without a mutex and watch it fail, then add one.
3. **Flags.** Add `-readonly` and `-audit`. `TestBinaryReadOnlyFlag` runs over stdio, so it passes before HTTP exists.
4. **HTTP.** Add `-http`: `net.Listen`, log the address, then serve the handler on a mux at `/mcp`. Get `TestHTTPServe`,
   `TestHTTPTwoClients`, `TestHTTPRejectsForeignHost` and `TestBinaryAuditFlag` passing.
5. **Bonus.** Graceful shutdown on a signal.

## Hints

<details><summary>The middleware's shape</summary>

```go
func auditMiddleware(w io.Writer) mcp.Middleware {
	var mu sync.Mutex // ONE mutex for every request: create it here, not inside the closures below
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			ctr, ok := req.(*mcp.CallToolRequest) // comma-ok: most requests aren't tool calls
			if !ok {
				return next(ctx, method, req)
			}
			start := time.Now()
			res, err := next(ctx, method, req)
			// ... build the entry from ctr.Params.Name, start, err and res ...
			return res, err // always pass the result on unchanged
		}
	}
}
```

- On the server side, `ctr.Params.Arguments` is raw JSON (`json.RawMessage`). You don't need the arguments here, and
  in a real audit log you'd think twice before logging them, since they may contain secrets.
- `res` is an `mcp.Result` interface. For `tools/call` it holds a `*mcp.CallToolResult` when `err` is nil.
- `json.Marshal` the entry, append `'\n'`, and write it with **one** `Write` call while holding the mutex.
  `json.NewEncoder(w).Encode(entry)` also writes one line, but it still needs the mutex.
- Milliseconds as a number: `float64(d.Microseconds()) / 1000`. (`d.Milliseconds()` is an int64 and rounds a fast call to 0. That's allowed, just less useful.)
</details>

<details><summary>Why do I need a mutex? I only write one line.</summary>

The SDK runs tool calls concurrently: across sessions always, and within a session too. So two `Write` calls on the
same `bytes.Buffer` can run at once. That's a data race, and it can corrupt the buffer or interleave two lines into
garbage. `*os.File` writes don't corrupt memory, but nothing guarantees that a large write lands in one piece. The rule
for any `io.Writer` you didn't write yourself: assume it isn't safe for concurrent use unless its docs say so.
(`log.Logger` is one that is: it has a mutex inside.)

The race detector alone is a weak test here. The SDK takes its own locks while dispatching requests, and those can
order your writes just enough for `-race` to stay quiet. That's why `TestAuditLogConcurrent` uses a writer that
deliberately takes a few milliseconds per `Write` and records any overlap.
</details>

<details><summary>Read-only: why not just hide or deny run_tests?</summary>

Lessons 8.2 and 8.3 showed both. Each has a hole:
- **Hiding** it from `tools/list` stops the model from *seeing* it, but a client that knows the name can still call it.
- **Denying** it in middleware works, but every new dangerous tool has to be added to a list, and forgetting one fails open.

Not registering it is the simplest thing that's actually safe: the tool doesn't exist in that server. Put the
`if !cfg.ReadOnly` around the registration of *every* tool that executes or modifies, and the rule stays obvious.
It's also worth telling the model in your instructions that the server is read-only, so it doesn't go looking for
`run_tests`.
</details>

<details><summary>Serving HTTP on a port the OS picks</summary>

`http.ListenAndServe(addr, h)` binds and serves in one call, so you never learn the port when `addr` ends in `:0`.
Split it:

```go
ln, err := net.Listen("tcp", addr)
if err != nil { ... }
log.Printf("listening on http://%s/mcp", ln.Addr()) // ln.Addr() is the real address, e.g. 127.0.0.1:54123

h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
mux := http.NewServeMux()
mux.Handle("/mcp", h)
srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
err = srv.Serve(ln) // blocks; returns http.ErrServerClosed after Shutdown/Close
```

- `getServer` runs on **every** HTTP request. Return the one server you built. Calling `server.New` inside it would
  build a throwaway server per request.
- `nil` options mean stateful sessions, SSE responses, and localhost protection on.
- With `-http :8080` (no host), Go listens on **every** interface and `ln.Addr()` prints `[::]:8080`. Use
  `127.0.0.1:8080` unless you really mean "anyone on my network".
</details>

<details><summary>Which protocol version does an HTTP client get?</summary>

Log `cs.InitializeResult().ProtocolVersion` in a test and you'll see **2025-11-25**, not the SDK's newest, 2026-07-28.
In SDK v1.8.0 the Streamable HTTP handler accepts 2026-07-28 only when `StreamableHTTPOptions{Stateless: true}`. A
stateful handler answers a 2026-07-28 request with an "unsupported protocol version" error, and the Go client falls
back to the older handshake on its own.

Stateless mode has real advantages: no sessions to keep, and any replica behind a load balancer can answer. The cost
is that a stateless server can't send requests to a client. That's harmless for the newest clients, who get questions
back as `InputRequests`, but it breaks elicitation for 2025-11-25 clients. Milestone 6 needs elicitation, so the
checkpoint stays stateful. The tests pass either way.
</details>

<details><summary>TestHTTPRejectsForeignHost gets 200</summary>

You turned the protection off, or you're not using the SDK's handler. `DisableLocalhostProtection` must stay false. If
you wrapped the handler in your own middleware, make sure it doesn't rewrite `r.Host`.

Trying this by hand: `curl -H 'Host: evil.example' ...` works. Go's `http.Client` ignores `req.Header.Set("Host", ...)`,
so set `req.Host` instead. That's what the test does.
</details>

<details><summary>Bonus: shutting down cleanly</summary>

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

serveErr := make(chan error, 1)
go func() { serveErr <- srv.Serve(ln) }()
select {
case err := <-serveErr:
	return err // Serve failed on its own
case <-ctx.Done():
}
sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()
if err := srv.Shutdown(sctx); err != nil {
	srv.Close() // the grace period is over: cut the remaining connections
}
<-serveErr // http.ErrServerClosed: expected, not a failure
```

`Shutdown` stops accepting connections and waits for in-flight requests to finish. That's the graceful part. But an
MCP client keeps a GET request open to receive server-to-client messages, and that request never finishes on its own.
`Shutdown(context.Background())` would wait forever. The deadline plus `Close` is the standard fix.

Exit 0 means returning normally from `main`. `log.Fatal` exits with 1, and it also skips deferred calls such as closing
the audit file.
</details>

## Try it for real

Install the binary somewhere on your `PATH`, then run it as a service:

```sh
go install ./cmd/devmcp                                     # into $(go env GOPATH)/bin; or: make build → bin/devmcp
devmcp -root /path/to/a/go/repo -http 127.0.0.1:8080 -audit /tmp/devmcp-audit.jsonl
```

In another terminal, register it with Claude Code as an HTTP server:

```sh
claude mcp add --transport http devmcp-http http://127.0.0.1:8080/mcp
```

(Scopes and other options vary by version; see `claude mcp add --help`.) Claude Code doesn't start an HTTP server for
you, so devmcp must already be running. Run `/mcp` to check it's connected, then ask Claude to search the code or run
the tests, and watch the calls arrive:

```sh
tail -f /tmp/devmcp-audit.jsonl
```

Open a second client at the same time, with the Inspector: run `npx @modelcontextprotocol/inspector`, choose the
**Streamable HTTP** transport, and enter `http://127.0.0.1:8080/mcp`. Both clients share one server, and the audit log
shows both. Restart devmcp with `-readonly` and reconnect: `run_tests` is gone, and Claude says it can't run the
tests instead of trying.

Then try the protection yourself:

```sh
curl -si -X POST http://127.0.0.1:8080/mcp -H 'Host: evil.example' -H 'Content-Type: application/json' \
     -H 'Accept: application/json, text/event-stream' -d '{}' | head -1          # HTTP/1.1 403 Forbidden
```

Press Ctrl-C in devmcp's terminal while Claude Code is connected. It should exit within a couple of seconds.

## Before you expose it beyond localhost

Everything above assumes `127.0.0.1`. Listening on a reachable address changes the threat model completely. Anyone
who can reach the port can call every tool, and `run_tests` runs **arbitrary code from the repo**. Before you do it:

- **Authentication.** MCP's HTTP authorization is OAuth 2.1 with bearer tokens. In the Go SDK you wrap the handler with
  `auth.RequireBearerToken` and a `TokenVerifier` that accepts only tokens issued *for your server* (never pass a
  client's token through to another API). That's beyond this course: read `docs/protocol.md#authorization` and
  `examples/auth` in the SDK repo.
- **TLS.** Put devmcp behind a reverse proxy that terminates HTTPS, or use `srv.ServeTLS`.
- **Least privilege.** Run remote instances with `-readonly`. Treat a session ID as an identifier, not a credential:
  check auth on every request.
- **Limits.** Look at `StreamableHTTPOptions.SessionTimeout` (close idle sessions) and `MaxRequestBodyBytes`
  (4 MiB by default).
- **Scaling out.** Several replicas behind a load balancer need `Stateless: true` (see the protocol-version hint), or
  sticky sessions. See `examples/server/distributed`.

## Stretch (optional, untested)

- **Log arguments safely.** Add an `args_bytes` field with the size of the raw arguments, or log the arguments with
  values longer than 100 characters replaced by `"…"`. Decide what must never be logged.
- **Rotate the audit log** on `SIGHUP`: reopen the file, so `logrotate` can move the old one away.
- **Per-client identity.** Over HTTP, `req.GetExtra().Header` carries the request headers. Log the `User-Agent`, or
  the session ID from `req.GetSession().ID()`.
- **Tracing.** Use `slog` with a JSON handler instead of hand-made lines, and add a request counter as a metric
  (`expvar` is in the standard library) served on a second path, e.g. `/debug/vars`.
- **Stateless mode.** Add a `-stateless` flag, connect with a 2026-07-28 client, and compare what still works. Come
  back after milestone 6 to see what happens to `write_file` for an older client.

# Module 09: real transports and a complete notes server

## Primer: leaving the in-memory sandbox

Every module so far used mcp.NewInMemoryTransports. Real MCP traffic travels over one of two
standard TRANSPORTS. Your server code (tools, resources, prompts) doesn't change at all; only the
transport passed to Connect/Run does.

```
stdio            the host starts your server as a SUBPROCESS and speaks newline-delimited JSON over
                 its stdin/stdout. One process = one client. This is what `claude mcp add` uses
                 for local servers.
  server side    server.Run(ctx, &mcp.StdioTransport{})          (os.Stdin + os.Stdout)
                 server.Run(ctx, &mcp.IOTransport{Reader: r, Writer: w})   (any pipes you like)
  client side    &mcp.CommandTransport{Command: exec.Command("my-server")}  (spawns the process)
```

```
Streamable HTTP  your server is a web service: clients POST JSON-RPC messages to ONE endpoint
                 (e.g. https://example.com/mcp) and get replies as JSON or as a Server-Sent Events
                 stream. Many clients at once, possibly remote.
  server side    mcp.NewStreamableHTTPHandler(getServer, opts)   (an http.Handler)
  client side    &mcp.StreamableClientTransport{Endpoint: url}
```

**THE STDOUT RULE:** with stdio, stdout IS the protocol. A stray fmt.Println corrupts the stream and the
host drops the connection. Log to stderr (the log package's default) and never print to stdout.

### Before you deploy over HTTP

ONE ServerSession PER CLIENT. The handler runs the handshake for each new client and

```
gives it an Mcp-Session-Id header; later requests carrying that header reach the same
*mcp.ServerSession. The getServer callback picks the *mcp.Server for a request. Returning the
same server every time is normal: all sessions then share its tools AND its state (your note
store). getServer runs on EVERY request (the handler also uses it to check the protocol version),
not only when a session starts. So `return NewNotesServer()` would build a throwaway server per
request and give every client its own empty store. Create the server once, outside the callback.
The *http.Request argument lets you choose per request, e.g. a different server per tenant.
```

**STATELESS MODE:** &mcp.StreamableHTTPOptions{Stateless: true}. No Mcp-Session-Id; each request gets
a temporary session with default initialization parameters, so any replica behind a load balancer
can answer it. Cost: the server can't send REQUESTS to the client (there's nowhere for the reply
to go), so sampling and elicitation (module 7) are rejected. GET/DELETE answer 405. The newest
protocol revision (2026-07-28) is accepted ONLY by stateless handlers. Example:
examples/server/distributed.

**RESUMABILITY:** by default a dropped SSE stream loses its in-flight messages. Setting
StreamableHTTPOptions.EventStore (mcp.NewMemoryEventStore(nil) for tests; a persistent store in
production) lets clients reconnect with Last-Event-ID and get the missed events replayed
(protocol 2025-11-25 and earlier; 2026-07-28 drops this and clients just retry). Also worth
knowing: SessionTimeout closes idle sessions; MaxRequestBodyBytes defaults to 4 MiB.

SECURITY CHECKLIST (docs/protocol.md#security, and the spec's Security Best Practices page:

```
https://modelcontextprotocol.io/docs/tutorials/security/security_best_practices):
DNS rebinding      a web page can trick a browser into calling http://localhost:PORT. The handler
                   rejects (403) requests that arrive on a loopback address with a non-localhost
                   Host header. This is ON by default; leave DisableLocalhostProtection false.
                   For browser cross-origin checks, wrap the handler:
                   http.NewCrossOriginProtection().Handler(h). Background:
https://modelcontextprotocol.io/specification/2025-11-25/basic/security_best_practices#local-mcp-server-compromise
Token passthrough  never forward the token a client gave you to some other API, and accept only
                   tokens issued FOR your server. With auth, that check lives in your TokenVerifier.
Confused deputy    a proxy server that holds its own credentials for a third-party API must get
                   user consent for each dynamically registered client, or an attacker's client
                   borrows the proxy's authority.
Session hijacking  a session ID is not a credential. Verify auth on EVERY request, keep the SDK's
                   crypto-random session IDs (or use crypto/rand.Text if you set GetSessionID),
                   and set TokenInfo.UserID so the handler rejects (403) a session ID presented
                   by a different user.
AUTHORIZATION (OAuth 2.1 bearer tokens, auth.RequireBearerToken wrapping the handler, client-side
OAuthHandler) is beyond this track: see docs/protocol.md#authorization and examples/auth.
```

**FROM PYTHON/JS:** FastMCP's mcp.run(transport="stdio") / "streamable-http" and the TS SDK's
StdioServerTransport / StreamableHTTPServerTransport are the same two transports. In Go the HTTP
side is a plain http.Handler, so it composes with any router or middleware (http.ServeMux, chi...).

**READ NEXT:** docs/protocol.md#transports (stdio, streamable, stateless mode, resumability),
docs/protocol.md#security, and examples/http in the SDK repo
(github.com/modelcontextprotocol/go-sdk/tree/main/docs).

## Lesson 9.1: Streamable HTTP

**CONCEPT:** serving and dialing MCP over HTTP

```
Server side: an http.Handler built from a getServer callback.
  h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return s }, nil)
  http.ListenAndServe("localhost:8080", h)      // or mux.Handle("/mcp", h)
nil options = stateful sessions, SSE responses, localhost protection on.
```

```
Client side: a transport that knows the endpoint URL.
  t := &mcp.StreamableClientTransport{Endpoint: "http://localhost:8080/mcp"}
  cs, err := client.Connect(ctx, t, nil)        // POSTs initialize, remembers Mcp-Session-Id
cs.Close() sends an HTTP DELETE that ends the session on the server.
```

```
Tests use net/http/httptest: srv := httptest.NewServer(h) starts a real server on a free local
port; srv.URL is its address; srv.Close() shuts it down.
```

**TASK:**
NewHTTPHandler: return a handler that serves s to every client (one shared server).
ConnectHTTP: connect a new client (Name "capstone-client", Version "v1.0.0") to the MCP endpoint
at url and return the session, or the error from Connect.

**Worked example:**

```go
func NewHTTPHandler(s *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
}

func ConnectHTTP(ctx context.Context, url string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "capstone-client", Version: "v1.0.0"}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url}, nil)
}
```

## Lesson 9.2: NewNotesServer

**CONCEPT:** one server, every feature, shared state

```
This is modules 2–5 in one place. The pieces you need:
  mcp.AddTool(s, &mcp.Tool{...}, func(ctx, req, in In) (*mcp.CallToolResult, Out, error))
      return nil, out, nil  → the SDK sends out as StructuredContent (checked against the output
                              schema inferred from Out) plus a JSON text copy in Content
  s.AddResourceTemplate(&mcp.ResourceTemplate{Name: "...", URITemplate: "notes://{id}"}, h)
      h(ctx, req) reads req.Params.URI ("notes://3") and returns
      &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: ..., MIMEType: ..., Text: ...}}}
      unknown id → return nil, mcp.ResourceNotFoundError(uri)
  s.AddPrompt(&mcp.Prompt{Name: "...", Arguments: []*mcp.PromptArgument{{Name: "id", Required: true}}}, h)
      h(ctx, req) reads req.Params.Arguments["id"] and returns
      &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{...}}}}
```

```
The SDK handles tools/call requests CONCURRENTLY, from every connected session at once (think
HTTP: one server, many clients). Every handler that touches the store runs on its own goroutine,
so the store needs a mutex (module 01), held in EVERY method, reads included.
```

**TASK:** Return a server (Name "notes", Version "v1.0.0") with an in-memory note store and:
tool "add_note"        input AddNoteInput; stores the note under a new unique id ("1", "2", ...)

```
                       and returns AddNoteOutput{ID} as structured output.
tool "search_notes"    input SearchNotesInput; returns SearchNotesOutput with every note whose
                       title OR body contains the query, case-insensitively, in any order.
resource template      URI template "notes://{id}" (any Name); reading notes://<id> returns ONE
                       content block with that URI, MIMEType "text/plain" and Text = the note's
                       body. Unknown id → mcp.ResourceNotFoundError.
prompt "summarize_note" one required argument "id"; returns a single user message whose text
                       asks for a summary and includes the note's title and body. Unknown id →
                       return an error.
Give each tool and the prompt a Description. Filling in noteStore below is the suggested split.
```

**HINT:** strings.Contains(strings.ToLower(s), strings.ToLower(q)). strconv.Itoa(n) makes the ids.
The resource handler can recover the id with strings.TrimPrefix(uri, "notes://").
Start search results as []Note{}: a nil slice is sent as "notes": null (the inferred schema
allows it, but [] is kinder to whoever reads the JSON).

**Worked example:**

```go
type Note struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

type AddNoteInput struct {
	Title string `json:"title" jsonschema:"a short title for the note"`
	Body  string `json:"body" jsonschema:"the text of the note"`
}

type AddNoteOutput struct {
	ID string `json:"id" jsonschema:"the id of the new note"`
}

type SearchNotesInput struct {
	Query string `json:"query" jsonschema:"text to look for in titles and bodies (case-insensitive)"`
}

type SearchNotesOutput struct {
	Notes []Note `json:"notes" jsonschema:"the matching notes"`
}

// noteStore holds the notes. Its methods are called from many goroutines at once.
type noteStore struct {
	mu     sync.Mutex
	notes  map[string]Note
	nextID int
}

func newNoteStore() *noteStore {
	return &noteStore{notes: make(map[string]Note)}
}

// add stores a new note and returns its id.
func (st *noteStore) add(title, body string) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.nextID++
	id := strconv.Itoa(st.nextID)
	st.notes[id] = Note{ID: id, Title: title, Body: body}
	return id
}

// get returns the note with the given id, and whether it exists.
func (st *noteStore) get(id string) (Note, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	n, ok := st.notes[id]
	return n, ok
}

// search returns the notes whose title or body contains query (case-insensitive).
func (st *noteStore) search(query string) []Note {
	st.mu.Lock()
	defer st.mu.Unlock()
	q := strings.ToLower(query)
	found := []Note{}
	for _, n := range st.notes {
		if strings.Contains(strings.ToLower(n.Title), q) || strings.Contains(strings.ToLower(n.Body), q) {
			found = append(found, n)
		}
	}
	return found
}

func NewNotesServer() *mcp.Server {
	store := newNoteStore()
	s := mcp.NewServer(&mcp.Implementation{Name: "notes", Version: "v1.0.0"}, nil)

	mcp.AddTool(s, &mcp.Tool{Name: "add_note", Description: "Save a new note and return its id"},
		func(_ context.Context, _ *mcp.CallToolRequest, in AddNoteInput) (*mcp.CallToolResult, AddNoteOutput, error) {
			return nil, AddNoteOutput{ID: store.add(in.Title, in.Body)}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "search_notes", Description: "Find notes whose title or body contains the query"},
		func(_ context.Context, _ *mcp.CallToolRequest, in SearchNotesInput) (*mcp.CallToolResult, SearchNotesOutput, error) {
			return nil, SearchNotesOutput{Notes: store.search(in.Query)}, nil
		})

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "note",
		Description: "The body of one note",
		URITemplate: "notes://{id}",
		MIMEType:    "text/plain",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		n, ok := store.get(strings.TrimPrefix(uri, "notes://"))
		if !ok {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
			{URI: uri, MIMEType: "text/plain", Text: n.Body},
		}}, nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "summarize_note",
		Description: "Ask the model to summarize one note",
		Arguments:   []*mcp.PromptArgument{{Name: "id", Description: "the note's id", Required: true}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		id := req.Params.Arguments["id"]
		n, ok := store.get(id)
		if !ok {
			return nil, fmt.Errorf("no note with id %q", id)
		}
		return &mcp.GetPromptResult{
			Description: "Summarize note " + id,
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: fmt.Sprintf("Summarize this note in one sentence.\n\nTitle: %s\n\n%s", n.Title, n.Body)},
			}},
		}, nil
	})

	return s
}
```

## Lesson 9.3: Serve over stdio

**CONCEPT:** stdio is just two pipes

```
mcp.IOTransport{Reader: in, Writer: out} speaks newline-delimited JSON over any
io.ReadCloser / io.WriteCloser pair. mcp.StdioTransport{} is the same thing hard-wired to
os.Stdin and os.Stdout (it wraps stdout so closing the session doesn't close it). Taking the
streams as parameters (accept an io.Reader, not a file) is what makes this testable with
io.Pipe instead of a real subprocess.
```

```
server.Run(ctx, transport) connects and then BLOCKS until the client hangs up (returns the
session's error, usually nil) or ctx is cancelled (closes the session, returns ctx.Err()).
Compare server.Connect, which returns at once and leaves the session running.
```

```
In a standalone notes app, main would call these helpers:
  ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
  defer stop()
  if err := Serve(ctx, NewNotesServer(), os.Stdin, os.Stdout); err != nil {
      log.Print(err) // stderr: stdout carries the protocol
  }
```

**TASK:** Serve s over in/out and block until the client disconnects or ctx is cancelled. Return what
Run returns.

To try this repository's server, follow [milestone 5](../milestones/05_ship.md#try-it-for-real) after completing it.
Its binary is `cmd/devmcp`; the notes server here is a standalone worked example.

**Worked example:**

```go
func Serve(ctx context.Context, s *mcp.Server, in io.ReadCloser, out io.WriteCloser) error {
	return s.Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
}
```

## Lesson 9.4: ConnectCommand (bonus)

**CONCEPT:** the host's side of stdio

```
This is what Claude Code does with `claude mcp add devmcp -- /absolute/path/to/bin/devmcp`:
  cmd := exec.Command(name, args...)
  t := &mcp.CommandTransport{Command: cmd}   // Connect starts cmd and wires its stdin/stdout
  cs, err := client.Connect(ctx, t, nil)
cs.Close() closes the child's stdin (the polite "please exit"), waits, then sends SIGTERM if the
child is still running after TerminateDuration (default 5s).
Use exec.Command, NOT exec.CommandContext(ctx, ...): ctx here bounds the CONNECT, and
CommandContext would kill the server the moment that context ends.
```

**TASK:** Start the program name with args as an MCP server subprocess, connect a new client
(Name "capstone-client", Version "v1.0.0") to it, and return the session.

**Worked example:**

```go
func ConnectCommand(ctx context.Context, name string, args ...string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "capstone-client", Version: "v1.0.0"}, nil)
	return client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(name, args...)}, nil)
}
```

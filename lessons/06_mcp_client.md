# Module 06: the client side of MCP and the protocol utilities (pagination, progress, logging, cancellation, list-changed notifications)

## Primer: clients and protocol utilities

A client talks to a server in two directions:

```
REQUESTS you send      cs.ListTools, cs.CallTool, cs.SetLoggingLevel, ...   (you wait for a reply)
NOTIFICATIONS it sends  progress, log messages, "the tool list changed"      (no reply)
```

You can't predict when a notification arrives, so you don't call anything to get one. You
register a callback in ClientOptions BEFORE connecting, and the SDK calls it on its own goroutine:

```
client := mcp.NewClient(impl, &mcp.ClientOptions{
    ProgressNotificationHandler: func(ctx context.Context, req *mcp.ProgressNotificationClientRequest) {...},
    LoggingMessageHandler:       func(ctx context.Context, req *mcp.LoggingMessageRequest) {...},
    ToolListChangedHandler:      func(ctx context.Context, req *mcp.ToolListChangedRequest) {...},
})
req.Params holds the notification's data. req.Session is the *mcp.ClientSession it arrived on.
```

The SDK runs these callbacks one at a time, in arrival order. Keep them short and don't block:
hand data to the rest of your program over a channel, or behind a mutex (module 01).

A notification about a request can even arrive AFTER that request's reply: CallTool may return
before your ProgressNotificationHandler has run. The tests therefore WAIT for notifications on
channels with a timeout. Never sleep and hope.

The utilities in this module, all from the MCP spec's "utilities" sections:

```
PAGINATION    list results come in pages, joined by an opaque cursor             (6.1)
PROGRESS      the server reports how far a long request has got                  (6.2)
LOGGING       the server sends log lines to the client, filtered by level        (6.3)
CANCELLATION  the client gives up on a request, and the server stops working     (6.4)
LIST CHANGED  the server says its tool list changed, and the client re-lists     (6.5)
```

A NOTE ON PROTOCOL VERSIONS. SDK v1.8.0 speaks MCP up to 2026-07-28, and its clients
negotiate that version by default. Check yours with cs.InitializeResult().ProtocolVersion.
You'll often meet servers and hosts on 2025-11-25 or older. Most of this module works the same
on both. Logging doesn't (see 6.3), so the logging tests run on both versions.

**FROM PYTHON/JS:** the Python SDK's ClientSession takes logging_callback / message_handler arguments,
and the TS SDK uses client.setNotificationHandler(Schema, fn). Same idea: callbacks you register
up front. Go's context.Context replaces the TS AbortSignal for cancellation.

**READ NEXT:** docs/protocol.md#progress and docs/protocol.md#cancellation, then
docs/server.md#pagination, docs/server.md#logging and docs/server.md#list-changed-notifications
(github.com/modelcontextprotocol/go-sdk/tree/main/docs).

## Lesson 6.1: AllToolNames

**CONCEPT:** pagination and range-over-func iterators

```
A server returns at most ServerOptions.PageSize items per list call (default 1000). The reply
carries NextCursor. You pass it back as Cursor to get the next page; "" means no more pages:
  params := &mcp.ListToolsParams{}
  for {
      res, err := cs.ListTools(ctx, params)
      if err != nil { return err }
      ... res.Tools ...
      if res.NextCursor == "" { break }
      params.Cursor = res.NextCursor
  }
The SDK wraps that loop in an iterator, an iter.Seq2[*mcp.Tool, error], so you can range over it:
  for tool, err := range cs.Tools(ctx, nil) {
      if err != nil { return err }     // check err FIRST: on error, tool is nil
      ... tool.Name ...
  }
It fetches the next page only when you reach it, and it stops at the first error.
cs.Resources, cs.Prompts and cs.ResourceTemplates work the same way.
```

**FROM PYTHON/JS:** it works like a Python generator, or a JS async iterator in `for await (...)`.
Go 1.23+ lets `range` drive any func(yield func(K, V) bool), and that's all iter.Seq2 is.

**TASK:** Return the names of ALL tools on the server, across every page, in the order received.
Return the error if any page fails to load.

**Worked example:**

```go
func AllToolNames(ctx context.Context, cs *mcp.ClientSession) ([]string, error) {
	var names []string
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		names = append(names, tool.Name)
	}
	return names, nil
}
```

## Lesson 6.2: AddBatchTool

**CONCEPT:** progress notifications

```
A client that wants progress puts a PROGRESS TOKEN in the request's _meta. The token is a string
or an int it picks to tell its requests apart:
  params := &mcp.CallToolParams{Name: "process_batch", Arguments: ...}
  params.SetProgressToken("batch-1")         // same as Meta: mcp.Meta{"progressToken": "batch-1"}
                                              // (SetProgressToken panics on anything but int/string)
and sets ClientOptions.ProgressNotificationHandler to receive the updates.
In the tool handler, read the token and echo it back with every update:
  token := req.Params.GetProgressToken()      // nil if the client didn't ask
  err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
      ProgressToken: token,                    // echo it as-is: it's opaque, don't convert it
      Progress:      3,                        // must increase with every update
      Total:         10,                       // optional; 0 means unknown
      Message:       "processed item 3 of 10", // optional, for humans
  })
No token means the client didn't ask for progress: send nothing (the spec says so).
```

**TASK:** Register a tool named "process_batch" (any non-empty Description) that takes BatchInput.
It "processes" items 1..Items. After each item i, IF the request carries a progress token, it
sends a progress notification with that token, Progress i, Total Items, and a non-empty Message.
Then it replies with the text `"processed <Items> items"`. If NotifyProgress fails, return that error.

**Worked example:**

```go
type BatchInput struct {
	Items int `json:"items" jsonschema:"how many items to process"`
}

func AddBatchTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "process_batch", Description: "Process a batch of items, reporting progress"},
		func(ctx context.Context, req *mcp.CallToolRequest, in BatchInput) (*mcp.CallToolResult, any, error) {
			token := req.Params.GetProgressToken()
			for i := 1; i <= in.Items; i++ {
				// (real work for item i would go here)
				if token == nil {
					continue
				}
				err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: token,
					Progress:      float64(i),
					Total:         float64(in.Items),
					Message:       fmt.Sprintf("processed item %d of %d", i, in.Items),
				})
				if err != nil {
					return nil, nil, err
				}
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("processed %d items", in.Items)}},
			}, nil, nil
		})
}
```

## Lesson 6.3: AddImportTool and CallWithLogLevel

**CONCEPT:** server → client logging, and where the level comes from

```
Server side, inside a handler:
  req.Session.Log(ctx, &mcp.LoggingMessageParams{
      Level:  "warning",        // debug < info < notice < warning < error < critical < alert < emergency
      Logger: "importer",       // optional: which component is talking
      Data:   "row 2: missing email",   // any JSON value: a string, a map, ...
  })
Log sends the message only if Level >= the client's chosen minimum. Below it, Log silently
returns nil, and if the client never chose a level, NOTHING is sent.
Pass the handler's own ctx: on 2026-07-28 that's where the level lives (see below).
Alternative: logger := slog.New(mcp.NewLoggingHandler(req.Session, &mcp.LoggingHandlerOptions{
LoggerName: "importer"})) gives you a *slog.Logger. Use logger.WarnContext(ctx, ...), not
logger.Warn(...), for the same ctx reason. Data then arrives as a JSON object {"msg": ...}.
```

```
Client side: ClientOptions.LoggingMessageHandler receives the messages (req.Params.Level/Data/Logger).
HOW the client chooses the level depends on the protocol version. We verified this in v1.8.0:
  2025-11-25 and older:  cs.SetLoggingLevel(ctx, &mcp.SetLoggingLevelParams{Level: "warning"})
                         sets a level for the whole SESSION.
  2026-07-28:            each request carries its own level in _meta:
                           params.Meta = mcp.Meta{mcp.MetaKeyLogLevel: "warning"}
                         The session level is NOT used for logs sent while handling that request.
                         (SetLoggingLevel still returns nil, but those logs stay silent.)
Each mechanism is ignored by the other version, so a client that may meet either does both.
Heads-up: MCP logging is deprecated as of 2026-07-28 (SEP-2577) in favor of stderr/OpenTelemetry.
It still works and you'll meet it in existing servers, which is why it's here.
```

TASK (a): Register a tool named "import_rows" (any non-empty Description, no input: use `any` or

```
struct{} as In) that logs these four messages, in this order, all with Logger "importer":
  "debug"   "reading rows"
  "info"    "imported 3 rows"
  "warning" "row 2: missing email"
  "error"   "row 3: invalid date"
and then replies with the text "imported 3 rows".
```

TASK (b): CallWithLogLevel calls the tool described by params so that logs at `level` and above

```
reach the client on EITHER protocol version: set the session level with SetLoggingLevel (return
its error), AND add the level to params.Meta under mcp.MetaKeyLogLevel. Keep any entries already
in params.Meta, such as a progress token. Then return CallTool's result and error.
```

**HINT:** params.Meta is a map (mcp.Meta is map[string]any) and may be nil. Writing to a nil map panics.

**Worked example:**

```go
func AddImportTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "import_rows", Description: "Import rows, logging what happens"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			logs := []struct {
				level mcp.LoggingLevel
				msg   string
			}{
				{"debug", "reading rows"},
				{"info", "imported 3 rows"},
				{"warning", "row 2: missing email"},
				{"error", "row 3: invalid date"},
			}
			for _, l := range logs {
				err := req.Session.Log(ctx, &mcp.LoggingMessageParams{Level: l.level, Logger: "importer", Data: l.msg})
				if err != nil {
					return nil, nil, err
				}
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "imported 3 rows"}}}, nil, nil
		})
}

func CallWithLogLevel(ctx context.Context, cs *mcp.ClientSession, params *mcp.CallToolParams, level mcp.LoggingLevel) (*mcp.CallToolResult, error) {
	if err := cs.SetLoggingLevel(ctx, &mcp.SetLoggingLevelParams{Level: level}); err != nil {
		return nil, err
	}
	if params.Meta == nil {
		params.Meta = mcp.Meta{}
	}
	params.Meta[mcp.MetaKeyLogLevel] = level
	return cs.CallTool(ctx, params)
}
```

## Lesson 6.4: AddSlowTool and CallWithTimeout

**CONCEPT:** cancellation crosses the wire

```
When the client's ctx is cancelled (or times out) during cs.CallTool:
  1. CallTool returns at once with ctx's error (context.Canceled / context.DeadlineExceeded).
  2. The SDK sends "notifications/cancelled" for that request to the server.
  3. The server SDK cancels the ctx it passed to your handler.
Step 3 only helps if your handler WATCHES ctx. A handler stuck in time.Sleep keeps running, and
keeps using CPU, rate limits and locks, for a reply nobody will read. Wait like this instead:
  select {
  case <-ctx.Done():
      return nil, nil, ctx.Err()
  case <-time.After(StepDuration):   // or a time.Ticker, or a channel with real work results
  }
Surprise: the server sees context.Canceled even when the client hit a DEADLINE. The cancelled
notification doesn't say why, so only the client knows it was a timeout.
```

TASK (a): Register a tool named "slow" (any non-empty Description) that takes SlowInput. It does

```
Steps steps, each one waiting StepDuration, and then replies with the text
"finished <Steps> steps". If ctx is cancelled at any point, it returns ctx.Err() right away.
```

TASK (b): CallWithTimeout calls the tool described by params, but gives up after d: it returns

```
CallTool's error (which wraps context.DeadlineExceeded) if the call takes longer.
```

**HINT:** context.WithTimeout(ctx, d) returns a cancel func. defer it (module 01).

**Worked example:**

```go
const StepDuration = 50 * time.Millisecond

type SlowInput struct {
	Steps int `json:"steps" jsonschema:"how many steps to run"`
}

func AddSlowTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "slow", Description: "Run a slow job, step by step"},
		func(ctx context.Context, req *mcp.CallToolRequest, in SlowInput) (*mcp.CallToolResult, any, error) {
			ticker := time.NewTicker(StepDuration)
			defer ticker.Stop()
			for range in.Steps {
				select {
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				case <-ticker.C:
				}
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("finished %d steps", in.Steps)}},
			}, nil, nil
		})
}

func CallWithTimeout(ctx context.Context, cs *mcp.ClientSession, params *mcp.CallToolParams, d time.Duration) (*mcp.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return cs.CallTool(ctx, params)
}
```

## Lesson 6.5: ToolCache (bonus)

**CONCEPT:** keeping a client-side cache fresh with list-changed notifications

```
Listing tools on every model turn is wasteful, so clients cache the list. When the server adds or
removes a tool (s.AddTool / s.RemoveTools on a live server), it sends
"notifications/tools/list_changed". The notification has no payload: it only says "re-list".
  ClientOptions.ToolListChangedHandler: func(ctx context.Context, req *mcp.ToolListChangedRequest)
A chicken-and-egg problem: you set the handler BEFORE Connect, so there's no session yet to call
ListTools on. The answer is req.Session, the session the notification arrived on.
Fine print, from v1.8.0's source:
  - The server advertises tools.listChanged only if it has tools at Connect time (or sets
    ServerOptions.Capabilities). On 2026-07-28 the client subscribes only if you set the handler.
  - The server waits ~10ms and merges quick changes into ONE notification, so count states, not
    notifications.
  - The handler runs on an SDK goroutine while other code reads the cache: guard it with a mutex.
```

**TASK:** Refresh replaces the cached names with the server's current tool names (all pages: reuse
AllToolNames). Names returns a COPY of the cached names, so callers can't modify the cache
(slices share memory). OnToolListChanged refreshes using req.Session. It has nowhere
to return an error, so it ignores one (a real client would log it).

**Worked example:**

```go
type ToolCache struct {
	mu    sync.Mutex
	names []string
}

func (c *ToolCache) Refresh(ctx context.Context, cs *mcp.ClientSession) error {
	names, err := AllToolNames(ctx, cs)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.names = names
	return nil
}

func (c *ToolCache) Names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.names)
}

func (c *ToolCache) OnToolListChanged(ctx context.Context, req *mcp.ToolListChangedRequest) {
	_ = c.Refresh(ctx, req.Session)
}
```

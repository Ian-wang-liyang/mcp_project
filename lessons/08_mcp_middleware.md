# Module 08: middleware around MCP messages

## Primer: middleware

Every message a session sends or receives passes through ONE function type (mcp/shared.go):

```
type MethodHandler func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error)
  method  the JSON-RPC method: "tools/call", "tools/list", "ping", ...
  req     an interface; the concrete type depends on the method (see below)
  result  an interface too; nil for notifications, which also return a nil error
```

A MIDDLEWARE wraps a handler in another handler:

```
type Middleware func(next mcp.MethodHandler) mcp.MethodHandler
```

```
func logging(next mcp.MethodHandler) mcp.MethodHandler {
    return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
        start := time.Now()                       // before: inspect, or reject by returning early
        res, err := next(ctx, method, req)        // call the rest of the chain
        log.Printf("%s took %v", method, time.Since(start))   // after: inspect or replace res
        return res, err
    }
}
```

Four places to hook in. Server receiving middleware is the one you'll write most often:

```
server.AddReceivingMiddleware(m...)   requests arriving at the server: auth, logging, metrics
server.AddSendingMiddleware(m...)     messages the server sends (notifications, ...)
client.AddSendingMiddleware(m...)     requests the client sends: tracing, timing, progress tokens
client.AddReceivingMiddleware(m...)   messages arriving at the client
```

**ORDER:** within one call, AddReceivingMiddleware(a, b) runs a first (a wraps b). Each LATER Add
call wraps everything added before it, so the middleware you add last runs first. Install
middleware before Connect so it sees every message.

The concrete request types are generic structs, with aliases for the common ones:

```
*mcp.CallToolRequest  = *mcp.ServerRequest[*mcp.CallToolParamsRaw]   (server side)
*mcp.ListToolsRequest = *mcp.ServerRequest[*mcp.ListToolsParams]
if ctr, ok := req.(*mcp.CallToolRequest); ok { ... ctr.Params.Name ... }
```

The server keeps tool arguments as raw JSON here (Params.Arguments is a json.RawMessage),
because middleware runs before any handler has unmarshalled them. Every Request also has
GetSession(), GetParams(), and GetExtra(), which carries the HTTP header and the OAuth TokenInfo
when the transport is HTTP.

What middleware sees depends on the protocol version. On 2026-07-28 (the default) a session
opens with "server/discover"; older clients send "initialize" and then
"notifications/initialized". And on 2026-07-28 a multi round-trip tool call (module 7)
reaches the server's receiving middleware once per round.

**FROM PYTHON/JS:** this is Express/Koa middleware (`(req, res, next) => ...`), or a Python
decorator / ASGI middleware, around JSON-RPC methods instead of HTTP routes. Go has no
decorator syntax: you wrap functions by hand, and `next` is an explicit argument.

**READ NEXT:** examples/server/middleware, examples/client/middleware and examples/server/custom-method
in the SDK repo, and the Middleware docs at pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp.

## Lesson 8.1: MethodRecorder

**CONCEPT:** a middleware that keeps state

```
A method value turns a struct's method into a plain func that remembers its receiver, so a
struct can hand out a middleware bound to itself:
  func (r *MethodRecorder) Middleware() mcp.Middleware {
      return func(next mcp.MethodHandler) mcp.MethodHandler {
          return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
              ... r.something ...
              return next(ctx, method, req)
          }
      }
  }
The SDK handles requests concurrently, so the inner func runs on many goroutines at once.
Guard shared state with the mutex (module 01) and run the tests with -race.
```

**TASK:** Middleware returns a middleware that appends every method name it sees to the recorder,
then passes the request on unchanged. Methods returns the names in the order they were seen,
as a copy the caller can modify safely. The zero MethodRecorder must be ready to use.
**HINT:** slices.Clone makes the copy.

**Worked example:**

```go
type MethodRecorder struct {
	mu      sync.Mutex
	methods []string
}

func (r *MethodRecorder) Middleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			r.mu.Lock()
			r.methods = append(r.methods, method)
			r.mu.Unlock()
			return next(ctx, method, req)
		}
	}
}

func (r *MethodRecorder) Methods() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.methods)
}
```

## Lesson 8.2: DenyTools

**CONCEPT:** rejecting a request before it reaches the handler

```
Return early WITHOUT calling next, and the handler never runs:
  if ctr, ok := req.(*mcp.CallToolRequest); ok && blocked(ctr.Params.Name) {
      return nil, fmt.Errorf("...")
  }
  return next(ctx, method, req)          // everything else passes through untouched
An error returned here reaches the client as a JSON-RPC error: cs.CallTool returns err != nil.
That's right for policy ("you may not"). A tool result with IsError is for the tool itself
failing (module 3). Use comma-ok on the type assertion: most requests are not tool calls,
and a bare req.(*mcp.CallToolRequest) would panic on them.
```

**TASK:** Return a middleware that rejects tools/call for any tool named in names, with an error
whose message contains the tool name and the word "forbidden". Every other request (other
tools, tools/list, ping, ...) passes through unchanged.
**HINT:** build a set (map[string]bool) once, outside the returned closures, not on every request.

**Worked example:**

```go
func DenyTools(names ...string) mcp.Middleware {
	denied := make(map[string]bool, len(names))
	for _, n := range names {
		denied[n] = true
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if ctr, ok := req.(*mcp.CallToolRequest); ok && denied[ctr.Params.Name] {
				return nil, fmt.Errorf("tool %q is forbidden", ctr.Params.Name)
			}
			return next(ctx, method, req)
		}
	}
}
```

## Lesson 8.3: HideTools (bonus)

**CONCEPT:** rewriting the response on the way out

```
Call next first, then inspect the result:
  res, err := next(ctx, method, req)
  if lr, ok := res.(*mcp.ListToolsResult); ok && err == nil { ... }
Never modify the *mcp.Tool values themselves: they are the server's registered tools, shared by
every session. Building a filtered copy of the result is the safe habit, and a shallow copy
is enough:
  out := *lr                      // copies the struct (Tools still shares the old array)
  out.Tools = filtered            // a fresh slice with only the tools to show
  return &out, nil
Hiding is not denying: a client that knows the name can still call a hidden tool. Real setups
combine HideTools with DenyTools.
```

**TASK:** Return a middleware that removes the tools named in names from tools/list results (keep
the others, in order). Everything else passes through unchanged.

**Worked example:**

```go
func HideTools(names ...string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			lr, ok := res.(*mcp.ListToolsResult)
			if !ok || err != nil {
				return res, err
			}
			out := *lr
			out.Tools = make([]*mcp.Tool, 0, len(lr.Tools))
			for _, tool := range lr.Tools {
				if !slices.Contains(names, tool.Name) {
					out.Tools = append(out.Tools, tool)
				}
			}
			return &out, nil
		}
	}
}
```

## Lesson 8.4: SendCounter (bonus)

**CONCEPT:** client sending middleware

```
client.AddSendingMiddleware(m) wraps every request the client SENDS, which makes it the place
for metrics, tracing, or stamping something on outgoing params. It is installed OUTSIDE the
client's multi round-trip loop (module 7). One cs.CallTool is one tools/call here, even if
the client actually sent it twice to answer the server's questions. Server receiving
middleware on a 2026-07-28 session sees both rounds.
```

**TASK:** Middleware returns a client sending middleware that counts requests per method; Count
reports the count for one method (0 if never seen). Safe for concurrent use; the zero
SendCounter must be ready to use.
**HINT:** a nil map reads fine but panics on write. Create it lazily, under the lock.

**Worked example:**

```go
type SendCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (c *SendCounter) Middleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			c.mu.Lock()
			if c.counts == nil {
				c.counts = make(map[string]int)
			}
			c.counts[method]++
			c.mu.Unlock()
			return next(ctx, method, req)
		}
	}
}

func (c *SendCounter) Count(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[method]
}
```

## Lesson 8.5: a custom method (bonus)

**CONCEPT:** JSON-RPC methods outside the MCP spec

```
Params and results are your own structs. Embed mcp.ParamsBase / mcp.ResultBase so they carry
_meta and satisfy the SDK's interfaces. Then register each side:
  server:  mcp.AddReceivingCustomMethod(server, "acme/search",
               func(ctx context.Context, ss *mcp.ServerSession, p *SearchParams) (*SearchResult, error) {...})
  client:  mcp.AddSendingCustomMethod[*SearchParams, *SearchResult](client, "acme/search")
  call:    res, err := mcp.CallCustomMethod[*SearchParams, *SearchResult](ctx, cs, "acme/search", &SearchParams{...})
Both Add functions return an error if the name is a standard MCP method. Prefix custom names
with a namespace ("acme/..."). Custom methods go through middleware like any other method.
Only another Go SDK peer that registered the same method will understand it, so reach for
this rarely: a tool is usually the better choice.
```

**TASK:** AddShoutMethod registers ShoutMethod on s: reply with the text upper-cased plus "!"
("hi" → "HI!"). NewShoutClient returns a client named "shout-client" (Version "v1.0.0") that
can send ShoutMethod. Shout calls ShoutMethod over cs and returns the reply's Text.

**Worked example:**

```go
const ShoutMethod = "practice/shout"

type ShoutParams struct {
	mcp.ParamsBase
	Text string `json:"text"`
}

type ShoutResult struct {
	mcp.ResultBase
	Text string `json:"text"`
}

func AddShoutMethod(s *mcp.Server) error {
	return mcp.AddReceivingCustomMethod(s, ShoutMethod,
		func(_ context.Context, _ *mcp.ServerSession, p *ShoutParams) (*ShoutResult, error) {
			return &ShoutResult{Text: strings.ToUpper(p.Text) + "!"}, nil
		})
}

func NewShoutClient() *mcp.Client {
	client := mcp.NewClient(&mcp.Implementation{Name: "shout-client", Version: "v1.0.0"}, nil)
	if err := mcp.AddSendingCustomMethod[*ShoutParams, *ShoutResult](client, ShoutMethod); err != nil {
		panic(err) // only fails for a standard method name, which ShoutMethod is not
	}
	return client
}

func Shout(ctx context.Context, cs *mcp.ClientSession, text string) (string, error) {
	res, err := mcp.CallCustomMethod[*ShoutParams, *ShoutResult](ctx, cs, ShoutMethod, &ShoutParams{Text: text})
	if err != nil {
		return "", err
	}
	return res.Text, nil
}
```

# Module 02: your first MCP server and client

## Primer: what MCP is

MCP (Model Context Protocol) is how an AI app plugs into outside tools and data.

```
HOST    the AI app the user talks to (Claude Code, Claude Desktop, an IDE)
CLIENT  lives inside the host; holds ONE connection to one server
SERVER  your program; offers features to the client
```

A server offers three kinds of features:

```
TOOLS      functions the model can call             ("search_notes", "create_issue")
RESOURCES  data the app can read, addressed by URI  ("file:///README.md", "notes://42")
PROMPTS    reusable message templates the user picks ("summarize this note")
```

Under the hood it's JSON-RPC 2.0: every message is a JSON object with a "method" and "params".

```
requests       expect a response matched by "id"   {"jsonrpc":"2.0","id":1,"method":"tools/call",...}
notifications  fire-and-forget, no id              {"jsonrpc":"2.0","method":"notifications/progress",...}
```

The SDK writes and parses all of that for you. You work with Go structs.

Every connection starts with a HANDSHAKE, where the two sides agree on a protocol VERSION and
learn each other's name and CAPABILITIES (what each side supports). There are two styles:

```
up to 2025-11-25   client → initialize {clientInfo, capabilities}; server ← {serverInfo, capabilities}
                   client → notifications/initialized
2026-07-28         client → server/discover; after that, every request carries the version and
                   client capabilities in its _meta (no long-lived "initialized" state)
```

The v1.8.0 SDK speaks both, picks the newest one the other side supports (2026-07-28 between two
v1.8.0 peers), and hides the difference: client.Connect does the handshake, and
cs.InitializeResult() gives you serverInfo, capabilities and instructions either way.
The Go SDK fills in the server's capabilities for you: register a tool and the server advertises
"tools"; register a prompt and it advertises "prompts".

PROTOCOL VERSIONS matter in a few places later on, and those modules say so:

```
6 logging levels, 7 server→client requests, 9 stateless HTTP.
Real hosts may still negotiate an older version. 2026-07-28 also deprecates logging, roots and
sampling; they still work and this track still teaches them.
```

The Go SDK in four lines (package github.com/modelcontextprotocol/go-sdk/mcp):

```
server := mcp.NewServer(&mcp.Implementation{Name: "demo", Version: "v1.0.0"}, nil)
mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "say hi"}, greetHandler)
client := mcp.NewClient(&mcp.Implementation{Name: "app", Version: "v1.0.0"}, nil)
session, err := client.Connect(ctx, transport, nil)   // runs the handshake
```

A TRANSPORT carries the bytes: stdio (a subprocess), Streamable HTTP, or in-memory (tests).
Each Connect returns a SESSION: *mcp.ServerSession on the server side, *mcp.ClientSession on the
client side. You call methods on the session: session.CallTool, session.ListTools, session.Close.

**FROM PYTHON/JS:** the Python SDK's FastMCP uses decorators (@mcp.tool()) and the TS SDK uses
server.registerTool(...). Go has neither decorators nor runtime type info in function signatures,
so it uses GENERICS: mcp.AddTool infers the tool's JSON schema from your input struct type.

**READ NEXT:** docs/quick_start.md and docs/protocol.md#lifecycle in the SDK repo
(github.com/modelcontextprotocol/go-sdk/tree/main/docs).

## Lesson 2.1: NewHelloServer

**CONCEPT:** creating a server

```
mcp.NewServer(impl *mcp.Implementation, opts *mcp.ServerOptions) *mcp.Server
Implementation is the server's name card: {Name, Version, Title}. The client sees it in the
handshake as serverInfo. nil options are fine.
```

**TASK:** Return a server whose Implementation has Name "hello" and Version "v1.0.0".

**Worked example:**

```go
func NewHelloServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "hello", Version: "v1.0.0"}, nil)
}
```

## Lesson 2.2: ConnectInMemory

**CONCEPT:** transports and sessions

```
serverT, clientT := mcp.NewInMemoryTransports()     // two ends of an in-process pipe
ss, err := server.Connect(ctx, serverT, nil)       // server side: *mcp.ServerSession
cs, err := client.Connect(ctx, clientT, nil)       // client side: runs the handshake
Connect the server end FIRST: the client's Connect sends the handshake and waits for the reply.
Real servers use server.Run(ctx, &mcp.StdioTransport{}) instead, which blocks until the client
hangs up. In-memory transports are how you test an MCP server without spawning a process.
```

**TASK:** Connect a new client (Name "test-client", Version "v1.0.0") to server over in-memory
transports and return the client session. Return any error from either Connect.

**Worked example:**

```go
func ConnectInMemory(ctx context.Context, server *mcp.Server) (*mcp.ClientSession, error) {
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1.0.0"}, nil)
	return client.Connect(ctx, clientT, nil)
}
```

## Lesson 2.3: AddGreetTool

**CONCEPT:** a typed tool handler

```
mcp.AddTool(server, &mcp.Tool{Name: "...", Description: "..."}, handler)
handler has the shape
  func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error)
In is a struct; the SDK infers the tool's input JSON schema from it, validates the client's
arguments against that schema, and unmarshals them into `in` before your handler runs.
To answer with plain text, return a result with Content set, and `any` as Out:
  return &mcp.CallToolResult{
      Content: []mcp.Content{&mcp.TextContent{Text: "..."}},
  }, nil, nil
The Description matters: it's what the model reads to decide when to call your tool.
```

**TASK:** Register a tool named "greet" (with any non-empty Description) on s. It takes GreetInput
and replies with the text `"Hello, <name>!"`.

**Worked example:**

```go
type GreetInput struct {
	Name string `json:"name" jsonschema:"the name of the person to greet"`
}

func AddGreetTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "greet", Description: "Say hello to someone"},
		func(ctx context.Context, req *mcp.CallToolRequest, in GreetInput) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "Hello, " + in.Name + "!"}},
			}, nil, nil
		})
}
```

## Lesson 2.4: CallGreet

**CONCEPT:** calling a tool from the client

```
res, err := cs.CallTool(ctx, &mcp.CallToolParams{
    Name:      "greet",
    Arguments: map[string]any{"name": "Ada"},   // anything that marshals to a JSON object
})
err != nil       → the call itself failed (unknown tool, connection closed)
res.IsError      → the tool reported a failure, OR the arguments didn't match its schema
                   (module 3 covers the difference)
res.Content      → []mcp.Content, an interface; type-assert to read it:
    if tc, ok := res.Content[0].(*mcp.TextContent); ok { ... tc.Text ... }
```

**TASK:** Call the "greet" tool with the given name and return the text of its first content block.
Return an error if the call fails, if res.IsError is set, or if the first block isn't text.

**Worked example:**

```go
func CallGreet(ctx context.Context, cs *mcp.ClientSession, name string) (string, error) {
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "greet",
		Arguments: map[string]any{"name": name},
	})
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", errors.New("greet reported an error")
	}
	if len(res.Content) == 0 {
		return "", errors.New("greet returned no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return "", fmt.Errorf("greet returned %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text, nil
}
```

## Lesson 2.5: NewGuideServer (bonus)

**CONCEPT:** server instructions

```
mcp.NewServer(impl, &mcp.ServerOptions{Instructions: "..."})
Instructions travel in the handshake result. Hosts often put them in the model's system prompt,
so use them to explain how your tools fit together ("call search before read").
The client reads them with cs.InitializeResult().Instructions.
```

**TASK:** Return a server named "guide" (Version "v1.0.0") whose instructions are exactly
GuideInstructions.

**Worked example:**

```go
const GuideInstructions = "Call greet before anything else."

func NewGuideServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "guide", Version: "v1.0.0"},
		&mcp.ServerOptions{Instructions: GuideInstructions})
}
```

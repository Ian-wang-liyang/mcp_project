# Module 15: requests that flow server → client

## Primer: when the server asks the client

Until now every request flowed client → server (tools/call, resources/read, ...). MCP also has
three requests that flow the OTHER way, each one backed by a client capability:

```
ROOTS        roots/list               "which folders is the user working in?"
SAMPLING     sampling/createMessage   "run this prompt through YOUR LLM for me"
ELICITATION  elicitation/create       "ask the USER this question (a small form)"
```

The server never talks to a model or a human itself. It asks the client, and the HOST decides
what to do (and can show the user what's being asked). The client opts in by configuring
itself; the SDK infers the capability from that:

```
client.AddRoots(&mcp.Root{URI: "file:///repo"})                  → roots
&mcp.ClientOptions{CreateMessageHandler: func(...) {...}}        → sampling
&mcp.ClientOptions{ElicitationHandler: func(...) {...}}          → elicitation
```

Roots and sampling are deprecated as of protocol 2026-07-28 (SEP-2577) but still work for at
least twelve more months; you will meet them in existing servers. Elicitation is not deprecated.

MULTI ROUND-TRIP REQUESTS (MRTR, SEP-2322, mcp/mrtr.go). HOW a tool asks depends on the
negotiated protocol version, and SDK v1.8.0 defaults to the newest one, 2026-07-28:

```
≤ 2025-11-25  the tool handler calls req.Session.ListRoots / CreateMessage / Elicit, which
              sends a separate JSON-RPC request to the client and blocks until it answers.
2026-07-28    the server no longer sends requests of its own. On such a session those same
              calls ALWAYS return an error: `"elicitation/create" cannot be sent while serving
              a request on protocol version 2026-07-28: return an InputRequests map instead`.
              (docs/server.md claims the old calls still work for new clients. In v1.8.0 they
              don't.) Instead, the handler RETURNS its questions and gets called again:
```

```
func handler(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
    resp, ok := req.Params.InputResponses["ok"]   // round 2: the answers come back here
    if !ok {                                      // round 1: ask
        return &mcp.CallToolResult{
            InputRequests: mcp.InputRequestMap{"ok": &mcp.ElicitParams{...}},
        }, nil, nil
    }
    answer := resp.(*mcp.ElicitResult)            // act on the answer
    ...
}
```

The key ("ok") is a label you pick; the answer comes back under the same key. The value's Go
type picks the request: *mcp.ListRootsParams, *mcp.CreateMessageParams or *mcp.ElicitParams.
The client installs an MRTR middleware BY DEFAULT. It sees InputRequests, calls the matching
client handler for each one, and re-sends the SAME tools/call (same arguments) with
InputResponses filled in. The code calling cs.CallTool only ever sees the final result.
Your handler, though, RUNS TWICE per call. Two rules follow:

```
- Do nothing irreversible in round 1. Act only once you hold the answer.
- Never return Content AND InputRequests together: the SDK rejects that as a server bug.
```

Older clients get the same behavior: the server SDK installs a shim that fulfils your
InputRequests with the old-style calls and re-invokes your handler once. An MRTR-style handler
works with EVERY client, which is why this module uses it and its tests run against both eras.

Not drilled here (advanced): a handler can also return RequestState, an opaque string the client
echoes back on the retry so you can resume without server-side memory (sign or encrypt it, the
client can tamper with it). With ClientOptions.MultiRoundTrip = &mcp.MultiRoundTripOptions{
Disabled: true}, the client hands input-required results back to you (res.NeedsInput() is true)
and you drive the retry yourself.

**FROM PYTHON/JS:** the Python and TS SDKs offer awaitable calls for this (e.g. FastMCP's
`await ctx.elicit(...)`): the handler pauses until the client answers. MRTR turns
that `await` into "return the question; you'll be called again with the answer", like a step
function or a resumable generator.

**READ NEXT:** docs/client.md#roots, #sampling, #elicitation and #multi-round-trip-requests, then
docs/server.md#multi-round-trip-requests in the SDK repo.

## Lesson 15.1: NewRootsClient

**CONCEPT:** the client side of roots

```
client := mcp.NewClient(&mcp.Implementation{Name: "...", Version: "..."}, nil)
client.AddRoots(&mcp.Root{URI: "file:///home/ada/repo", Name: "repo"}, ...)   // variadic
A Root is a URI (file:// for now) plus an optional display Name. AddRoots works before or after
Connect; afterwards, connected servers get a notifications/roots/list_changed. The SDK client
advertises the roots capability by default, so there's nothing else to switch on.
```

**TASK:** Return a client named "roots-client" (Version "v1.0.0") that offers one root per URI in
uris, in the same order.

**Worked example:**

```go
func NewRootsClient(uris ...string) *mcp.Client {
	client := mcp.NewClient(&mcp.Implementation{Name: "roots-client", Version: "v1.0.0"}, nil)
	for _, uri := range uris {
		client.AddRoots(&mcp.Root{URI: uri})
	}
	return client
}
```

## Lesson 15.2: AddListRootsTool

**CONCEPT:** your first multi round-trip handler

```
Round 1: req.Params.InputResponses is empty, so return only the question:
  return &mcp.CallToolResult{
      InputRequests: mcp.InputRequestMap{"roots": &mcp.ListRootsParams{}},
  }, nil, nil
Round 2: the client's answer is waiting under the same key, typed as an mcp.InputResponse
interface. Type-assert it with comma-ok:
  list, ok := req.Params.InputResponses["roots"].(*mcp.ListRootsResult)
  for _, root := range list.Roots { ... root.URI ... }
```

**TASK:** Register a tool named "list_roots" (non-empty Description, no input: use struct{} as In)
that asks the client for its roots and replies with one text block: the root URIs joined by
"\n", or exactly "no roots" if the client has none.
**HINT:** if your result says "cannot be sent while serving a request", you called
req.Session.ListRoots: the old way. Re-read the primer.

**Worked example:**

```go
func AddListRootsTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "list_roots", Description: "List the folders the user is working in"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			resp, ok := req.Params.InputResponses["roots"]
			if !ok {
				return &mcp.CallToolResult{
					InputRequests: mcp.InputRequestMap{"roots": &mcp.ListRootsParams{}},
				}, nil, nil
			}
			list, ok := resp.(*mcp.ListRootsResult)
			if !ok {
				return nil, nil, fmt.Errorf("roots answer is %T, want *mcp.ListRootsResult", resp)
			}
			if len(list.Roots) == 0 {
				return textResult("no roots"), nil, nil
			}
			uris := make([]string, 0, len(list.Roots))
			for _, root := range list.Roots {
				uris = append(uris, root.URI)
			}
			return textResult(strings.Join(uris, "\n")), nil, nil
		})
}

// textResult wraps text in a one-block tool result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
```

## Lesson 15.3: AddSummarizeTool

**CONCEPT:** sampling, i.e. borrowing the client's LLM

```
Ask with a *mcp.CreateMessageParams, a mini chat-completion request:
  &mcp.CreateMessageParams{
      Messages: []*mcp.SamplingMessage{
          {Role: "user", Content: &mcp.TextContent{Text: "..."}},
      },
      MaxTokens:    200,                 // required: the client's model needs a budget
      SystemPrompt: "...",               // optional; the host may ignore it
  }
GOTCHA: the answer does NOT come back as *mcp.CreateMessageResult. The SDK upgrades every
sampling answer to the newer, tool-capable shape, so assert *mcp.CreateMessageWithToolsResult,
whose Content is a []mcp.Content slice (not a single Content):
  out, ok := resp.(*mcp.CreateMessageWithToolsResult)
  out.Content[0].(*mcp.TextContent).Text      // guard both the length and the type
The client's ClientOptions.CreateMessageHandler produces that answer. In the tests it's a fake
that never touches a real model; in a host it's the app's own LLM, with the user's consent.
```

**TASK:** Register a tool named "summarize" (non-empty Description) taking SummarizeInput. Ask the
client's LLM for a summary with a single "user" message whose text contains in.Text, and
MaxTokens > 0. Reply with the text of the LLM's first content block, unchanged. If the answer
has no text block, return an error from the handler (the SDK turns it into an IsError result).

**Worked example:**

```go
type SummarizeInput struct {
	Text string `json:"text" jsonschema:"the text to summarize"`
}

func AddSummarizeTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "summarize", Description: "Summarize text in one sentence"}, summarize)
}

func summarize(ctx context.Context, req *mcp.CallToolRequest, in SummarizeInput) (*mcp.CallToolResult, any, error) {
	resp, ok := req.Params.InputResponses["summary"]
	if !ok {
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{"summary": &mcp.CreateMessageParams{
				Messages: []*mcp.SamplingMessage{{
					Role:    "user",
					Content: &mcp.TextContent{Text: "Summarize in one sentence:\n\n" + in.Text},
				}},
				SystemPrompt: "You write short, faithful summaries.",
				MaxTokens:    200,
			}},
		}, nil, nil
	}
	out, ok := resp.(*mcp.CreateMessageWithToolsResult)
	if !ok {
		return nil, nil, fmt.Errorf("sampling answer is %T, want *mcp.CreateMessageWithToolsResult", resp)
	}
	if len(out.Content) == 0 {
		return nil, nil, errors.New("the LLM returned no content")
	}
	tc, ok := out.Content[0].(*mcp.TextContent)
	if !ok {
		return nil, nil, fmt.Errorf("the LLM returned %T, want text", out.Content[0])
	}
	return textResult(tc.Text), nil, nil
}
```

## Lesson 15.4: AddDeleteTool

**CONCEPT:** elicitation, i.e. asking the user

```
&mcp.ElicitParams{
    Message:         "Delete notes.txt?",          // what the user sees
    RequestedSchema: &jsonschema.Schema{          // the form: a FLAT object of primitives
        Type: "object",
        Properties: map[string]*jsonschema.Schema{
            "confirm": {Type: "boolean", Description: "Yes, delete it"},
        },
        Required: []string{"confirm"},
    },
}
(package github.com/google/jsonschema-go/jsonschema, the schema library the SDK itself uses.
RequestedSchema is typed `any`, so a map[string]any shaped like JSON Schema works too.)
The answer is a *mcp.ElicitResult:
  Action   "accept" (submitted the form), "decline" (said no), "cancel" (dismissed it)
  Content  map[string]any with the form values, only on "accept"; JSON booleans arrive as bool
The Go SDK client rejects a schema that isn't flat, and checks an accepted answer against it
before sending it back. Other clients might not, so still read Content defensively.
This is the "are you sure?" pattern: the MODEL decided to call the tool, but a destructive
action should wait for the HUMAN.
```

**TASK:** Register a tool named "delete_file" (non-empty Description) taking DeleteInput. Before
deleting, ask the user with a Message that contains the path and a RequestedSchema with a
required boolean property "confirm". Only if the user accepts with confirm == true, call
onDelete(path) once and reply `"deleted <path>"`. Any other answer (decline, cancel, or
confirm == false) replies `"kept <path>"`: a normal result, not an error.
**HINT:** round 1 must NOT call onDelete. A comma-ok assertion handles a missing or non-bool value:
confirmed, _ := answer.Content["confirm"].(bool)

**Worked example:**

```go
type DeleteInput struct {
	Path string `json:"path" jsonschema:"the file to delete"`
}

func AddDeleteTool(s *mcp.Server, onDelete func(path string)) {
	mcp.AddTool(s, &mcp.Tool{Name: "delete_file", Description: "Delete a file, after the user confirms"},
		func(ctx context.Context, req *mcp.CallToolRequest, in DeleteInput) (*mcp.CallToolResult, any, error) {
			resp, ok := req.Params.InputResponses["confirm"]
			if !ok {
				return &mcp.CallToolResult{
					InputRequests: mcp.InputRequestMap{"confirm": &mcp.ElicitParams{
						Message: fmt.Sprintf("Delete %s? This cannot be undone.", in.Path),
						RequestedSchema: &jsonschema.Schema{
							Type: "object",
							Properties: map[string]*jsonschema.Schema{
								"confirm": {Type: "boolean", Description: "Yes, delete it"},
							},
							Required: []string{"confirm"},
						},
					}},
				}, nil, nil
			}
			answer, ok := resp.(*mcp.ElicitResult)
			if !ok {
				return nil, nil, fmt.Errorf("elicitation answer is %T, want *mcp.ElicitResult", resp)
			}
			confirmed, _ := answer.Content["confirm"].(bool)
			if answer.Action != "accept" || !confirmed {
				return textResult("kept " + in.Path), nil, nil
			}
			onDelete(in.Path)
			return textResult("deleted " + in.Path), nil, nil
		})
}
```

## Lesson 15.5: AddSafeSummarizeTool (bonus)

**CONCEPT:** checking the client's capabilities first

```
Ask a client that has no CreateMessageHandler for sampling and the whole call FAILS: cs.CallTool
returns a protocol error ("client does not support CreateMessage"). There is no tool result,
so the model can't see what went wrong or work around it. Check first, then degrade:
  p := req.Session.InitializeParams()           // what the client declared about itself
  canSample := p != nil && p.Capabilities != nil && p.Capabilities.Sampling != nil
(The same check works for Capabilities.Elicitation.) This works on both protocol eras: under
2026-07-28 there's no initialize request, and the SDK fills this in from per-request metadata.
```

**TASK:** Register a tool named "safe_summarize" (non-empty Description) taking SummarizeInput. If
the client can sample, behave exactly like "summarize". If it can't, don't ask: reply with the
first FallbackRunes runes of the text followed by "...", or the whole text if it has at most
FallbackRunes runes.
**HINT:** runes, not bytes: []rune(s) or utf8.RuneCountInString. To reuse 15.3, move
its handler into a named func and call it from both tools.

**Worked example:**

```go
const FallbackRunes = 20

func AddSafeSummarizeTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "safe_summarize", Description: "Summarize text, or truncate it if no LLM is available"},
		func(ctx context.Context, req *mcp.CallToolRequest, in SummarizeInput) (*mcp.CallToolResult, any, error) {
			p := req.Session.InitializeParams()
			if p == nil || p.Capabilities == nil || p.Capabilities.Sampling == nil {
				runes := []rune(in.Text)
				if len(runes) <= FallbackRunes {
					return textResult(in.Text), nil, nil
				}
				return textResult(string(runes[:FallbackRunes]) + "..."), nil, nil
			}
			return summarize(ctx, req, in)
		})
}
```

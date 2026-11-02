# Module 11: MCP tools in depth

## Primer: tools in depth

A TOOL is a function the MODEL decides to call. The model only ever sees three things about it:

```
name + description   "search: find notes whose title contains a word"
inputSchema          a JSON Schema describing the arguments object
outputSchema         (optional) a JSON Schema describing the structured result
```

The model writes the arguments as JSON, the client sends tools/call, your handler runs, and the
result goes back to the model. Good schemas and descriptions are how you steer the model.

The generic mcp.AddTool does the busywork for you, all driven by Go types:

```
mcp.AddTool(server, &mcp.Tool{Name: "search", Description: "..."}, handler)
handler: func(ctx, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error)
  In   → inputSchema is INFERRED from the struct; arguments are VALIDATED, then unmarshaled into in
  Out  → outputSchema is inferred (unless Out is `any`); the value becomes StructuredContent
  error → becomes a result with IsError: true (the model sees the message and can retry)
```

A tools/call has three possible outcomes, and Go code must handle all three:

```
res, err := cs.CallTool(ctx, params)
err != nil            PROTOCOL error: the call itself failed (unknown tool, connection gone)
res.IsError == true   TOOL error: the tool ran (or tried to) and failed; the text says why
otherwise             success: read res.Content and/or res.StructuredContent
```

The lower-level server.AddTool takes a raw mcp.ToolHandler and does NONE of that: you write the
schema, unmarshal the arguments, and set IsError yourself (BONUS 11.6).

**FROM PYTHON/JS:** FastMCP builds the schema from type hints + docstrings, the TS SDK from a zod
object. Go builds it from struct fields + tags, via github.com/google/jsonschema-go.

**READ NEXT:** docs/server.md#tools and docs/server.md#tool-result-content in the SDK repo
(github.com/modelcontextprotocol/go-sdk/tree/main/docs).

## Lesson 11.1: AddSearchTool

**CONCEPT:** input schemas are inferred from struct tags

```
type In struct {
    Query string `json:"query" jsonschema:"text to search for"`
    Limit int    `json:"limit,omitempty" jsonschema:"maximum number of results"`
}
json tag            → the property name ("query"); untagged fields keep their Go name ("Query")
no omitempty        → the property is listed in "required"
omitempty/omitzero  → optional: the model may leave it out, and you get the zero value
jsonschema tag      → the property's "description" (plain text; must not start with "WORD=")
Go type             → JSON type: string→"string", int→"integer", float64→"number",
                      bool→"boolean", []T→"array", struct/map→"object"
Inferred schemas also set "additionalProperties": false, so an argument the schema doesn't
know about is rejected. Gotcha: a pointer field (*string) WITHOUT omitempty is still required;
it just may be null.
The client sees all of this in ListTools as tool.InputSchema.
```

**FROM PYTHON/JS:** `jsonschema:"..."` plays the role of Pydantic's Field(description=...) or zod's
.describe(). omitempty is the Go spelling of `limit: int = 0` / `limit?: number`.

**TASK:** Give SearchInput two fields:

```
  Query  string, JSON name "query", required, with a description
  Limit  int,    JSON name "limit", optional, with a description
Then register a tool named "search" (non-empty Description) on s. It returns ONE text block:
the titles in NoteTitles that contain Query, ignoring case, in order, joined with "\n".
If Limit > 0, return at most Limit titles. If nothing matches, return the text "no matches".
```

**HINT:** strings.Contains(strings.ToLower(title), strings.ToLower(in.Query)); strings.Join.

**Worked example:**

```go
var NoteTitles = []string{"Go slices", "Go maps", "Python dicts", "MCP tools", "Go channels"}

type SearchInput struct {
	Query string `json:"query" jsonschema:"text to look for in note titles, ignoring case"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of titles to return; 0 means no limit"`
}

func AddSearchTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "search", Description: "Find notes whose title contains a word"},
		func(_ context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, any, error) {
			var hits []string
			for _, title := range NoteTitles {
				if strings.Contains(strings.ToLower(title), strings.ToLower(in.Query)) {
					hits = append(hits, title)
				}
				if in.Limit > 0 && len(hits) == in.Limit {
					break
				}
			}
			text := "no matches"
			if len(hits) > 0 {
				text = strings.Join(hits, "\n")
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
		})
}
```

## Lesson 11.2: AddStatsTool

**CONCEPT:** structured output

```
Give the handler a real Out type instead of `any`:
  func(ctx, req, in StatsInput) (*mcp.CallToolResult, StatsOutput, error)
and the SDK:
  1. infers an outputSchema from StatsOutput and advertises it in ListTools
  2. marshals your Out value into result.StructuredContent (and checks it against the schema)
  3. if you left Content empty, also puts the same JSON in a TextContent block, for clients
     that only read text
So the usual way to return structured data is:  return nil, out, nil
A nil *CallToolResult is fine; the SDK makes one.
Gotcha: NaN and ±Inf can't be encoded as JSON. Returning one makes the whole call fail with a
protocol error ("marshaling output"), so guard divisions by zero.
```

**FROM PYTHON/JS:** like returning a Pydantic model or a typed object from a FastMCP tool: the SDK
both serializes it and publishes its schema.

**TASK:** Register a tool named "stats" (non-empty Description) on s. It takes StatsInput and returns
StatsOutput: the count, sum and mean of Numbers. For an empty list, all three are 0.

**Worked example:**

```go
type StatsInput struct {
	Numbers []float64 `json:"numbers" jsonschema:"the numbers to summarize"`
}

type StatsOutput struct {
	Count int     `json:"count" jsonschema:"how many numbers there were"`
	Sum   float64 `json:"sum" jsonschema:"their sum"`
	Mean  float64 `json:"mean" jsonschema:"their average, or 0 for an empty list"`
}

func AddStatsTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "stats", Description: "Count, sum and average a list of numbers"},
		func(_ context.Context, _ *mcp.CallToolRequest, in StatsInput) (*mcp.CallToolResult, StatsOutput, error) {
			out := StatsOutput{Count: len(in.Numbers)}
			for _, n := range in.Numbers {
				out.Sum += n
			}
			if out.Count > 0 {
				out.Mean = out.Sum / float64(out.Count)
			}
			return nil, out, nil
		})
}
```

## Lesson 11.3: AddDivideTool

**CONCEPT:** tool errors vs protocol errors

```
With mcp.AddTool, returning an ordinary Go error from your handler does NOT fail the call:
  return nil, DivideOutput{}, ErrDivideByZero
The client gets err == nil and a result with IsError: true whose text is the error message,
and no StructuredContent. That's on purpose: the MODEL reads tool errors and can fix its call.
```

```
Arguments that don't match the input schema (a missing required field, a string where a number
should be, an unknown extra field) are ALSO reported as a tool error, IsError: true with text
like `validating "arguments": ...`, and your handler never runs.
```

```
Protocol errors, where CallTool's err != nil, are for calls that couldn't happen at all:
  an unknown tool name                → *jsonrpc.Error with Code -32602 (invalid params)
  your handler returns a *jsonrpc.Error (github.com/modelcontextprotocol/go-sdk/jsonrpc)
  the connection is closed, the ctx is cancelled
On the client, errors.As(err, &wireErr) with `var wireErr *jsonrpc.Error` exposes the Code.
```

```
If you build a result yourself and want to mark it failed, res.SetError(err) sets IsError and,
if Content is empty, puts err.Error() in a TextContent block.
```

**FROM PYTHON/JS:** raising inside a FastMCP tool also becomes an isError result. Here there are no
exceptions; the error is a return value, and the SDK routes it.

**TASK:** Register a tool named "divide" (non-empty Description) on s. It takes DivideInput (both
fields required) and returns DivideOutput{Quotient: A / B}. When B is 0, return ErrDivideByZero
as the handler's error so the client sees a tool error.

**Worked example:**

```go
var ErrDivideByZero = errors.New("division by zero")

type DivideInput struct {
	A float64 `json:"a" jsonschema:"the dividend"`
	B float64 `json:"b" jsonschema:"the divisor; must not be zero"`
}

type DivideOutput struct {
	Quotient float64 `json:"quotient"`
}

func AddDivideTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "divide", Description: "Divide a by b"},
		func(_ context.Context, _ *mcp.CallToolRequest, in DivideInput) (*mcp.CallToolResult, DivideOutput, error) {
			if in.B == 0 {
				return nil, DivideOutput{}, ErrDivideByZero
			}
			return nil, DivideOutput{Quotient: in.A / in.B}, nil
		})
}
```

## Lesson 11.4: CallStats

**CONCEPT:** reading structured output on the client

```
The client has no idea what Go type the server used. res.StructuredContent arrives as decoded
JSON: a map[string]any for an object, with every number a float64. To get a typed value back,
round-trip it through JSON:
  raw, err := json.Marshal(res.StructuredContent)
  var out StatsOutput
  err = json.Unmarshal(raw, &out)
Check the failure cases first: err from CallTool, then res.IsError (the message is in
res.Content), then a missing StructuredContent (a server with no outputSchema may send only text).
```

**FROM PYTHON/JS:** the same as JSON.parse giving you a plain object you then validate or cast;
json.Unmarshal into a struct is the cast, with the json tags picking the fields.

**TASK:** Call the "stats" tool with numbers and decode its structured output into a StatsOutput.
Return an error if the call fails, if res.IsError is set (include the result's text in your
error), or if there's no StructuredContent.

**Worked example:**

```go
func CallStats(ctx context.Context, cs *mcp.ClientSession, numbers []float64) (StatsOutput, error) {
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "stats",
		Arguments: StatsInput{Numbers: numbers},
	})
	if err != nil {
		return StatsOutput{}, err
	}
	if res.IsError {
		var msg string
		if len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcp.TextContent); ok {
				msg = tc.Text
			}
		}
		return StatsOutput{}, fmt.Errorf("stats tool failed: %s", msg)
	}
	if res.StructuredContent == nil {
		return StatsOutput{}, errors.New("stats returned no structured content")
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return StatsOutput{}, err
	}
	var out StatsOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return StatsOutput{}, fmt.Errorf("decoding stats output: %w", err)
	}
	return out, nil
}
```

## Lesson 11.5: AddDeleteNoteTool (bonus)

**CONCEPT:** tool annotations

```
&mcp.Tool{Name: "...", Annotations: &mcp.ToolAnnotations{...}} describes HOW a tool behaves, so
a host can decide e.g. to ask the user before running it:
  ReadOnlyHint     bool    true: only reads                         (default false)
  DestructiveHint  *bool   true: may delete/overwrite (if not read-only)  (default TRUE)
  IdempotentHint   bool    true: calling twice = calling once       (default false)
  OpenWorldHint    *bool   true: talks to the outside world (web, APIs)   (default TRUE)
  Title            string  a human-friendly name for UIs
The two hints that default to true are *bool, so "unset" (nil) differs from an explicit false.
Get a *bool with new(true) (Go 1.26+), or jsonschema.Ptr(true) / a tiny ptr helper.
They are HINTS: a client must not trust them from a server it doesn't trust.
```

**TASK:** Register a tool named "delete_note" (non-empty Description) on s, taking DeleteInput and
replying with the text `"deleted <id>"`. Its annotations: Title "Delete note", not read-only,
destructive (explicitly true), idempotent, and NOT open-world (explicitly false).

**Worked example:**

```go
type DeleteInput struct {
	ID string `json:"id" jsonschema:"the id of the note to delete"`
}

func AddDeleteNoteTool(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_note",
		Description: "Delete a note by id",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Delete note",
			ReadOnlyHint:    false,
			DestructiveHint: new(true),
			IdempotentHint:  true,
			OpenWorldHint:   new(false),
		},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in DeleteInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "deleted " + in.ID}}}, nil, nil
	})
}
```

## Lesson 11.6: AddRawEchoTool (bonus)

**CONCEPT:** the low-level server.AddTool

```
s.AddTool(&mcp.Tool{Name: "...", InputSchema: schema}, handler)   // a METHOD, not generic
handler is an mcp.ToolHandler:
  func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error)
Nothing is automatic:
  - InputSchema is REQUIRED and must have "type": "object", else AddTool panics. Any value that
    marshals to JSON works: json.RawMessage(`{...}`), a map[string]any, or a *jsonschema.Schema.
  - arguments are NOT validated; req.Params.Arguments is the raw json.RawMessage to unmarshal
  - a Go error returned from a ToolHandler becomes a PROTOCOL error (CallTool's err), not
    IsError. For a tool error, build the result and call res.SetError(err), then return it
    with a nil error.
Use it when you need a schema the Go types can't express, or you proxy tools from elsewhere.
```

**TASK:** With s.AddTool, register a tool named "echo" (non-empty Description) whose hand-written input
schema is an object with one required property "text" of type "string" (give it a
"description"). The handler replies with the text unchanged. If the arguments don't unmarshal,
or "text" is missing, reply with a TOOL error (IsError), not a protocol error.

**HINT:** unmarshal into struct{ Text *string `json:"text"` } and check for nil to tell "missing" from "".

**Worked example:**

```go
func AddRawEchoTool(s *mcp.Server) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"text": {"type": "string", "description": "the text to echo back"}},
		"required": ["text"]
	}`)
	s.AddTool(&mcp.Tool{Name: "echo", Description: "Echo the text back", InputSchema: schema},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args struct {
				Text *string `json:"text"`
			}
			var res mcp.CallToolResult
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				res.SetError(fmt.Errorf("bad arguments: %w", err))
				return &res, nil
			}
			if args.Text == nil {
				res.SetError(errors.New(`missing required argument "text"`))
				return &res, nil
			}
			res.Content = []mcp.Content{&mcp.TextContent{Text: *args.Text}}
			return &res, nil
		})
}
```

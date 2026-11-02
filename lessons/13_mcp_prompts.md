# Module 13: MCP prompts and argument completion

## Primer: prompts

The three server features differ in WHO decides to use them:

```
TOOLS      the MODEL decides to call them
RESOURCES  the APP decides what to read and attach
PROMPTS    the USER picks them, typically from a menu or a slash command ("/code_review")
```

A prompt is a reusable conversation starter. The user picks one, fills in its arguments, and the
host asks the server to expand it into ready-made messages that start the chat.

Two requests, both answered by the SDK for you:

```
prompts/list  → []*mcp.Prompt         names, descriptions and argument metadata (for the menu)
prompts/get   → *mcp.GetPromptResult  your handler expands one prompt with the user's arguments
```

The types (package github.com/modelcontextprotocol/go-sdk/mcp):

```
&mcp.Prompt{
    Name:        "code_review",          // the id clients use; unique per server
    Title:       "Code review",          // optional human-friendly label for menus
    Description: "Ask for a review of a code snippet",
    Arguments: []*mcp.PromptArgument{
        {Name: "code", Description: "the code to review", Required: true},
    },
}
type PromptHandler func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error)
req.Params.Name       the prompt being expanded
req.Params.Arguments  map[string]string. Prompt arguments are ALWAYS strings, never typed structs.
```

Unlike mcp.AddTool there are no generics here: prompts have no input schema to infer, so
registration is a plain method: server.AddPrompt(prompt, handler).

Client side:

```
for p, err := range cs.Prompts(ctx, nil) { ... }       // or cs.ListPrompts(ctx, nil)
res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "code_review",
    Arguments: map[string]string{"code": "x := 1"}})
```

**FROM PYTHON/JS:** FastMCP's @mcp.prompt() turns a function's parameters into prompt arguments, and
the TS SDK's server.registerPrompt takes a zod argsSchema. In Go you write the []*PromptArgument
list yourself, and the handler reads the arguments out of a map.

**READ NEXT:** docs/server.md#prompts and docs/server.md#completion in the SDK repo
(github.com/modelcontextprotocol/go-sdk/tree/main/docs).

## Lesson 13.1: AddCodeReviewPrompt

**CONCEPT:** registering a prompt with required and optional arguments

```
s.AddPrompt(&mcp.Prompt{Name: ..., Arguments: []*mcp.PromptArgument{...}}, handler)
PromptArgument.Required is METADATA: it tells the client's UI which fields to ask for.
The handler returns the expanded conversation:
  return &mcp.GetPromptResult{
      Messages: []*mcp.PromptMessage{
          {Role: "user", Content: &mcp.TextContent{Text: "..."}},
      },
  }, nil
Content is the same mcp.Content interface tools return (module 10), so a message could also
carry an image or an embedded resource.
Reading an optional argument: a missing map key gives "", the zero value. Reading from a nil
map is fine too, so req.Params.Arguments["language"] never panics.
```

**FROM PYTHON/JS:** a missing key in a Python dict raises KeyError, and in JS gives undefined.
In Go it gives the zero value, so  if lang == "" { ... }  covers "not provided".

**TASK:** Register a prompt on s with
Name "code_review", Title "Code review", and a non-empty Description;
Arguments, in this order:

```
  "code"      Required: true    with a non-empty Description
  "language"  Required: false   with a non-empty Description
Its handler returns ONE message with Role "user" and this text:
  language given:   "Please review this <language> code:\n\n<code>"
  language empty:   "Please review this code:\n\n<code>"
(Exercise 13.2 handles a missing "code". You may ignore it here.)
```

**Worked example:**

```go
func AddCodeReviewPrompt(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "code_review",
		Title:       "Code review",
		Description: "Ask for a review of a code snippet",
		Arguments: []*mcp.PromptArgument{
			{Name: "code", Description: "the code to review", Required: true},
			{Name: "language", Description: "the programming language, e.g. Go"},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		code := req.Params.Arguments["code"]
		intro := "Please review this code:"
		if lang := req.Params.Arguments["language"]; lang != "" {
			intro = "Please review this " + lang + " code:"
		}
		return &mcp.GetPromptResult{
			Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: intro + "\n\n" + code}},
			},
		}, nil
	})
}
```

## Lesson 13.2: CheckRequired and AddDebugPrompt

**CONCEPT:** multi-message prompts, and who enforces Required

```
1. A prompt can return a whole conversation. Each PromptMessage has a Role, "user" or
   "assistant", so you can pre-write the assistant's first reply to steer how the chat continues.
2. The SDK does NOT check Required for you. In v1.8.0, Server.getPrompt (mcp/server.go) looks up
   the prompt by name and calls your handler directly, so a client that omits a required
   argument reaches your handler anyway. Validate it yourself.
3. How to fail. An error returned from a handler travels back to the client as a JSON-RPC error.
   A plain fmt.Errorf arrives with error code 0, which means "no code". The MCP spec recommends
   "invalid params", code -32602, for a missing required argument. The SDK uses that code for an
   unknown prompt name too. To send it, return a *jsonrpc.Error (package
   github.com/modelcontextprotocol/go-sdk/jsonrpc):
     return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "..."}
   The client gets it back as an error you can inspect:
     var rpcErr *jsonrpc.Error
     if errors.As(err, &rpcErr) && rpcErr.Code == jsonrpc.CodeInvalidParams { ... }
```

TASK (a): CheckRequired returns nil if every argument of p marked Required has a non-empty value

```
in args. Otherwise it returns a *jsonrpc.Error with Code jsonrpc.CodeInvalidParams whose Message
names the FIRST missing required argument (in p.Arguments order), e.g.
`missing required argument "error"`. args may be nil.
```

TASK (b): Register a prompt on s with Name "debug_error", a non-empty Description, and Arguments

```
"error" (Required: true) and "goal" (Required: false). Its handler first calls CheckRequired and
returns that error if there is one. Otherwise it returns these messages, in order:
  1. Role "user":       "I hit this error:\n\n<error>"
  2. Role "assistant":  DebugAssistantReply
  3. Role "user":       "<goal>"    (ONLY when goal is non-empty)
```

**HINT:** the handler is a closure, so it can use the *mcp.Prompt variable you declared just above it.

**Worked example:**

```go
const DebugAssistantReply = "Happy to help. What were you trying to do when it happened?"

func CheckRequired(p *mcp.Prompt, args map[string]string) error {
	for _, arg := range p.Arguments {
		if arg.Required && args[arg.Name] == "" {
			return &jsonrpc.Error{
				Code:    jsonrpc.CodeInvalidParams,
				Message: fmt.Sprintf("missing required argument %q", arg.Name),
			}
		}
	}
	return nil
}

func AddDebugPrompt(s *mcp.Server) {
	prompt := &mcp.Prompt{
		Name:        "debug_error",
		Description: "Start a debugging conversation about an error message",
		Arguments: []*mcp.PromptArgument{
			{Name: "error", Description: "the error message", Required: true},
			{Name: "goal", Description: "what you were trying to do"},
		},
	}
	s.AddPrompt(prompt, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		args := req.Params.Arguments
		if err := CheckRequired(prompt, args); err != nil {
			return nil, err
		}
		msgs := []*mcp.PromptMessage{
			{Role: "user", Content: &mcp.TextContent{Text: "I hit this error:\n\n" + args["error"]}},
			{Role: "assistant", Content: &mcp.TextContent{Text: DebugAssistantReply}},
		}
		if goal := args["goal"]; goal != "" {
			msgs = append(msgs, &mcp.PromptMessage{Role: "user", Content: &mcp.TextContent{Text: goal}})
		}
		return &mcp.GetPromptResult{Messages: msgs}, nil
	})
}
```

## Lesson 13.3: CompleteLanguage (bonus)

**CONCEPT:** argument completion (server side)

```
While the user types a prompt argument, the client can ask the server for suggestions:
"completion/complete". You answer it with ONE handler for the whole server, set in the options:
  mcp.NewServer(impl, &mcp.ServerOptions{CompletionHandler: handler})
Setting it makes the server advertise the "completions" capability. Without it, the request
fails with "method not found".
  func(ctx context.Context, req *mcp.CompleteRequest) (*mcp.CompleteResult, error)
req.Params.Ref       which prompt (or resource) is being filled in:
                       Ref.Type "ref/prompt" with Ref.Name, or "ref/resource" with Ref.URI
req.Params.Argument  {Name, Value}: the argument being typed and what the user has typed so far
Reply with:
  &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{Values: [...], Total: n}}
Keep Values short (the MCP spec suggests at most 100) and set HasMore when you leave some out.
```

**TASK:** Complete the "language" argument of the "code_review" prompt: return every entry of
Languages that starts with Argument.Value, compared case-insensitively ("J" matches
"javascript"), in Languages order, with Total set to the number of matches. For any other ref or
argument, return an empty (non-nil) Values slice and no error.

**Worked example:**

```go
var Languages = []string{"go", "javascript", "python", "rust", "typescript"}

func CompleteLanguage(ctx context.Context, req *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
	values := []string{}
	ref, arg := req.Params.Ref, req.Params.Argument
	if ref.Type == "ref/prompt" && ref.Name == "code_review" && arg.Name == "language" {
		prefix := strings.ToLower(arg.Value)
		for _, lang := range Languages {
			if strings.HasPrefix(lang, prefix) {
				values = append(values, lang)
			}
		}
	}
	return &mcp.CompleteResult{
		Completion: mcp.CompletionResultDetails{Values: values, Total: len(values)},
	}, nil
}
```

## Lesson 13.4: SuggestLanguages (bonus)

**CONCEPT:** argument completion (client side)

```
res, err := cs.Complete(ctx, &mcp.CompleteParams{
    Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "some_prompt"},
    Argument: mcp.CompleteParamsArgument{Name: "some_arg", Value: "what the user typed"},
})
res.Completion.Values holds the suggestions.
Ref is a pointer and is required: the SDK rejects a request without it.
```

**TASK:** Ask the server for completions of the "language" argument of the "code_review" prompt,
given what the user has typed (prefix), and return the suggested values. Return any error.

**Worked example:**

```go
func SuggestLanguages(ctx context.Context, cs *mcp.ClientSession, prefix string) ([]string, error) {
	res, err := cs.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/prompt", Name: "code_review"},
		Argument: mcp.CompleteParamsArgument{Name: "language", Value: prefix},
	})
	if err != nil {
		return nil, err
	}
	return res.Completion.Values, nil
}
```

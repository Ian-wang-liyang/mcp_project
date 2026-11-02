# Milestone 3: Files and prompts

**Goal:** let the app attach workspace files to the chat as resources, and give the user a one-click "review this file" prompt.
**Time:** about 60 minutes. **Check:** `make check M=03`

## What you'll learn

- **Resources vs tools.** The model decides when to call a tool. The app or the user decides which resource to read and
  attach, for example through an "attach file" menu or an `@` mention. A file's contents is data, so it belongs in a resource.
- **Resource templates.** One handler serves a whole URI family. `{+path}` matches slashes and `{path}` doesn't.
- **Text vs binary contents.** `Text` must be UTF-8, so anything else goes in `Blob`.
- **Not-found errors.** The client can only tell "no such file" apart from "the server broke" if you return `mcp.ResourceNotFoundError`.
- **Prompts with arguments,** and an **embedded resource** inside a prompt message.
- **Bonus: argument completion.**

The lessons for this milestone:
- Resources: lessons 12.1 to 12.4 in [`lessons/12_mcp_resources.md`](../lessons/12_mcp_resources.md). They cover static resources,
  templates and not-found, `Blob`, and recognizing not-found on the client.
- Prompts: lessons 13.1 to 13.3 in [`lessons/13_mcp_prompts.md`](../lessons/13_mcp_prompts.md). They cover arguments, why `Required`
  is not enforced for you, and completion.

Read the primers at the top of those two files first if resources or prompts feel hazy.
The SDK's own guide is `docs/server.md` (sections Resources, Prompts, Completion).

From milestone 2 you only need the `workspace.resolve` path check, not the search tools.

## Requirements

The acceptance tests check exactly these, over the MCP protocol. In the table below, *path* means a slash-separated path
relative to the workspace root, like `internal/strutil/reverse.go`.

1. **Resource template** with URITemplate `workspace:///{+path}` and Name `"workspace-file"`.
   - Note the three slashes. The URI `workspace:///calc/calc.go` names the file `calc/calc.go`.
2. **Reading `workspace:///<path>`** returns exactly one `ResourceContents` whose `URI` is the URI that was read.
   - If the file is valid UTF-8, put the contents in `Text` and leave `Blob` empty. Pick `MIMEType` by name:

     | file | MIMEType |
     |---|---|
     | `*.go` | `text/x-go` |
     | `*.md` | `text/markdown` |
     | `go.mod`, `go.sum` | `text/plain` |
     | anything else | `mime.TypeByExtension(ext)`, or `text/plain` if that's empty |

   - Otherwise (binary), put the exact bytes in `Blob`, leave `Text` empty, and set `MIMEType` to `http.DetectContentType(data)`.
     For `assets/logo.png` that gives `image/png`.
3. **Not found.** All of these cases fail with `mcp.ResourceNotFoundError(uri)`, which is code `mcp.CodeResourceNotFound`:
   - a missing file
   - a directory, including `workspace:///` itself
   - anything that would read outside the root, however it is spelled:
     - `workspace:///../secret.txt` and `workspace:///calc/../../secret.txt`
     - `workspace:///..%2Fsecret.txt`
     - an absolute path such as `workspace:////etc/passwd`
     - a **symlink** inside the workspace that points outside it

   Treating "outside" the same as "missing" is deliberate: a client learns nothing about what exists beyond the root.
4. **Static resources.** `New` also registers `workspace:///go.mod` and `workspace:///README.md` with `s.AddResource`, each
   with a non-empty Name, **but only when the file exists**. This puts them in `resources/list`. Reading them gives the same
   result as reading through the template.
5. **Prompt `review_file`** with a Description and two arguments:
   - `path`, with `Required: true`
   - `focus`, with `Required: false`

   Getting it returns messages that all have Role `"user"`:
   - a `TextContent` asking for a code review. When `focus` is given, the text must contain it.
   - an `EmbeddedResource` whose `Resource` is the file's contents: `URI` `workspace:///<path>`, plus the same `Text`
     and `MIMEType` a resource read would give.
6. **review_file errors.**
   - A missing or empty `path` fails with a `*jsonrpc.Error` with Code `jsonrpc.CodeInvalidParams`, and its Message must
     name `path`.
   - A path that is missing, is a directory, or is outside the workspace fails with any error.
7. **Bonus (`TestBonusPathCompletion`): completion.** Set `ServerOptions.CompletionHandler` so the server advertises the
   `completions` capability. When the client completes the argument `path` of the prompt `review_file`
   (`Ref{Type: "ref/prompt", Name: "review_file"}`):
   - Return the workspace's regular files whose *path* starts with the typed value, sorted, at most 100.
   - Set `HasMore: true` when you dropped some.
   - Skip hidden entries (names starting with `.`, such as `.git`).
   - For any other prompt or argument, return an empty `Values` slice and no error.

## Suggested steps

1. **Template and text reads.** Register the template with a handler that does these steps:
   - cut `workspace:///` off `req.Params.URI`
   - check the path stays in the workspace (reuse `workspace.resolve` from milestone 2)
   - read the file
   - return `Text` with a MIME type
   `TestResourceTemplateListed` and `TestReadTextFiles` should pass.
2. **Binary.** Branch on `utf8.Valid`. `TestReadBinaryFile` should pass.
3. **Not found.** Turn every "can't read that" into `mcp.ResourceNotFoundError`. Then try to break out of the workspace
   until `TestReadOutsideWorkspace` and `TestReadSymlinkOutside` pass.
   - Put the read logic in one function that takes a path and returns `*mcp.ResourceContents`. Step 5 reuses it.
4. **Static resources.** In `New`, `os.Stat` each of the two files and call `s.AddResource` with the **same** handler.
5. **The prompt.** Use `s.AddPrompt`, validate `path`, read the file with your step-3 function, and return two messages.
6. **Bonus.** Walk the workspace with `filepath.WalkDir` and filter by prefix.

## Hints

<details><summary>Only top-level files can be read</summary>

You registered `workspace:///{path}`. A simple `{path}` expansion matches a single path segment, so
`workspace:///internal/strutil/reverse.go` never reaches your handler, and the SDK answers "Resource not found" for you.
`{+path}` is RFC 6570 "reserved expansion": it lets `/` through.
</details>

<details><summary>Getting the path out of the URI</summary>

The handler receives the whole URI, so use `rel, ok := strings.CutPrefix(req.Params.URI, "workspace:///")`.

Decoding `%XX` escapes with `url.PathUnescape` is optional. It's nice for file names with spaces, but if you do it, do it
**before** the safety check. Otherwise `..%2Fsecret.txt` passes the check as an odd file name and only becomes
`../secret.txt` after it.
</details>

<details><summary>The symlink test leaks the secret</summary>

Milestone 2's `resolve` checks the path lexically: it sees only `leak.txt`, which is a perfectly local name. (In milestone 2
the walks skipped symlinks, and `list_files` checked its `dir` with `filepath.EvalSymlinks`.) Here the escape happens
when the OS follows the link. `os.Root` (Go 1.24+; `ReadFile` since 1.25) does the safe thing for you, as milestone 2's
stretch goals mentioned:

```go
root, err := os.OpenRoot(ws.root)
if err != nil { ... }
defer root.Close()
info, err := root.Stat(filepath.FromSlash(rel)) // fails for "../x" and for links that leave the root
data, err := root.ReadFile(filepath.FromSlash(rel))
```

Links that stay inside the root still work.
</details>

<details><summary>The not-found tests say "code 0"</summary>

Any plain `error` you return (`fmt.Errorf`, or the `*fs.PathError` from `os.ReadFile`) reaches the client as a JSON-RPC
error with code 0. Map it yourself: `return nil, mcp.ResourceNotFoundError(req.Params.URI)`.

Reading a directory is the sneaky case: `os.ReadFile` on a directory returns an error ("is a directory"), not
`fs.ErrNotExist`. Check `info.Mode().IsRegular()` first.
</details>

<details><summary>How do I put a file inside a prompt message?</summary>

Each `PromptMessage` has exactly **one** `Content`. That means the instruction and the file are two messages, both from the user:

```go
Messages: []*mcp.PromptMessage{
	{Role: "user", Content: &mcp.TextContent{Text: instruction}},
	{Role: "user", Content: &mcp.EmbeddedResource{Resource: contents}}, // the *mcp.ResourceContents you already build
},
```
</details>

<details><summary>The missing-path test fails even though I return an error</summary>

The SDK does not enforce `PromptArgument.Required`, so your handler runs with `path == ""`. That empty path resolves to the
workspace root, a directory, and fails as "not found", which is the wrong reason. Check it first:

```go
return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: `missing required argument "path"`}
```

(`jsonrpc` is `github.com/modelcontextprotocol/go-sdk/jsonrpc`.)
</details>

<details><summary>Bonus: completion returns null, or never gets called</summary>

- There is one `CompletionHandler` for the whole server, set in `mcp.ServerOptions`. Inside it, check
  `req.Params.Ref.Type == "ref/prompt"`, `Ref.Name == "review_file"` and `req.Params.Argument.Name == "path"`.
  `req.Params.Argument.Value` is what the user has typed so far.
- Start from `Values: []string{}`. A nil slice marshals to JSON `null`, which clients reject.
- In `filepath.WalkDir`, return `filepath.SkipDir` for a hidden directory so you never descend into `.git`.
  Convert each path with `filepath.Rel` and then `filepath.ToSlash`.
</details>

## Try it for real

```sh
go build -o bin/devmcp ./cmd/devmcp
npx @modelcontextprotocol/inspector ./bin/devmcp -root .
```

In the Inspector:
- The **Resources** tab lists go.mod and README.md, and the template. Read `workspace:///server/server.go`, then try
  `workspace:///../go.mod`.
- The **Prompts** tab runs `review_file` and shows the embedded file.

In Claude Code, reconnect with `/mcp`. The exact syntax may vary between versions:
- MCP resources show up in `@` mentions, e.g. `@devmcp:workspace:///go.mod`.
- MCP prompts show up as slash commands, e.g. `/mcp__devmcp__review_file server/server.go "error handling"`.

Ask for a review and watch the file arrive as context, without Claude calling a tool.

## Stretch (optional, untested)

- **Subscriptions.** Set `SubscribeHandler` and `UnsubscribeHandler`, watch subscribed files (polling `os.Stat` mod times
  is fine), and call `s.ResourceUpdated` when one changes. See lesson 12.5 in [`lessons/12_mcp_resources.md`](../lessons/12_mcp_resources.md).
  A later milestone comes back to notifications.
- **Resource links from tools.** Have milestone 2's `search_code` return `&mcp.ResourceLink{URI: "workspace:///" + path, Name: path}`
  content next to its structured output. The client can then open a hit as a resource instead of asking for another tool call.
- **Template completion.** Also complete `{path}` of the template itself (`Ref{Type: "ref/resource", URI: "workspace:///{+path}"}`),
  so the Inspector's resource-template form autocompletes.

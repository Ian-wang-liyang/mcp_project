# Milestone 1: Hello, Claude Code

**Goal:** a real MCP server with one tool, running inside Claude Code by the end of the hour.
**Time:** about 45 minutes. **Check:** `make check M=01`

## What you'll learn

- How an MCP server is put together: `mcp.NewServer`, a typed tool with `mcp.AddTool`, and `server.Run`.
- What the handshake gives a client: `serverInfo`, capabilities, `instructions`.
- The stdio transport, and why **nothing but protocol may go to stdout**.

New to these ideas? Read the primer at the top of [`lessons/10_mcp_basics.md`](../lessons/10_mcp_basics.md) first (about 10 minutes).
For tool details, see lessons 11.1 and 11.2 in [`lessons/11_mcp_tools.md`](../lessons/11_mcp_tools.md).

## Requirements

The acceptance tests check exactly these, over the MCP protocol.

1. **`server.New(cfg)`** returns an `*mcp.Server`:
   - its `Implementation` has Name `"devmcp"` and any non-empty Version
   - it sets non-empty `ServerOptions.Instructions`: tell the model what the server is for and which tool to call first
2. **New validates `cfg.Root`.**
   - It returns an error if Root doesn't exist or isn't a directory.
   - An empty Root means the current directory.
3. **Tool `workspace_info`** takes no arguments and returns structured output with these JSON fields:

   | field | value |
   |---|---|
   | `root` | the workspace root as an **absolute** path with symlinks resolved |
   | `module` | the `module` line of `go.mod`, or `""` when there's no go.mod |
   | `go_version` | the `go` line of `go.mod` (e.g. `"1.26"`), or `""` |

   - It has a Description.
   - It is annotated `ReadOnlyHint: true`.
   - It has an output schema, which you get for free by returning a typed struct as `Out`.
4. **`cmd/devmcp/main.go`:**
   - a `-root` flag (default `"."`) that feeds `Config.Root`
   - serves over stdio with `mcp.StdioTransport`
   - logs only to **stderr**

## Suggested steps

1. Make `New` build a server with the right name and instructions and no tools. Run `make check M=01`; `TestServerIdentity` should pass.
2. Add a `workspace` type that holds the cleaned, absolute root. Build it in `New` and return its error.
   - You'll hang path-safety logic on this type in milestone 2, so give it its own file.
3. Add `workspace_info`.
   - Use a struct with `json:"..."` and `jsonschema:"..."` tags as the output type.
   - Use `struct{}` as the input type.
   - Parse go.mod line by line with `bufio.Scanner` and `strings.Fields`. No extra dependency is needed.
4. Write `main.go` and run the whole check.

## Hints

<details><summary>The tool handler's shape</summary>

```go
mcp.AddTool(s, &mcp.Tool{Name: "...", Description: "...", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
	func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, workspaceInfo, error) {
		return nil, info, nil // nil result: the SDK fills Content with the JSON of info
	})
```
</details>

<details><summary>The test says root is wrong, but it looks right</summary>

On macOS, temp dirs live under `/var/...`, which is a symlink to `/private/var/...`. Use `filepath.Abs` and then
`filepath.EvalSymlinks` so the root you report is the real path.
</details>

<details><summary>TestBinaryOverStdio can't connect</summary>

- Something is probably writing to stdout. `fmt.Println` and the default `log` setup both corrupt the JSON-RPC
  stream; use `log.SetOutput(os.Stderr)`.
- Also check that `main` calls `s.Run(ctx, &mcp.StdioTransport{})`, which blocks until the client hangs up.
- Use `signal.NotifyContext` so Ctrl-C shuts down cleanly.
</details>

## Try it for real

```sh
go build -o bin/devmcp ./cmd/devmcp
claude mcp add devmcp -- "$PWD/bin/devmcp" -root /path/to/any/go/repo
```

Start Claude Code, run `/mcp` to see devmcp connected, then ask: *"What Go version does this workspace use?"*.
Claude should call `workspace_info`.

Or look at everything the server offers in a browser with the MCP Inspector:

```sh
npx @modelcontextprotocol/inspector ./bin/devmcp -root .
```

After every rebuild, reconnect with `/mcp` in Claude Code, or restart the Inspector.

## Stretch (optional, untested)

- Add `go list -m all | wc -l` style info (the dependency count) by shelling out to `go`.
- Add a `-version` flag that prints the version and exits. Keep the version in one constant shared with `New`.

# devmcp: learn the MCP Go SDK by building a real server

You'll build **devmcp**, an MCP server that helps Claude Code work in a Go repository. It searches code, serves files,
runs tests with live progress, and finally ships over HTTP. You build it in milestones. Each one adds an MCP concept
to the same codebase, and at the end of each one you can use the server for real in Claude Code.

It uses the official SDK, [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)
(v1.8.0). You need basic Go and a rough idea of what MCP is. If MCP is new to you, read the primer at the top of
[`lessons/10_mcp_basics.md`](lessons/10_mcp_basics.md) (10 minutes) before milestone 1.

**Budget:** about 4.5–5.5 hours for milestones 1–5, plus about an hour for the optional milestone 6.

## How it works

Each milestone has three parts:

| | Where | What it is |
|---|---|---|
| **Brief** | `milestones/NN_*.md` | the goal, the concepts, the exact requirements, suggested steps, hints, and how to try it in Claude Code |
| **Acceptance tests** | `acceptance/mNN/` | black-box tests that talk to your server over MCP. Don't edit them. |
| **Checkpoint** | `_checkpoints/NN/` | one possible finished version of the code after milestone NN |
| **Lessons** | `lessons/NN_*.md` | short reference lessons on each SDK concept, each with a primer and worked examples. Briefs point to the ones they need. |

The loop: read the brief, write code in `server/` and `cmd/devmcp/`, run `make check M=NN` until it's green, try it in
Claude Code, then move on.

**You design the code.** The tests rely on only two things:

1. `server.New(server.Config) (*mcp.Server, error)` in `server/`. Briefs tell you which `Config` fields to add.
2. The `cmd/devmcp` binary and its flags.

Everything else (files, types, helpers) is up to you. The checkpoints show one way, not the way. One piece is given:
[`internal/gotest`](internal/gotest/gotest.go) runs `go test` and stops it cleanly, so milestone 4 can focus on MCP
rather than Unix process handling.

## Milestones

| # | Brief | You build | MCP concepts | Time |
|---|---|---|---|---|
| 1 | `01_hello.md` | `workspace_info` tool; stdio binary registered in Claude Code | server, tools, handshake, stdio | 45 min |
| 2 | `02_search.md` | `list_files`, `search_code`, with paths kept inside the workspace | input schemas, structured output, tool errors | 60 min |
| 3 | `03_files_prompts.md` | files as `workspace:///…` resources; a `review_file` prompt | resources, templates, blobs, prompts, completion | 60 min |
| 4 | `04_test_runner.md` | `run_tests` on `go test -json` with live progress and real cancellation | progress, logging, cancellation, timeouts | 50 min |
| 5 | `05_ship.md` | audit middleware, read-only mode, Streamable HTTP mode | middleware, HTTP transport, deployment | 60 min |
| 6 | `06_safe_writes.md` *(optional)* | `write_file` that asks the user before writing | elicitation, capability checks | 60 min |

Do them in order; each builds on the last. A milestone you haven't reached may show as "does not compile" in
`make progress` until you add the `Config` field its brief asks for. That's expected.

## Commands

```sh
make check M=01        # run one milestone's tests (verbose)
make check M=02 RUN=TestSearch   # just the tests matching a name
make progress          # one line per milestone
make build             # build bin/devmcp
make compare M=02      # diff your code against checkpoint 02
make checkpoint M=02   # back up your code to .backup/ and jump to checkpoint 02
```

## Trying it in a real host

```sh
make build
claude mcp add devmcp -- "$PWD/bin/devmcp" -root /path/to/a/go/repo   # then run /mcp inside Claude Code
npx @modelcontextprotocol/inspector ./bin/devmcp -root .              # a web UI for every tool, resource and prompt
```

After rebuilding, reconnect with `/mcp` in Claude Code, or restart the Inspector. Every brief ends with things to try.

## When you're stuck

1. Read the failure message. The tests say what they expected and usually hint at the cause.
2. Open the brief's **Hints**.
3. Re-read the matching lesson. Each brief links to the ones it needs in [`lessons/`](lessons/).
4. `make compare M=NN` shows how the checkpoint did it. If you just want to keep going, `make checkpoint M=NN` moves you
   there; your own code is saved in `.backup/`.

## References

- API: [pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp)
- SDK guides: [`docs/`](https://github.com/modelcontextprotocol/go-sdk/tree/main/docs) and [`examples/`](https://github.com/modelcontextprotocol/go-sdk/tree/main/examples)
- The protocol: [modelcontextprotocol.io/specification](https://modelcontextprotocol.io/specification)

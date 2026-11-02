# Milestone 6 (optional): Safe writes

**Goal:** a `write_file` tool that lets Claude create and change files in the workspace, but only after the **user**
says yes in a confirmation form. It falls back safely when the client can't ask.
**Time:** about 60 minutes. **Check:** `make check M=06`

> **Before you start:** add `AllowUnconfirmedWrites bool` to `server.Config`. The m06 tests set it, so they won't
> compile until it exists.

## What you'll learn

- **Elicitation:** a server asking the *user* a question (a small form) through the client, in the middle of a tool call.
- **Multi round-trip requests (MRTR).** On protocol 2026-07-28 your handler doesn't wait for the answer. It *returns*
  the question, and the SDK calls it again with the answer. So the handler runs twice, and that shapes how you write it.
- **Capability checks and graceful fallback.** Not every client can ask the user. Find out before you ask, and fail in
  a way the model can read and explain.
- **Destructive-tool annotations,** and what they can and can't do.
- **Path safety for writes:** the milestone 3 rules again, now with parent directories that may not exist yet.

The lessons for this milestone:
- Elicitation and MRTR: the primer of [`lessons/07_mcp_server_calls.md`](../lessons/07_mcp_server_calls.md) (the "MULTI
  ROUND-TRIP REQUESTS" part), then lesson 7.4 (the "are you sure?" pattern) and lesson 7.5 (checking capabilities first).
  The m06 tests run every case on both 2025-11-25 and 2026-07-28, so your handler must work in both eras.
- Annotations: lesson 3.5 in [`lessons/03_mcp_tools.md`](../lessons/03_mcp_tools.md).
- `os.Root`: your milestone 3 code, and its "symlink" hint.

The SDK's own guide is `docs/server.md#multi-round-trip-requests` (and `docs/client.md#elicitation` for the other side).

### Why ask the user, when Claude Code already asks?

Claude Code can ask before it runs an MCP tool, but users can allow a tool for good with "don't ask again". After
that the model can overwrite any file in the repo, and the server can't tell whether a human ever looked. With
elicitation the **server** decides when to ask, and it decides what the user sees: the exact path and size of this
write, every time.

Be clear about what that does and doesn't protect against. The client shows the question and sends back the answer.
A buggy or malicious *client* could answer "yes" itself. On 2026-07-28 it could even send an answer with the very first
request. So elicitation protects the user from a confused **model**, not from a hostile **client**. Path checks are
different: those you enforce on the server, every time, whatever the client says.

### What the two protocol versions look like

Your handler is written once, in the MRTR style from module 7, and works with both:

```
2026-07-28 (newest)                               2025-11-25 (older clients)
client ── tools/call write_file ─▶ handler        client ── tools/call write_file ─▶ SDK shim ─▶ handler
       ◀── result: InputRequests{"confirm"}               ◀── elicitation/create ─────── shim (asks for you)
  (client asks the user)                                  ── answer ─────────────────▶ shim
client ── tools/call + InputResponses ─▶ handler                                       shim ─▶ handler again
       ◀── result: written true/false                     ◀── result: written true/false
```

Either way the handler runs **twice**: round 1 without `req.Params.InputResponses`, round 2 with the answer. Round 1
must not change anything.

This shows up in your milestone 5 audit log. On 2026-07-28 you'll see **two** lines per `write_file` call, because
your middleware sees both rounds. On 2025-11-25 you'll see one, because the SDK's shim sits *inside* your middleware
and re-runs the handler there.

## Requirements

The acceptance tests check exactly these, over the MCP protocol, on protocol versions 2025-11-25 and 2026-07-28.

1. **Tool `write_file`** has a Description, an output schema, and two **required** string inputs, `path` and
   `content`. Its annotations:

   | annotation | value |
   |---|---|
   | `ReadOnlyHint` | `false` |
   | `DestructiveHint` | `true`, set explicitly (`new(true)`): it can overwrite a file |
   | `IdempotentHint` | `true`: writing the same content twice changes nothing more |

2. **Paths** follow milestone 3's rules. `path` is slash-separated and relative to the workspace root. The tool
   refuses all of these with an `IsError` result, **before asking the user anything**:
   - an empty path
   - anything outside the root: `../x`, `calc/../../x`, an absolute path
   - a symlink inside the workspace that leads outside it, either as the file itself or as a parent directory
   - an existing directory

   Missing parent directories are created, inside the workspace.
3. **Confirmation.** If the client supports elicitation, ask **once** per call before writing, with:
   - a `Message` that contains the path and the size of `content` in bytes as a plain number, e.g.
     `"Create docs/todo.md with 42 bytes?"`
   - a `RequestedSchema` that is an object with a **required boolean** property `confirm`
4. **Only** an answer with Action `"accept"` **and** `confirm == true` writes. The result's structured output is:

   | field | value |
   |---|---|
   | `written` | `true` |
   | `path` | the path, relative to the root, e.g. `"docs/notes/todo.md"` |
   | `bytes` | `len(content)`: bytes, not runes |

5. **Anything else** (`"decline"`, `"cancel"`, or `"accept"` with `confirm` false or missing) writes nothing and creates no
   directories. That is **not an error**: return a normal result with `written: false`, the same `path` and `bytes`,
   and a text block that contains the word `declined`, so the model knows to stop rather than retry.
6. **Nothing irreversible happens in round 1.** While the user is being asked, the target file and any new parent
   directory must not exist yet.
7. **A client without elicitation** gets an `IsError` result whose text says the client can't confirm writes, and
   nothing is written. **Not** a protocol error: the model should read it and tell the user.
   - Exception: with **`Config.AllowUnconfirmedWrites`**, such a client gets the write without a question
     (`written: true`). A client that *can* ask is still asked: the flag is only a fallback.
8. **`Config.ReadOnly`** leaves `write_file` unregistered, like `run_tests`.
9. **Flag** `-allow-unconfirmed-writes` sets `Config.AllowUnconfirmedWrites`.

## Suggested steps

1. **Tool and path checks.** Register `write_file` (not when `ReadOnly`) with the annotations. Validate the path with
   `workspace.resolve` and `os.Root`. For now, write right away. `TestWriteFileTool`, `TestWriteFileReadOnly` and
   `TestWriteFileUnsafePaths` should pass. Watch the symlink cases.
2. **Ask.** Return `InputRequests` when there's no answer yet, and act on the answer when there is.
   `TestWriteFileConfirmed`, `TestWriteFileOverwrite` and `TestWriteFileNotConfirmed` should pass on both versions.
3. **Fallback.** Run `TestWriteFileWithoutElicitation` before adding the capability check, and read the error. That
   protocol error is what the model would get. Then check the capability and return a readable error result instead.
4. **The escape hatch.** Add `AllowUnconfirmedWrites` and the flag. `TestWriteFileAllowUnconfirmed` and
   `TestBinaryAllowUnconfirmedWritesFlag` should pass.

## Hints

<details><summary>The handler's shape</summary>

```go
func(ctx context.Context, req *mcp.CallToolRequest, in writeInput) (*mcp.CallToolResult, writeOutput, error) {
	// 1. validate the path (error → IsError result; nobody is asked)
	// 2. no elicitation capability? → error result, or write if AllowUnconfirmedWrites
	resp, ok := req.Params.InputResponses["confirm"]
	if !ok { // round 1: only ask
		return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{
			"confirm": &mcp.ElicitParams{Message: ..., RequestedSchema: ...},
		}}, writeOutput{}, nil
	}
	answer, ok := resp.(*mcp.ElicitResult) // round 2: the answer, under the same key
	// 3. accept + confirm == true → write; anything else → written: false
}
```

- A typed `Out` is fine here. When the result carries `InputRequests`, the SDK skips filling in structured content, so
  round 1 doesn't trip the "both content and inputRequests" check. Just don't set `Content` yourself in round 1.
- For the decline result, return your own `Content` (the text with "declined") **and** the typed `Out` value. The SDK
  keeps your text and still fills in `StructuredContent`.
- `RequestedSchema` is typed `any`. A `map[string]any` shaped like JSON Schema works:
  `{"type": "object", "properties": {"confirm": {"type": "boolean"}}, "required": ["confirm"]}`.
- `confirmed, _ := answer.Content["confirm"].(bool)` covers a missing or non-boolean value.
</details>

<details><summary>"cannot be sent while serving a request"</summary>

You called `req.Session.Elicit`. That's the pre-2026 API: it sends a request to the client and blocks until the answer
comes back. In SDK v1.8.0, on a 2026-07-28 session, it always fails while a request is being served. Return
`InputRequests` instead. The SDK turns that into an `elicitation/create` request for older clients, so the same code
works for both.
</details>

<details><summary>Checking for elicitation support</summary>

```go
p := req.Session.InitializeParams()
canAsk := p != nil && p.Capabilities != nil && p.Capabilities.Elicitation != nil
```

On 2025-11-25 this comes from the `initialize` handshake. On 2026-07-28 there is no handshake, and the SDK fills it in
from metadata that the client sends with each request. The check is the same either way.

Without the check, the SDK still tries to ask, the client refuses, and the whole `tools/call` fails with a protocol
error ("client does not support elicitation"). The model gets nothing it can explain to the user.
</details>

<details><summary>Validating a path that doesn't exist yet</summary>

`os.Root` does the hard part, even for files that don't exist:

```go
root, err := os.OpenRoot(ws.root)
...
info, err := root.Stat(local)
switch {
case err == nil && !info.Mode().IsRegular(): // a directory (or a device...): refuse
case err == nil:                             // an existing file: this is an overwrite
case errors.Is(err, fs.ErrNotExist):         // a new file: fine
default:                                     // "path escapes from parent": a symlink leads out, or a parent is a file
}
```

`root.Stat("escape/new.txt")` fails with "path escapes from parent" when `escape` is a symlink out of the root, even
though `new.txt` doesn't exist. Then, in round 2, write through the same root: `root.MkdirAll(dir, 0o755)` and
`root.WriteFile(local, data, 0o644)` (Go 1.25+). Those check again at write time, so a symlink swapped in between the
two rounds can't redirect the write.
</details>

<details><summary>TestWriteFileNotConfirmed: "newdir/ was created"</summary>

You created the parent directories before you had a "yes". Either you did it in round 1, or you did it before
checking the answer in round 2. A declined write should leave the workspace exactly as it was, so `MkdirAll` belongs
right next to the write.
</details>

<details><summary>Why is a declined write not an error?</summary>

`IsError` tells the model that the *tool* failed, and models often respond to that by retrying, maybe with a slightly
different path. Here the tool worked: it asked, and the user said no. A normal result that says "the user declined"
makes the model stop and ask the user what they want instead. Compare a client that can't ask at all. That **is** an
error, because the operation couldn't be done, and the model should explain why.
</details>

## Try it for real

Use a scratch repo, so you can `git diff` (or throw away) whatever gets written:

```sh
make build
cp -R acceptance/testdata/sampleproj /tmp/scratch && (cd /tmp/scratch && git init -q && git add -A && git commit -qm init)
npx @modelcontextprotocol/inspector ./bin/devmcp -root /tmp/scratch
```

The MCP Inspector supports elicitation. Call `write_file` from the **Tools** tab and a confirmation form pops up with
your message and a `confirm` checkbox. Try accept, decline and cancel, and check the results and `git status`. Then
try `../escape.txt`: an error comes back, and no form appears.

**Claude Code:** whether your version of Claude Code supports elicitation as a client hasn't been checked for this
course. Register devmcp as usual, ask Claude to *"add a CONTRIBUTING.md that explains how to run the tests"*, and see
what happens:
- If a confirmation dialog appears, answer it and watch the result.
- If Claude reports that the client can't confirm writes, you're seeing the fallback, and that's exactly what users
  of such a client will see. Restart devmcp with `-allow-unconfirmed-writes` to let it write anyway. Then Claude
  Code's own tool-permission prompt is the only check left, so keep `git` handy.

If you run devmcp with `-audit`, look at the lines for `write_file` and count the rounds. Over milestone 5's
(stateful) HTTP, every client is on 2025-11-25 or older, so you'll see one line per call.

## Stretch (optional, untested)

- **`apply_edit`**, the tool Claude really wants: `{path, old, new}` replaces exactly one occurrence of `old` with
  `new`. Refuse it with an error if `old` appears zero times or more than once. Put a small unified diff in the
  confirmation message so the user sees what changes, not just a byte count. Keep the message short: hosts may
  truncate long ones.
- **Bind the answer to the request.** Use `CallToolResult.RequestState` (module 7's primer): in round 1, return a
  string that holds an HMAC of `path` and `content`. In round 2, refuse unless it matches. Then a retry with different
  content can't reuse an old "yes". It still doesn't stop a client that forges answers, so think about what it
  *does* buy you.
- **Protect what matters.** Refuse writes into `.git/`, or to `go.mod`, unless the user ticks an extra
  `allow_sensitive` box in the form.
- **Write atomically.** Write to a temp file in the same directory, then `root.Rename` it over the target, so a crash
  never leaves a half-written file.
- **Stateless HTTP.** With milestone 5's stretch `-stateless` flag, connect a 2025-11-25 client and call `write_file`.
  A stateless server can't send `elicitation/create`, and it doesn't even know the client's capabilities, so you get
  the fallback. A 2026-07-28 client gets the form. Explain why.

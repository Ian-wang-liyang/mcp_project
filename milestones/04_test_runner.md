# Milestone 4: Test runner

**Goal:** a `run_tests` tool that runs `go test`, reports each test's result, shows progress while it runs, and stops cleanly when Claude gives up or the time runs out.
**Time:** about 50 minutes. **Check:** `make check M=04`

> **Before you start:** add a `TestTimeout time.Duration` field to `server.Config`. The m04 tests set it, so they
> won't even compile until it exists.
>
> **Given to you:** [`internal/gotest`](../internal/gotest/gotest.go) starts `go` and stops it cleanly. You call
> `gotest.Run` and write everything around it.
>
> **Unix only.** The helper uses process groups (`Setpgid`, `kill -pgid`), which exist on macOS and Linux but
> not on Windows.

## What you'll learn

- How to build a **long-running tool**: report progress while it works, log what it's doing, and stop when the client cancels.
- **Progress notifications.** The client opts in with a progress token, and you echo that token back.
- **Logging** from a handler, and why the handler's `ctx` matters on protocol 2026-07-28.
- **Cancellation and timeouts.** A cancelled client request becomes a cancelled handler `ctx`, and that `ctx` has to stop a real process.
- Go practice along the way:
  - decoding `go test -json` events with `encoding/json`, one line at a time as they stream in
  - grouping output per test with maps and `strings.Builder`
  - `context.WithTimeout`, and telling your own timeout apart from the caller's cancel

Progress, logging and cancellation are lessons 14.2, 14.3 and 14.4 in [`lessons/14_mcp_client.md`](../lessons/14_mcp_client.md).
Read the "NOTE ON PROTOCOL VERSIONS" in that module's primer again, because it matters for logging.
For `context.WithTimeout`, `ctx.Done()` and `ctx.Err()`, see the primer of [`lessons/08_sync_context.md`](../lessons/08_sync_context.md).

### What `gotest.Run` does for you

```go
stderr, err := gotest.Run(ctx, dir, []string{"test", "-json", "-count=1", "./..."}, func(line []byte) {
	// called with each line of stdout as soon as it arrives
})
```

- **It streams.** Your callback sees each line of `go test -json` the moment `go` prints it. It runs on your
  goroutine, one line at a time, so it needs no locking.
- **It stops everything when `ctx` is done.** `go test` doesn't run your tests itself. It compiles a test binary per
  package and runs it as a **child process**:

  ```
  devmcp ─── go test -json ./... ─┬─ calc.test     ← your tests run here
                                  └─ broken.test
  ```

  Killing only `go` would leave the test binaries running, so `gotest.Run` puts `go` and its children in their own
  process group and kills the whole group. `TestRunTestsCancel` and `TestRunTestsTimeout` check for exactly this.
- **Its error** is `nil` (everything passed), an `*exec.ExitError` (go exited non-zero, or was killed because `ctx`
  is done), or anything else when `go` couldn't be started.

You pass the arguments yourself, because choosing them safely is part of the milestone (see requirement 2).

<details><summary>Curious why the process group matters? See it fail.</summary>

Temporarily replace the `gotest.Run` call with a plain `exec.CommandContext(tctx, "go", args...)` and
`cmd.Output()`, then run `make check M=04 RUN=TestRunTestsCancel`. The test reports that the test binary is still
running after `go` was killed. The fix is in [`internal/gotest/gotest.go`](../internal/gotest/gotest.go):
`Setpgid`, a `cmd.Cancel` that kills the negative PID (the whole group), and `cmd.WaitDelay` as a safety net.
Check for leftovers from your experiment with `pgrep -fl '\.test'`.
</details>

## Requirements

The acceptance tests check exactly these, over the MCP protocol.

1. **`Config.TestTimeout`** bounds one `run_tests` call. Zero means 2 minutes.
2. **Tool `run_tests`** has a Description and two optional string inputs:

   | input | meaning |
   |---|---|
   | `packages` | a go package pattern, default `"./..."`. A value starting with `-` is an error result: it would be read as a flag, and `-exec=anything` runs any program. |
   | `run` | a regexp passed to go test as `-run`. Empty means all tests. |

3. It runs `go test -json` in the workspace root, **without the test cache**: two calls in a row both really run the tests.
4. It returns **structured output** with at least these fields:

   | field | value |
   |---|---|
   | `passed` | `true` only if `go test` succeeded: everything compiled and no test failed |
   | `summary` | exactly `"<n> passed, <n> failed, <n> skipped"`, e.g. `"2 passed, 2 failed, 0 skipped"` |
   | `tests` | an array (never `null`), one entry per finished test, each `{package, name, status, output}`:<br>• `package`: the import path, e.g. `example.com/sampleproj/calc`<br>• `name`: e.g. `TestAdd`<br>• `status`: `"pass"`, `"fail"` or `"skip"`<br>• `output`: what the test printed, for **failed tests only**; empty for the others |
   | `output` | text that doesn't belong to any one test: compiler errors, failed packages' output, stderr |

   - Count only events for **tests**. The sample repo's `internal/strutil` has no tests, and go reports that package
     as `skip`. It must not appear in `tests` or `summary`.
   - **Failing tests are not a tool error.** They give a normal result with `passed: false`. So does a package that
     doesn't compile, with the compiler's message in `output`.
5. **Error results** (`IsError`):
   - an invalid `packages` value (see 2)
   - `go` can't be started, e.g. it isn't on `PATH`. `server.New` must still succeed.
   - the run took longer than `TestTimeout`. The message contains `"timed out"`.
6. **Progress:** if the request carries a progress token, send one progress notification after each test finishes:
   - `Progress` increases with every notification
   - `Message` contains the status in upper case and the test name, e.g. `"PASS example.com/sampleproj/calc TestAdd"`

   Without a token, send none.
7. **Logging:** when the run starts, log at level `"info"` with a message that contains the command (`go test ...`).
   Use `req.Session.Log` with the **handler's ctx**. This must work on protocol 2025-11-25 and 2026-07-28.
8. **Cancellation and timeout:** when the client cancels the call, or `TestTimeout` passes, the test binary that
   `go test` started must exit within 5 seconds.

## Suggested steps

1. Add `Config.TestTimeout`. Register a `run_tests` tool that validates `packages`, builds the arguments
   (`test -json -count=1`, `-run=...`, the package pattern) and calls `gotest.Run`. For a first version, have the
   callback append each line to a slice and parse them all afterwards. Get `TestRunTestsAll`,
   `TestRunTestsPackagesAndRun`, `TestRunTestsBuildFailure` and `TestRunTestsRejectsFlags` passing.
2. Parse each line **inside the callback** instead, so you know the moment each test finishes. Add progress
   notifications (`TestRunTestsProgress`) and the start log (`TestRunTestsLogs`).
3. Derive a `context.WithTimeout` from the handler's `ctx` and pass that to `gotest.Run`. Then tell a timeout apart
   from a client cancel, and turn the timeout into an error result (`TestRunTestsCancel`, `TestRunTestsTimeout`).

## Hints

<details><summary>What <code>go test -json</code> prints</summary>

One JSON object per line (`go doc cmd/test2json` has the details). Here are the lines that matter, trimmed:

```json
{"Action":"start","Package":"example.com/sampleproj/broken"}
{"Action":"run","Package":"example.com/sampleproj/broken","Test":"TestDouble"}
{"Action":"output","Package":"example.com/sampleproj/broken","Test":"TestDouble","Output":"    broken_test.go:7: Double(5) = 7, want 10\n"}
{"Action":"fail","Package":"example.com/sampleproj/broken","Test":"TestDouble","Elapsed":0}
{"Action":"output","Package":"example.com/sampleproj/broken","Output":"FAIL\n"}
{"Action":"fail","Package":"example.com/sampleproj/broken","Elapsed":0.438}
{"Action":"skip","Package":"example.com/sampleproj/internal/strutil","Elapsed":0}
{"ImportPath":"example.com/sampleproj/bad [example.com/sampleproj/bad.test]","Action":"build-output","Output":"bad/bad.go:2:23: cannot use \"x\" ...\n"}
```

- Events **with** `Test` are about one test. Events **without** it are about the whole package. That's why
  `strutil` shows up as `skip`: it has no test files.
- Packages run in parallel, so their lines interleave. Key test output by package and test name.
- A test's `output` events all arrive before its `pass`/`fail`/`skip` event, so buffer them until it finishes.
- Compiler errors arrive as `build-output` events, which have `ImportPath` but no `Package`.
- Decode into a small struct with only the fields you need. Keep any line that isn't JSON as plain output.
</details>

<details><summary>Why <code>-count=1</code>?</summary>

go caches passing test results. When nothing it tracks has changed, it replays the old output, marked `(cached)`,
without running anything. That's right for a human who just wants to know if things still pass. For Claude it's the
wrong signal. Claude reruns tests to check a fix, to see whether a failure is flaky, or after changing something
go doesn't track, such as a service the tests talk to. A replayed "ok" would mislead it. `-count=1` is the
documented way to bypass the cache. The cost is speed, and that's what the stretch idea below is for.
</details>

<details><summary>Timeout or cancel? And what to return</summary>

Derive `tctx, cancel := context.WithTimeout(ctx, timeout)` and pass `tctx` to `gotest.Run`. When it returns:

- `ctx.Err() != nil`: the **client** cancelled. Return `ctx.Err()`; nobody will read the result anyway.
- otherwise, `tctx.Err() != nil`: **your** timeout fired. Return an error that says `timed out`.
- otherwise, an `*exec.ExitError` just means go test exited non-zero (failing tests, build errors): `passed: false`.
- any other error means `go` couldn't run at all (e.g. not on `PATH`): an error result.

`go test` also has its own `-timeout` flag, which panics inside the test binary. That's useful, but it can't stop a
hung build or a `go` that's waiting on something, so your `ctx` has to be the real limit.
</details>

<details><summary>Progress and logging details</summary>

- `token := req.Params.GetProgressToken()`. It's `nil` when the client didn't ask for progress. Echo it unchanged.
- Once the client cancels, `NotifyProgress` starts failing. Ignore that error: progress is best-effort, and the
  run is about to stop anyway.
- On 2026-07-28 the client's log level travels in the request's `_meta`, and the SDK puts it in the **handler's
  ctx**. `req.Session.Log(context.Background(), ...)` can't see it, so it silently sends nothing. The
  `TestRunTestsLogs/2026-07-28` subtest catches that.
- Log **before** calling `gotest.Run`, so the log arrives even if the run fails at once.
</details>

<details><summary>TestRunTestsTimeout fails or takes far too long</summary>

Check which context you passed to `gotest.Run`. The handler's `ctx` only ends when the client cancels, so your
timeout never fires. Pass `tctx`, the one from `context.WithTimeout`. Keep using `ctx` for `Log` and
`NotifyProgress`: on 2026-07-28 it carries the client's settings.
</details>

## Try it for real

Rebuild, reconnect with `/mcp` in Claude Code, open a Go repo that has tests, and ask: *"Run the tests and fix
whatever fails."* Claude should call `run_tests`, read the failing tests' `output`, edit the code, and run the tests
again (now you see why the cache must be off).

To see cancellation work, add a test that runs `time.Sleep(time.Minute)` and ask Claude to run the tests. Press
**Esc** while it waits, then run `pgrep -fl '\.test'` in a terminal: nothing should be left. In the MCP Inspector you
can also watch the progress notifications and log messages arrive.

## Stretch (optional, untested)

- **An `explain_failure` prompt:** it takes a test name, runs just that test (`-run '^Name$'`), and puts the failure
  output and the test's source into a prompt message for the model.
- **A `race` option** that adds `-race`. Data races are among the failures Claude is worst at spotting by reading code.
- **Partial results on timeout:** return what finished before the deadline, as the error message or as structured
  output with a `timed_out` field, so Claude learns which package hangs.
- **Shut down gently.** `SIGKILL` gives `go` no chance to clean up, so every kill leaves a `go-build*` work
  directory in `$TMPDIR`. Change `cmd.Cancel` in `internal/gotest` to send `SIGINT` to the group first, then
  `SIGKILL` if it's still alive a few seconds later.
- **A cache option:** a `cached: true` input that drops `-count=1`, for quick "is it still green?" checks.
- **Limit the size of output:** cap each failure's `output` (keep the tail, where the panic usually is) so one noisy
  test can't flood the model's context.
- A `-test-timeout` flag in `main.go` that feeds `Config.TestTimeout`.

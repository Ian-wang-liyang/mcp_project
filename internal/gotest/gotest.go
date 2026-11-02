// Package gotest runs the go command for milestone 4's run_tests tool. It is given to you, so that milestone 4 is
// about MCP (progress, logging, cancellation) rather than Unix process management. Read it once; you don't edit it.
//
// This file is Unix-only: it uses process groups (Setpgid, kill -pgid) to stop go test cleanly.
package gotest

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// Run runs `go <args...>` in dir, e.g. args = []string{"test", "-json", "./..."}, and calls onLine with each
// non-empty line of its stdout as soon as that line arrives. onLine runs on the caller's goroutine, one line at a
// time, so it needs no locking. Keep it quick: go blocks while onLine runs.
//
// When ctx is done, Run kills go and every test binary it started, then returns.
//
// Run always waits for go to exit. It returns go's stderr and an error, which is one of:
//   - nil: go exited with status 0.
//   - an *exec.ExitError: go exited non-zero (a test failed, or a package didn't build), or was killed
//     because ctx is done. Check ctx.Err() to tell those apart.
//   - anything else: go couldn't be run at all, e.g. it isn't on PATH.
func Run(ctx context.Context, dir string, args []string, onLine func(line []byte)) (stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	// go test runs each compiled test binary as a CHILD process. Killing only `go` would orphan it,
	// so put go and its children in a new process group and kill the whole group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second // don't wait forever on a grandchild that escaped and holds our pipes
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	pr, pw := io.Pipe()
	cmd.Stdout = pw

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	waitErr := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close() // the read loop below sees EOF
		waitErr <- err
	}()

	rd := bufio.NewReader(pr) // not bufio.Scanner: its 64KB line limit breaks on long test output
	for {
		line, readErr := rd.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			onLine(line)
		}
		if readErr != nil {
			break
		}
	}
	err = <-waitErr
	return errBuf.Bytes(), err
}

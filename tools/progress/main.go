// Command progress runs the acceptance tests and prints one line per milestone.
// A skipped test counts as NOT done.
//
//	go run ./tools/progress           # acceptance/
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

type event struct {
	Action  string
	Package string
	Test    string
}

type module struct {
	passed, total int
	next          string // first test that isn't passing, in run order
	failed        bool   // package-level failure with no test to blame (e.g. compile error)
}

func main() {
	dir := "acceptance"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	cmd := exec.Command("go", "test", "-json", "-count=1", "./"+dir+"/...")
	out, err := cmd.StdoutPipe()
	if err != nil {
		fatal(err)
	}
	if err := cmd.Start(); err != nil {
		fatal(err)
	}

	mods := map[string]*module{}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Package == "" {
			continue
		}
		name := e.Package[strings.LastIndex(e.Package, "/")+1:]
		m := mods[name]
		if m == nil {
			m = &module{}
			mods[name] = m
		}
		if e.Test == "" { // package-level event
			m.failed = m.failed || e.Action == "fail"
			continue
		}
		if strings.Contains(e.Test, "/") { // subtests roll up into their parent
			continue
		}
		switch e.Action {
		case "pass":
			m.passed++
			m.total++
		case "fail", "skip":
			m.total++
			if m.next == "" {
				m.next = e.Test
			}
		}
	}
	_ = cmd.Wait() // a failing test run exits non-zero; that's expected

	names := make([]string, 0, len(mods))
	for n := range mods {
		names = append(names, n)
	}
	sort.Strings(names)

	var done, all int
	var upNext string
	for _, n := range names {
		m := mods[n]
		done, all = done+m.passed, all+m.total
		status := "  "
		note := ""
		switch {
		case m.total > 0 && m.passed == m.total:
			status = "✓ "
		case m.total == 0 && m.failed:
			note = "  does not compile yet: add the Config fields its brief asks for (details: go vet ./" + dir + "/" + n + "/)"
		case m.next != "":
			note = "  next: " + m.next
			if upNext == "" {
				upNext = n + " " + m.next
			}
		}
		if m.total == 0 && !m.failed {
			continue // helper packages with no tests
		}
		fmt.Printf("%s%-20s %d/%d%s\n", status, n, m.passed, m.total, note)
	}
	fmt.Printf("\n%d/%d tests passing\n", done, all)
	if upNext != "" {
		fmt.Println("up next:", upNext)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

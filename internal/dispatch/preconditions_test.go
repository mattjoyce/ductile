package dispatch

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// preconditions_test.go makes the fast suite's host-environment assumptions
// explicit instead of folklore (card #120). A skip is never a pass: each guard
// names the assumption it is protecting so a skip in CI output is self-explaining.

// requireScriptInterpreter guards tests that exec a script by its shebang. A
// slim CI image without bash or python3 would otherwise fail these tests with
// an exec error that reads like a dispatch bug. It skips, naming the missing
// interpreter, when the shebang's interpreter is absent; a script with no
// shebang is left to fail on its own terms.
func requireScriptInterpreter(t *testing.T, script string) {
	t.Helper()
	first, _, _ := strings.Cut(script, "\n")
	if !strings.HasPrefix(first, "#!") {
		return
	}
	fields := strings.Fields(strings.TrimPrefix(first, "#!"))
	if len(fields) == 0 {
		return
	}
	interp := fields[0]
	if strings.HasSuffix(interp, "/env") {
		interp = ""
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "-") {
				interp = f
				break
			}
		}
		if interp == "" {
			return
		}
		if _, err := exec.LookPath(interp); err != nil {
			t.Skipf("requires %s on PATH: the test execs a %q script (assumes the interpreter is installed)", interp, first)
		}
		return
	}
	if _, err := os.Stat(interp); err != nil {
		t.Skipf("requires %s: the test execs a %q script (assumes the interpreter is installed)", interp, first)
	}
}

func TestRequireScriptInterpreterSkipsOnlyWhenMissing(t *testing.T) {
	cases := []struct {
		name     string
		script   string
		wantSkip bool
	}{
		{"absolute present", "#!/bin/sh\necho ok\n", false},
		{"env present", "#!/usr/bin/env sh\necho ok\n", false},
		{"absolute missing", "#!/nonexistent/bash\necho ok\n", true},
		{"env missing", "#!/usr/bin/env ductile-no-such-interpreter\n", true},
		{"env flag then missing", "#!/usr/bin/env -S ductile-no-such-interpreter -u\n", true},
		{"no shebang", "echo ok\n", false},
	}
	for _, tc := range cases {
		var inner *testing.T
		t.Run(tc.name, func(st *testing.T) {
			inner = st
			requireScriptInterpreter(st, tc.script)
		})
		if inner.Skipped() != tc.wantSkip {
			t.Errorf("%s: skipped = %v, want %v", tc.name, inner.Skipped(), tc.wantSkip)
		}
	}
}

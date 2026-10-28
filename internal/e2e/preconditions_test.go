package e2e

import (
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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

// requireEntrypointInterpreter applies requireScriptInterpreter to a bundled
// plugin's entrypoint, read from the repo's plugins directory.
func requireEntrypointInterpreter(t *testing.T, root, plugin, entrypoint string) {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS(filepath.Join(root, "plugins")), path.Join(plugin, entrypoint))
	if err != nil {
		t.Fatalf("read %s entrypoint %s: %v", plugin, entrypoint, err)
	}
	requireScriptInterpreter(t, string(data))
}

// requireAnyCommand skips if none of the named commands are found on PATH.
// A skip is never a pass; the reason names the assumption.
func requireAnyCommand(t *testing.T, why string, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := exec.LookPath(name); err == nil {
			return
		}
	}
	t.Skipf("requires one of %s on PATH: %s (assumes a JSON parser is installed)", strings.Join(names, ", "), why)
}

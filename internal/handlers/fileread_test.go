// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/fileread_test.go
// Port of the upstream failure-lines vectors: a file read versus a run (#358).

package handlers

import (
	"fmt"
	"strings"
	"testing"
)

func TestFailureLinesFileReadVersusRun(t *testing.T) {
	// Source that mentions errors past the 25-line head, as a file read sees it.
	lines := make([]string, 0, 31)
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("const v%d = %d;", i, i))
	}
	lines = append(lines, `throw new ApiError("validation_error", "Validation failed.");`)
	out := bashObj(strings.Join(lines, "\n"), "")

	t.Run("recognises a pipeline that starts by reading a file", func(t *testing.T) {
		for _, command := range []string{
			"cat src/x.ts", "head -n 200 x.log", "tail -n 500 /var/log/syslog", "sed -n 1,400p x.ts",
			"cat x.ts | grep -v '^$'", "rtk read src/x.ts", "ssh host cat /etc/x",
			"cat x.log | grep -v DEBUG | sort | uniq -c | head -50", `cat x.log | grep -E "warn|fail"`,
		} {
			if !isFileRead(command) {
				t.Errorf("isFileRead(%q) = false", command)
			}
		}
	})
	t.Run("treats a command piped to head, and a file fed to a run, as runs", func(t *testing.T) {
		for _, command := range []string{
			"journalctl -b -1 | head -200", "cat <<'EOF' | ssh host bash", "concat a b", "python3 x.py",
			"cat q.sql | psql db", "cat urls | xargs -n1 curl -sS", "head -5 f | python3 x.py",
			"cat x.log | grep ERR | bash",
		} {
			if isFileRead(command) {
				t.Errorf("isFileRead(%q) = true", command)
			}
		}
	})
	t.Run("adds failure lines to a file fed to a run", func(t *testing.T) {
		if s := GetBashHandler(cmdInput(t, "cat q.sql | psql db"))("Bash", out).Summary; !strings.Contains(s, "Validation failed") {
			t.Errorf("summary = %q", s)
		}
	})
	t.Run("adds no failure lines to a file read", func(t *testing.T) {
		s := GetBashHandler(cmdInput(t, "cd /repo && cat src/x.ts"))("Bash", out).Summary
		if !strings.Contains(s, "+6 more lines") || strings.Contains(s, "Validation failed") {
			t.Errorf("summary = %q", s)
		}
	})
	t.Run("adds them to a command piped to head (control)", func(t *testing.T) {
		if s := GetBashHandler(cmdInput(t, "journalctl -b -1 | head -200"))("Bash", out).Summary; !strings.Contains(s, "Validation failed") {
			t.Errorf("summary = %q", s)
		}
	})
	t.Run("routes an rtk read to the content-read handler", func(t *testing.T) {
		if h := GetBashHandler(cmdInput(t, "rtk read f")); !samePtr(h, contentReadHandler) {
			t.Errorf("routed to %s", HandlerName(h))
		}
	})
}

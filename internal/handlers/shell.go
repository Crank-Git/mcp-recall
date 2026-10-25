// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/shell.go

package handlers

import (
	"fmt"
	"regexp"
	"strings"

	"mcprecall/internal/jsonx"
)

const (
	headStdout = 25
	headStderr = 20
)

// ansiRe covers colors, cursor movement, erase sequences, and other escapes.
var ansiRe = regexp.MustCompile(`[\x1b\x9b][\[()#;?]*(?:[0-9]{1,4}(?:;[0-9]{0,4})*)?[0-9A-ORZcf-nqry=><~]`)

// stripAnsi removes ANSI escape codes.
func stripAnsi(text string) string {
	return ansiRe.ReplaceAllString(text, "")
}

var sshNoiseRe = regexp.MustCompile(`^\*\* .+`)

// stripSshNoise removes SSH banner noise lines (starting with "** ") and
// collapses consecutive blank lines, then trims leading blank lines.
func stripSshNoise(text string) string {
	lines := strings.Split(text, "\n")
	var filtered []string
	for _, l := range lines {
		if !sshNoiseRe.MatchString(l) {
			filtered = append(filtered, l)
		}
	}
	var result []string
	prevBlank := false
	for _, line := range filtered {
		blank := strings.TrimSpace(line) == ""
		if blank && prevBlank {
			continue
		}
		result = append(result, line)
		prevBlank = blank
	}
	for len(result) > 0 && strings.TrimSpace(result[0]) == "" {
		result = result[1:]
	}
	return strings.Join(result, "\n")
}

func trimTrailingEmpty(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[:end]
}

// formatLines keeps the first max lines after collapsing repeats (upstream
// #340), so the budget goes to distinct lines, each clipped to MaxLineChars
// (#349), then any rtk recovery hints (#353) from the lines cut. The header
// and the overflow count input lines, not summary lines.
func formatLines(text string, max int) (header, body string) {
	lines := trimTrailingEmpty(strings.Split(text, "\n"))
	total := len(lines)
	shown := collapseRepeats(lines, max, MaxLineChars)
	hidden := total
	texts := make([]string, len(shown))
	for i, l := range shown {
		hidden -= l.count
		texts[i] = l.text
	}
	head := strings.Join(texts, "\n")
	overflow := ""
	if hidden > 0 {
		overflow = fmt.Sprintf("\n… (+%d more lines)", hidden)
	}
	// Collapsed like the head, so a repeated hint costs one line.
	var extra strings.Builder
	for _, h := range recoveryHints(collapsedTexts(lines[total-hidden:])) {
		extra.WriteString("\n" + clipLine(h, MaxLineChars))
	}
	plural := "s"
	if total == 1 {
		plural = ""
	}
	return fmt.Sprintf("%d line%s", total, plural), head + overflow + extra.String()
}

// parseStructured parses a {stdout, stderr, output, returncode, exit_code}
// object, returning nil if raw is not such a JSON object.
func parseStructured(raw string) *jsonx.Obj {
	parsed, err := jsonx.ParseString(raw)
	if err != nil {
		return nil
	}
	obj, ok := parsed.(*jsonx.Obj)
	if !ok {
		return nil
	}
	_, hasStdout := obj.Get("stdout")
	_, hasStderr := obj.Get("stderr")
	_, hasOutput := obj.Get("output")
	if hasStdout || hasStderr || hasOutput {
		return obj
	}
	return nil
}

func startsWithBraceOrBracket(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

// shellHandler strips ANSI/SSH noise and caps stdout/stderr line counts. Handles
// structured {stdout, stderr, returncode} JSON as well as plain string output.
func shellHandler(toolName string, output any) Result {
	raw := ExtractText(output)
	structured := parseStructured(raw)
	originalSize := byteLen(raw)

	if structured != nil {
		// stdout ?? output ?? "" — non-null stdout wins, else non-null output.
		var soStr string
		if v, ok := structured.Get("stdout"); ok && v != nil {
			soStr, _ = v.(string)
		} else if v, ok := structured.Get("output"); ok && v != nil {
			soStr, _ = v.(string)
		}
		stderrStr, _ := objStr(structured, "stderr")
		// Size the output itself, not the JSON envelope around it (upstream #306).
		originalSize = byteLen(joinNonEmpty(soStr, stderrStr))
		stdout := stripSshNoise(stripAnsi(soStr))
		stderr := stripSshNoise(stripAnsi(stderrStr))

		var exitStr string
		if rc, ok := objNum(structured, "returncode"); ok {
			exitStr = "exit:" + jsonx.Number(rc) + " · "
		} else if ec, ok := objNum(structured, "exit_code"); ok {
			exitStr = "exit:" + jsonx.Number(ec) + " · "
		}

		trimmedStdout := strings.TrimSpace(stdout)
		if startsWithBraceOrBracket(trimmedStdout) {
			if _, err := jsonx.ParseString(trimmedStdout); err == nil {
				return Result{Summary: jsonHandler(toolName, trimmedStdout).Summary, OriginalSize: originalSize}
			}
		}

		stdoutHeader, stdoutBody := formatLines(stdout, headStdout)
		hasStderr := strings.TrimSpace(stderr) != ""
		stderrDesc := ""
		var stderrHeader, stderrBody string
		if hasStderr {
			stderrHeader, stderrBody = formatLines(stderr, headStderr)
			stderrDesc = " · " + stderrHeader + " stderr"
		}
		header := fmt.Sprintf("[bash · %s%s stdout%s]", exitStr, stdoutHeader, stderrDesc)

		parts := []string{header}
		if strings.TrimSpace(stdout) != "" {
			parts = append(parts, stdoutBody)
		}
		if hasStderr {
			parts = append(parts, "stderr:")
			parts = append(parts, stderrBody)
		}
		return Result{Summary: strings.Join(parts, "\n"), OriginalSize: originalSize}
	}

	text := stripSshNoise(stripAnsi(raw))
	trimmedText := strings.TrimSpace(text)
	if startsWithBraceOrBracket(trimmedText) {
		if _, err := jsonx.ParseString(trimmedText); err == nil {
			return Result{Summary: jsonHandler(toolName, trimmedText).Summary, OriginalSize: originalSize}
		}
	}

	header, body := formatLines(text, headStdout)
	return Result{Summary: fmt.Sprintf("[bash · %s]\n%s", header, body), OriginalSize: originalSize}
}

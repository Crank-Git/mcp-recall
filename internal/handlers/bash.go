// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/bash.go

package handlers

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
)

// ── terraform plan ────────────────────────────────────────────────────────────

var (
	terraformResourceRe    = regexp.MustCompile(`^\s+#\s+(.+?)\s+will\s+be\s+(created|destroyed|updated in-place|replaced)`)
	terraformPlanSummaryRe = regexp.MustCompile(`(?m)^Plan:\s+.+$`)
	terraformSymbol        = map[string]string{
		"created":          "+",
		"destroyed":        "-",
		"updated in-place": "~",
		"replaced":         "-/+",
	}
)

func terraformPlanHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))

	summaryLine := terraformPlanSummaryRe.FindString(stdout)

	var resources []string
	for _, line := range strings.Split(stdout, "\n") {
		if m := terraformResourceRe.FindStringSubmatch(line); m != nil {
			symbol, ok := terraformSymbol[m[2]]
			if !ok {
				symbol = "?"
			}
			resources = append(resources, fmt.Sprintf("  %s %s", symbol, m[1]))
		}
	}

	if summaryLine == "" && len(resources) == 0 {
		return shellHandler(toolName, output)
	}

	lines := []string{"terraform plan"}
	if summaryLine != "" {
		lines = append(lines, "  "+summaryLine)
	}
	n := len(resources)
	if n > maxTerraformResource {
		n = maxTerraformResource
	}
	lines = append(lines, resources[:n]...)
	if len(resources) > maxTerraformResource {
		lines = append(lines, fmt.Sprintf("  … (+%d more resources)", len(resources)-maxTerraformResource))
	}
	return Result{Summary: strings.Join(lines, "\n"), OriginalSize: originalSize}
}

// ── package installers ────────────────────────────────────────────────────────

var (
	pkgWarnRe = regexp.MustCompile(`(?i)^(npm warn|warn |warning )`)
	pkgErrRe  = regexp.MustCompile(`(?i)^(npm error|error |err )`)
	bunPkgRe  = regexp.MustCompile(`(?i)(\d+)\s+packages?\s+installed`)
	npmPkgRe  = regexp.MustCompile(`(?i)added\s+(\d+)[^,\n]*`)
	pipPkgRe  = regexp.MustCompile(`Successfully installed (.+)`)
	yarnPkgRe = regexp.MustCompile(`(?i)success Saved (\d+) new dependenc`)
)

func packageInstallHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	stderr := extractStderr(output)
	combined := strings.TrimSpace(stdout + "\n" + stderr)
	originalSize := byteLen(BashOutputText(output))

	var warnings, errors []string
	for _, line := range strings.Split(combined, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if pkgWarnRe.MatchString(t) {
			warnings = append(warnings, firstChars(t, 100))
		} else if pkgErrRe.MatchString(t) {
			errors = append(errors, firstChars(t, 100))
		}
	}

	countLine, hasCount := "", false
	if m := bunPkgRe.FindStringSubmatch(combined); m != nil {
		countLine = m[1] + " packages installed"
		hasCount = true
	}
	if !hasCount {
		if m := npmPkgRe.FindString(combined); m != "" {
			countLine = firstChars(strings.TrimSpace(m), 60)
			hasCount = true
		}
	}
	if !hasCount {
		if m := pipPkgRe.FindStringSubmatch(combined); m != nil {
			pkgs := strings.Fields(strings.TrimSpace(m[1]))
			plural := "s"
			if len(pkgs) == 1 {
				plural = ""
			}
			countLine = fmt.Sprintf("pip: %d package%s installed", len(pkgs), plural)
			hasCount = true
		}
	}
	if !hasCount {
		if m := yarnPkgRe.FindStringSubmatch(combined); m != nil {
			countLine = "yarn: " + m[1] + " new dependencies saved"
			hasCount = true
		}
	}

	if !hasCount && len(errors) == 0 {
		return shellHandler(toolName, output)
	}

	first := countLine
	if !hasCount {
		first = "package install"
	}
	lines := []string{first}
	if len(warnings) > 0 {
		plural := "s"
		if len(warnings) == 1 {
			plural = ""
		}
		detail := ""
		if len(warnings) <= 3 {
			detail = ": " + strings.Join(warnings, "; ")
		}
		lines = append(lines, fmt.Sprintf("  %d warning%s%s", len(warnings), plural, detail))
	}
	if len(errors) > 0 {
		n := len(errors)
		if n > 5 {
			n = 5
		}
		for _, e := range errors[:n] {
			lines = append(lines, "  error: "+e)
		}
	}
	return Result{Summary: strings.Join(lines, "\n"), OriginalSize: originalSize}
}

// ── make / just build output ──────────────────────────────────────────────────

var (
	makeLineRe    = regexp.MustCompile(`^make(\[\d+\])?:\s`)
	justErrRe     = regexp.MustCompile(`(?i)^(error:|justfile|warning:)\s`)
	compilerErrRe = regexp.MustCompile(`(?i)^[^\s]+:\d+:\d*:?\s*(error|fatal error):`)
	errorBracket  = regexp.MustCompile(`^error\[`)
	caretOnlyRe   = regexp.MustCompile(`^\s*\^\s*$`)
)

func buildToolHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	stderr := extractStderr(output)
	combined := strings.TrimSpace(stdout + "\n" + stderr)
	originalSize := byteLen(BashOutputText(output))

	var errorLines, targetLines []string
	for _, line := range strings.Split(combined, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if makeLineRe.MatchString(t) {
			if strings.Contains(t, "Error") || strings.Contains(t, "***") {
				errorLines = append(errorLines, firstChars(t, 120))
			} else if strings.Contains(t, "Leaving directory") || strings.Contains(t, "Entering directory") {
				continue
			} else {
				targetLines = append(targetLines, firstChars(t, 80))
			}
			continue
		}
		if justErrRe.MatchString(t) {
			errorLines = append(errorLines, firstChars(t, 120))
			continue
		}
		if compilerErrRe.MatchString(t) || errorBracket.MatchString(t) || caretOnlyRe.MatchString(t) {
			errorLines = append(errorLines, firstChars(t, 120))
		}
	}

	// A recipe that runs tests (`just test`, `make check`) is summarised as a
	// test run, since the recipe error alone names no failing test (upstream
	// #325). Errors the test summary does not already show stay below it: a
	// later step can fail after the tests pass.
	if tests, ok := summarizeTestRun(output); ok {
		var extra []string
		for _, e := range errorLines {
			if !strings.Contains(tests.Summary, e) {
				extra = append(extra, e)
			}
		}
		if len(extra) == 0 {
			return tests
		}
		lines := []string{tests.Summary, fmt.Sprintf("  build errors (%d):", len(extra))}
		n := len(extra)
		if n > maxBuildErrors {
			n = maxBuildErrors
		}
		for _, e := range extra[:n] {
			lines = append(lines, "    "+e)
		}
		if len(extra) > maxBuildErrors {
			lines = append(lines, fmt.Sprintf("    … (+%d more)", len(extra)-maxBuildErrors))
		}
		return Result{Summary: strings.Join(lines, "\n"), OriginalSize: originalSize}
	}

	if len(errorLines) == 0 && len(targetLines) == 0 {
		return shellHandler(toolName, output)
	}

	// Determine overall result from the exit code if available. Use
	// extractExitCode so the JSON-string Bash payload is parsed (a bare property
	// read sees nothing).
	status := ""
	if ec, ok := extractExitCode(output); ok {
		if ec == 0 {
			status = "✓"
		} else {
			status = "✗"
		}
	}

	first := "build"
	if status != "" {
		first = status + " build"
	}
	lines := []string{first}
	if len(errorLines) > 0 {
		lines = append(lines, fmt.Sprintf("  errors (%d):", len(errorLines)))
		n := len(errorLines)
		if n > maxBuildErrors {
			n = maxBuildErrors
		}
		for _, e := range errorLines[:n] {
			lines = append(lines, "    "+e)
		}
		if len(errorLines) > maxBuildErrors {
			lines = append(lines, fmt.Sprintf("    … (+%d more)", len(errorLines)-maxBuildErrors))
		}
	} else {
		lines = append(lines, "  completed successfully")
	}
	return Result{Summary: strings.Join(lines, "\n"), OriginalSize: originalSize}
}

// ── gh CLI ────────────────────────────────────────────────────────────────────

var (
	ghListRe = regexp.MustCompile(`^#?\d+\t`)
	ghPassRe = regexp.MustCompile(`(?i)\tpass\b`)
	ghFailRe = regexp.MustCompile(`(?i)\tfail\b`)
	ghKvRe   = regexp.MustCompile(`(?i)^(title|state|author|labels|number|assignees|milestone):\t`)
)

func ghHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))

	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) <= 5 {
		return shellHandler(toolName, output)
	}

	var listLines []string
	for _, l := range lines {
		if ghListRe.MatchString(l) {
			listLines = append(listLines, l)
		}
	}
	if float64(len(listLines)) >= math.Ceil(float64(len(lines))*0.5) {
		n := len(listLines)
		if n > 10 {
			n = 10
		}
		var rows []string
		for _, l := range listLines[:n] {
			parts := strings.Split(l, "\t")
			if len(parts) > 3 {
				parts = parts[:3]
			}
			rows = append(rows, "  "+firstChars(strings.Join(parts, "  "), 100))
		}
		overflow := ""
		if len(listLines) > 10 {
			overflow = fmt.Sprintf("\n  … (+%d more)", len(listLines)-10)
		}
		plural := "s"
		if len(listLines) == 1 {
			plural = ""
		}
		return Result{
			Summary:      fmt.Sprintf("gh — %d item%s\n", len(listLines), plural) + strings.Join(rows, "\n") + overflow,
			OriginalSize: originalSize,
		}
	}

	passCount, failCount := 0, 0
	for _, l := range lines {
		if ghPassRe.MatchString(l) {
			passCount++
		}
		if ghFailRe.MatchString(l) {
			failCount++
		}
	}
	if float64(passCount+failCount) >= math.Ceil(float64(len(lines))*0.4) {
		var failLines []string
		for _, l := range lines {
			if ghFailRe.MatchString(l) {
				failLines = append(failLines, l)
			}
		}
		if len(failLines) > 5 {
			failLines = failLines[:5]
		}
		var out []string
		out = append(out, fmt.Sprintf("gh checks — %d pass, %d fail", passCount, failCount))
		for _, l := range failLines {
			out = append(out, "  fail: "+firstChars(strings.TrimSpace(strings.Split(l, "\t")[0]), 80))
		}
		return Result{Summary: strings.Join(out, "\n"), OriginalSize: originalSize}
	}

	var kvLines []string
	for _, l := range lines {
		if ghKvRe.MatchString(l) {
			kvLines = append(kvLines, l)
		}
	}
	if len(kvLines) >= 2 {
		n := len(kvLines)
		if n > 5 {
			n = 5
		}
		var meta []string
		for _, l := range kvLines[:n] {
			parts := strings.Split(l, "\t")
			key := parts[0]
			vals := parts[1:]
			key = strings.TrimSuffix(key, ":")
			meta = append(meta, fmt.Sprintf("  %s: %s", key, firstChars(strings.TrimSpace(strings.Join(vals, " ")), 100)))
		}
		return Result{Summary: "gh view\n" + strings.Join(meta, "\n"), OriginalSize: originalSize}
	}

	return shellHandler(toolName, output)
}

// ── dispatcher ────────────────────────────────────────────────────────────────

var (
	cmdGitDiff   = regexp.MustCompile(`^git\s+(diff|show)(\s|$)`)
	cmdGitLog    = regexp.MustCompile(`^git\s+log(\s|$)`)
	cmdGitStatus = regexp.MustCompile(`^git\s+status(\s|$)`)
	cmdTerraform = regexp.MustCompile(`^terraform\s+plan(\s|$)`)
	cmdPkgNode   = regexp.MustCompile(`^(npm|bun|yarn|pnpm)\s+install(\s|$)`)
	cmdPkgPip    = regexp.MustCompile(`^pip\d*\s+install(\s|$)`)
	cmdTestPy    = regexp.MustCompile(`^(pytest|python\s+-m\s+pytest)(\s|$)`)
	cmdTestJs    = regexp.MustCompile(`^(jest|npx\s+jest|bun\s+test|vitest|npx\s+vitest)(\s|$)`)
	cmdTestGo    = regexp.MustCompile(`^go\s+test(\s|$)`)
	cmdDocker1   = regexp.MustCompile(`^docker(-compose)?\s+(ps|compose\s+ps)(\s|$)`)
	cmdDocker2   = regexp.MustCompile(`^docker\s+compose\s+ps(\s|$)`)
	cmdBuild     = regexp.MustCompile(`^(make|just)(\s|$)`)
	cmdGh        = regexp.MustCompile(`^gh\s+`)

	cmdGitGrep      = regexp.MustCompile(`^git\s+grep(\s|$)`)
	cmdGitBranch    = regexp.MustCompile(`^git\s+branch(\s|$)`)
	cmdGitStashList = regexp.MustCompile(`^git\s+stash\s+list(\s|$)`)
	cmdGitRemote    = regexp.MustCompile(`^git\s+remote(\s|$)`)

	cmdCargoBuild = regexp.MustCompile(`^cargo\s+(build|check|clippy)(\s|$)`)
	cmdGoBuild    = regexp.MustCompile(`^go\s+(build|vet)(\s|$)`)
	cmdTsc        = regexp.MustCompile(`^(npx\s+)?tsc(\s|$)`)
	cmdEslint     = regexp.MustCompile(`^(npx\s+)?eslint(\s|$)`)
	cmdRuff       = regexp.MustCompile(`^ruff(\s|$)`)
	cmdRunScript  = regexp.MustCompile(`^(npm|pnpm|yarn|bun)\s+run\s+(typecheck|lint|build|check)(\s|$)`)

	cmdGrep = regexp.MustCompile(`^(grep|egrep|fgrep|rg|ag)(\s|$)`)
	cmdLs   = regexp.MustCompile(`^ls(\s|$)`)
	cmdFind = regexp.MustCompile(`^(find|fd)(\s|$)`)

	// A leading `cd <dir>` prefix up to and including its separator: `&&`, `;`,
	// or a bare newline — multi-line calls written as `cd <dir>` then the real
	// command are routine, and treating that as the command `cd` sent it to the
	// generic fallback (upstream #260). The dir may be double- or single-quoted
	// or contain backslash-escaped whitespace; an unquoted run stops at
	// whitespace or a shell operator so it can't swallow the separator. A bare
	// `cd <dir>` with no following command does not match.
	cdPrefixRe    = regexp.MustCompile(`(?s)^cd\s+(?:"[^"]*"|'[^']*'|(?:\\[\s\S]|[^\s\\&;|<>])+)[ \t]*(?:&&|;|\r?\n)\s*(.+)$`)
	gitGlobalOpts = regexp.MustCompile(`^git\s+(?:(?:--no-pager|--paginate|-P)\s+|-[cC]\s+\S+\s+)+`)
)

// assignPat is a `VAR=value` assignment. The value alternatives are disjoint (a
// quoted span, or a run with no whitespace or quote), as in cdPrefixRe.
const assignPat = `[A-Za-z_]\w*=(?:"[^"]*"|'[^']*'|[^\s"']*)`

// wrapperPrefixes are leading wrappers that run another command unchanged
// (upstream #302). Each is stripped only in a shape whose argument span is
// unambiguous. Upstream ends each with a lookahead for the start of a command
// word; RE2 has none, so each pattern consumes that one ASCII character
// ([A-Za-z_./~]) and the caller keeps it. A flag, operator or end-of-string
// there means the wrapper is not wrapping anything parseable (`env | grep`,
// `sudo -u www …`, `timeout 30`), so it is left in place.
var wrapperPrefixes = []*regexp.Regexp{
	regexp.MustCompile(`^timeout\s+(?:(?:-[sk]\s+\S+|--[a-z-]+(?:=\S+)?|-v)\s+)*\d+(?:\.\d+)?[smhd]?\s+[A-Za-z_./~]`),
	regexp.MustCompile(`^time(?:\s+-p)?\s+[A-Za-z_./~]`),
	regexp.MustCompile(`^nice(?:\s+(?:-n\s*-?\d+|--adjustment=-?\d+|-\d+))?\s+[A-Za-z_./~]`),
	regexp.MustCompile(`^env(?:\s+-i)?(?:\s+` + assignPat + `)*\s+[A-Za-z_./~]`),
	regexp.MustCompile(`^(?:` + assignPat + `\s+)+[A-Za-z_./~]`),
	regexp.MustCompile(`^(?:sudo|doas)\s+[A-Za-z_./~]`),
}

// quotedCommand returns the command a quoted or bare argument runs, as a
// shell would pass it on: one single-quoted span, one double-quoted span (a
// backslash before a double quote, backslash, backtick or $ unescaped), or
// the bare words as they are. A quoted span may be followed by a local
// pipeline or separator (`'cmd' | head`), which runs on this side but reads
// the same output, so it is kept. Anything else after the quote (an escaped
// quote, or 'a' b) is ambiguous: false.
func quotedCommand(arg string) (string, bool) {
	s := strings.TrimSpace(arg)
	var body, rest string
	switch {
	case strings.HasPrefix(s, "'"):
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", false
		}
		body, rest = s[1:1+end], s[2+end:]
	case strings.HasPrefix(s, `"`):
		m := doubleQuotedRe.FindStringSubmatch(s)
		if m == nil {
			return "", false
		}
		body = dqEscapeRe.ReplaceAllString(m[1], "$1")
		rest = s[len(m[0]):]
	default:
		return s, true
	}
	if strings.TrimSpace(rest) == "" {
		return strings.TrimSpace(body), true
	}
	if localOpRe.MatchString(rest) {
		return strings.TrimSpace(body) + " " + strings.TrimSpace(rest), true
	}
	return "", false
}

var (
	doubleQuotedRe = regexp.MustCompile(`^"((?:[^"\\]|\\[\s\S])*)"`)
	dqEscapeRe     = regexp.MustCompile("\\\\([\"\\\\`$])")
	localOpRe      = regexp.MustCompile(`^\s*[|;&<>]`)
	optionWordRe   = regexp.MustCompile(`^(-[^\s'"]*)(?:\s+|$)`)
	optionValueRe  = regexp.MustCompile(`^[^\s'"]+(?:\s+|$)`)
	// targetRe is a host, container or `user@host`: no leading dash, quote or
	// operator.
	targetRe = regexp.MustCompile(`^[A-Za-z0-9_][\w.@:-]*\s+`)
)

// skipOptions splits `-xyz` option clusters off the front of args. withValue
// lists the single-letter options that take a value (attached, `-p22`, or as
// the next word), flags those that take none, long the long options (`--name`
// or `--name=value`; true when they take a value). Returns what follows the
// options, or false on any option outside those lists or a quoted value.
func skipOptions(args, withValue, flags string, long map[string]bool) (string, bool) {
	rest := args
	for i := 0; i < 16; i++ {
		opt := optionWordRe.FindStringSubmatch(rest)
		if opt == nil {
			return rest, true
		}
		rest = rest[len(opt[0]):]
		word := opt[1]
		if word == "--" {
			return rest, true
		}
		needsValue := false
		if strings.HasPrefix(word, "--") {
			name, _, hasInline := strings.Cut(word[2:], "=")
			takesValue, known := long[name]
			if !known {
				return "", false
			}
			needsValue = takesValue && !hasInline
		} else {
			for j := 1; j < len(word); j++ {
				ch := word[j]
				if strings.IndexByte(withValue, ch) >= 0 {
					needsValue = j == len(word)-1 // otherwise the value is attached
					break
				}
				if strings.IndexByte(flags, ch) < 0 {
					return "", false
				}
			}
		}
		if needsValue {
			value := optionValueRe.FindString(rest)
			if value == "" {
				return "", false
			}
			rest = rest[len(value):]
		}
	}
	return "", false
}

// ssh(1) single-letter options. -N (no remote command) is in neither list, so
// a tunnel is never unwrapped.
const (
	sshWithValue        = "BbcDEeFIiJLlmOoPpQRSWw"
	sshFlags            = "46AaCfGgKkMnqsTtVvXxYy"
	dockerExecWithValue = "euw"
	dockerExecFlags     = "dit"
)

var dockerExecLong = map[string]bool{
	"detach": false, "interactive": false, "tty": false, "privileged": false,
	"env": true, "env-file": true, "user": true, "workdir": true, "detach-keys": true,
}

var (
	rtkPrefixRe  = regexp.MustCompile(`^rtk\s+(?:(?:-v+|--verbose|--skip-env)\s+)*`)
	rtkOwnReadRe = regexp.MustCompile(`^(read|recall)(\s|$)`)
	sshPrefixRe  = regexp.MustCompile(`^ssh\s+`)
	pctExecRe    = regexp.MustCompile(`^pct\s+exec\s+\d+\s+(?:--\s+)?`)
	dockerExecRe = regexp.MustCompile(`^docker\s+exec\s+`)
	// Upstream ends with a lookahead for the opening quote; RE2 has none, so
	// the quote is matched and left in place by the caller.
	shellDashCRe = regexp.MustCompile(`^(?:ba)?sh\s+-[elx]*c[elx]*\s+['"]`)
)

// remoteWrapper is a command that runs another command elsewhere (upstream
// #339): `ssh host cmd`, `pct exec <id> -- cmd`, `docker exec ctr cmd`, and
// `bash -c 'cmd'`; or through rtk's proxy, `rtk git diff` (#353). unwrap
// returns the command run, or false when the shape is not unambiguous. label
// names the wrapper in the fingerprint; `bash -c` runs locally and has none.
type remoteWrapper struct {
	label  string
	unwrap func(c string) (string, bool)
}

// optionTarget unwraps `<verb> [options] <target> <command>` for ssh and
// docker exec.
func optionTarget(prefix *regexp.Regexp, withValue, flags string, long map[string]bool) func(string) (string, bool) {
	return func(c string) (string, bool) {
		loc := prefix.FindStringIndex(c)
		if loc == nil {
			return "", false
		}
		args, ok := skipOptions(c[loc[1]:], withValue, flags, long)
		if !ok {
			return "", false
		}
		target := targetRe.FindString(args)
		if target == "" {
			return "", false
		}
		return quotedCommand(args[len(target):])
	}
}

var remoteWrappers = []remoteWrapper{
	// rtk's own `read` and `recall` run no other command, so they stay as they
	// are: `rtk read f` fingerprints as `rtk read` and is a content read.
	{"rtk", func(c string) (string, bool) {
		loc := rtkPrefixRe.FindStringIndex(c)
		if loc == nil {
			return "", false
		}
		inner := c[loc[1]:]
		if rtkOwnReadRe.MatchString(inner) {
			return "", false
		}
		return inner, true
	}},
	{"ssh", optionTarget(sshPrefixRe, sshWithValue, sshFlags, nil)},
	{"pct exec", func(c string) (string, bool) {
		loc := pctExecRe.FindStringIndex(c)
		if loc == nil {
			return "", false
		}
		return quotedCommand(c[loc[1]:])
	}},
	{"docker exec", optionTarget(dockerExecRe, dockerExecWithValue, dockerExecFlags, dockerExecLong)},
	{"", func(c string) (string, bool) {
		loc := shellDashCRe.FindStringIndex(c)
		if loc == nil {
			return "", false
		}
		return quotedCommand(c[loc[1]-1:]) // keep the opening quote
	}},
}

// unwrapCommand is NormalizeCommand, plus the labelled wrappers it stepped
// through, outermost first.
func unwrapCommand(command string) (string, []string) {
	c := strings.TrimSpace(command)
	var wrappers []string
	// Bounded: chained prefixes are rare, and a bound keeps a pathological
	// input from looping. Stops as soon as no prefix matches.
	for i := 0; i < 8; i++ {
		if m := cdPrefixRe.FindStringSubmatch(c); m != nil {
			c = strings.TrimSpace(m[1])
			continue
		}
		stripped := false
		for _, re := range wrapperPrefixes {
			if loc := re.FindStringIndex(c); loc != nil {
				c = c[loc[1]-1:] // keep the command word's first character
				stripped = true
				break
			}
		}
		if stripped {
			continue
		}
		unwrapped := false
		for _, w := range remoteWrappers {
			if inner, ok := w.unwrap(c); ok && inner != "" {
				if w.label != "" {
					wrappers = append(wrappers, w.label)
				}
				c = inner
				unwrapped = true
				break
			}
		}
		if !unwrapped {
			break
		}
	}
	return gitGlobalOpts.ReplaceAllString(c, "git "), wrappers
}

// NormalizeCommand normalises a Bash command so routing sees the real
// subcommand: it unwraps leading `cd <dir>` prefixes (see cdPrefixRe for the
// separator and quoting shapes) and strips git global options (--no-pager,
// -C <path>, -c <k=v>, --paginate, -P) that would otherwise push `git diff`
// output to the generic shell fallback.
//
// It also strips leading wrappers that run the real command unchanged
// (`timeout 30`, `time`, `nice -n 10`, `env A=1`, `A=1`, `sudo`; see
// wrapperPrefixes), so routing, the fingerprint and retention all see the
// wrapped command (upstream #302).
//
// And it unwraps commands run elsewhere (`ssh host 'git status'`, `pct exec
// 108 -- journalctl`, `docker exec ctr ps`, `bash -c '…'`; see
// remoteWrappers), so they reach the handler for the command they run (#339).
// `rtk <cmd>` unwraps the same way for the fingerprint and retention, but not
// for routing (#353).
//
// Chained prefixes are unwrapped across mixed separators and wrappers (`cd /a
// && timeout 60 git diff`, `ssh h 'cd /x && git diff'`) up to 8 hops, as
// upstream bounds it.
func NormalizeCommand(command string) string {
	c, _ := unwrapCommand(command)
	return c
}

// BashFingerprint is the fingerprint the hook stores for a raw Bash command:
// CommandFingerprint of the normalised command, prefixed by any wrappers it
// ran through (`ssh git status`, `pct exec journalctl`, `rtk git diff`), so
// local, remote and rtk-run stay apart in analytics (upstream #339, #353).
// Wrapper labels are constants: no host, id or container name.
func BashFingerprint(command string) string {
	inner, wrappers := unwrapCommand(command)
	parts := wrappers
	if fp := CommandFingerprint(inner); fp != "" {
		parts = append(parts, fp)
	}
	return strings.Join(parts, " ")
}

// subcommandTools are dispatchers whose first token names a tool family, not the
// operation — the meaningful fingerprint is "verb subcommand" ("git diff").
// rtk is here for its own commands (`rtk read`), which are not unwrapped.
var subcommandTools = map[string]bool{
	"git": true, "cargo": true, "go": true, "npm": true, "pnpm": true,
	"yarn": true, "bun": true, "docker": true, "kubectl": true, "rtk": true,
}

// wrapperTools prefix a real command; skipped so the fingerprint names the
// wrapped operation ("sudo apt-get …" → "apt-get").
var wrapperTools = map[string]bool{"sudo": true, "doas": true, "time": true, "nice": true}

var (
	envAssignRe = regexp.MustCompile(`^(?:[A-Za-z_]\w*=\S*\s+)+`)
	// A bare token: letters then letters/digits/-/_, terminated by whitespace, a
	// shell operator (; & | < > ( )), or end-of-string. Upstream uses a
	// lookahead for the terminator; RE2 has none, so it is matched and only
	// group 1 is used. The terminator chars are outside the token charset, so
	// the captured token is the same.
	bareTokenRe = regexp.MustCompile(`^([a-zA-Z][\w-]*)(?:[\s;&|<>()]|$)`)
	// A wrapper word directly followed by another bare verb; the trailing
	// [a-zA-Z] stands in for upstream's lookahead and is not consumed.
	wrapperRe = regexp.MustCompile(`^([a-zA-Z][\w-]*)\s+[a-zA-Z]`)
)

// CommandFingerprint derives a privacy-safe command "family" from a NORMALIZED
// command (see NormalizeCommand) for per-command savings attribution (upstream
// #251). It returns the leading bare token, plus the second when the first is a
// subcommand dispatcher. Extraction stops at the first flag, path, quote, `=`,
// pipe or redirection, so an argument or secret can never enter it. Leading
// VAR=value assignments and wrappers (sudo/doas/time/nice, when directly
// followed by a bare verb) are skipped and discarded; a flagged wrapper
// (`sudo -u www …`) keeps the wrapper name. Returns "" when there is no bare
// leading token (subshell, ./script); callers store that as unknown.
func CommandFingerprint(command string) string {
	c := envAssignRe.ReplaceAllString(strings.TrimSpace(command), "")
	for i := 0; i < 4; i++ {
		m := wrapperRe.FindStringSubmatchIndex(c)
		if m == nil || !wrapperTools[c[m[2]:m[3]]] {
			break
		}
		c = c[m[1]-1:]
	}
	first := bareTokenRe.FindStringSubmatch(c)
	if first == nil {
		return ""
	}
	verb := first[1]
	if !subcommandTools[verb] {
		return verb
	}
	second := bareTokenRe.FindStringSubmatch(strings.TrimLeft(c[len(verb):], " \t\n\v\f\r"))
	if second == nil {
		return verb
	}
	return verb + " " + second[1]
}

// ReadPassthroughMaxBytes is the size up to which a content read's output is
// delivered whole (upstream #327, #332).
const ReadPassthroughMaxBytes = 8 * 1024

// commandSplitRe splits a command on pipes and sequence operators.
var commandSplitRe = regexp.MustCompile(`\||;|&&|\n`)

// splitTopLevel splits command at every |, ||, ;, &&, & and newline outside
// quotes, escapes and ( … ), so a quoted remote command (`ssh h 'cat f |
// head'`) stays one segment until it is unwrapped. Every operator is ASCII,
// so the scan is byte-wise.
func splitTopLevel(command string) []string {
	var parts []string
	var quote byte
	depth, start := 0, 0
	for i := 0; i < len(command); i++ {
		ch := command[i]
		switch {
		case quote == '\'':
			if ch == '\'' {
				quote = 0
			}
		case ch == '\\':
			i++
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '\'' || ch == '"' || ch == '`':
			quote = ch
		case ch == '(':
			depth++
		case ch == ')':
			if depth > 0 {
				depth--
			}
		case depth == 0 && (ch == '|' || ch == ';' || ch == '&' || ch == '\n'):
			parts = append(parts, command[start:i])
			start = i + 1
		}
	}
	parts = append(parts, command[start:])
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// unwrappedSegments returns the quote-aware commands in command, each without
// its cd/wrapper prefixes. A segment that unwraps to a command line of its own
// (`ssh h 'cat f | head'`) is split again, a bounded number of times.
func unwrappedSegments(command string, depth int) []string {
	var out []string
	for _, part := range splitTopLevel(command) {
		c := NormalizeCommand(part)
		if depth < 3 && len(splitTopLevel(c)) > 1 {
			out = append(out, unwrappedSegments(c, depth+1)...)
		} else {
			out = append(out, c)
		}
	}
	return out
}

// commandSegments returns the commands in command, each without its
// cd/wrapper prefixes: the unwrappedSegments, plus a split that ignores
// quoting. The second finds a read no unwrap reaches (`for h in a b; do ssh
// $h '… | head'`, a multi-range `sed -n '1,5p;9,12p'`). Missing a read
// summarizes lines Claude asked for; a false one (`echo "x | head"`) delivers
// at most ReadPassthroughMaxBytes.
func commandSegments(command string) []string {
	parts := commandSplitRe.Split(command, -1)
	for i, p := range parts {
		parts[i] = NormalizeCommand(p)
	}
	return append(unwrappedSegments(command, 0), parts...)
}

// sedLine is a sed line address: a number, a shell expansion the shell turns
// into one (`$n`, `${n}`, `$((n+40))`), or a bare `$` for the last line. A
// command substitution `$(…)` is not matched: commandSegments splits on its
// pipes.
const sedLine = `(?:\d+|\$(?:\w+|\{\w+\}|\(\([^()]*\)\))|\$)`

var (
	// sedLineRangeRe matches `sed -n` printing one line or a line range (`N,M`,
	// `N,$`, `N,+M`).
	sedLineRangeRe = regexp.MustCompile(`^sed\s+(?:-\S+\s+)*-n\s+['"]?` + sedLine + `(?:,\s*\+?` + sedLine + `)?p['"]?(\s|$)`)
	headTailRe     = regexp.MustCompile(`^(head|tail)(\s|$)`)
	grepCmdRe      = regexp.MustCompile(`^(grep|egrep|fgrep|rg|ag|git\s+grep)\s`)
	grepLimitRe    = regexp.MustCompile(`\s(-[a-zA-Z]*[ABCm]\s*\d+|--(?:after-|before-)?context[=\s]\d+|--max-count[=\s]\d+)`)
	catDiffShowRe  = regexp.MustCompile(`^(cat|git\s+(diff|show))(\s|$)`)
	logPatchRe     = regexp.MustCompile(`^git\s+(log|stash\s+show)(\s.*)?\s(-p|--patch|-u)(\s|$)`)
	rtkReadRe      = regexp.MustCompile(`^rtk\s+(?:-\S+\s+)*(read|recall)(\s|$)`)
)

// IsBoundedRead reports whether any command in command limits its own output:
// head/tail, a `sed -n` line range, or grep context and match limits (-A/-B/-C
// N, -m N). That output is what Claude asked for, so a summary would drop
// requested lines (upstream #327).
func IsBoundedRead(command string) bool {
	for _, s := range commandSegments(command) {
		if headTailRe.MatchString(s) || sedLineRangeRe.MatchString(s) {
			return true
		}
		if grepCmdRe.MatchString(s) && grepLimitRe.MatchString(s) {
			return true
		}
	}
	return false
}

// IsContentRead reports whether command reads content for Claude: a bounded
// read, `cat`, `git diff`/`git show`, or a patch from `git log -p`/`git stash
// show -p`. Their summaries keep stats or a head and tail, which drops what
// Claude asked for, so the hook delivers small ones whole and balanced
// retention keeps the body of large ones (upstream #332). rtk's rewrite of a
// read, `rtk read f`, and `rtk recall <hash>` count too (#353).
func IsContentRead(command string) bool {
	if IsBoundedRead(command) {
		return true
	}
	for _, s := range commandSegments(command) {
		if catDiffShowRe.MatchString(s) || logPatchRe.MatchString(s) || rtkReadRe.MatchString(s) {
			return true
		}
	}
	return false
}

// IsCompoundCommand reports whether command runs more than one command in
// sequence: a `;`, `&&`, `||` or newline outside quotes, escapes and `( … )` /
// `$( … )`, with a command after it. A pipe is not a separator: it yields one
// output stream. Every operator is ASCII, so the scan is byte-wise.
func IsCompoundCommand(command string) bool {
	var quote byte
	depth := 0
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
			}
			continue
		}
		if ch == '\\' {
			i++
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch {
		case ch == '\'' || ch == '"' || ch == '`':
			quote = ch
		case ch == '(':
			depth++
		case ch == ')':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			width := 0
			if strings.HasPrefix(command[i:], "&&") || strings.HasPrefix(command[i:], "||") {
				width = 2
			} else if ch == ';' || ch == '\n' {
				width = 1
			}
			if width > 0 && strings.TrimSpace(command[i+width:]) != "" {
				return true
			}
		}
	}
	return false
}

// Compound-command windows: wide enough that a typical multi-command
// diagnostic (a few short sections) passes through whole, since every section
// is a separate answer and any one may be the one needed.
const (
	compoundHeadLines = 30
	compoundTailLines = 15
)

// compoundHandler summarises several commands' output (upstream #308): no
// single command's handler fits, and an `ls` handler on `ls …; git diff | wc
// -c` dropped the second result. Short output is returned whole bar its
// over-wide lines (upstream #349), so the hook usually passes it through
// unstored; long output keeps head, tail and the error lines between.
func compoundHandler(_ string, output any) Result {
	text := BashOutputText(output)
	originalSize := byteLen(text)
	lines := strings.Split(text, "\n")
	if len(lines) <= compoundHeadLines+compoundTailLines {
		for i, l := range lines {
			lines[i] = clipLine(l, MaxLineChars)
		}
		return Result{Summary: strings.Join(lines, "\n"), OriginalSize: originalSize}
	}
	window := summarizeLineWindow(lines, compoundHeadLines, compoundTailLines)
	return Result{Summary: fmt.Sprintf("[bash · compound command · %d lines]\n%s", len(lines), window), OriginalSize: originalSize}
}

// GetBashHandler returns the handler for a native Bash tool call based on the
// command string in tool_input. Falls back to the shell handler.
func GetBashHandler(input any) Handler {
	rawCommand := extractCommand(input)
	if rawCommand == "" {
		return shellHandler
	}
	command, wrappers := unwrapCommand(rawCommand)
	if IsCompoundCommand(command) {
		return compoundHandler
	}
	// rtk reshapes output (a diff without its headers), so a parser for the
	// raw command could read it wrong and claim a false count, the #350 class
	// (upstream #353).
	for _, w := range wrappers {
		if w == "rtk" {
			return shellHandler
		}
	}
	switch {
	case cmdGitGrep.MatchString(command):
		return grepHandler
	case cmdGitDiff.MatchString(command):
		return gitDiffHandler
	case cmdGitLog.MatchString(command):
		return gitLogHandler
	case cmdGitStatus.MatchString(command):
		return gitStatusHandler
	case cmdGitBranch.MatchString(command), cmdGitStashList.MatchString(command), cmdGitRemote.MatchString(command):
		return gitRefsHandler
	case cmdTerraform.MatchString(command):
		return terraformPlanHandler
	case cmdPkgNode.MatchString(command) || cmdPkgPip.MatchString(command):
		return packageInstallHandler
	case cmdTestPy.MatchString(command) || cmdTestJs.MatchString(command) || cmdTestGo.MatchString(command):
		return testRunnerHandler
	case cmdDocker1.MatchString(command) || cmdDocker2.MatchString(command):
		return dockerPsHandler
	case cmdBuild.MatchString(command):
		return buildToolHandler
	case cmdGh.MatchString(command):
		return ghHandler
	// Compiler / linter diagnostics (build, typecheck, lint). Safe to route
	// broadly: the handler falls back to shell when it finds no diagnostics.
	case cmdCargoBuild.MatchString(command), cmdGoBuild.MatchString(command),
		cmdTsc.MatchString(command), cmdEslint.MatchString(command),
		cmdRuff.MatchString(command), cmdRunScript.MatchString(command):
		return compilerDiagnosticsHandler
	// Search / listing — bulk is length, not structure. Handlers report the count
	// plus a capped sample and fall back to shell on an unexpected shape.
	case cmdGrep.MatchString(command):
		return grepHandler
	case cmdLs.MatchString(command):
		return lsHandler
	case cmdFind.MatchString(command):
		return findHandler
	}
	return shellHandler
}

// readsStderr holds the handlers that read stderr themselves. Test runners,
// compilers and make report there (`bun test` prints its results on stderr),
// so empty stdout is normal. Handlers are funcs, which Go cannot compare, so
// the set is keyed by code pointer.
var readsStderr = map[uintptr]bool{}

func init() {
	for _, h := range []Handler{
		shellHandler, compoundHandler, testRunnerHandler, buildToolHandler,
		compilerDiagnosticsHandler, packageInstallHandler,
	} {
		readsStderr[reflect.ValueOf(h).Pointer()] = true
	}
}

// keepStderrError returns the shell handler in place of handler when the
// command's stdout is empty and its stderr is not: it failed, though inside a
// pipeline it still exits 0. A handler that summarises stdout would report
// that as "no matches" or "no changes", while the shell handler keeps stderr
// (upstream #326). New handlers get this by default.
func keepStderrError(handler Handler, output any) Handler {
	if readsStderr[reflect.ValueOf(handler).Pointer()] {
		return handler
	}
	if strings.TrimSpace(extractStdout(output)) == "" && strings.TrimSpace(extractStderr(output)) != "" {
		return shellHandler
	}
	return handler
}

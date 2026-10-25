// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/handlers/bash_docker.go

package handlers

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	dockerWsRe     = regexp.MustCompile(`\s{2,}`)
	dockerStatusRe = regexp.MustCompile(`(?i)^(Up|Exited|Restarting|Created|Paused|Dead)`)
)

func findFirst(parts []string, pred func(string) bool) string {
	for _, p := range parts {
		if pred(p) {
			return p
		}
	}
	return ""
}

func dockerPsHandler(toolName string, output any) Result {
	stdout := extractStdout(output)
	originalSize := byteLen(BashOutputText(output))

	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return Result{Summary: "[docker ps — no containers]", OriginalSize: originalSize}
	}

	dataLines := lines
	if strings.Contains(strings.ToUpper(lines[0]), "CONTAINER") {
		dataLines = lines[1:]
	}
	if len(dataLines) == 0 {
		return Result{Summary: "[docker ps — no containers running]", OriginalSize: originalSize}
	}

	type container struct{ name, status, ports string }
	var containers []container

	for _, line := range dataLines {
		parts := dockerWsRe.Split(strings.TrimSpace(line), -1)
		if len(parts) < 2 {
			continue
		}
		name := parts[len(parts)-1]
		statusPart := findFirst(parts, func(p string) bool { return dockerStatusRe.MatchString(p) })
		portsPart := findFirst(parts, func(p string) bool { return strings.Contains(p, "->") || strings.Contains(p, "0.0.0.0:") })
		if name == "" {
			continue
		}
		containers = append(containers, container{
			name:   name,
			status: firstChars(statusPart, 20),
			ports:  firstChars(portsPart, 40),
		})
	}

	if len(containers) == 0 {
		return shellHandler(toolName, output)
	}

	n := len(containers)
	if n > maxDockerContainers {
		n = maxDockerContainers
	}
	var rows []string
	for _, c := range containers[:n] {
		rows = append(rows, fmt.Sprintf("  %s  %s  %s", padEnd(c.name, 30), padEnd(c.status, 22), c.ports))
	}
	overflow := ""
	if len(containers) > maxDockerContainers {
		overflow = fmt.Sprintf("\n  … (+%d more)", len(containers)-maxDockerContainers)
	}
	plural := "s"
	if len(containers) == 1 {
		plural = ""
	}
	header := fmt.Sprintf("docker ps — %d container%s", len(containers), plural)
	out := append([]string{header}, rows...)
	return Result{Summary: strings.Join(out, "\n") + overflow, OriginalSize: originalSize}
}

// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// cmd/bench/report.go
//
// Static, screenshot-friendly ANSI report. Dependency-free (raw escape codes)
// so it runs anywhere, including CI. The live TUI (tui.go) renders the same
// data interactively.

package main

import (
	"fmt"
	"strings"

	"mcprecall/internal/format"
)

const (
	cReset  = "\x1b[0m"
	cDim    = "\x1b[38;5;244m"
	cRed    = "\x1b[38;5;203m"
	cYellow = "\x1b[38;5;221m"
	cGreen  = "\x1b[38;5;42m"
	cBright = "\x1b[38;5;48m"
	cCyan   = "\x1b[38;5;51m"
	cBold   = "\x1b[1m"
	cGray   = "\x1b[38;5;240m"
)

// reductionColor maps a byte-reduction percentage to a color.
func reductionColor(pct float64) string {
	switch {
	case pct >= 85:
		return cBright
	case pct >= 60:
		return cGreen
	case pct >= 30:
		return cYellow
	default:
		return cRed
	}
}

// bar renders a fixed-width reduction bar colored by magnitude.
func bar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	filled := int(pct/100*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	return reductionColor(pct) + strings.Repeat("█", filled) + cGray + strings.Repeat("░", width-filled) + cReset
}

// statusCell returns a short colored status token for a result.
func statusCell(r Result) string {
	switch {
	case !r.OK():
		return cRed + "✗ FAIL" + cReset
	case r.SecretBlocked:
		return cCyan + "• blocked" + cReset
	case !r.Stored:
		return cDim + "— pass" + cReset
	default:
		return cGreen + "✓ ok" + cReset
	}
}

// RenderReport prints the full static report to stdout.
func RenderReport(s Summary) {
	fmt.Println()
	fmt.Printf("%s%s  mcp-recall compression benchmark %s\n", cBold, cCyan, cReset)
	fmt.Printf("%s  corpus: %d fixtures · tokens: %s%s\n\n", cDim, len(s.Results), s.TokenizerName, cReset)

	fmt.Printf("%s  %-11s %-22s %10s %10s  %-16s %8s  %5s   %s%s\n",
		cBold, "CATEGORY", "FIXTURE", "IN", "OUT", "REDUCTION", "TOK SAVE", "FACTS", "STATUS", cReset)
	fmt.Printf("%s  %s%s\n", cGray, strings.Repeat("─", 103), cReset)

	for _, r := range s.Results {
		red := r.ReductionPct()
		redStr := fmt.Sprintf("%s%5.1f%%%s", reductionColor(red), red, cReset)
		if r.SecretBlocked || !r.Stored {
			redStr = fmt.Sprintf("%s%5s %s", cDim, "—", cReset)
		}
		fmt.Printf("  %s%-11s%s %-22s %10s %10s  %s %s %+8s  %5s   %s\n",
			cDim, r.Category, cReset,
			truncate(r.Name, 22),
			format.Bytes(r.InputBytes),
			format.Bytes(r.OutputBytes),
			bar(red, 10), redStr,
			savedStr(r),
			r.FactsCell(),
			statusCell(r),
		)
	}

	fmt.Printf("%s  %s%s\n", cGray, strings.Repeat("─", 103), cReset)
	fmt.Printf("%s  FACTS: the fixture's listed facts still in the summary (— when it lists none).%s\n", cDim, cReset)
	renderFooter(s)
}

func savedStr(r Result) string {
	if r.SecretBlocked || !r.Stored {
		return cDim + "—" + cReset
	}
	return fmt.Sprintf("%d", r.TokensSaved())
}

func renderFooter(s Summary) {
	overall := s.OverallReductionPct()
	failColor := cGreen
	if s.Failures > 0 {
		failColor = cRed
	}
	fmt.Println()
	fmt.Printf("  %sStored%s        %d fixtures compressed\n", cBold, cReset, s.StoredCount)
	fmt.Printf("  %sPass-through%s  %d (below threshold / incompressible)\n", cBold, cReset, s.PassThroughCount)
	fmt.Printf("  %sSecrets%s       %s%d blocked%s (never stored)\n", cBold, cReset, cCyan, s.SecretBlocked, cReset)
	fmt.Printf("  %sIntegrity%s     %s%d round-trip failures%s\n", cBold, cReset, failColor, s.Failures, cReset)
	typical := s.TypicalReductionPct()
	fmt.Println()
	fmt.Printf("  %sTypical output%s  %s%.1f%% reduction%s  (real tool payloads, edge cases excluded)\n",
		cBold, cReset, reductionColor(typical), typical, cReset)
	fmt.Printf("  %sAll fixtures%s    %s → %s   (%s%.1f%% reduction%s)\n",
		cBold, cReset, format.Bytes(s.TotalInputBytes), format.Bytes(s.TotalOutputBytes),
		reductionColor(overall), overall, cReset)
	fmt.Printf("  %sTokens%s          %s → %s  (%s%.1f%% reduction%s)\n",
		cBold, cReset, groupInt(s.TotalInputTokens), groupInt(s.TotalOutTokens),
		reductionColor(s.TokenReductionPct()), s.TokenReductionPct(), cReset)
	fmt.Printf("  %sTokens saved%s    %s%s gross%s   ·   net ~%s @25%% retrieval   ·   ~%s @50%%\n",
		cBold, cReset, cBright, groupInt(s.GrossTokensSaved()), cReset,
		groupInt(s.NetTokensSaved(0.25)), groupInt(s.NetTokensSaved(0.50)))
	fmt.Printf("  %sThroughput%s      %s compressed · %.0f MB/s\n",
		cBold, cReset, format.Bytes(s.TotalInputBytes), s.ThroughputMBps())
	fmt.Println()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// groupInt formats an int with thousands separators.
func groupInt(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

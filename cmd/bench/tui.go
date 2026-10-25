// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// cmd/bench/tui.go
//
// Live Bubble Tea TUI: streams each fixture through the compression pipeline,
// animating a colored reduction bar per row, then shows a summary panel. Purely
// a presentation layer over the same engine the static report uses — the
// numbers are identical.

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"mcprecall/internal/format"
)

// palette (256-color, matches report.go).
var (
	stDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	stGray   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	stRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	stYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("221"))
	stGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	stBright = lipgloss.NewStyle().Foreground(lipgloss.Color("48"))
	stCyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))

	stTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")).
		Padding(0, 1)
	stHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	stBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).Padding(0, 2)
)

func styleForPct(pct float64) lipgloss.Style {
	switch {
	case pct >= 85:
		return stBright
	case pct >= 60:
		return stGreen
	case pct >= 30:
		return stYellow
	default:
		return stRed
	}
}

func tuiBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	filled := int(pct/100*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	return styleForPct(pct).Render(strings.Repeat("█", filled)) +
		stGray.Render(strings.Repeat("░", width-filled))
}

type resultMsg Result
type doneMsg struct{}

type model struct {
	fixtures []Fixture
	tok      Tokenizer
	idx      int
	results  []Result
	spinner  spinner.Model
	done     bool
	summary  Summary
}

func newModel(fixtures []Fixture, tok Tokenizer) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = stCyan
	return model{fixtures: fixtures, tok: tok, spinner: sp}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.step())
}

// step processes the next fixture after a short delay so the run is watchable.
func (m model) step() tea.Cmd {
	if m.idx >= len(m.fixtures) {
		return func() tea.Msg { return doneMsg{} }
	}
	f := m.fixtures[m.idx]
	tok := m.tok
	return tea.Tick(45*time.Millisecond, func(time.Time) tea.Msg {
		return resultMsg(Run([]Fixture{f}, tok)[0])
	})
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c", "enter":
			return m, tea.Quit
		}
	case resultMsg:
		m.results = append(m.results, Result(msg))
		m.idx++
		return m, m.step()
	case doneMsg:
		m.done = true
		m.summary = Summarize(m.results, m.tok.Name())
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(stTitle.Render("mcp-recall compression benchmark") + "\n")
	b.WriteString(stDim.Render(fmt.Sprintf(" corpus: %d fixtures · tokens: %s", len(m.fixtures), m.tok.Name())) + "\n\n")

	b.WriteString(stHeader.Render(fmt.Sprintf(" %-11s %-20s %9s %9s  %-12s %8s  %5s  %s", "CATEGORY", "FIXTURE", "IN", "OUT", "REDUCTION", "TOK", "FACTS", "")) + "\n")
	b.WriteString(stGray.Render(" "+strings.Repeat("─", 88)) + "\n")

	for _, r := range m.results {
		b.WriteString(renderRow(r) + "\n")
	}

	if !m.done {
		if m.idx < len(m.fixtures) {
			b.WriteString(" " + m.spinner.View() + stDim.Render(fmt.Sprintf(" %d/%d  %s", m.idx+1, len(m.fixtures), m.fixtures[m.idx].Name)) + "\n")
		}
		return b.String()
	}

	b.WriteString(stGray.Render(" "+strings.Repeat("─", 88)) + "\n\n")
	b.WriteString(renderSummaryBox(m.summary) + "\n")
	b.WriteString(stDim.Render(" press q to quit") + "\n")
	return b.String()
}

func renderRow(r Result) string {
	red := r.ReductionPct()
	var redCell, tokCell, status string
	switch {
	case r.SecretBlocked:
		redCell, tokCell = stDim.Render("     —"), stDim.Render("—")
		status = stCyan.Render("• blocked")
	case !r.Stored:
		redCell, tokCell = stDim.Render("     —"), stDim.Render("—")
		status = stDim.Render("— pass-through")
	default:
		redCell = styleForPct(red).Render(fmt.Sprintf("%5.1f%%", red))
		tokCell = fmt.Sprintf("%d", r.TokensSaved())
		if r.OK() {
			status = stGreen.Render("✓")
		} else {
			status = stRed.Render("✗ FAIL")
		}
	}
	return fmt.Sprintf(" %s %-20s %9s %9s  %s %s %8s  %5s  %s",
		stDim.Render(fmt.Sprintf("%-11s", r.Category)),
		truncate(r.Name, 20),
		format.Bytes(r.InputBytes),
		format.Bytes(r.OutputBytes),
		tuiBar(red, 10), redCell,
		tokCell, r.FactsCell(), status,
	)
}

func renderSummaryBox(s Summary) string {
	overall := s.OverallReductionPct()
	failStyle := stGreen
	if s.Failures > 0 {
		failStyle = stRed
	}
	lines := []string{
		fmt.Sprintf("%s  %d compressed   %s  %d pass-through   %s  %s   %s  %s",
			stHeader.Render("Stored"), s.StoredCount,
			stHeader.Render("·"), s.PassThroughCount,
			stHeader.Render("Secrets"), stCyan.Render(fmt.Sprintf("%d blocked", s.SecretBlocked)),
			stHeader.Render("Integrity"), failStyle.Render(fmt.Sprintf("%d failures", s.Failures))),
		"",
		fmt.Sprintf("%s  %s   (real tool payloads · edge cases excluded)      %s  %s → %s (%s)",
			stHeader.Render("Typical output"),
			styleForPct(s.TypicalReductionPct()).Render(fmt.Sprintf("%.1f%% reduction", s.TypicalReductionPct())),
			stHeader.Render("All"),
			format.Bytes(s.TotalInputBytes), format.Bytes(s.TotalOutputBytes),
			styleForPct(overall).Render(fmt.Sprintf("%.1f%%", overall))),
		fmt.Sprintf("%s  %s → %s  (%s)   ·   %s gross saved   ·   ~%s @50%% retrieval",
			stHeader.Render("Tokens"),
			groupInt(s.TotalInputTokens), groupInt(s.TotalOutTokens),
			styleForPct(s.TokenReductionPct()).Render(fmt.Sprintf("%.1f%% reduction", s.TokenReductionPct())),
			stBright.Render(groupInt(s.GrossTokensSaved())), groupInt(s.NetTokensSaved(0.50))),
		fmt.Sprintf("%s  %s compressed · %s",
			stHeader.Render("Throughput"),
			format.Bytes(s.TotalInputBytes),
			stCyan.Render(fmt.Sprintf("%.0f MB/s", s.ThroughputMBps()))),
	}
	return stBox.Render(strings.Join(lines, "\n"))
}

// RunTUI runs the interactive benchmark. Falls back to the static report if no
// interactive terminal is available.
func RunTUI(fixtures []Fixture, tok Tokenizer) error {
	p := tea.NewProgram(newModel(fixtures, tok))
	_, err := p.Run()
	return err
}

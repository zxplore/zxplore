// model.go — the bubbletea TUI. This is the F1 filesystem browser: a scrolling
// dataset list on the left, a live dossier (properties + snapshots) on the
// right. F2 transfer / F3 restore / F4 pools follow the same Model pattern.
//
// zexplore is a terminal UI in the spirit of k9s — rich, full-screen, keyboard
// first (mouse enabled) — not a native window. It runs on any ZFS system.
package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Styles ───────────────────────────────────────────────────────────────────
// Teal = the zexplore brand; green = the kldload badge; blue = focus/cursor.
var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#5fc4bc")).Padding(0, 1)
	badgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#4cb98a")).Padding(0, 1)
	hostStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#7cd0c9"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	paneStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	paneFocus   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#5ab0ff"))
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#5ab0ff"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

type model struct {
	host     Host
	datasets []Dataset
	cursor   int
	dossier  string
	width    int
	height   int
	err      string
	kldload  bool
}

func newModel() model {
	m := model{host: LocalHost(), kldload: IsKldload()}
	m.reload()
	return m
}

func (m *model) reload() {
	ds, err := ListDatasets(m.host)
	m.datasets = ds
	if err != nil {
		m.err = err.Error()
	} else {
		m.err = ""
	}
	if m.cursor >= len(m.datasets) {
		m.cursor = len(m.datasets) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.refreshDossier()
}

func (m *model) refreshDossier() {
	if m.cursor >= 0 && m.cursor < len(m.datasets) {
		m.dossier = Dossier(m.host, m.datasets[m.cursor].Name)
	} else {
		m.dossier = "(no datasets)"
	}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "down", "j":
			if m.cursor < len(m.datasets)-1 {
				m.cursor++
				m.refreshDossier()
			}
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.refreshDossier()
			}
		case "g", "home":
			m.cursor = 0
			m.refreshDossier()
		case "G", "end":
			if len(m.datasets) > 0 {
				m.cursor = len(m.datasets) - 1
				m.refreshDossier()
			}
		case "r":
			m.reload()
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.width == 0 {
		return "loading…"
	}

	// ── header ──
	title := titleStyle.Render("zexplore")
	host := hostStyle.Render("  " + m.host.Label())
	badge := ""
	if m.kldload {
		badge = "  " + badgeStyle.Render("kldload")
	}
	header := lipgloss.JoinHorizontal(lipgloss.Top, title, host, badge)

	// ── body: list | dossier ──
	bodyH := m.height - 4 // header(1) + footer(1) + pane top/bottom border(2)
	if bodyH < 3 {
		bodyH = 3
	}
	leftW := m.width * 45 / 100
	if leftW < 24 {
		leftW = 24
	}
	rightW := m.width - leftW - 4 // account for the two panes' borders
	if rightW < 12 {
		rightW = 12
	}
	left := paneFocus.Width(leftW).Height(bodyH).Render(m.renderList(leftW, bodyH))
	right := paneStyle.Width(rightW).Height(bodyH).Render(m.renderDossier(bodyH))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	// ── footer ──
	foot := footerStyle.Render(" ↑/↓ move   r reload   q quit     [F1 files]  F2 transfer  F3 restore  F4 pools  (coming)")

	return lipgloss.JoinVertical(lipgloss.Left, header, body, foot)
}

// renderList draws the (scrolling) dataset list into a w×h area.
func (m model) renderList(w, h int) string {
	if m.err != "" {
		return dimStyle.Render(truncate("error: "+m.err, w))
	}
	if len(m.datasets) == 0 {
		return dimStyle.Render("(no datasets)")
	}
	top := 0
	if m.cursor >= h {
		top = m.cursor - h + 1
	}
	end := top + h
	if end > len(m.datasets) {
		end = len(m.datasets)
	}
	var b strings.Builder
	for i := top; i < end; i++ {
		d := m.datasets[i]
		meta := fmt.Sprintf("%s/%s ×%d", d.Used, d.Refer, d.Snaps)
		nameW := w - lipgloss.Width(meta) - 1
		if nameW < 4 {
			nameW = 4
		}
		line := padRight(truncate(d.Name, nameW), nameW) + " " + meta
		line = truncate(line, w)
		if i == m.cursor {
			line = cursorStyle.Render(padRight(line, w))
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// renderDossier draws the detail pane, clipped to h lines (scroll comes later).
func (m model) renderDossier(h int) string {
	lines := strings.Split(m.dossier, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

// truncate clips s to n display columns, adding an ellipsis when it cuts.
func truncate(s string, n int) string {
	if n < 1 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	// byte-truncate is fine for ASCII dataset names; leave room for the ellipsis.
	for len(s) > 0 && lipgloss.Width(s)+1 > n {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// padRight pads s with spaces to n display columns.
func padRight(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

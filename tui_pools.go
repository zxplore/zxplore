// tui_pools.go — the pools view (F4): every imported pool, drill-down into
// the full dossier (vitals · zfs-vs-df space truths · vdev tree with error
// counters · iostat · ARC · events), and the maintenance verbs — scrub, trim,
// clear, import scan. Mutations gate on :rw upstream.
package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type poolsView struct {
	host     Host
	names    []string
	cursor   int
	status   string
	overview string // cached — PoolsOverview costs zpool calls, never per frame
}

func newPoolsView(h Host) *poolsView {
	p := &poolsView{host: h}
	p.reload()
	return p
}

func (p *poolsView) reload() {
	names, err := ListPools(p.host)
	p.names = names
	if p.cursor >= len(names) {
		p.cursor = len(names) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
	if err != nil {
		p.status = "✗ " + err.Error()
	} else {
		p.status = fmt.Sprintf("%d pools", len(names))
	}
	p.overview = strings.TrimRight(PoolsOverview(p.host), "\n")
}

func (p *poolsView) current() (string, bool) {
	if p.cursor >= 0 && p.cursor < len(p.names) {
		return p.names[p.cursor], true
	}
	return "", false
}

func (p *poolsView) view(width, height int) string {
	bodyH := height - 3
	if bodyH < 4 {
		bodyH = 4
	}
	var b strings.Builder
	b.WriteString(hostStyle.Render("ZPOOLS — "+p.host.Label()) + "\n\n")
	if len(p.names) == 0 {
		b.WriteString(dimStyle.Render("  (no pools imported — press i to scan for importable pools)\n"))
	}
	for i, n := range p.names {
		line := "  " + n
		if i == p.cursor {
			line = cursorStyle.Render(padRight(line, width-6))
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + dimStyle.Render(p.overview))
	body := paneFocus.Width(width - 2).Height(bodyH).Render(b.String())
	status := footerStyle.Render(" " + truncate(p.status, width-2))
	return lipgloss.JoinVertical(lipgloss.Left, body, status)
}

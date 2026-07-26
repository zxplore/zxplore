// transfer.go — the F2 dual-pane commander (Midnight Commander for ZFS).
//
// Two panes; Tab switches the active one. The ACTIVE pane's highlighted item is
// the SOURCE; the OTHER pane's current location is the DESTINATION. F5 replicates
// source → destination (creating the dataset there), local or remote, incremental
// when a common snapshot exists. Each pane can sit on a different host, so the
// same gesture does local→remote, remote→local, or local→local.
package main

import (
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type entryKind int

const (
	entryUp entryKind = iota
	entryDataset
	entrySnapshot
)

type entry struct {
	kind entryKind
	name string // full dataset/snapshot name; ".." for up
	meta string // used, for display
}

// pane is one side of the commander: a host + a current location (dataset path,
// "" = the host's pool root) + the listed entries and a cursor.
type pane struct {
	host     Host
	location string
	entries  []entry
	cursor   int
	err      string
}

func (p *pane) load() {
	p.err = ""
	var es []entry
	if p.location == "" {
		pools, err := ListPools(p.host)
		if err != nil {
			p.err = err.Error()
		}
		for _, pool := range pools {
			es = append(es, entry{kind: entryDataset, name: pool})
		}
	} else {
		es = append(es, entry{kind: entryUp, name: ".."})
		children, err := ListChildren(p.host, p.location)
		if err != nil {
			p.err = err.Error()
		}
		for _, d := range children {
			es = append(es, entry{kind: entryDataset, name: d.Name, meta: d.Used})
		}
		if snaps, err := ListSnapshots(p.host, p.location); err == nil {
			for _, s := range snaps {
				es = append(es, entry{kind: entrySnapshot, name: s.Name, meta: s.Used})
			}
		}
	}
	p.entries = es
	if p.cursor >= len(es) {
		p.cursor = len(es) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

func (p pane) current() (entry, bool) {
	if p.cursor >= 0 && p.cursor < len(p.entries) {
		return p.entries[p.cursor], true
	}
	return entry{}, false
}

func (p *pane) descend() {
	e, ok := p.current()
	if !ok {
		return
	}
	switch e.kind {
	case entryUp:
		if i := strings.LastIndexByte(p.location, '/'); i >= 0 {
			p.location = p.location[:i]
		} else {
			p.location = ""
		}
		p.cursor = 0
		p.load()
	case entryDataset:
		p.location = e.name
		p.cursor = 0
		p.load()
	}
}

func (p pane) title() string {
	loc := p.location
	if loc == "" {
		loc = "/"
	}
	return p.host.Label() + ":" + loc
}

// commander holds the two panes + which is active.
type commander struct {
	panes  [2]pane
	active int
	status string
}

func newCommander(h Host, startLoc string) commander {
	c := commander{}
	c.panes[0] = pane{host: h, location: startLoc}
	c.panes[1] = pane{host: h, location: startLoc}
	c.panes[0].load()
	c.panes[1].load()
	return c
}

// update handles a key in transfer mode; returns a tea.Cmd (the replication
// exec, or nil).
func (c *commander) update(msg tea.KeyMsg) tea.Cmd {
	a := &c.panes[c.active]
	switch msg.String() {
	case "tab", "left", "right", "h", "l":
		c.active = 1 - c.active
	case "down", "j":
		if a.cursor < len(a.entries)-1 {
			a.cursor++
		}
	case "up", "k":
		if a.cursor > 0 {
			a.cursor--
		}
	case "enter":
		a.descend()
	case "r":
		a.load()
	case "s":
		if e, ok := a.current(); ok && e.kind == entryDataset {
			name := "manual-" + time.Now().Format("20060102-150405")
			if _, err := SnapshotNow(a.host, e.name, name); err != nil {
				c.status = "snapshot failed: " + err.Error()
			} else {
				c.status = "snapshot: " + e.name + "@" + name
				a.load()
			}
		}
	case "f5", " ":
		return c.replicateCmd()
	}
	return nil
}

// replicateDoneMsg is delivered after an F5 replication finishes.
type replicateDoneMsg struct {
	err    error
	dstIdx int
	dst    string
}

func (c *commander) replicateCmd() tea.Cmd {
	src := &c.panes[c.active]
	dst := &c.panes[1-c.active]
	e, ok := src.current()
	if !ok || e.kind == entryUp {
		c.status = "select a dataset or snapshot to replicate"
		return nil
	}
	if dst.location == "" {
		c.status = "open a destination pool/dataset in the other pane first"
		return nil
	}

	// Resolve the source snapshot: a snapshot as-is, or a dataset's newest
	// snapshot (taking one if it has none).
	var srcSnap string
	if e.kind == entrySnapshot {
		srcSnap = e.name
	} else {
		snaps, _ := ListSnapshots(src.host, e.name)
		if len(snaps) == 0 {
			s, err := SnapshotNow(src.host, e.name, "zx-"+time.Now().Format("20060102-150405"))
			if err != nil {
				c.status = "snapshot failed: " + err.Error()
				return nil
			}
			srcSnap = s
		} else {
			srcSnap = snaps[len(snaps)-1].Name
		}
	}

	srcDs := srcSnap
	if i := strings.IndexByte(srcSnap, '@'); i >= 0 {
		srcDs = srcSnap[:i]
	}
	leaf := srcDs
	if i := strings.LastIndexByte(srcDs, '/'); i >= 0 {
		leaf = srcDs[i+1:]
	}
	dstPath := dst.location + "/" + leaf
	dstIdx := 1 - c.active

	pipeline := ReplicatePipeline(src.host, srcSnap, dst.host, dstPath)
	cmd := exec.Command("sh", "-c", pipeline)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return replicateDoneMsg{err: err, dstIdx: dstIdx, dst: dst.host.Label() + ":" + dstPath}
	})
}

func (c commander) view(width, height int) string {
	bodyH := height - 1
	if bodyH < 3 {
		bodyH = 3
	}
	paneW := (width - 4) / 2
	if paneW < 12 {
		paneW = 12
	}
	left := c.renderPane(0, paneW, bodyH)
	right := c.renderPane(1, paneW, bodyH)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

func (c commander) renderPane(i, w, h int) string {
	p := c.panes[i]
	style := paneStyle
	if i == c.active {
		style = paneFocus
	}
	title := truncate(p.title(), w-2)
	if i == c.active {
		title = hostStyle.Render(title)
	} else {
		title = dimStyle.Render(title)
	}

	rows := h - 3 // title + top/bottom border
	if rows < 1 {
		rows = 1
	}
	top := 0
	if p.cursor >= rows {
		top = p.cursor - rows + 1
	}
	end := top + rows
	if end > len(p.entries) {
		end = len(p.entries)
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	if p.err != "" {
		b.WriteString(dimStyle.Render(truncate("! "+p.err, w-2)))
	} else if len(p.entries) == 0 {
		b.WriteString(dimStyle.Render("(empty)"))
	}
	for idx := top; idx < end; idx++ {
		line := truncate(entryLabel(p.entries[idx]), w-2)
		if idx == p.cursor {
			if i == c.active {
				line = cursorStyle.Render(padRight(line, w-2))
			} else {
				line = dimStyle.Render("› " + line)
			}
		}
		b.WriteString(line)
		if idx < end-1 {
			b.WriteByte('\n')
		}
	}
	return style.Width(w).Height(h).Render(b.String())
}

func entryLabel(e entry) string {
	switch e.kind {
	case entryUp:
		return ".."
	case entrySnapshot:
		short := e.name
		if i := strings.IndexByte(short, '@'); i >= 0 {
			short = "@" + short[i+1:]
		}
		return short
	default:
		leaf := e.name
		if i := strings.LastIndexByte(leaf, '/'); i >= 0 {
			leaf = leaf[i+1:]
		}
		return "▸ " + leaf
	}
}

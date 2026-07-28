// tui_explorer.go — the snapshot file explorer, terminal edition.
//
// Same fusion feature as the GUI: browse a dataset's files (live, or inside
// any snapshot via .zfs/snapshot), see any path across EVERY snapshot that
// contains it with size/mtime deltas, restore a version over live or
// alongside. Driven entirely by the shared engine; restores are gated by
// read-write mode + confirmation upstream in model.go's dispatch.
package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type explorer struct {
	host    Host
	ds      string
	mp      string
	rel     string
	source  string // "" = live, else snapshot short name
	snaps   []Snapshot
	entries []FileEntry
	cursor  int
	vers    []FileVersion
	vcursor int
	focus   int // 0 = files, 1 = versions
	status  string
}

// newExplorer opens the explorer on a dataset (source "" = live tree).
func newExplorer(h Host, ds, source string) (*explorer, error) {
	mp, err := Mountpoint(h, ds)
	if err != nil {
		return nil, err
	}
	snaps, _ := ListSnapshots(h, ds)
	e := &explorer{host: h, ds: ds, mp: mp, source: source, snaps: snaps}
	e.loadDir()
	return e, nil
}

func (e *explorer) base() string {
	b := e.mp
	if e.source != "" {
		b = e.mp + "/.zfs/snapshot/" + e.source
	}
	if e.rel != "" {
		b += "/" + e.rel
	}
	return b
}

func (e *explorer) loadDir() {
	es, err := ListDir(e.host, e.base())
	e.entries = es
	e.cursor = 0
	e.vers = nil
	e.vcursor = 0
	e.focus = 0
	if err != nil {
		e.status = "✗ " + err.Error()
	} else {
		e.status = fmt.Sprintf("%d entries", len(es))
	}
}

func (e *explorer) current() (FileEntry, bool) {
	if e.cursor >= 0 && e.cursor < len(e.entries) {
		return e.entries[e.cursor], true
	}
	return FileEntry{}, false
}

func (e *explorer) currentVersion() (FileVersion, bool) {
	if e.vcursor >= 0 && e.vcursor < len(e.vers) {
		return e.vers[e.vcursor], true
	}
	return FileVersion{}, false
}

// loadVersions stats the selected path across every snapshot (one round-trip)
// and orders the result along the dataset's snapshot timeline.
func (e *explorer) loadVersions() {
	f, ok := e.current()
	if !ok {
		return
	}
	rel := relJoin(e.rel, f.Name)
	vs, err := FileVersions(e.host, e.mp, rel)
	if err != nil {
		e.status = "✗ versions: " + err.Error()
		return
	}
	byName := map[string]FileVersion{}
	for _, v := range vs {
		byName[v.Snapshot] = v
	}
	ordered := make([]FileVersion, 0, len(vs))
	for _, s := range e.snaps {
		if v, ok := byName[snapShort(s.Name)]; ok {
			ordered = append(ordered, v)
			delete(byName, v.Snapshot)
		}
	}
	for _, v := range vs {
		if _, left := byName[v.Snapshot]; left {
			ordered = append(ordered, v)
		}
	}
	e.vers = ordered
	e.vcursor = len(ordered) - 1 // newest — the usual restore source
	if e.vcursor < 0 {
		e.vcursor = 0
	}
	switch {
	case len(ordered) == 0:
		e.status = "/" + rel + " — in no snapshot (created since the last one?)"
	default:
		e.focus = 1
		e.status = fmt.Sprintf("/%s — in %d of %d snapshots", rel, len(ordered), len(e.snaps))
	}
}

// up ascends one directory (false at the root).
func (e *explorer) up() bool {
	if e.rel == "" {
		return false
	}
	if i := strings.LastIndexByte(e.rel, '/'); i >= 0 {
		e.rel = e.rel[:i]
	} else {
		e.rel = ""
	}
	e.loadDir()
	return true
}

// enter descends into a directory or loads a file's version history.
func (e *explorer) enter() {
	f, ok := e.current()
	if !ok {
		return
	}
	if f.Dir {
		e.rel = relJoin(e.rel, f.Name)
		e.loadDir()
		return
	}
	e.loadVersions()
}

// cycleSource moves the browse point along live → snap1 → snap2 … (dir<0
// goes the other way). The rel path is kept — comparing one directory
// across time is the point.
func (e *explorer) cycleSource(dir int) {
	opts := []string{""}
	for _, s := range e.snaps {
		opts = append(opts, snapShort(s.Name))
	}
	cur := 0
	for i, o := range opts {
		if o == e.source {
			cur = i
		}
	}
	cur = (cur + dir + len(opts)) % len(opts)
	e.source = opts[cur]
	e.loadDir()
}

func (e *explorer) title() string {
	src := "live"
	if e.source != "" {
		src = "@" + e.source
	}
	return fmt.Sprintf("%s  [%s]  /%s", e.ds, src, e.rel)
}

func (e *explorer) view(width, height int) string {
	bodyH := height - 3
	if bodyH < 4 {
		bodyH = 4
	}
	leftW := width * 55 / 100
	rightW := width - leftW - 4
	if leftW < 30 {
		leftW = 30
	}
	if rightW < 24 {
		rightW = 24
	}
	rows := bodyH - 3

	// files pane
	var fb strings.Builder
	fb.WriteString(hostStyle.Render(truncate(e.title(), leftW-2)) + "\n")
	top, end := window(e.cursor, len(e.entries), rows)
	for i := top; i < end; i++ {
		f := e.entries[i]
		mark, sz := "  ", ""
		if f.Dir {
			mark = "▸ "
		} else {
			sz = humanBytes(f.Size)
		}
		line := truncate(fmt.Sprintf("%s%-*s %9s  %s", mark, leftW-26, f.Name, sz, f.MTime), leftW-2)
		if i == e.cursor && e.focus == 0 {
			line = cursorStyle.Render(padRight(line, leftW-2))
		} else if i == e.cursor {
			line = dimStyle.Render("› " + line)
		}
		fb.WriteString(line + "\n")
	}
	filesPane := paneStyle
	if e.focus == 0 {
		filesPane = paneFocus
	}
	left := filesPane.Width(leftW).Height(bodyH).Render(fb.String())

	// versions pane
	var vb strings.Builder
	vb.WriteString(hostStyle.Render("VERSIONS — this path across snapshots") + "\n")
	if len(e.vers) == 0 {
		vb.WriteString(dimStyle.Render("\n  ↵ on a file to see it across\n  every snapshot that holds it,\n  then r / R to restore"))
	}
	sel, hasSel := e.current()
	vtop, vend := window(e.vcursor, len(e.vers), rows)
	for i := vtop; i < vend; i++ {
		v := e.vers[i]
		mark := "≡"
		if hasSel && (v.Size != sel.Size || v.MTime != sel.MTime) {
			mark = "Δ"
		}
		line := truncate(fmt.Sprintf("%s @%-24s %9s  %s", mark, v.Snapshot, humanBytes(v.Size), v.MTime), rightW-2)
		if i == e.vcursor && e.focus == 1 {
			line = cursorStyle.Render(padRight(line, rightW-2))
		}
		vb.WriteString(line + "\n")
	}
	versPane := paneStyle
	if e.focus == 1 {
		versPane = paneFocus
	}
	right := versPane.Width(rightW).Height(bodyH).Render(vb.String())

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	status := footerStyle.Render(" " + truncate(e.status, width-2))
	return lipgloss.JoinVertical(lipgloss.Left, body, status)
}

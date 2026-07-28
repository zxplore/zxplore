// tui_propedit.go — the right-pane property editor (Tab from the browser).
//
// Tab moves focus into the dossier side: the pane becomes a navigable list of
// the dataset's SETTABLE properties, grouped like the GUI editor, blue bar on
// the current row. Enter edits: enum/bool properties get the same option list
// the GUI shows as a dropdown; free-text ones get an input. Mutations gate on
// :rw and risky properties confirm first — identical policy to everywhere else.
package main

import (
	"fmt"
	"strings"
)

// propRow is one rendered row: a group header (not selectable) or a property.
type propRow struct {
	header string
	p      Prop
}

type propEditor struct {
	ds     string
	rows   []propRow
	cursor int
}

// newPropEditor loads a dataset's settable properties, grouped.
func newPropEditor(h Host, ds string) (*propEditor, error) {
	props, err := DatasetProps(h, ds)
	if err != nil {
		return nil, err
	}
	byName := map[string]Prop{}
	for _, p := range props {
		if p.Settable {
			byName[p.Name] = p
		}
	}
	pe := &propEditor{ds: ds}
	seen := map[string]bool{}
	for _, g := range propGroups {
		var rows []propRow
		for _, name := range g.props {
			if p, ok := byName[name]; ok {
				rows = append(rows, propRow{p: p})
				seen[name] = true
			}
		}
		if len(rows) > 0 {
			pe.rows = append(pe.rows, propRow{header: g.title})
			pe.rows = append(pe.rows, rows...)
		}
	}
	var other []propRow
	for _, p := range props {
		if p.Settable && !seen[p.Name] {
			other = append(other, propRow{p: p})
		}
	}
	if len(other) > 0 {
		pe.rows = append(pe.rows, propRow{header: "OTHER / CUSTOM"})
		pe.rows = append(pe.rows, other...)
	}
	pe.cursor = pe.firstSelectable()
	return pe, nil
}

func (pe *propEditor) firstSelectable() int {
	for i, r := range pe.rows {
		if r.header == "" {
			return i
		}
	}
	return 0
}

// refresh re-reads values (post-apply), keeping the cursor on the same name.
func (pe *propEditor) refresh(h Host) {
	name := ""
	if r, ok := pe.current(); ok {
		name = r.Name
	}
	np, err := newPropEditor(h, pe.ds)
	if err != nil {
		return
	}
	pe.rows = np.rows
	if pe.cursor >= len(pe.rows) {
		pe.cursor = np.firstSelectable()
	}
	for i, r := range pe.rows {
		if r.header == "" && r.p.Name == name {
			pe.cursor = i
			break
		}
	}
}

func (pe *propEditor) current() (Prop, bool) {
	if pe.cursor >= 0 && pe.cursor < len(pe.rows) && pe.rows[pe.cursor].header == "" {
		return pe.rows[pe.cursor].p, true
	}
	return Prop{}, false
}

// move steps the cursor, skipping group headers.
func (pe *propEditor) move(d int) {
	i := pe.cursor
	for {
		i += d
		if i < 0 || i >= len(pe.rows) {
			return
		}
		if pe.rows[i].header == "" {
			pe.cursor = i
			return
		}
	}
}

func (pe *propEditor) view(w, h int) string {
	rows := h - 2
	if rows < 3 {
		rows = 3
	}
	top, end := window(pe.cursor, len(pe.rows), rows)
	var b strings.Builder
	b.WriteString(hostStyle.Render(truncate("EDIT "+pe.ds+" — ↵ change · tab/esc back", w)) + "\n")
	for i := top; i < end; i++ {
		r := pe.rows[i]
		var line string
		if r.header != "" {
			line = dimStyle.Render(truncate("── "+r.header+" ──", w))
		} else {
			src := r.p.Source
			if src == "local" {
				src = "[local]"
			} else if strings.HasPrefix(src, "inherited from ") {
				src = "[inh:" + strings.TrimPrefix(src, "inherited from ") + "]"
			} else {
				src = "[" + src + "]"
			}
			line = truncate(fmt.Sprintf("  %-22s %-18s %s", r.p.Name, r.p.Value, src), w)
			if i == pe.cursor {
				line = cursorStyle.Render(padRight(line, w))
			}
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

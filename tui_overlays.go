// tui_overlays.go — the TUI's overlay layer: command bar, prompts (y/n,
// typed-name confirmation, free-text input), the snapshot action menu, the
// full-screen help, and a shared text pager (diff output, pool dossiers).
//
// Overlays follow the k9s school: read-only by default (mutations demand :rw
// first), a typed confirmation for the destroys, and every key documented in
// "?" — safety is a UI feature, not a footnote.
package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

// ── prompts ──────────────────────────────────────────────────────────────────

type promptKind int

const (
	pkConfirm promptKind = iota // y / n
	pkTyped                     // must retype `match` exactly (destroys)
	pkInput                     // free text (names, targets)
)

// prompt is a modal question. action+payload(+typed text) go to dispatch()
// on accept — no closures, so bubbletea's value-copied model stays safe.
type prompt struct {
	kind    promptKind
	title   string
	detail  string
	match   string
	input   textinput.Model
	action  string
	payload []string
}

func newPrompt(kind promptKind, title, detail, match, placeholder, action string, payload ...string) *prompt {
	p := &prompt{kind: kind, title: title, detail: detail, match: match, action: action, payload: payload}
	if kind != pkConfirm {
		p.input = textinput.New()
		p.input.Placeholder = placeholder
		p.input.CharLimit = 256
		p.input.Width = 46
		p.input.Focus()
	}
	return p
}

// newSecretPrompt is newPrompt for passphrases — input is masked and the
// value only ever travels on stdin (the engine's rule).
func newSecretPrompt(title, detail, action string, payload ...string) *prompt {
	p := newPrompt(pkInput, title, detail, "", "passphrase", action, payload...)
	p.input.EchoMode = textinput.EchoPassword
	return p
}

func (p *prompt) view(width int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(p.title) + "\n\n")
	if p.detail != "" {
		b.WriteString(p.detail + "\n\n")
	}
	switch p.kind {
	case pkConfirm:
		b.WriteString(footerStyle.Render("y confirm   n / esc cancel"))
	case pkTyped:
		b.WriteString("Type  " + cursorStyle.Render(p.match) + "  to confirm:\n\n")
		b.WriteString(p.input.View() + "\n\n")
		b.WriteString(footerStyle.Render("↵ confirm (must match)   esc cancel"))
	case pkInput:
		b.WriteString(p.input.View() + "\n\n")
		b.WriteString(footerStyle.Render("↵ ok   esc cancel"))
	}
	w := width - 8
	if w < 30 {
		w = 30
	}
	return paneFocus.Width(w).Padding(1, 2).Render(b.String())
}

// ── snapshot action menu ─────────────────────────────────────────────────────

// snapMenu drives a dataset's snapshots: pick a snapshot (page 1), pick an
// action for it (page 2) — the TUI twin of the GUI's snapshot dialog.
type snapMenu struct {
	ds      string
	snaps   []Snapshot
	cursor  int
	inActs  bool
	acursor int
}

var snapMenuActs = []string{
	"explore — browse / restore files in this snapshot",
	"diff — what changed since this snapshot (vs live)",
	"diff — against another snapshot…",
	"clone to a new dataset…",
	"bookmark (keeps the incremental chain)…",
	"hold (prevent destroy)",
	"release hold",
	"⚠ roll back to this snapshot…",
	"✖ destroy snapshot…",
}

func (sm *snapMenu) current() (Snapshot, bool) {
	if sm.cursor >= 0 && sm.cursor < len(sm.snaps) {
		return sm.snaps[sm.cursor], true
	}
	return Snapshot{}, false
}

func (sm *snapMenu) view(width, height int) string {
	var b strings.Builder
	if !sm.inActs {
		b.WriteString(titleStyle.Render("snapshots — "+sm.ds) + "\n\n")
		if len(sm.snaps) == 0 {
			b.WriteString(dimStyle.Render("  (no snapshots yet — press s in the browser to take one)\n"))
		}
		top, end := window(sm.cursor, len(sm.snaps), height-8)
		for i := top; i < end; i++ {
			s := sm.snaps[i]
			line := fmt.Sprintf("  %-34s %8s  %s", snapShort(s.Name), s.Used, s.Creation)
			if i == sm.cursor {
				line = cursorStyle.Render(line)
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n" + footerStyle.Render("↵ actions   ↑/↓ move   esc close"))
	} else {
		s, _ := sm.current()
		b.WriteString(titleStyle.Render("@"+snapShort(s.Name)) + "\n\n")
		for i, a := range snapMenuActs {
			line := "  " + a
			if i == sm.acursor {
				line = cursorStyle.Render(line)
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n" + footerStyle.Render("↵ run   ↑/↓ move   esc back"))
	}
	w := width - 6
	if w < 40 {
		w = 40
	}
	return paneFocus.Width(w).Padding(0, 1).Render(b.String())
}

// ── dataset action menu ──────────────────────────────────────────────────────

// dsMenu is the dataset-level lifecycle menu ("a" in the browser): create /
// rename / mount / encryption / destroy — the TUI twin of the GUI's
// right-click menu. Snapshot-level actions live in snapMenu (Enter).
type dsMenu struct {
	ds     string
	cursor int
}

var dsMenuActs = []string{
	"create child dataset…",
	"create volume (zvol)…",
	"rename…",
	"mount",
	"unmount",
	"encryption — unlock (load key)…",
	"encryption — lock (unload key)",
	"encryption — change passphrase…",
	"encryption — create encrypted child…",
	"✖ destroy dataset (recursive)…",
}

func (dm *dsMenu) view(width int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(dm.ds) + "\n\n")
	for i, a := range dsMenuActs {
		line := "  " + a
		if i == dm.cursor {
			line = cursorStyle.Render(padRight(line, 44))
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + footerStyle.Render("↵ run   ↑/↓ move   esc close"))
	w := width / 3
	if w < 50 {
		w = 50
	}
	return paneFocus.Width(w).Padding(0, 1).Render(b.String())
}

// ── boot environments menu ───────────────────────────────────────────────────

// beMenu manages boot environments (":be" / "B"): restore points of the
// bootfs-derived boot dataset. create / roll back / delete.
type beMenu struct {
	bd     string
	bes    []BootEnv
	cursor int
}

func newBeMenu(h Host) (*beMenu, error) {
	bes, bd, err := ListBootEnvs(h)
	if err != nil {
		return nil, err
	}
	bm := &beMenu{bd: bd, bes: bes, cursor: len(bes) - 1}
	if bm.cursor < 0 {
		bm.cursor = 0
	}
	return bm, nil
}

func (bm *beMenu) current() (BootEnv, bool) {
	if bm.cursor >= 0 && bm.cursor < len(bm.bes) {
		return bm.bes[bm.cursor], true
	}
	return BootEnv{}, false
}

func (bm *beMenu) view(width, height int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("boot environments — "+bm.bd) + "\n")
	b.WriteString(dimStyle.Render("  (snapshots of the boot dataset = restore points)") + "\n\n")
	if len(bm.bes) == 0 {
		b.WriteString(dimStyle.Render("  (none yet — press c to create one)\n"))
	}
	top, end := window(bm.cursor, len(bm.bes), height-10)
	for i := top; i < end; i++ {
		be := bm.bes[i]
		line := fmt.Sprintf("  %-30s %8s  %s", snapShort(be.Snapshot), be.Used, be.Created)
		if i == bm.cursor {
			line = cursorStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + footerStyle.Render("c create   R roll back (reboot applies it)   D delete   r refresh   esc close"))
	w := width - 8
	if w < 60 {
		w = 60
	}
	return paneFocus.Width(w).Padding(0, 1).Render(b.String())
}

// ── option picker (the TUI's "dropdown") ─────────────────────────────────────

// picker offers a fixed option list — enum/bool property values, exactly what
// the GUI shows as a dropdown. Enter dispatches action(payload..., choice).
type picker struct {
	title   string
	options []string
	cursor  int
	action  string
	payload []string
}

func (pk *picker) view(width int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(pk.title) + "\n\n")
	for i, o := range pk.options {
		line := "  " + o
		if i == pk.cursor {
			line = cursorStyle.Render(padRight(line, 30))
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + footerStyle.Render("↵ apply   ↑/↓ move   esc cancel"))
	w := width / 3
	if w < 36 {
		w = 36
	}
	return paneFocus.Width(w).Padding(0, 2).Render(b.String())
}

// ── pager (diff output, pool dossier, importable pools…) ─────────────────────

type pager struct {
	title     string
	lines     []string
	off       int
	q         string // "/" filter — only matching lines show
	filtering bool
	fin       textinput.Model
}

func newPager(title, text string) *pager {
	p := &pager{title: title, lines: strings.Split(strings.TrimRight(text, "\n"), "\n")}
	p.fin = textinput.New()
	p.fin.Placeholder = "filter lines…"
	p.fin.CharLimit = 128
	p.fin.Width = 30
	return p
}

// visible applies the "/" filter (diff panes by path, dossiers by keyword).
func (p *pager) visible() []string {
	if p.q == "" {
		return p.lines
	}
	q := strings.ToLower(p.q)
	var out []string
	for _, l := range p.lines {
		if strings.Contains(strings.ToLower(l), q) {
			out = append(out, l)
		}
	}
	return out
}

func (p *pager) move(d, page int) {
	p.off += d * page
	max := len(p.visible()) - 1
	if p.off > max {
		p.off = max
	}
	if p.off < 0 {
		p.off = 0
	}
}

func (p *pager) view(width, height int) string {
	rows := height - 4
	if rows < 3 {
		rows = 3
	}
	lines := p.visible()
	end := p.off + rows
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	head := titleStyle.Render(p.title) +
		dimStyle.Render(fmt.Sprintf("  %d–%d/%d", p.off+1, end, len(lines)))
	if p.filtering {
		head += "   / " + p.fin.View()
	} else if p.q != "" {
		head += dimStyle.Render("   /" + p.q)
	}
	b.WriteString(head + "\n")
	start := p.off
	if start > len(lines) {
		start = len(lines)
	}
	for _, l := range lines[start:end] {
		b.WriteString(truncate(l, width-4) + "\n")
	}
	b.WriteString(footerStyle.Render(" j/k scroll   ctrl+d/u page   g/G ends   / filter   q/esc close"))
	return paneFocus.Width(width - 2).Height(height - 1).Render(b.String())
}

// ── help overlay ─────────────────────────────────────────────────────────────

const tuiHelp = `  BROWSER (F1)
    ↑/↓ j/k         move          g/G        first / last
    ctrl+d/ctrl+u   half page     /          filter datasets
    ↵               snapshot menu (explore / diff vs live or any snapshot /
                    clone / bookmark / rollback / destroy)
    a               dataset menu (create / zvol / rename / mount /
                    encryption / destroy)          B   boot environments
    tab             edit properties (blue bar on the right pane; ↵ = change
                    with the same option lists as the GUI; tab/esc back)
    x  or F3        file explorer on the dataset
    s               snapshot now            b   bookmark location
    c               connect a host          r   reload
    F2 transfer   F4 pools   :  command bar   ?  this help   q quit

  EXPLORER
    ↑/↓ j/k   move        ↵ / l    enter dir · pick file    h/bksp  up dir
    tab       files ⇄ versions     [ / ]    browse live / inside snapshots
    r         restore as copy      R        restore OVER live (:rw + confirm)
    d         diff snapshot ↔ live           esc  back

  POOLS (F4)
    ↵ / d     drill-down dossier (vitals · zfs-vs-df space · vdevs · iostat)
    s scrub   S stop   t trim   c clear errors   i scan for importable

  COMMANDS  (:)
    :browse :transfer :pools :explore [ds] :connect user@host:pool
    :importpool <name>   :ro / :rw   read-only ⇄ read-write   :q quit

  SAFETY — read-only by default: every mutation needs :rw first (footer
  shows the mode); destroys additionally demand retyping the target's name.`

func helpView(width, height int) string {
	return paneFocus.Width(width - 2).Height(height - 1).
		Render(titleStyle.Render("zxplore — keys") + "\n" + tuiHelp + "\n\n" +
			footerStyle.Render(" any key to close   ·   full docs: man zxplore"))
}

// window computes a scroll window [top,end) that keeps cursor visible.
func window(cursor, n, rows int) (int, int) {
	if rows < 1 {
		rows = 1
	}
	top := 0
	if cursor >= rows {
		top = cursor - rows + 1
	}
	end := top + rows
	if end > n {
		end = n
	}
	return top, end
}

// overlayCenter floats content in the middle of the screen.
func overlayCenter(content string, width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content)
}

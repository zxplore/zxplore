// model.go — the bubbletea TUI core.
//
// A real console in the k9s mold, driven by the same engine as the GUI:
//
//	browse (F1)    dataset list + live dossier · ↵ snapshot menu · / filter
//	transfer (F2)  dual-pane commander (local/remote replication)
//	explorer (F3)  files across snapshots + restore (tui_explorer.go)
//	pools (F4)     pools + drill-down dossier (tui_pools.go)
//
// Overlays: ":" command bar, "?" help, prompts (y/n, typed-name confirm,
// text input), snapshot action menu, shared pager (tui_overlays.go).
//
// SAFETY: read-only by default. Every mutation requires :rw first; the
// destructive ones additionally demand retyping the target's name. The footer
// always shows which mode you're in.
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Styles ───────────────────────────────────────────────────────────────────
var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#5fc4bc")).Padding(0, 1)
	badgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#4cb98a")).Padding(0, 1)
	rwStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#ff5c5c")).Padding(0, 1)
	roStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#7cd0c9")).Padding(0, 1)
	hostStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#7cd0c9"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	paneStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	paneFocus   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#5ab0ff"))
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1220")).Background(lipgloss.Color("#5ab0ff"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#4cb98a"))
)

type uiMode int

const (
	modeBrowse uiMode = iota
	modeFavorites
	modeConnect
	modeTransfer
	modeExplorer
	modePools
)

type model struct {
	host     Host
	datasets []Dataset
	cursor   int
	dossier  string
	width    int
	height   int
	err      string
	status   string
	kldload  bool
	platform string
	mode     uiMode

	favorites []Favorite
	favCursor int
	input     textinput.Model
	cmdr      commander
	// connectPane: -1 = connect the browser itself; 0/1 = a commander pane.
	connectPane int
	dossCols    int

	// safety: read-only until :rw
	rw bool

	// overlays / extras
	filter     string
	filterIn   textinput.Model
	filterEdit bool
	cmdIn      textinput.Model
	cmdActive  bool
	helpOn     bool
	pr         *prompt
	sm         *snapMenu
	exp        *explorer
	pv         *poolsView
	pg         *pager
}

func newModel() model {
	ti := textinput.New()
	ti.Placeholder = "user@host:pool/path   (blank host = local)"
	ti.CharLimit = 256
	ti.Width = 48
	fi := textinput.New()
	fi.Placeholder = "filter…"
	fi.CharLimit = 128
	fi.Width = 28
	ci := textinput.New()
	ci.Placeholder = "browse · transfer · pools · explore [ds] · connect target · rw · ro · q"
	ci.CharLimit = 256
	ci.Width = 60
	m := model{host: LocalHost(), kldload: IsKldload(), input: ti, filterIn: fi, cmdIn: ci,
		connectPane: -1, dossCols: 1}
	m.platform = HostPlatform(m.host)
	m.reload()
	return m
}

// fds is the filtered dataset view the browser renders and indexes.
func (m model) fds() []Dataset {
	if m.filter == "" {
		return m.datasets
	}
	q := strings.ToLower(m.filter)
	var out []Dataset
	for _, d := range m.datasets {
		if strings.Contains(strings.ToLower(d.Name), q) {
			out = append(out, d)
		}
	}
	return out
}

func (m model) selDataset() (Dataset, bool) {
	ds := m.fds()
	if m.cursor >= 0 && m.cursor < len(ds) {
		return ds[m.cursor], true
	}
	return Dataset{}, false
}

// dossierPaneWidth mirrors viewBrowse's split so the dossier is packed for
// the width it will actually render in.
func (m model) dossierPaneWidth() int {
	leftW := m.width * 45 / 100
	if leftW < 24 {
		leftW = 24
	}
	rightW := m.width - leftW - 4
	if rightW < 12 {
		rightW = 12
	}
	return rightW - 2
}

func (m model) dossierColsFor() int {
	c := m.dossierPaneWidth() / 46
	if c < 1 {
		c = 1
	}
	if c > 4 {
		c = 4
	}
	return c
}

func (m *model) reload() {
	ds, err := ListDatasets(m.host)
	m.datasets = ds
	if err != nil {
		m.err = err.Error()
	} else {
		m.err = ""
	}
	m.clampCursor()
	m.refreshDossier()
}

func (m *model) clampCursor() {
	n := len(m.fds())
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *model) refreshDossier() {
	if d, ok := m.selDataset(); ok {
		m.dossier = DossierCols(m.host, d.Name, m.dossCols)
	} else if m.err == "" {
		if wt := WelcomeText(DiagnoseHost(m.host)); wt != "" {
			m.dossier = wt
		} else {
			m.dossier = "(no datasets)"
		}
	} else {
		m.dossier = "(no datasets)"
	}
}

// requireRW gates every mutation behind :rw. Returns false (and explains)
// in read-only mode.
func (m *model) requireRW() bool {
	if m.rw {
		return true
	}
	m.status = "read-only — type :rw to unlock mutations (then :ro to relock)"
	return false
}

// connect re-homes the browser at a favorite's host + lands on its path.
func (m model) connect(f Favorite) model {
	m.host = f.Host()
	m.platform = HostPlatform(m.host)
	m.cursor = 0
	m.filter = ""
	m.reload()
	if f.Path != "" {
		for i, d := range m.fds() {
			if d.Name == f.Path {
				m.cursor = i
				break
			}
		}
		m.refreshDossier()
	}
	if m.err != "" {
		m.status = "✗ " + m.host.Label() + ": " + m.err
	} else {
		m.status = "connected: " + f.Target()
	}
	return m
}

// applyConnect applies a chosen favorite/target: to a commander pane if we
// entered connect from the commander, else to the browser itself.
func (m model) applyConnect(f Favorite) model {
	if m.connectPane == 0 || m.connectPane == 1 {
		p := &m.cmdr.panes[m.connectPane]
		p.host = f.Host()
		p.location = f.Path
		p.cursor = 0
		p.load()
		if p.err != "" {
			m.cmdr.status = "✗ " + f.Target() + ": " + p.err
		} else {
			m.cmdr.status = "pane → " + f.Target()
		}
		m.connectPane = -1
		m.mode = modeTransfer
		return m
	}
	m.mode = modeBrowse
	return m.connect(f)
}

func (m model) cancelConnect() model {
	if m.connectPane == 0 || m.connectPane == 1 {
		m.mode = modeTransfer
	} else {
		m.mode = modeBrowse
	}
	m.connectPane = -1
	return m
}

func (m *model) bookmarkCurrent() {
	d, ok := m.selDataset()
	if !ok {
		return
	}
	f := Favorite{Name: (Favorite{SSH: m.host.SSH, Path: d.Name}).Target(), SSH: m.host.SSH, Path: d.Name}
	m.favorites = AddFavorite(LoadFavorites(), f)
	_ = SaveFavorites(m.favorites)
	m.status = "bookmarked location: " + f.Name
}

// openExplorer opens the file explorer on a dataset (source ""=live).
func (m model) openExplorer(ds, source string) model {
	e, err := newExplorer(m.host, ds, source)
	if err != nil {
		m.status = "✗ " + err.Error()
		return m
	}
	m.exp = e
	m.mode = modeExplorer
	return m
}

// openSnapMenu opens the snapshot action menu for a dataset.
func (m model) openSnapMenu(ds string) model {
	snaps, err := ListSnapshots(m.host, ds)
	if err != nil {
		m.status = "✗ " + err.Error()
		return m
	}
	m.sm = &snapMenu{ds: ds, snaps: snaps, cursor: len(snaps) - 1}
	if m.sm.cursor < 0 {
		m.sm.cursor = 0
	}
	return m
}

func (m model) Init() tea.Cmd { return nil }

// ── dispatch: every mutation funnels through here ───────────────────────────

func (m model) dispatch(action string, payload []string, text string) (model, tea.Cmd) {
	get := func(i int) string {
		if i < len(payload) {
			return payload[i]
		}
		return ""
	}
	report := func(verb string, err error) {
		if err != nil {
			m.status = "✗ " + err.Error()
		} else {
			m.status = "✓ " + verb
		}
	}
	switch action {
	case "snapshot-now":
		name := strings.TrimSpace(text)
		if name == "" {
			name = "manual-" + time.Now().Format("20060102-150405")
		}
		snap, err := SnapshotNow(m.host, get(0), name)
		report("snapshot "+snap, err)
		m.reload()
	case "rollback":
		report("rolled back to "+get(0), Rollback(m.host, get(0)))
		m.reload()
	case "clone":
		report("cloned → "+text, Clone(m.host, get(0), strings.TrimSpace(text)))
		m.reload()
	case "bookmark":
		report("bookmarked #"+text, CreateBookmark(m.host, get(0), strings.TrimSpace(text)))
	case "hold":
		report("held "+get(0), HoldSnap(m.host, get(0)))
	case "release":
		report("released "+get(0), ReleaseSnap(m.host, get(0)))
	case "destroy-snap":
		report("destroyed "+get(0), DestroySnapshot(m.host, get(0)))
		if m.sm != nil {
			return m.openSnapMenu(m.sm.ds), nil
		}
	case "restore":
		argv, dst := RestoreArgv(get(0), get(1), get(2), get(3) == "1", get(4) == "1")
		report("restored → "+dst, RestoreFromSnapshot(m.host, argv))
		if m.exp != nil {
			m.exp.loadDir()
		}
	case "scrub":
		report("scrub started on "+get(0), ScrubPool(m.host, get(0), true))
	case "scrub-stop":
		report("scrub stopped on "+get(0), ScrubPool(m.host, get(0), false))
	case "trim":
		report("trim started on "+get(0), TrimPool(m.host, get(0)))
	case "clear":
		report("errors cleared on "+get(0), ClearPool(m.host, get(0)))
	case "importpool":
		report("imported "+text, ImportPool(m.host, strings.TrimSpace(text)))
		if m.pv != nil {
			m.pv.reload()
		}
		m.reload()
	}
	return m, nil
}

// ── Update ───────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if c := m.dossierColsFor(); c != m.dossCols {
			m.dossCols = c
			m.refreshDossier()
		}
		return m, nil
	case replicateDoneMsg:
		if msg.err != nil {
			m.cmdr.status = "✗ replicate failed: " + msg.err.Error()
		} else {
			m.cmdr.status = "✓ replicated → " + msg.dst
			if msg.dstIdx >= 0 && msg.dstIdx < 2 {
				m.cmdr.panes[msg.dstIdx].load()
			}
		}
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		// overlays first, most-modal wins
		if m.pg != nil {
			return m.updatePager(msg), nil
		}
		if m.helpOn {
			m.helpOn = false
			return m, nil
		}
		if m.pr != nil {
			return m.updatePrompt(msg)
		}
		if m.cmdActive {
			return m.updateCommand(msg)
		}
		if m.filterEdit {
			return m.updateFilter(msg), nil
		}
		if m.sm != nil {
			return m.updateSnapMenu(msg)
		}
		switch m.mode {
		case modeFavorites:
			return m.updateFavorites(msg), nil
		case modeConnect:
			return m.updateConnect(msg)
		case modeTransfer:
			return m.updateTransfer(msg)
		case modeExplorer:
			return m.updateExplorer(msg)
		case modePools:
			return m.updatePools(msg)
		default:
			return m.updateBrowse(msg)
		}
	}
	return m, nil
}

// global handles keys shared by the main modes. handled=false → caller's turn.
func (m model) global(k string) (model, tea.Cmd, bool) {
	switch k {
	case ":":
		m.cmdIn.SetValue("")
		m.cmdIn.Focus()
		m.cmdActive = true
		return m, nil, true
	case "?":
		m.helpOn = true
		return m, nil, true
	case "f1":
		m.mode = modeBrowse
		m.reload()
		return m, nil, true
	case "f2":
		if len(m.cmdr.panes[0].entries) == 0 {
			m.cmdr = newCommander(m.host, "")
		}
		m.mode = modeTransfer
		return m, nil, true
	case "f3":
		if d, ok := m.selDataset(); ok {
			return m.openExplorer(d.Name, ""), nil, true
		}
		return m, nil, true
	case "f4":
		m.pv = newPoolsView(m.host)
		m.mode = modePools
		return m, nil, true
	}
	return m, nil, false
}

func (m model) updateBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if mm, cmd, ok := m.global(msg.String()); ok {
		return mm, cmd
	}
	page := m.height / 2
	if page < 4 {
		page = 4
	}
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "down", "j":
		if m.cursor < len(m.fds())-1 {
			m.cursor++
			m.refreshDossier()
		}
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.refreshDossier()
		}
	case "ctrl+d":
		m.cursor += page
		m.clampCursor()
		m.refreshDossier()
	case "ctrl+u":
		m.cursor -= page
		m.clampCursor()
		m.refreshDossier()
	case "g", "home":
		m.cursor = 0
		m.refreshDossier()
	case "G", "end":
		if n := len(m.fds()); n > 0 {
			m.cursor = n - 1
			m.refreshDossier()
		}
	case "/":
		m.filterIn.SetValue(m.filter)
		m.filterIn.Focus()
		m.filterEdit = true
	case "enter":
		if d, ok := m.selDataset(); ok {
			return m.openSnapMenu(d.Name), nil
		}
	case "x":
		if d, ok := m.selDataset(); ok {
			return m.openExplorer(d.Name, ""), nil
		}
	case "r":
		m.reload()
		m.status = "reloaded"
	case "c":
		m.connectPane = -1
		m.favorites = LoadFavorites()
		m.favCursor = 0
		m.mode = modeFavorites
	case "b":
		m.bookmarkCurrent()
	case "s":
		if d, ok := m.selDataset(); ok {
			if !m.requireRW() {
				return m, nil
			}
			m.pr = newPrompt(pkInput, "snapshot "+d.Name,
				"Name for the snapshot (empty = manual-<timestamp>):", "",
				"snapshot name", "snapshot-now", d.Name)
		}
	}
	return m, nil
}

func (m model) updateFilter(msg tea.KeyMsg) model {
	switch msg.String() {
	case "enter":
		m.filterEdit = false
		m.refreshDossier()
	case "esc":
		m.filterEdit = false
		m.filter = ""
		m.filterIn.SetValue("")
		m.clampCursor()
		m.refreshDossier()
	default:
		var cmd tea.Cmd
		m.filterIn, cmd = m.filterIn.Update(msg)
		_ = cmd
		m.filter = m.filterIn.Value()
		m.cursor = 0
	}
	return m
}

func (m model) updateCommand(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.cmdActive = false
		return m, nil
	case "enter":
		m.cmdActive = false
		return m.runCommand(strings.TrimSpace(m.cmdIn.Value()))
	default:
		var cmd tea.Cmd
		m.cmdIn, cmd = m.cmdIn.Update(msg)
		return m, cmd
	}
}

// runCommand executes a ":" command.
func (m model) runCommand(s string) (tea.Model, tea.Cmd) {
	if s == "" {
		return m, nil
	}
	f := strings.Fields(s)
	arg := ""
	if len(f) > 1 {
		arg = f[1]
	}
	switch f[0] {
	case "q", "quit":
		return m, tea.Quit
	case "help":
		m.helpOn = true
	case "browse", "files":
		m.mode = modeBrowse
		m.reload()
	case "transfer":
		if len(m.cmdr.panes[0].entries) == 0 {
			m.cmdr = newCommander(m.host, "")
		}
		m.mode = modeTransfer
	case "pools":
		m.pv = newPoolsView(m.host)
		m.mode = modePools
	case "explore":
		ds := arg
		if ds == "" {
			if d, ok := m.selDataset(); ok {
				ds = d.Name
			}
		}
		if ds != "" {
			return m.openExplorer(ds, ""), nil
		}
	case "snaps":
		ds := arg
		if ds == "" {
			if d, ok := m.selDataset(); ok {
				ds = d.Name
			}
		}
		if ds != "" {
			return m.openSnapMenu(ds), nil
		}
	case "connect":
		if arg == "" {
			m.status = "usage: :connect [user@]host:pool  (or pool/path)"
			return m, nil
		}
		fav := ParseTarget(arg)
		m.favorites = AddFavorite(LoadFavorites(), fav)
		_ = SaveFavorites(m.favorites)
		return m.applyConnect(fav), nil
	case "importpool":
		if !m.requireRW() {
			return m, nil
		}
		if arg == "" {
			m.status = "usage: :importpool <name>   (press i in :pools to scan)"
			return m, nil
		}
		return m.dispatch("importpool", nil, arg)
	case "rw":
		m.rw = true
		m.status = "⚠ READ-WRITE — mutations enabled (:ro to relock)"
	case "ro":
		m.rw = false
		m.status = "read-only locked"
	case "refresh":
		m.reload()
		m.status = "reloaded"
	default:
		m.status = "unknown command: " + f[0] + "   (:help)"
	}
	return m, nil
}

func (m model) updatePrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.pr
	switch p.kind {
	case pkConfirm:
		switch msg.String() {
		case "y", "Y", "enter":
			m.pr = nil
			return m.dispatch(p.action, p.payload, "")
		case "n", "N", "esc":
			m.pr = nil
			m.status = "cancelled"
		}
		return m, nil
	default:
		switch msg.String() {
		case "esc":
			m.pr = nil
			m.status = "cancelled"
			return m, nil
		case "enter":
			v := p.input.Value()
			if p.kind == pkTyped && v != p.match {
				m.status = "✗ name mismatch — type exactly: " + p.match
				return m, nil
			}
			m.pr = nil
			return m.dispatch(p.action, p.payload, v)
		default:
			var cmd tea.Cmd
			p.input, cmd = p.input.Update(msg)
			return m, cmd
		}
	}
}

func (m model) updateSnapMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sm := m.sm
	k := msg.String()
	if !sm.inActs {
		switch k {
		case "esc", "q":
			m.sm = nil
		case "down", "j":
			if sm.cursor < len(sm.snaps)-1 {
				sm.cursor++
			}
		case "up", "k":
			if sm.cursor > 0 {
				sm.cursor--
			}
		case "enter":
			if _, ok := sm.current(); ok {
				sm.inActs = true
				sm.acursor = 0
			}
		}
		return m, nil
	}
	switch k {
	case "esc", "q":
		sm.inActs = false
	case "down", "j":
		if sm.acursor < len(snapMenuActs)-1 {
			sm.acursor++
		}
	case "up", "k":
		if sm.acursor > 0 {
			sm.acursor--
		}
	case "enter":
		s, ok := sm.current()
		if !ok {
			return m, nil
		}
		short := snapShort(s.Name)
		switch sm.acursor {
		case 0: // explore
			ds := sm.ds
			m.sm = nil
			return m.openExplorer(ds, short), nil
		case 1: // diff vs live
			rows, err := SnapshotDiff(m.host, s.Name, "")
			if err != nil {
				m.status = "✗ diff: " + err.Error()
				return m, nil
			}
			m.pg = newPager("zfs diff "+s.Name+" → live", renderDiffText(rows))
		case 2: // clone
			if !m.requireRW() {
				return m, nil
			}
			m.pr = newPrompt(pkInput, "clone @"+short, "New dataset name:", "",
				"pool/new-dataset", "clone", s.Name)
		case 3: // bookmark
			if !m.requireRW() {
				return m, nil
			}
			m.pr = newPrompt(pkInput, "bookmark @"+short,
				"Bookmark name (→ "+sm.ds+"#…):", "", short, "bookmark", s.Name)
		case 4:
			if !m.requireRW() {
				return m, nil
			}
			return m.dispatch("hold", []string{s.Name}, "")
		case 5:
			if !m.requireRW() {
				return m, nil
			}
			return m.dispatch("release", []string{s.Name}, "")
		case 6: // rollback — typed confirm on the DATASET name
			if !m.requireRW() {
				return m, nil
			}
			m.pr = newPrompt(pkTyped, "⚠ roll back "+sm.ds,
				"Rolls back to @"+short+" and DESTROYS every newer snapshot\n(and their clones).",
				sm.ds, "", "rollback", s.Name)
		case 7: // destroy — typed confirm on the snapshot short name
			if !m.requireRW() {
				return m, nil
			}
			m.pr = newPrompt(pkTyped, "✖ destroy @"+short,
				"Permanently destroys this snapshot.", short, "", "destroy-snap", s.Name)
		}
	}
	return m, nil
}

func (m model) updateExplorer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if mm, cmd, ok := m.global(msg.String()); ok {
		return mm, cmd
	}
	e := m.exp
	if e == nil {
		m.mode = modeBrowse
		return m, nil
	}
	move := func(cur *int, n, d int) {
		*cur += d
		if *cur >= n {
			*cur = n - 1
		}
		if *cur < 0 {
			*cur = 0
		}
	}
	page := m.height / 2
	if page < 4 {
		page = 4
	}
	switch msg.String() {
	case "esc", "q":
		m.exp = nil
		m.mode = modeBrowse
		m.reload()
	case "tab":
		if len(e.vers) > 0 {
			e.focus = 1 - e.focus
		}
	case "down", "j":
		if e.focus == 0 {
			move(&e.cursor, len(e.entries), 1)
		} else {
			move(&e.vcursor, len(e.vers), 1)
		}
	case "up", "k":
		if e.focus == 0 {
			move(&e.cursor, len(e.entries), -1)
		} else {
			move(&e.vcursor, len(e.vers), -1)
		}
	case "ctrl+d":
		if e.focus == 0 {
			move(&e.cursor, len(e.entries), page)
		} else {
			move(&e.vcursor, len(e.vers), page)
		}
	case "ctrl+u":
		if e.focus == 0 {
			move(&e.cursor, len(e.entries), -page)
		} else {
			move(&e.vcursor, len(e.vers), -page)
		}
	case "g":
		if e.focus == 0 {
			e.cursor = 0
		} else {
			e.vcursor = 0
		}
	case "G":
		if e.focus == 0 && len(e.entries) > 0 {
			e.cursor = len(e.entries) - 1
		} else if len(e.vers) > 0 {
			e.vcursor = len(e.vers) - 1
		}
	case "enter", "l":
		if e.focus == 0 {
			e.enter()
		}
	case "h", "backspace":
		if e.focus == 1 {
			e.focus = 0
		} else {
			e.up()
		}
	case "[":
		e.cycleSource(-1)
	case "]":
		e.cycleSource(1)
	case "d": // diff selected snapshot (version row, else source) vs live
		snap := e.source
		if v, ok := e.currentVersion(); ok && e.focus == 1 {
			snap = v.Snapshot
		}
		if snap == "" {
			m.status = "pick a snapshot version (or browse one with ]) first"
			return m, nil
		}
		rows, err := SnapshotDiff(m.host, e.ds+"@"+snap, "")
		if err != nil {
			m.status = "✗ diff: " + err.Error()
			return m, nil
		}
		m.pg = newPager("zfs diff "+e.ds+"@"+snap+" → live", renderDiffText(rows))
	case "r", "R": // restore as copy / over live
		f, okF := e.current()
		v, okV := e.currentVersion()
		if !okF || !okV {
			m.status = "↵ on a file first, then pick a version (tab)"
			return m, nil
		}
		if !m.requireRW() {
			return m, nil
		}
		overwrite := msg.String() == "R"
		rel := relJoin(e.rel, f.Name)
		argv, dst := RestoreArgv(e.mp, v.Snapshot, rel, v.Dir, overwrite)
		payload := []string{e.mp, v.Snapshot, rel, boolStr(v.Dir), boolStr(overwrite)}
		detail := "From @" + v.Snapshot + "\n→ " + dst + "\n\nRuns exactly (root):\n  " + strings.Join(argv, " ")
		if overwrite {
			m.pr = newPrompt(pkTyped, "⚠ restore OVER live: "+f.Name, detail, f.Name, "", "restore", payload...)
		} else {
			m.pr = newPrompt(pkConfirm, "restore as copy: "+f.Name, detail, "", "", "restore", payload...)
		}
	}
	return m, nil
}

func (m model) updatePools(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if mm, cmd, ok := m.global(msg.String()); ok {
		return mm, cmd
	}
	p := m.pv
	if p == nil {
		m.mode = modeBrowse
		return m, nil
	}
	switch msg.String() {
	case "esc", "q":
		m.mode = modeBrowse
		m.reload()
	case "down", "j":
		if p.cursor < len(p.names)-1 {
			p.cursor++
		}
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "enter", "d":
		if pool, ok := p.current(); ok {
			m.pg = newPager("pool — "+pool, PoolDossier(m.host, pool))
		}
	case "s":
		if pool, ok := p.current(); ok && m.requireRW() {
			m.pr = newPrompt(pkConfirm, "scrub "+pool, "Start a scrub?", "", "", "scrub", pool)
		}
	case "S":
		if pool, ok := p.current(); ok && m.requireRW() {
			return m.dispatch("scrub-stop", []string{pool}, "")
		}
	case "t":
		if pool, ok := p.current(); ok && m.requireRW() {
			m.pr = newPrompt(pkConfirm, "trim "+pool, "Start a TRIM of free space?", "", "", "trim", pool)
		}
	case "c":
		if pool, ok := p.current(); ok && m.requireRW() {
			return m.dispatch("clear", []string{pool}, "")
		}
	case "i":
		names, err := ImportablePools(m.host)
		switch {
		case err != nil:
			p.status = "✗ scan: " + err.Error()
		case len(names) == 0:
			p.status = "no exported pools found on this machine's devices"
		default:
			m.pg = newPager("importable pools",
				strings.Join(names, "\n")+"\n\nimport with  :importpool <name>   (:rw first)")
		}
	case "r":
		p.reload()
	}
	return m, nil
}

func (m model) updatePager(msg tea.KeyMsg) model {
	page := m.height - 6
	if page < 4 {
		page = 4
	}
	switch msg.String() {
	case "q", "esc", "enter":
		m.pg = nil
	case "down", "j":
		m.pg.move(1, 1)
	case "up", "k":
		m.pg.move(-1, 1)
	case "ctrl+d", "pgdown":
		m.pg.move(1, page)
	case "ctrl+u", "pgup":
		m.pg.move(-1, page)
	case "g":
		m.pg.off = 0
	case "G":
		m.pg.off = len(m.pg.lines) - page
		if m.pg.off < 0 {
			m.pg.off = 0
		}
	}
	return m
}

func (m model) updateTransfer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if mm, cmd, ok := m.global(msg.String()); ok {
		return mm, cmd
	}
	switch msg.String() {
	case "esc":
		m.mode = modeBrowse
		m.reload()
		return m, nil
	case "c":
		m.connectPane = m.cmdr.active
		m.favorites = LoadFavorites()
		m.favCursor = 0
		m.mode = modeFavorites
		return m, nil
	case "s", "f5", " ":
		// mutations (snapshot / replicate) gate on :rw like everything else
		if !m.requireRW() {
			return m, nil
		}
		cmd := m.cmdr.update(msg)
		return m, cmd
	default:
		cmd := m.cmdr.update(msg)
		return m, cmd
	}
}

func (m model) updateFavorites(msg tea.KeyMsg) model {
	switch msg.String() {
	case "esc", "c", "q":
		return m.cancelConnect()
	case "down", "j":
		if m.favCursor < len(m.favorites)-1 {
			m.favCursor++
		}
	case "up", "k":
		if m.favCursor > 0 {
			m.favCursor--
		}
	case "n":
		m.input.SetValue("")
		m.input.Focus()
		m.mode = modeConnect
	case "d":
		if m.favCursor < len(m.favorites) {
			m.favorites = append(m.favorites[:m.favCursor], m.favorites[m.favCursor+1:]...)
			_ = SaveFavorites(m.favorites)
			if m.favCursor >= len(m.favorites) && m.favCursor > 0 {
				m.favCursor--
			}
		}
	case "enter":
		if m.favCursor >= 0 && m.favCursor < len(m.favorites) {
			return m.applyConnect(m.favorites[m.favCursor])
		}
	}
	return m
}

func (m model) updateConnect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m.cancelConnect(), nil
	case "enter":
		v := strings.TrimSpace(m.input.Value())
		if v != "" {
			f := ParseTarget(v)
			m.favorites = AddFavorite(LoadFavorites(), f)
			_ = SaveFavorites(m.favorites)
			return m.applyConnect(f), nil
		}
		return m.cancelConnect(), nil
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

// ── View ─────────────────────────────────────────────────────────────────────

func (m model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	var base string
	switch m.mode {
	case modeFavorites:
		base = m.viewFavorites()
	case modeConnect:
		base = m.viewConnect()
	case modeTransfer:
		base = m.viewTransfer()
	case modeExplorer:
		base = m.viewExplorer()
	case modePools:
		base = m.viewPools()
	default:
		base = m.viewBrowse()
	}
	switch {
	case m.pg != nil:
		return m.pg.view(m.width, m.height)
	case m.helpOn:
		return helpView(m.width, m.height)
	case m.pr != nil:
		return overlayCenter(m.pr.view(m.width), m.width, m.height)
	case m.sm != nil:
		return overlayCenter(m.sm.view(m.width, m.height), m.width, m.height)
	case m.cmdActive:
		return base + "\n" + titleStyle.Render(":") + " " + m.cmdIn.View()
	}
	return base
}

// header renders the shared top line: brand, host, platform, RO/RW, kldload.
func (m model) header(extra string) string {
	title := titleStyle.Render("zxplore")
	host := hostStyle.Render("  " + m.host.Label())
	plat := ""
	if m.platform != "" {
		plat = dimStyle.Render("  " + m.platform)
	}
	safety := "  " + roStyle.Render("RO")
	if m.rw {
		safety = "  " + rwStyle.Render("⚠ RW")
	}
	badge := ""
	if m.kldload {
		badge = "  " + badgeStyle.Render("kldload")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, title, host, plat, safety, badge, extra)
}

func (m model) viewBrowse() string {
	header := m.header("")
	bodyH := m.height - 4
	if bodyH < 3 {
		bodyH = 3
	}
	leftW := m.width * 45 / 100
	if leftW < 24 {
		leftW = 24
	}
	rightW := m.width - leftW - 4
	if rightW < 12 {
		rightW = 12
	}
	left := paneFocus.Width(leftW).Height(bodyH).Render(m.renderList(leftW, bodyH))
	right := paneStyle.Width(rightW).Height(bodyH).Render(m.renderDossier(rightW-2, bodyH))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	filterLine := ""
	if m.filterEdit {
		filterLine = " / " + m.filterIn.View()
	} else if m.filter != "" {
		filterLine = dimStyle.Render(" /" + m.filter + "  (/ edit · esc clear)")
	}
	foot := footerStyle.Render(" ↵ snapshots  x explorer  / filter  s snap  c connect  b bookmark  r reload   F2 transfer F4 pools  : cmd  ? help  q quit")
	if m.status != "" {
		foot = okStyle.Render(" "+m.status) + "\n" + foot
	}
	if filterLine != "" {
		foot = filterLine + "\n" + foot
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, foot)
}

func (m model) viewExplorer() string {
	header := m.header(dimStyle.Render("  · explorer"))
	body := m.exp.view(m.width, m.height-3)
	foot := footerStyle.Render(" ↵/l open  h up  tab versions  [/] time-travel  r copy  R over-live  d diff  esc back  ? help")
	return lipgloss.JoinVertical(lipgloss.Left, header, body, foot)
}

func (m model) viewPools() string {
	header := m.header(dimStyle.Render("  · pools"))
	body := m.pv.view(m.width, m.height-3)
	foot := footerStyle.Render(" ↵/d drill-down  s scrub  S stop  t trim  c clear  i importable  r reload  esc back  ? help")
	return lipgloss.JoinVertical(lipgloss.Left, header, body, foot)
}

func (m model) viewTransfer() string {
	header := m.header(dimStyle.Render("  · transfer"))
	if m.cmdr.status != "" {
		header += "   " + okStyle.Render(m.cmdr.status)
	}
	body := m.cmdr.view(m.width, m.height-2)
	foot := footerStyle.Render(" tab swap source/target  ↵ open  c connect pane  F5 replicate (needs :rw)  r reload  esc back  ? help")
	return lipgloss.JoinVertical(lipgloss.Left, header, body, foot)
}

func (m model) viewFavorites() string {
	title := titleStyle.Render("zxplore") + hostStyle.Render("  quick connect")
	var b strings.Builder
	if len(m.favorites) == 0 {
		b.WriteString(dimStyle.Render("  no saved connections yet — press n to add one\n"))
	}
	for i, f := range m.favorites {
		label := fmt.Sprintf("  %-40s %s", f.Name, dimStyle.Render(f.Target()))
		if i == m.favCursor {
			label = cursorStyle.Render(fmt.Sprintf(" %-*s", m.width-2, f.Name+"   "+f.Target()))
		}
		b.WriteString(label + "\n")
	}
	foot := footerStyle.Render(" ↵ connect   n new   d delete   esc back")
	box := paneFocus.Width(m.width - 2).Height(m.height - 4).Render(b.String())
	return lipgloss.JoinVertical(lipgloss.Left, title, box, foot)
}

func (m model) viewConnect() string {
	title := titleStyle.Render("zxplore") + hostStyle.Render("  new connection")
	prompt := "Dial a target — [user@]host:pool/path  (or a local pool/path):\n\n" + m.input.View()
	box := paneFocus.Width(m.width - 2).Height(m.height - 4).Render(prompt)
	foot := footerStyle.Render(" ↵ connect + save   esc cancel")
	return lipgloss.JoinVertical(lipgloss.Left, title, box, foot)
}

func (m model) renderList(w, h int) string {
	if m.err != "" {
		return dimStyle.Render(truncate("error: "+m.err, w))
	}
	ds := m.fds()
	if len(ds) == 0 {
		if m.filter != "" {
			return dimStyle.Render("(nothing matches /" + m.filter + ")")
		}
		return dimStyle.Render("(no datasets)")
	}
	top, end := window(m.cursor, len(ds), h)
	var b strings.Builder
	for i := top; i < end; i++ {
		d := ds[i]
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

func (m model) renderDossier(w, h int) string {
	lines := strings.Split(m.dossier, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		lines[i] = truncate(l, w)
	}
	return strings.Join(lines, "\n")
}

// renderDiffText renders zfs diff rows for the pager.
func renderDiffText(rows []DiffEntry) string {
	if len(rows) == 0 {
		return "(no differences)"
	}
	var b strings.Builder
	var mo, ad, rm, rn int
	for _, d := range rows {
		label := d.Change
		switch d.Change {
		case "M":
			label, mo = "M modified", mo+1
		case "+":
			label, ad = "+ added   ", ad+1
		case "-":
			label, rm = "- removed ", rm+1
		case "R":
			label, rn = "R renamed ", rn+1
		}
		b.WriteString(label + "  " + d.Path)
		if d.Extra != "" {
			b.WriteString("  →  " + d.Extra)
		}
		b.WriteByte('\n')
	}
	return fmt.Sprintf("%d changes · M %d · + %d · - %d · R %d\n\n%s",
		len(rows), mo, ad, rm, rn, b.String())
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

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
	for len(s) > 0 && lipgloss.Width(s)+1 > n {
		s = s[:len(s)-1]
	}
	return s + "…"
}

func padRight(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

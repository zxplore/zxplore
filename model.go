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
	pe         *propEditor // right-pane property editor (Tab in browse)
	pk         *picker     // enum/bool value picker (the TUI dropdown)
	dm         *dsMenu     // dataset lifecycle menu ("a")
	bm         *beMenu     // boot environments (":be" / "B")

	// async generations — stale background results are dropped
	scanGen  int
	dossGen  int
	landPath string // dataset to land on once the post-connect scan arrives
}

// ── async messages ───────────────────────────────────────────────────────────
// ZFS enumeration can cost SECONDS (12k snapshots ≈ 7s of kernel time), so
// NOTHING blocks the Update loop: the dataset list lands fast, snapshot
// counts and the dossier stream in behind it, and generation counters drop
// anything the cursor has already moved past.

type datasetsMsg struct {
	gen  int
	rows []Dataset
	err  error
}
type countsMsg struct {
	gen    int
	counts map[string]int
}
type dossierTick struct{ gen int }
type dossierMsg struct {
	gen  int
	text string
}

// startScan kicks off a background dataset scan (list fast, counts slow).
func (m *model) startScan() tea.Cmd {
	m.scanGen++
	gen := m.scanGen
	h := m.host
	m.status = "scanning…"
	return func() tea.Msg {
		rows, err := ListDatasets(h)
		return datasetsMsg{gen: gen, rows: rows, err: err}
	}
}

func (m model) fetchCountsCmd(gen int) tea.Cmd {
	h := m.host
	return func() tea.Msg {
		counts, _ := SnapshotCounts(h)
		return countsMsg{gen: gen, counts: counts}
	}
}

// scheduleDossier debounces dossier fetches: cursor moves just bump the
// generation; the fetch fires only after 120ms of stillness.
func (m *model) scheduleDossier() tea.Cmd {
	m.dossGen++
	gen := m.dossGen
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return dossierTick{gen} })
}

func (m model) fetchDossierCmd(gen int) tea.Cmd {
	h, cols := m.host, m.dossCols
	name := ""
	if d, ok := m.selDataset(); ok {
		name = d.Name
	}
	hadErr := m.err != ""
	return func() tea.Msg {
		switch {
		case name != "":
			return dossierMsg{gen: gen, text: DossierCols(h, name, cols)}
		case hadErr:
			return dossierMsg{gen: gen, text: "(no datasets)"}
		default:
			if wt := WelcomeText(DiagnoseHost(h)); wt != "" {
				return dossierMsg{gen: gen, text: wt}
			}
			return dossierMsg{gen: gen, text: "(no datasets)"}
		}
	}
}

// platformMsg refreshes the header chip after a connect (remote lookups are
// several ssh round-trips — never inline).
type platformMsg struct {
	gen  int
	text string
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
	m.dossier = "…"
	m.scanGen = 1 // Init()'s scan carries this generation
	m.status = "scanning…"
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

// Init fires the first scan. newModel pre-set scanGen/status for it — Init
// cannot mutate the model (bubbletea only keeps its returned Cmd).
func (m model) Init() tea.Cmd {
	h := m.host
	gen := m.scanGen
	return func() tea.Msg {
		rows, err := ListDatasets(h)
		return datasetsMsg{gen: gen, rows: rows, err: err}
	}
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
// Everything network-ish happens in background cmds.
func (m model) connect(f Favorite) (model, tea.Cmd) {
	m.host = f.Host()
	m.platform = ""
	m.cursor = 0
	m.filter = ""
	m.landPath = f.Path
	m.status = "connecting " + f.Target() + "…"
	scan := (&m).startScan()
	gen := m.scanGen
	h := m.host
	plat := func() tea.Msg { return platformMsg{gen: gen, text: HostPlatform(h)} }
	return m, tea.Batch(scan, plat)
}

// applyConnect applies a chosen favorite/target: to a commander pane if we
// entered connect from the commander, else to the browser itself.
func (m model) applyConnect(f Favorite) (model, tea.Cmd) {
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
		return m, nil
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
		return m, (&m).startScan()
	case "rollback":
		report("rolled back to "+get(0), Rollback(m.host, get(0)))
		return m, (&m).startScan()
	case "clone":
		report("cloned → "+text, Clone(m.host, get(0), strings.TrimSpace(text)))
		return m, (&m).startScan()
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
	case "create-ds":
		report("created "+get(0)+"/"+text, CreateDataset(m.host, get(0)+"/"+strings.TrimSpace(text), ""))
		return m, (&m).startScan()
	case "create-zvol-name":
		// chain: got the name, now ask the size
		m.pr = newPrompt(pkInput, "size for "+get(0)+"/"+text,
			"Volume size (e.g. 10G):", "", "10G", "create-zvol", get(0), strings.TrimSpace(text))
		return m, nil
	case "create-zvol":
		report("created zvol "+get(0)+"/"+get(1), CreateDataset(m.host, get(0)+"/"+get(1), strings.TrimSpace(text)))
		return m, (&m).startScan()
	case "rename-ds":
		report("renamed → "+text, RenameDataset(m.host, get(0), strings.TrimSpace(text)))
		return m, (&m).startScan()
	case "mount-ds":
		report("mounted "+get(0), SetMounted(m.host, get(0), true))
		return m, (&m).scheduleDossier()
	case "unmount-ds":
		report("unmounted "+get(0), SetMounted(m.host, get(0), false))
		return m, (&m).scheduleDossier()
	case "loadkey":
		report("unlocked "+get(0), LoadKey(m.host, get(0), text))
		return m, (&m).scheduleDossier()
	case "unloadkey":
		report("locked "+get(0), UnloadKey(m.host, get(0)))
		return m, (&m).scheduleDossier()
	case "changekey":
		report("passphrase changed on "+get(0), ChangeKey(m.host, get(0), text))
	case "create-enc-name":
		m.pr = newSecretPrompt("passphrase for "+get(0)+"/"+text,
			"Passphrase for the new encrypted dataset:", "create-enc", get(0), strings.TrimSpace(text))
		return m, nil
	case "create-enc":
		report("created encrypted "+get(0)+"/"+get(1), CreateEncrypted(m.host, get(0)+"/"+get(1), text))
		return m, (&m).startScan()
	case "destroy-ds":
		report("destroyed "+get(0), DestroyDataset(m.host, get(0)))
		return m, (&m).startScan()
	case "be-create":
		report("boot environment @"+text, CreateBootEnv(m.host, strings.TrimSpace(text)))
		if nb, err := newBeMenu(m.host); err == nil {
			m.bm = nb
		}
	case "be-rollback":
		report("boot dataset rolled back — takes effect on REBOOT", RollbackBootEnv(m.host, get(0)))
		if nb, err := newBeMenu(m.host); err == nil {
			m.bm = nb
		}
	case "be-delete":
		report("deleted "+get(0), DeleteBootEnv(m.host, get(0)))
		if nb, err := newBeMenu(m.host); err == nil {
			m.bm = nb
		}
	case "diff2":
		from := get(0) + "@" + text
		to := get(0) + "@" + get(1)
		rows, err := SnapshotDiff(m.host, from, to)
		if err != nil {
			m.status = "✗ diff: " + err.Error()
			return m, nil
		}
		m.pg = newPager("zfs diff "+from+" → @"+get(1), renderDiffText(rows))
	case "setprop":
		val := text
		if val == "" {
			val = get(2)
		}
		report("set "+get(1)+"="+val+" on "+get(0), SetProp(m.host, get(0), get(1), val))
		if m.pe != nil && m.pe.ds == get(0) {
			m.pe.refresh(m.host)
		}
		return m, (&m).scheduleDossier()
	case "importpool":
		report("imported "+text, ImportPool(m.host, strings.TrimSpace(text)))
		if m.pv != nil {
			m.pv.reload()
		}
		return m, (&m).startScan()
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
			return m, (&m).scheduleDossier()
		}
		return m, nil
	case datasetsMsg:
		if msg.gen != m.scanGen {
			return m, nil
		}
		m.datasets = msg.rows
		if msg.err != nil {
			m.err = msg.err.Error()
			m.status = "✗ " + m.err
		} else {
			m.err = ""
			m.status = ""
		}
		if m.landPath != "" {
			for i, d := range m.fds() {
				if d.Name == m.landPath {
					m.cursor = i
					break
				}
			}
			m.landPath = ""
			m.status = "connected: " + m.host.Label()
		}
		m.clampCursor()
		return m, tea.Batch(m.fetchCountsCmd(msg.gen), (&m).scheduleDossier())
	case countsMsg:
		if msg.gen != m.scanGen || msg.counts == nil {
			return m, nil
		}
		for i := range m.datasets {
			if c, ok := msg.counts[m.datasets[i].Name]; ok {
				m.datasets[i].Snaps = c
			} else {
				m.datasets[i].Snaps = 0
			}
		}
		return m, nil
	case dossierTick:
		if msg.gen != m.dossGen {
			return m, nil
		}
		return m, m.fetchDossierCmd(msg.gen)
	case dossierMsg:
		if msg.gen == m.dossGen {
			m.dossier = msg.text
		}
		return m, nil
	case platformMsg:
		if msg.gen == m.scanGen {
			m.platform = msg.text
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
		if m.pk != nil {
			return m.updatePicker(msg)
		}
		if m.cmdActive {
			return m.updateCommand(msg)
		}
		if m.filterEdit {
			mm, cmd := m.updateFilter(msg)
			return mm, cmd
		}
		if m.sm != nil {
			return m.updateSnapMenu(msg)
		}
		if m.dm != nil {
			return m.updateDsMenu(msg)
		}
		if m.bm != nil {
			return m.updateBeMenu(msg)
		}
		switch m.mode {
		case modeFavorites:
			return m.updateFavorites(msg)
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
		return m, (&m).startScan(), true
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
	// right pane focused → the property editor owns movement + Enter
	if m.pe != nil {
		return m.updatePropEdit(msg)
	}
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "tab":
		if d, ok := m.selDataset(); ok {
			pe, err := newPropEditor(m.host, d.Name)
			if err != nil {
				m.status = "✗ " + err.Error()
				return m, nil
			}
			m.pe = pe
			m.status = "editing properties — ↵ change · tab/esc back to the list"
		}
	case "down", "j":
		if m.cursor < len(m.fds())-1 {
			m.cursor++
			return m, (&m).scheduleDossier()
		}
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			return m, (&m).scheduleDossier()
		}
	case "ctrl+d":
		m.cursor += page
		m.clampCursor()
		return m, (&m).scheduleDossier()
	case "ctrl+u":
		m.cursor -= page
		m.clampCursor()
		return m, (&m).scheduleDossier()
	case "g", "home":
		m.cursor = 0
		return m, (&m).scheduleDossier()
	case "G", "end":
		if n := len(m.fds()); n > 0 {
			m.cursor = n - 1
			return m, (&m).scheduleDossier()
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
	case "a":
		if d, ok := m.selDataset(); ok {
			m.dm = &dsMenu{ds: d.Name}
		}
	case "B":
		bm, err := newBeMenu(m.host)
		if err != nil {
			m.status = "✗ " + err.Error()
			return m, nil
		}
		m.bm = bm
	case "r":
		return m, (&m).startScan()
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

// updateDsMenu drives the dataset lifecycle menu ("a"): create / rename /
// mount / encryption / destroy. Mutations gate on :rw; destroy is typed.
func (m model) updateDsMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	dm := m.dm
	switch msg.String() {
	case "esc", "q":
		m.dm = nil
	case "down", "j":
		if dm.cursor < len(dsMenuActs)-1 {
			dm.cursor++
		}
	case "up", "k":
		if dm.cursor > 0 {
			dm.cursor--
		}
	case "enter":
		ds := dm.ds
		if !m.requireRW() {
			return m, nil
		}
		m.dm = nil
		switch dm.cursor {
		case 0:
			m.pr = newPrompt(pkInput, "create dataset under "+ds,
				"Name of the new child:", "", "name", "create-ds", ds)
		case 1:
			m.pr = newPrompt(pkInput, "create volume under "+ds,
				"Name of the new zvol:", "", "name", "create-zvol-name", ds)
		case 2:
			pr := newPrompt(pkInput, "rename "+ds, "New full name:", "",
				ds, "rename-ds", ds)
			pr.input.SetValue(ds)
			m.pr = pr
		case 3:
			return m.dispatch("mount-ds", []string{ds}, "")
		case 4:
			return m.dispatch("unmount-ds", []string{ds}, "")
		case 5:
			m.pr = newSecretPrompt("unlock "+ds,
				"Passphrase (travels on stdin, never argv):", "loadkey", ds)
		case 6:
			return m.dispatch("unloadkey", []string{ds}, "")
		case 7:
			m.pr = newSecretPrompt("change passphrase for "+ds,
				"New passphrase:", "changekey", ds)
		case 8:
			m.pr = newPrompt(pkInput, "create ENCRYPTED dataset under "+ds,
				"Name of the new encrypted child:", "", "name", "create-enc-name", ds)
		case 9:
			m.pr = newPrompt(pkTyped, "✖ destroy "+ds,
				"Recursively destroys the dataset, its children,\nand every snapshot of them.",
				ds, "", "destroy-ds", ds)
		}
	}
	return m, nil
}

// updateBeMenu drives boot environments: c create, R roll back, D delete.
func (m model) updateBeMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	bm := m.bm
	switch msg.String() {
	case "esc", "q":
		m.bm = nil
	case "down", "j":
		if bm.cursor < len(bm.bes)-1 {
			bm.cursor++
		}
	case "up", "k":
		if bm.cursor > 0 {
			bm.cursor--
		}
	case "r":
		if nb, err := newBeMenu(m.host); err == nil {
			nb.cursor = bm.cursor
			if nb.cursor >= len(nb.bes) {
				nb.cursor = len(nb.bes) - 1
			}
			m.bm = nb
		}
	case "c":
		if !m.requireRW() {
			return m, nil
		}
		m.pr = newPrompt(pkInput, "create boot environment",
			"Snapshot of "+bm.bd+" — a restore point for the OS:", "",
			"be name", "be-create")
	case "R":
		be, ok := bm.current()
		if !ok || !m.requireRW() {
			return m, nil
		}
		short := snapShort(be.Snapshot)
		m.pr = newPrompt(pkTyped, "⚠ roll back the BOOT dataset",
			"Rolls "+bm.bd+" back to @"+short+".\nTakes effect on REBOOT and destroys newer boot environments.",
			short, "", "be-rollback", be.Snapshot)
	case "D":
		be, ok := bm.current()
		if !ok || !m.requireRW() {
			return m, nil
		}
		short := snapShort(be.Snapshot)
		m.pr = newPrompt(pkTyped, "✖ delete boot environment",
			"Permanently deletes @"+short+".", short, "", "be-delete", be.Snapshot)
	}
	return m, nil
}

// updatePropEdit drives the right-pane property editor (Tab in browse):
// arrows walk the settable properties, Enter edits with the GUI's option
// lists, tab/esc return to the dataset list.
func (m model) updatePropEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	pe := m.pe
	switch msg.String() {
	case "tab", "esc", "q", "h":
		m.pe = nil
		m.status = ""
	case "down", "j":
		pe.move(1)
	case "up", "k":
		pe.move(-1)
	case "g":
		pe.cursor = pe.firstSelectable()
	case "G":
		pe.cursor = len(pe.rows) - 1
		if pe.rows[pe.cursor].header != "" {
			pe.move(-1)
		}
	case "?":
		m.helpOn = true
	case "enter":
		p, ok := pe.current()
		if !ok {
			return m, nil
		}
		if !m.requireRW() {
			return m, nil
		}
		switch p.Control.Kind {
		case "bool", "enum":
			cur := 0
			for i, o := range p.Control.Options {
				if o == p.Value {
					cur = i
				}
			}
			m.pk = &picker{title: p.Name + " = ?  (now " + p.Value + ")",
				options: p.Control.Options, cursor: cur,
				action: "setprop", payload: []string{pe.ds, p.Name}}
		default:
			pr := newPrompt(pkInput, "set "+p.Name+" on "+pe.ds,
				"Current: "+p.Value+"  ("+p.Source+")", "", p.Value,
				"setprop", pe.ds, p.Name)
			pr.input.SetValue(p.Value)
			m.pr = pr
		}
	}
	return m, nil
}

// updatePicker drives the enum/bool value picker. Risky properties detour
// through a y/n confirm carrying the chosen value in the payload.
func (m model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	pk := m.pk
	switch msg.String() {
	case "esc", "q":
		m.pk = nil
		m.status = "cancelled"
	case "down", "j":
		if pk.cursor < len(pk.options)-1 {
			pk.cursor++
		}
	case "up", "k":
		if pk.cursor > 0 {
			pk.cursor--
		}
	case "enter":
		choice := pk.options[pk.cursor]
		action, payload := pk.action, pk.payload
		m.pk = nil
		if action == "setprop" && len(payload) >= 2 && riskyProps[payload[1]] {
			m.pr = newPrompt(pkConfirm, "⚠ set "+payload[1]+" = "+choice,
				"On "+payload[0]+" — this can unmount data, break boot,\nor hide a filesystem. Apply?",
				"", "", "setprop", payload[0], payload[1], choice)
			return m, nil
		}
		return m.dispatch(action, payload, choice)
	}
	return m, nil
}

func (m model) updateFilter(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.filterEdit = false
		return m, (&m).scheduleDossier()
	case "esc":
		m.filterEdit = false
		m.filter = ""
		m.filterIn.SetValue("")
		m.clampCursor()
		return m, (&m).scheduleDossier()
	default:
		var cmd tea.Cmd
		m.filterIn, cmd = m.filterIn.Update(msg)
		_ = cmd
		m.filter = m.filterIn.Value()
		m.cursor = 0
	}
	return m, nil
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
		return m, (&m).startScan()
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
		mm, cmd := m.applyConnect(fav)
		return mm, cmd
	case "importpool":
		if !m.requireRW() {
			return m, nil
		}
		if arg == "" {
			m.status = "usage: :importpool <name>   (press i in :pools to scan)"
			return m, nil
		}
		return m.dispatch("importpool", nil, arg)
	case "be":
		bm, err := newBeMenu(m.host)
		if err != nil {
			m.status = "✗ " + err.Error()
			return m, nil
		}
		m.bm = bm
	case "rw":
		m.rw = true
		m.status = "⚠ READ-WRITE — mutations enabled (:ro to relock)"
	case "ro":
		m.rw = false
		m.status = "read-only locked"
	case "refresh":
		return m, (&m).startScan()
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
			if p.kind == pkInput && p.action == "setprop" &&
				len(p.payload) >= 2 && riskyProps[p.payload[1]] {
				m.pr = newPrompt(pkConfirm, "⚠ set "+p.payload[1]+" = "+v,
					"On "+p.payload[0]+" — this can unmount data, break boot,\nor hide a filesystem. Apply?",
					"", "", "setprop", p.payload[0], p.payload[1], v)
				return m, nil
			}
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
		case 2: // diff against another snapshot — picker of the rest
			var opts []string
			for _, o := range sm.snaps {
				if o.Name != s.Name {
					opts = append(opts, snapShort(o.Name))
				}
			}
			if len(opts) == 0 {
				m.status = "no other snapshot to diff against"
				return m, nil
			}
			m.pk = &picker{title: "diff @" + short + " → @?", options: opts,
				action: "diff2", payload: []string{sm.ds, short}}
		case 3: // clone
			if !m.requireRW() {
				return m, nil
			}
			m.pr = newPrompt(pkInput, "clone @"+short, "New dataset name:", "",
				"pool/new-dataset", "clone", s.Name)
		case 4: // bookmark
			if !m.requireRW() {
				return m, nil
			}
			m.pr = newPrompt(pkInput, "bookmark @"+short,
				"Bookmark name (→ "+sm.ds+"#…):", "", short, "bookmark", s.Name)
		case 5:
			if !m.requireRW() {
				return m, nil
			}
			return m.dispatch("hold", []string{s.Name}, "")
		case 6:
			if !m.requireRW() {
				return m, nil
			}
			return m.dispatch("release", []string{s.Name}, "")
		case 7: // rollback — typed confirm on the DATASET name
			if !m.requireRW() {
				return m, nil
			}
			m.pr = newPrompt(pkTyped, "⚠ roll back "+sm.ds,
				"Rolls back to @"+short+" and DESTROYS every newer snapshot\n(and their clones).",
				sm.ds, "", "rollback", s.Name)
		case 8: // destroy — typed confirm on the snapshot short name
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
	if m.pg.filtering {
		switch msg.String() {
		case "enter":
			m.pg.filtering = false
		case "esc":
			m.pg.filtering = false
			m.pg.q = ""
			m.pg.fin.SetValue("")
		default:
			m.pg.fin, _ = m.pg.fin.Update(msg)
			m.pg.q = m.pg.fin.Value()
			m.pg.off = 0
		}
		return m
	}
	switch msg.String() {
	case "/":
		m.pg.filtering = true
		m.pg.fin.Focus()
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

func (m model) updateFavorites(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "c", "q":
		return m.cancelConnect(), nil
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
	return m, nil
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
			mm, cmd := m.applyConnect(f)
			return mm, cmd
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
	case m.pk != nil:
		return overlayCenter(m.pk.view(m.width), m.width, m.height)
	case m.sm != nil:
		return overlayCenter(m.sm.view(m.width, m.height), m.width, m.height)
	case m.dm != nil:
		return overlayCenter(m.dm.view(m.width), m.width, m.height)
	case m.bm != nil:
		return overlayCenter(m.bm.view(m.width, m.height), m.width, m.height)
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
	leftSt, rightSt := paneFocus, paneStyle
	rightBody := m.renderDossier(rightW-2, bodyH)
	if m.pe != nil {
		leftSt, rightSt = paneStyle, paneFocus
		rightBody = m.pe.view(rightW-2, bodyH)
	}
	left := leftSt.Width(leftW).Height(bodyH).Render(m.renderList(leftW, bodyH))
	right := rightSt.Width(rightW).Height(bodyH).Render(rightBody)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	filterLine := ""
	if m.filterEdit {
		filterLine = " / " + m.filterIn.View()
	} else if m.filter != "" {
		filterLine = dimStyle.Render(" /" + m.filter + "  (/ edit · esc clear)")
	}
	foot := footerStyle.Render(" ↵ snapshots  a dataset actions  tab edit props  x explorer  B boot envs  / filter  s snap  c connect  r reload  F2/F4 : cmd  ? help  q quit")
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
		meta := fmt.Sprintf("%s/%s", d.Used, d.Refer)
		if d.Snaps >= 0 {
			meta += fmt.Sprintf(" ×%d", d.Snaps)
		}
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

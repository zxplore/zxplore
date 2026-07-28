//go:build gui

// gui.go — the native GUI (Fyne). Default surface for zxplore.
//
// A real window: keyboard navigation AND mouse (click, scroll, right-click),
// copy-paste, and its own icon — all native, no terminal involved. Shares the
// zfs.go engine with the --tui mode. Layout, top→bottom:
//   - title row (host/kldload badge + ASCII wordmark, right)
//   - ZPOOLS machine overview (every pool's vitals, pinned)
//   - tabs: Browser (find + dataset list │ dossier) and Transfer
//   - tmux-style help bar (key hints)
package main

import (
	_ "embed"
	"fmt"
	"image/color"
	"net/url"
	"regexp"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

//go:embed assets/zxplore.svg
var iconSVG []byte

// repoURL / siteURL back the top-right links. repoURL points at the project site
// (operator owns zxplore.dev); there's no public git remote yet.
const (
	repoURL = "https://zxplore.dev"
	siteURL = "https://kldload.com"
)

// helpHints is the tmux-style status line along the bottom. Keep it TRUE — only
// keys/gestures that actually work, so it stays a contract, not decoration.
const helpHints = "  F1 browser   F2 transfer    ↑↓ move   PgUp/PgDn page   Ctrl+F or / find   right-click = actions   Alt+Q quit  "

// navPage is how many rows PgUp/PgDn jump.
const navPage = 12

// ── theme ──────────────────────────────────────────────────────────────────
// compactTheme keeps list rows tight (small inner padding) but gives the major
// regions breathing room (larger between-widget padding), brands the accent
// zxplore-teal, tints the light background a soft teal off-white (plain white
// read as flat), warms the dark background to a brand blue-grey, and turns the
// selection bar teal. Everything else delegates to GNOME's light/dark variant.
type compactTheme struct{ fyne.Theme }

func (t compactTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameInnerPadding:
		return 3 // tight rows (single-spaced list)
	case theme.SizeNamePadding:
		return 6 // space between panes / regions
	case snDossier:
		return t.Theme.Size(theme.SizeNameText) * 1.25 // dossier reads +25%
	}
	return t.Theme.Size(name)
}

// snDossier is a custom theme size for the dossier/properties pane — resolved
// to 125% of the base text size so the dense property grid stays readable.
const snDossier fyne.ThemeSizeName = "zxploreDossier"

func (t compactTheme) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	dark := v == theme.VariantDark
	switch name {
	case theme.ColorNamePrimary, theme.ColorNameHyperlink:
		if dark {
			return acGreen.dark // electric green
		}
		return acGreen.light // deep green
	case theme.ColorNameSelection:
		if dark {
			return color.NRGBA{R: 0x2a, G: 0x3a, B: 0x48, A: 0xff} // steel-blue highlight
		}
		return color.NRGBA{R: 0xba, G: 0xf5, B: 0xce, A: 0xff} // bright light-green
	case theme.ColorNameBackground:
		if dark {
			return color.NRGBA{R: 0x08, G: 0x09, B: 0x0c, A: 0xff} // very dark steel-black — the base "between the separators"
		}
		return color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff} // pure bright white
	case theme.ColorNameInputBackground:
		if dark {
			return color.NRGBA{R: 0x13, G: 0x18, B: 0x20, A: 0xff}
		}
		return color.NRGBA{R: 0xf1, G: 0xf6, B: 0xf3, A: 0xff}
	case theme.ColorNameForeground:
		if dark {
			return color.NRGBA{R: 0xe3, G: 0xe9, B: 0xef, A: 0xff} // crisp steel-white — pops on the dark steel
		}
		return color.NRGBA{R: 0x18, G: 0x1e, B: 0x1a, A: 0xff} // near-black on white
	case cnTopic:
		if dark {
			return acTopic.dark
		}
		return acTopic.light
	}
	return t.Theme.Color(name, v)
}

// Bold section-accent palette — the look is "old-school ANSI BBS, modernized":
// ELECTRIC/neon on the near-black dark theme (alive), DEEP-saturated on the
// bright light theme (colors that cut through white). Each accent carries both,
// picked by the active theme variant.
type accentPair struct{ dark, light color.NRGBA }

func (a accentPair) at() color.Color {
	if variantDark() {
		return a.dark
	}
	return a.light
}

// repaint holds recolor closures for the hand-colored canvas objects (cards,
// headings). They can't read the theme automatically, and the GNOME light/dark
// variant may not be resolved when they're first built — so we register a
// recolor for each and re-run them via applyPalette() once the variant is known
// and again whenever it changes (the theme toggle). Fixes "dark boxes on a white
// app" from baking the wrong variant at build time.
var repaint []func()

func applyPalette() {
	for _, f := range repaint {
		f()
	}
}

var (
	acGreen  = accentPair{color.NRGBA{0x3d, 0xff, 0x88, 0xff}, color.NRGBA{0x0e, 0x9d, 0x4a, 0xff}}
	acGold   = accentPair{color.NRGBA{0xff, 0xd0, 0x43, 0xff}, color.NRGBA{0xb0, 0x7d, 0x00, 0xff}}
	acBlue   = accentPair{color.NRGBA{0x4d, 0xa6, 0xff, 0xff}, color.NRGBA{0x14, 0x66, 0xd8, 0xff}}
	acPurple = accentPair{color.NRGBA{0xc7, 0x7d, 0xff, 0xff}, color.NRGBA{0x7a, 0x2f, 0xe0, 0xff}}
	acCyan   = accentPair{color.NRGBA{0x33, 0xe6, 0xe6, 0xff}, color.NRGBA{0x0a, 0x8f, 0x9c, 0xff}}
	acYellow = accentPair{color.NRGBA{0xff, 0xe1, 0x4d, 0xff}, color.NRGBA{0xb5, 0x83, 0x00, 0xff}} // warning
	acRed    = accentPair{color.NRGBA{0xff, 0x5c, 0x5c, 0xff}, color.NRGBA{0xd1, 0x1f, 0x1f, 0xff}} // error/danger
	acTopic  = accentPair{color.NRGBA{0x62, 0xa6, 0xe6, 0xff}, color.NRGBA{0x2a, 0x63, 0xc8, 0xff}} // dossier ━━ headers (slightly duller blue)
)

// cnTopic is a custom theme color name the compactTheme resolves to acTopic, so
// dossier ━━ section headers render blue (and adapt to light/dark) via RichText.
const cnTopic fyne.ThemeColorName = "zxploreTopic"

// topicRE matches a "━━ TITLE ━━" run so those spans can be colored while the
// rest of the (packed, multi-column) dossier line stays default foreground.
var topicRE = regexp.MustCompile(`━+[^━]*━+`)

// dossierSegments turns the plain dossier text into RichText segments, coloring
// the ━━ topic headers blue (cnTopic) and everything else default foreground.
// All monospace, so the packed 4-column alignment is preserved.
func dossierSegments(text string) []widget.RichTextSegment {
	mono := fyne.TextStyle{Monospace: true}
	// Inline:true so runs flow on ONE row (default false makes each a block →
	// everything stacks vertically); \n segments break lines, preserving the
	// packed multi-column grid.
	seg := func(s string, cn fyne.ThemeColorName) *widget.TextSegment {
		return &widget.TextSegment{Text: s, Style: widget.RichTextStyle{
			Inline: true, TextStyle: mono, ColorName: cn, SizeName: snDossier}}
	}
	var out []widget.RichTextSegment
	for _, line := range strings.Split(text, "\n") {
		pos := 0
		for _, m := range topicRE.FindAllStringIndex(line, -1) {
			if m[0] > pos {
				out = append(out, seg(line[pos:m[0]], theme.ColorNameForeground))
			}
			out = append(out, seg(line[m[0]:m[1]], cnTopic))
			pos = m[1]
		}
		if pos < len(line) {
			out = append(out, seg(line[pos:], theme.ColorNameForeground))
		}
		out = append(out, seg("\n", theme.ColorNameForeground))
	}
	return out
}

// heading is a bold, theme-aware colored section title (canvas.Text takes an
// arbitrary color; widget.Label can't).
func heading(text string, a accentPair) *canvas.Text {
	t := canvas.NewText(text, a.at())
	t.TextStyle = fyne.TextStyle{Bold: true}
	t.TextSize = 14
	repaint = append(repaint, func() { t.Color = a.at(); t.Refresh() })
	return t
}

// dialogHeading / paneCard are heading()/card() for SHORT-LIVED surfaces
// (dialogs, secondary windows): same look, but no repaint registration —
// registering would leak one closure per open, and a dialog never outlives a
// theme flip long enough to matter.
func dialogHeading(text string, a accentPair) *canvas.Text {
	t := canvas.NewText(text, a.at())
	t.TextStyle = fyne.TextStyle{Bold: true}
	t.TextSize = 14
	return t
}

func paneCard(content fyne.CanvasObject) fyne.CanvasObject {
	r := canvas.NewRectangle(cardColor())
	r.CornerRadius = 8
	return container.NewStack(r, container.NewPadded(content))
}

func variantDark() bool {
	return fyne.CurrentApp() != nil && fyne.CurrentApp().Settings().ThemeVariant() == theme.VariantDark
}

// cardColor is the panel backdrop — lifted just off the window base for depth.
func cardColor() color.Color {
	if variantDark() {
		return color.NRGBA{R: 0x15, G: 0x1b, B: 0x23, A: 0xff} // dark steel — a step brighter than the #08090c base
	}
	return color.NRGBA{R: 0xf3, G: 0xf7, B: 0xf4, A: 0xff} // faint panel on white
}

// card wraps a section in its own rounded backdrop so sections read as separated
// panels with gaps between them. (Fyne can't make the window transparent to the
// desktop, so this is a layered look, not true see-through.)
func card(content fyne.CanvasObject) fyne.CanvasObject {
	r := canvas.NewRectangle(cardColor())
	r.CornerRadius = 8
	repaint = append(repaint, func() { r.FillColor = cardColor(); r.Refresh() })
	return container.NewStack(r, container.NewPadded(content))
}

// ── navList ──────────────────────────────────────────────────────────────────
// navList extends widget.List with the keys Fyne's List omits: PgUp/PgDn and
// Home/End (they MOVE the selection, firing OnSelected → dossier) plus "/" to
// open find. Up/Down/Space fall through to the native handler. cursor mirrors
// the current selection (the parent keeps it in sync from OnSelected).
type navList struct {
	widget.List
	cursor      int
	onFind      func()
	onFunc      func(fyne.KeyName)     // F-key section switch
	onEnter     func()                 // Enter/Return acts on the current row
	onSecondary func(*fyne.PointEvent) // right-click → context menu
}

// TappedSecondary fires on right-click. Row labels don't handle it, so Fyne
// routes it here; we act on the current (hover-selected) row.
func (l *navList) TappedSecondary(e *fyne.PointEvent) {
	if l.onSecondary != nil {
		l.onSecondary(e)
	}
}

func newNavList(length func() int, create func() fyne.CanvasObject, update func(widget.ListItemID, fyne.CanvasObject)) *navList {
	l := &navList{}
	l.List.Length = length
	l.List.CreateItem = create
	l.List.UpdateItem = update
	l.ExtendBaseWidget(l)
	return l
}

func (l *navList) count() int {
	if l.List.Length == nil {
		return 0
	}
	return l.List.Length()
}

// selectAt clamps i to the list and selects it (firing OnSelected → dossier),
// scrolling it into view.
func (l *navList) selectAt(i int) {
	n := l.count()
	if n == 0 {
		return
	}
	if i < 0 {
		i = 0
	}
	if i > n-1 {
		i = n - 1
	}
	l.Select(i)
	l.ScrollTo(i)
}

func (l *navList) TypedKey(e *fyne.KeyEvent) {
	switch e.Name {
	case fyne.KeyPageDown:
		l.selectAt(l.cursor + navPage)
	case fyne.KeyPageUp:
		l.selectAt(l.cursor - navPage)
	case fyne.KeyHome:
		l.selectAt(0)
	case fyne.KeyEnd:
		l.selectAt(l.count() - 1)
	case fyne.KeySlash:
		if l.onFind != nil {
			l.onFind()
		}
	case fyne.KeyF1, fyne.KeyF2, fyne.KeyF3, fyne.KeyF4:
		if l.onFunc != nil {
			l.onFunc(e.Name)
		}
	case fyne.KeyReturn, fyne.KeyEnter:
		if l.onEnter != nil {
			l.onEnter()
		}
	default:
		l.List.TypedKey(e) // native ↑/↓/Space
	}
}

// ── window ─────────────────────────────────────────────────────────────────
func runGUI() {
	a := app.NewWithID("ca.zxplore")
	a.Settings().SetTheme(compactTheme{theme.DefaultTheme()})
	a.SetIcon(fyne.NewStaticResource("zxplore.svg", iconSVG))

	host := LocalHost()
	w := a.NewWindow("zxplore — ZFS console")
	// Normal window (title bar + min/max/close). Fyne has no Maximize() API, but
	// opening larger than the work area triggers GNOME's auto-maximize.
	w.Resize(fyne.NewSize(1920, 1200))
	w.CenterOnScreen()

	// Data loads asynchronously AFTER the window paints (a splash covers the
	// first scan) — zfs/zpool enumeration can take seconds and must never block
	// first pixel.
	var all []Dataset
	var listErr error
	var visible []Dataset // the filtered view the list renders

	// Dossier: a selectable monospace Label (SetText reliably repaints and follows
	// the theme) inside a Scroll — no RichText/scroll refresh fight.
	// RichText (no internal Scroll — wrapped in container.Scroll) so the ━━ topic
	// headers can be colored blue while the body stays steel-white. Segments+Refresh
	// is the same repaint path as ParseMarkdown, so it updates reliably.
	dossier := widget.NewRichText()
	dossier.Wrapping = fyne.TextWrapOff
	dossierScroll := container.NewScroll(dossier)
	renderDossier := func(s string) {
		dossier.Segments = dossierSegments(s)
		dossier.Refresh()
	}

	lastShown := -1
	dossierGen := 0 // drops stale async renders when the selection moves on
	setDossier := func(i int) {
		if i == lastShown {
			return
		}
		lastShown = i
		dossierGen++
		if i < 0 || i >= len(visible) {
			renderDossier("")
			return
		}
		name := visible[i].Name
		gen := dossierGen
		renderDossier("… " + name)
		// Dossier makes several zfs/zpool calls (seconds over ssh) — fetch off
		// the UI thread so arrowing through the list never stutters.
		go func() {
			text := Dossier(host, name)
			fyne.Do(func() {
				if gen == dossierGen {
					renderDossier(text)
				}
			})
		}()
	}

	list := newNavList(
		func() int { return len(visible) },
		func() fyne.CanvasObject { return widget.NewLabel("template") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			d := visible[i]
			o.(*widget.Label).SetText(fmt.Sprintf("%s    %s / %s   ×%d", d.Name, d.Used, d.Refer, d.Snaps))
		},
	)
	// OnHighlighted (↑/↓ and hover) forwards into Select so the blue bar itself
	// moves; OnSelected is wired later (it also drives the edit form), once the
	// edit-toggle state exists.
	list.OnHighlighted = func(i widget.ListItemID) { list.Select(i) }

	// Find: "/" focuses this entry; typing filters the list live; Enter returns
	// focus to the list. Substring match on the dataset name (case-insensitive).
	search := widget.NewEntry()
	search.SetPlaceHolder("filter datasets…  (press / )")
	applyFilter := func(q string) {
		q = strings.ToLower(strings.TrimSpace(q))
		visible = visible[:0]
		for _, d := range all {
			if q == "" || strings.Contains(strings.ToLower(d.Name), q) {
				visible = append(visible, d)
			}
		}
		lastShown = -1
		list.UnselectAll()
		list.Refresh()
		if len(visible) > 0 {
			list.selectAt(0)
		} else {
			renderDossier("(no datasets match \"" + q + "\")")
		}
	}
	search.OnChanged = applyFilter
	search.OnSubmitted = func(string) { w.Canvas().Focus(list) }
	list.onFind = func() { w.Canvas().Focus(search) }

	// ZPOOLS machine overview, pinned at the top.
	poolsLabel := widget.NewLabel("… scanning pools")
	poolsLabel.TextStyle = fyne.TextStyle{Monospace: true}

	// applyLoad lands a finished scan on the UI thread; reload runs one in the
	// background (refresh button, post-mutation refreshes). diag explains an
	// EMPTY result (no ZFS installed / no pools imported) so the browser never
	// sits there blank — it shows the way out instead.
	applyLoad := func(rows []Dataset, err error, pools string, diag HostDiagnosis) {
		all, listErr = rows, err
		poolsLabel.SetText(pools)
		if listErr != nil {
			renderDossier("cannot list datasets:\n" + listErr.Error() +
				"\n\n(zxplore needs permission to read ZFS — run it as root, or grant\n" +
				" `zfs allow` on the pool.)")
			return
		}
		applyFilter(search.Text) // rebuild visible + select row 0
		if len(all) == 0 && diag != HostOK {
			renderDossier(WelcomeText(diag))
		}
	}
	// scan gathers everything applyLoad needs, off the UI thread.
	scan := func() ([]Dataset, error, string, HostDiagnosis) {
		rows, err := ListDatasets(host)
		pools := PoolsOverview(host)
		diag := HostOK
		if err == nil && len(rows) == 0 {
			diag = DiagnoseHost(host)
		}
		return rows, err, pools, diag
	}
	reload := func() {
		go func() {
			rows, err, pools, diag := scan()
			fyne.Do(func() { applyLoad(rows, err, pools, diag) })
		}()
	}

	// ── top: title row (status + kldload flare left, wordmark right) ──
	status := widget.NewLabelWithStyle(host.Label(), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	var leftHead fyne.CanvasObject = status
	// Flare: on a kldload host, zxplore lights up the extra k-command primitives
	// (boot envs, etc.) and shows a green chip. Stays fully generic elsewhere.
	if kt := KldloadTools(); len(kt) > 0 {
		flare := heading(fmt.Sprintf("● kldload — %d extra tools", len(kt)), acGreen)
		flare.TextSize = 13
		leftHead = container.NewHBox(status, flare)
	}
	repoU, _ := url.Parse(repoURL)
	siteU, _ := url.Parse(siteURL)
	verLink := widget.NewHyperlink("zxplore v"+version, repoU)
	verLink.Alignment = fyne.TextAlignTrailing
	siteLink := widget.NewHyperlink("powered by kldload.com", siteU)
	siteLink.Alignment = fyne.TextAlignTrailing
	wordmark := container.NewVBox(verLink, siteLink)
	titleRow := container.NewBorder(nil, nil, leftHead, wordmark)

	poolsHeader := container.NewVBox(
		heading("ZPOOLS — machine overview", acGold),
		poolsLabel,
	)
	toolbar := container.NewHBox(
		widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), reload),
		widget.NewButton("Servers…", func() { showServerManager(w, func(Server) {}) }),
		widget.NewButton("Pools…", func() { showPoolManager(w, reload) }),
	)
	if IsKldload() {
		toolbar.Add(widget.NewButton("Boot Envs…", func() { showBootEnvManager(w) }))
	}
	top := container.NewVBox(titleRow, card(poolsHeader), toolbar)

	// ── right pane: read dossier ⇄ inline edit form, toggled by ✎ Edit ──
	editing := false
	curName := func() string {
		if list.cursor >= 0 && list.cursor < len(visible) {
			return visible[list.cursor].Name
		}
		return ""
	}
	editBtn := widget.NewButton("✎ Edit", nil)
	rightBody := container.NewStack(dossierScroll)
	var buildRight func()
	buildRight = func() {
		if name := curName(); editing && name != "" {
			editBtn.SetText("✓ Done")
			rightBody.Objects = []fyne.CanvasObject{
				buildEditForm(host, name, w, func() { buildRight() }),
			}
		} else {
			editBtn.SetText("✎ Edit")
			lastShown = -1 // force the read view to re-render (may be post-edit)
			setDossier(list.cursor)
			rightBody.Objects = []fyne.CanvasObject{dossierScroll}
		}
		rightBody.Refresh()
	}
	editBtn.OnTapped = func() { editing = !editing; buildRight() }

	// Interactive snapshots list (below the dossier). Arrows move the highlight;
	// Enter or click opens the action menu (roll back / clone / hold / destroy).
	var snaps []Snapshot
	snapsList := newNavList(
		func() int { return len(snaps) },
		func() fyne.CanvasObject { return widget.NewLabel("t") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			s := snaps[i]
			short := s.Name
			if j := strings.IndexByte(short, '@'); j >= 0 {
				short = short[j+1:]
			}
			o.(*widget.Label).SetText(fmt.Sprintf("%-28s %8s  %s", short, s.Used, s.Creation))
		},
	)
	var reloadSnaps func()
	openSnapAction := func() {
		if snapsList.cursor >= 0 && snapsList.cursor < len(snaps) {
			snapshotActionDialog(host, snaps[snapsList.cursor].Name, w, func() {
				reloadSnaps()
				lastShown = -1
				setDossier(list.cursor) // usage/props may have shifted
			})
		}
	}
	snapsList.OnHighlighted = func(i widget.ListItemID) { snapsList.cursor = int(i) } // arrows: move only
	snapsList.OnSelected = func(i widget.ListItemID) { snapsList.cursor = int(i); openSnapAction() }
	snapsList.onEnter = openSnapAction
	snapsGen := 0
	reloadSnaps = func() {
		snapsGen++
		gen := snapsGen
		name := curName()
		if name == "" {
			snaps = nil
			snapsList.cursor = -1
			snapsList.UnselectAll()
			snapsList.Refresh()
			return
		}
		// One `zfs list` per selection change — off the UI thread, stale
		// results dropped, so arrow-scrolling stays fluid even over ssh.
		go func() {
			rows, _ := ListSnapshots(host, name)
			fyne.Do(func() {
				if gen != snapsGen {
					return
				}
				snaps = rows
				if snapsList.cursor >= len(snaps) {
					snapsList.cursor = -1
				}
				snapsList.UnselectAll()
				snapsList.Refresh()
			})
		}()
	}

	// Selecting a dataset: refresh the read dossier (or the edit form) AND the
	// snapshot list for that dataset.
	list.OnSelected = func(i widget.ListItemID) {
		list.cursor = int(i)
		reloadSnaps()
		if editing {
			buildRight()
		} else {
			setDossier(int(i))
		}
	}
	// Right-click a dataset → the full lifecycle menu (snapshot / clone /
	// replicate / boot-env / rollback / edit / destroy), acting on that row.
	list.onSecondary = func(e *fyne.PointEvent) {
		if list.cursor < 0 || list.cursor >= len(visible) {
			return
		}
		m := datasetContextMenu(host, visible[list.cursor].Name, w,
			func() { reload() },
			func() { editing = true; buildRight() })
		widget.ShowPopUpMenuAtPosition(m, w.Canvas(), e.AbsolutePosition)
	}

	rightHead := container.NewBorder(nil, nil, heading("DETAILS", acBlue), editBtn)
	dossierArea := container.NewBorder(rightHead, nil, nil, nil, rightBody)
	snapsHead := heading("SNAPSHOTS — Enter or click to roll back / clone / hold", acPurple)
	snapsArea := container.NewBorder(snapsHead, nil, nil, nil, snapsList)
	rightPane := container.NewVSplit(card(dossierArea), card(snapsArea))
	rightPane.SetOffset(0.68) // dossier ~⅔, snapshots ~⅓

	// ── Browser tab: find above the list, list │ (dossier/editor) with a divider ──
	leftPane := container.NewBorder(search, nil, nil, nil, list)
	split := container.NewHSplit(card(leftPane), rightPane)
	split.SetOffset(0.25) // narrow list, wide dossier (Transfer stays 50/50)

	var tabs *container.AppTabs
	switchTab := func(n fyne.KeyName) {
		if tabs == nil {
			return
		}
		switch n {
		case fyne.KeyF1:
			tabs.SelectIndex(0)
		case fyne.KeyF2:
			tabs.SelectIndex(1)
		}
	}
	list.onFunc = switchTab
	snapsList.onFunc = switchTab
	tabs = container.NewAppTabs(
		container.NewTabItem("Browser", split),
		container.NewTabItem("Transfer", transferTab(w, switchTab)),
	)

	// ── bottom: tmux-style teal help bar ──
	helpBG := canvas.NewRectangle(color.NRGBA{R: 0x08, G: 0x09, B: 0x0c, A: 0xff}) // very dark steel status strip
	helpText := canvas.NewText(helpHints, color.NRGBA{R: 0xcd, G: 0xd9, B: 0xe3, A: 0xff})
	helpText.TextStyle = fyne.TextStyle{Monospace: true}
	helpText.TextSize = 12
	helpBar := container.NewStack(helpBG, helpText)

	mainUI := container.NewBorder(top, helpBar, nil, nil, tabs)

	// ── splash: covers the UI while the first ZFS scan runs ──
	splashColor := func() color.Color {
		if variantDark() {
			return color.NRGBA{R: 0x08, G: 0x09, B: 0x0c, A: 0xff}
		}
		return color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	}
	splashBG := canvas.NewRectangle(splashColor())
	repaint = append(repaint, func() { splashBG.FillColor = splashColor(); splashBG.Refresh() })
	splashLogo := canvas.NewImageFromResource(fyne.NewStaticResource("zxplore.svg", iconSVG))
	splashLogo.FillMode = canvas.ImageFillContain
	splashLogo.SetMinSize(fyne.NewSize(128, 128))
	splashTitle := heading("z x p l o r e", acGreen)
	splashTitle.TextSize = 26
	splashTitle.Alignment = fyne.TextAlignCenter
	splashSub := heading("the universal ZFS console", acCyan)
	splashSub.TextSize = 13
	splashSub.Alignment = fyne.TextAlignCenter
	splashPhase := widget.NewLabelWithStyle("connecting to ZFS…",
		fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
	splashBar := widget.NewProgressBarInfinite()
	splash := container.NewStack(splashBG, container.NewCenter(container.NewVBox(
		container.NewCenter(splashLogo),
		splashTitle, splashSub,
		container.NewCenter(container.NewGridWrap(
			fyne.NewSize(300, splashBar.MinSize().Height), splashBar)),
		splashPhase,
	)))
	w.SetContent(container.NewStack(mainUI, splash))

	// Ctrl+F opens find from anywhere — a modifier shortcut fires globally
	// (unlike bare "/"/F-keys, which only reach the focused widget). Super/Win
	// combos are grabbed by the compositor, so Ctrl+F is the reliable choice.
	w.Canvas().AddShortcut(
		&desktop.CustomShortcut{KeyName: fyne.KeyF, Modifier: fyne.KeyModifierControl},
		func(fyne.Shortcut) { w.Canvas().Focus(search) },
	)
	// Alt+Q quits the app.
	w.Canvas().AddShortcut(
		&desktop.CustomShortcut{KeyName: fyne.KeyQ, Modifier: fyne.KeyModifierAlt},
		func(fyne.Shortcut) { w.Close() },
	)
	// Escape dismisses the open dialog/menu (= cancel); F-keys switch tabs when
	// nothing else is focused (each list's navList handles them when focused).
	w.Canvas().SetOnTypedKey(func(e *fyne.KeyEvent) {
		if e.Name == fyne.KeyEscape {
			if ov := w.Canvas().Overlays().Top(); ov != nil {
				w.Canvas().Overlays().Remove(ov)
			}
			return
		}
		switchTab(e.Name)
	})

	// Focus the list so ↑/↓/PgUp/PgDn/Home/End and "/" reach it immediately.
	w.Canvas().Focus(list)

	// First scan: off the UI thread, phase-labelled on the splash, which lifts
	// once the data lands.
	go func() {
		fyne.Do(func() { splashPhase.SetText("scanning ZFS…") })
		rows, err, pools, diag := scan()
		fyne.Do(func() {
			applyLoad(rows, err, pools, diag)
			splashBar.Stop()
			splash.Hide()
			w.Canvas().Focus(list)
		})
	}()

	// Recolor the hand-colored panels/headers now the variant is resolved, and
	// again whenever the GNOME light/dark setting changes.
	a.Settings().AddListener(func(fyne.Settings) { fyne.Do(applyPalette) })
	applyPalette()

	w.ShowAndRun()
}

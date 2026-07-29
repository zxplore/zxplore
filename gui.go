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
	"os"
	"os/exec"
	"regexp"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// The dark terminal-steel mark (animated cursor in SVG-capable viewers) is the
// app icon everywhere: window, dock, and the front page. The light teal tile
// stays in assets/ for the site.
//
//go:embed assets/zxplore-tui.svg
var iconSVG []byte

//go:embed docs/zxplore.1
var manPage []byte

// repoURL backs the top-right version link (source + issues). siteURL backs
// the "powered by kldload.com" credit on the front page.
const (
	repoURL = "https://github.com/zxplore/zxplore"
	siteURL = "https://kldload.com"
)

// helpHints is the tmux-style status line along the bottom. Keep it TRUE — only
// keys/gestures that actually work, so it stays a contract, not decoration.
const helpHints = "  F1 browser   F2 transfer   F3 explorer   ? manual    ↑↓ move   Tab pane   PgUp/PgDn page   Ctrl+F or / find   Enter/right-click = actions   Alt+Q quit  "

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

// tabTint colors one tab-bar button inside a ThemeOverride — old-school ANSI
// BBS contrast: the fill (selected) and hover wash are MUTED accent, the text
// stays the BRIGHT accent, so the label pops instead of drowning in its own
// color. This is why the tab bar is hand-built — AppTabs can neither color
// nor space its headers.
type tabTint struct {
	fyne.Theme
	a accentPair
}

func (t tabTint) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	ac := t.a.light
	if v == theme.VariantDark {
		ac = t.a.dark
	}
	switch name {
	case theme.ColorNameForeground, theme.ColorNameForegroundOnPrimary:
		return ac // bright accent text, selected or not
	case theme.ColorNamePrimary: // selected fill: muted accent under bright text
		if v == theme.VariantDark {
			return color.NRGBA{ac.R / 4, ac.G / 4, ac.B / 4, 0xff}
		}
		return color.NRGBA{
			uint8((int(ac.R) + 3*255) / 4), uint8((int(ac.G) + 3*255) / 4),
			uint8((int(ac.B) + 3*255) / 4), 0xff}
	case theme.ColorNameHover: // mouseover: translucent accent wash
		return color.NRGBA{ac.R, ac.G, ac.B, 0x2e}
	}
	return t.Theme.Color(name, v)
}

// openExplorerTab jumps to the F3 Explorer tab mounted on a dataset — set in
// runGUI, called from the browser's right-click menu.
var openExplorerTab func(h Host, dataset, source string)

// blueAccent retints the primary color inside a ThemeOverride — used to make
// the ✎ Edit button match the DETAILS pane's blue instead of the brand green.
type blueAccent struct{ fyne.Theme }

func (t blueAccent) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if name == theme.ColorNamePrimary {
		if v == theme.VariantDark {
			return acBlue.dark
		}
		return acBlue.light
	}
	return t.Theme.Color(name, v)
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
	viaKey      bool // true while a key event is in flight (keyNavSelect)
	onFind      func()
	onFunc      func(fyne.KeyName)     // F-key section switch
	onEnter     func()                 // Enter/Return acts on the current row
	onSecondary func(*fyne.PointEvent) // right-click → context menu
	onTab       func()                 // Tab hops to the sibling pane
	onHelp      func()                 // "?" opens the manual
}

// keyNavSelect wires OnHighlighted so ARROW/PAGE navigation moves the
// selection but MOUSE TRAVEL does not — for panes where the selection is a
// LOCKED CHOICE (transfer source/destination, explorer files) rather than a
// live preview like the browser, where hover-follows is the feature. A click
// still selects (OnSelected); hover alone changes nothing.
func (l *navList) keyNavSelect() {
	l.OnHighlighted = func(i widget.ListItemID) {
		if l.viaKey {
			l.Select(i)
		}
	}
}

// TypedRune catches "?" (a shifted rune, never delivered as a KeyName) for
// the in-app manual; everything else falls through.
func (l *navList) TypedRune(r rune) {
	if r == '?' && l.onHelp != nil {
		l.onHelp()
		return
	}
	l.List.TypedRune(r)
}

// AcceptsTab lets Tab reach TypedKey — without it Fyne's focus walk swallows
// the key and "tabbing between panes" dies in a random button. Only claimed
// when a pane hop is actually wired.
func (l *navList) AcceptsTab() bool { return l.onTab != nil }

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
	// Mark the window where OnHighlighted events are KEYBOARD-driven, so
	// keyNavSelect can tell arrows (select) apart from hover (ignore).
	l.viaKey = true
	defer func() { l.viaKey = false }()
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
	case fyne.KeyTab:
		if l.onTab != nil {
			l.onTab()
		}
	default:
		l.List.TypedKey(e) // native ↑/↓/Space
	}
}

// ── in-app manual ────────────────────────────────────────────────────────────
// The manual ships EMBEDDED in the binary, so "?" works even where man/mandoc
// were never installed (rendered nicely when they are, raw mdoc as the last
// resort). It renders as the full-window front page — see runGUI.

func renderManual() string {
	tmp, err := os.CreateTemp("", "zxplore-man-*.1")
	if err == nil {
		_, _ = tmp.Write(manPage)
		tmp.Close()
		defer os.Remove(tmp.Name())
		// No col(1) in the pipeline — overstrikes are stripped in Go, so the
		// only external need is mandoc OR man, and neither is required.
		for _, c := range []string{
			"mandoc -Tutf8 -O width=100 " + tmp.Name() + " 2>/dev/null",
			"MANWIDTH=100 man -l " + tmp.Name() + " 2>/dev/null",
		} {
			if out, err := exec.Command("sh", "-c", c).Output(); err == nil && len(out) > 200 {
				return stripOverstrike(string(out))
			}
		}
	}
	return string(manPage)
}

// stripOverstrike removes nroff bold/underline overstrike pairs (c\bc, _\bc)
// from rendered man output — the job col -bx used to do, done portably.
var overstrikeRE = regexp.MustCompile(`.\x08`)

func stripOverstrike(s string) string {
	for i := 0; i < 4 && strings.Contains(s, "\x08"); i++ {
		s = overstrikeRE.ReplaceAllString(s, "")
	}
	return strings.ReplaceAll(s, "\x08", "") // stray leading backspaces
}

// manHeadRE matches a man SECTION HEADER line (all caps, column 0).
var manHeadRE = regexp.MustCompile(`^[A-Z][A-Z0-9 /()-]*$`)

// manualSegments colors the rendered manual for RichText: section headers in
// the dossier topic blue, body default foreground, all monospace so the
// man(1) indentation survives.
func manualSegments(text string) []widget.RichTextSegment {
	mono := fyne.TextStyle{Monospace: true}
	seg := func(s string, cn fyne.ThemeColorName, bold bool) *widget.TextSegment {
		st := mono
		st.Bold = bold
		return &widget.TextSegment{Text: s, Style: widget.RichTextStyle{
			Inline: true, TextStyle: st, ColorName: cn}}
	}
	var out []widget.RichTextSegment
	for _, line := range strings.Split(text, "\n") {
		if line != "" && manHeadRE.MatchString(strings.TrimRight(line, " ")) {
			out = append(out, seg(line, cnTopic, true))
		} else {
			out = append(out, seg(line, theme.ColorNameForeground, false))
		}
		out = append(out, seg("\n", theme.ColorNameForeground, false))
	}
	return out
}

// ── safety: read-only by default (parity with the TUI's :rw) ────────────────

// guiRW gates every mutation. The toolbar lock button flips it; default OFF.
var guiRW = false

// guiMutOK is checked at the top of every mutation path: in read-only mode it
// explains and refuses. One chokepoint per surface, same policy as the TUI.
func guiMutOK(w fyne.Window) bool {
	if guiRW {
		return true
	}
	dialog.ShowInformation("Read-only",
		"zxplore is in READ-ONLY mode — browsing, diffing and drill-downs only.\n\n"+
			"Click the 🔒 button in the toolbar to enable changes\n(it turns red while unlocked).", w)
	return false
}

// confirmTyped is the typed-name confirmation for the destructive verbs —
// same policy as the TUI: retype the target exactly, or nothing happens.
func confirmTyped(w fyne.Window, title, detail, match string, onOK func()) {
	e := widget.NewEntry()
	e.SetPlaceHolder(match)
	body := container.NewVBox(
		widget.NewLabel(detail),
		widget.NewLabel("Type  "+match+"  to confirm:"),
		e,
	)
	d := dialog.NewCustomConfirm(title, "Confirm", "Cancel", body, func(ok bool) {
		if !ok {
			return
		}
		if strings.TrimSpace(e.Text) != match {
			dialog.ShowError(fmt.Errorf("name mismatch — expected %q", match), w)
			return
		}
		onOK()
	}, w)
	d.Resize(fyne.NewSize(560, 260))
	d.Show()
	w.Canvas().Focus(e)
}

// ── action menu ──────────────────────────────────────────────────────────────
// menuAction is one row of showActionMenu.
type menuAction struct {
	label string
	fn    func()
}

// showActionMenu is the keyboard-first replacement for a button-stack dialog:
// ↑/↓ move, Enter or click runs, Esc cancels. Fyne buttons only activate via
// Tab+Space, which is why button stacks felt dead to the arrows — a list row
// menu behaves like a real console menu.
func showActionMenu(title string, acts []menuAction, w fyne.Window) {
	var dlg dialog.Dialog
	list := newNavList(
		func() int { return len(acts) },
		func() fyne.CanvasObject { return widget.NewLabel("t") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			o.(*widget.Label).SetText(acts[i].label)
		},
	)
	run := func() {
		if list.cursor >= 0 && list.cursor < len(acts) {
			dlg.Hide()
			acts[list.cursor].fn()
		}
	}
	// arrows move the highlight only; Enter or a click runs
	list.OnHighlighted = func(i widget.ListItemID) { list.cursor = int(i) }
	list.OnSelected = func(i widget.ListItemID) { list.cursor = int(i); run() }
	list.onEnter = run
	list.cursor = 0

	h := float32(len(acts))*34 + 12
	if h > 480 {
		h = 480
	}
	content := container.NewGridWrap(fyne.NewSize(440, h), list)
	dlg = dialog.NewCustom(title, "Cancel", content, w)
	dlg.Show()
	w.Canvas().Focus(list)
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
			snaps := "" // counts stream in behind the fast list (Snaps<0 = not yet)
			if d.Snaps >= 0 {
				snaps = fmt.Sprintf("   ×%d", d.Snaps)
			}
			o.(*widget.Label).SetText(fmt.Sprintf("%s    %s / %s%s", d.Name, d.Used, d.Refer, snaps))
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

	// Explorer tab state — declared before applyLoad so each scan can refresh
	// the dataset picker; the page itself is assembled with the tab bar below.
	explorerBody := container.NewStack(container.NewCenter(
		widget.NewLabel("Pick a zpool above (or right-click a dataset in the Browser)\nto walk its files across every snapshot.")))
	var explorerFocus fyne.Focusable
	explorerSel := widget.NewSelect(nil, nil)
	explorerSel.PlaceHolder = "zpool…"
	// Picking a pool first shows its DATASETS with mount state — a pool root
	// is often none/canmount=off (rpool, zxdemo), so there are no files AT the
	// root; the browsable filesystems are the children. Descend from there.
	var showPoolDatasets func(pool string)
	mountExplorer := func(h Host, dataset, source string) {
		view, focus := snapExplorerView(w, h, dataset, source)
		explorerFocus = focus
		content := view
		if h.SSH == "" { // a back row returns to the pool's dataset list
			pool := dataset
			if i := strings.IndexByte(pool, '/'); i >= 0 {
				pool = pool[:i]
			}
			back := widget.NewButtonWithIcon("« "+pool+" datasets", theme.NavigateBackIcon(),
				func() { showPoolDatasets(pool) })
			content = container.NewBorder(container.NewHBox(back), nil, nil, nil, view)
		}
		explorerBody.Objects = []fyne.CanvasObject{content}
		explorerBody.Refresh()
	}
	showPoolDatasets = func(pool string) {
		status := widget.NewLabelWithStyle("… reading "+pool,
			fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		var rows []MountRow
		dsList := newNavList(
			func() int { return len(rows) },
			func() fyne.CanvasObject {
				return widget.NewLabelWithStyle("t", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
			},
			func(i widget.ListItemID, o fyne.CanvasObject) {
				r := rows[i]
				mark, note := "▸ ", ""
				if !r.Browsable() {
					mark = "  "
					if r.Mounted {
						note = "— not browsable (mountpoint " + r.Mountpoint + ")"
					} else {
						note = "— not mounted"
					}
				}
				o.(*widget.Label).SetText(fmt.Sprintf("%s%-40s %-24s %s", mark, r.Name, r.Mountpoint, note))
			},
		)
		open := func() {
			if dsList.cursor >= 0 && dsList.cursor < len(rows) && rows[dsList.cursor].Browsable() {
				mountExplorer(host, rows[dsList.cursor].Name, "")
				if explorerFocus != nil {
					w.Canvas().Focus(explorerFocus)
				}
			}
		}
		dsList.OnHighlighted = func(i widget.ListItemID) {
			if dsList.viaKey { // arrows move the target; hover doesn't
				dsList.cursor = int(i)
			}
		}
		dsList.OnSelected = func(i widget.ListItemID) { dsList.cursor = int(i); open() }
		dsList.onEnter = open
		explorerFocus = dsList
		explorerBody.Objects = []fyne.CanvasObject{container.NewBorder(
			widget.NewLabelWithStyle(pool+" — pick a mounted dataset (▸) to browse",
				fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			status, nil, nil, dsList)}
		explorerBody.Refresh()
		w.Canvas().Focus(dsList)
		go func() {
			rs, err := ListMounts(host, pool)
			fyne.Do(func() {
				if err != nil {
					status.SetText("✗ " + err.Error())
					return
				}
				rows = rs
				browsable := 0
				for i, r := range rs {
					if r.Browsable() {
						if browsable == 0 {
							dsList.cursor = i // land on the first openable one
						}
						browsable++
					}
				}
				dsList.Refresh()
				status.SetText(fmt.Sprintf("%d datasets, %d browsable — Enter or click opens", len(rs), browsable))
			})
		}()
	}
	explorerSel.OnChanged = func(name string) {
		if name != "" {
			showPoolDatasets(name)
		}
	}

	// applyLoad lands a finished scan on the UI thread; reload runs one in the
	// background (refresh button, post-mutation refreshes). diag explains an
	// EMPTY result (no ZFS installed / no pools imported) so the browser never
	// sits there blank — it shows the way out instead.
	applyLoad := func(rows []Dataset, err error, pools string, diag HostDiagnosis) {
		all, listErr = rows, err
		poolsLabel.SetText(pools)
		// Explorer picker offers POOLS (browse from the root down) — derived
		// from the dataset list, so no extra zpool call.
		seenPool := map[string]bool{}
		var poolNames []string
		for _, d := range all {
			p := d.Name
			if i := strings.IndexByte(p, '/'); i >= 0 {
				p = p[:i]
			}
			if !seenPool[p] {
				seenPool[p] = true
				poolNames = append(poolNames, p)
			}
		}
		explorerSel.Options = poolNames
		explorerSel.Refresh()
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
	// Snapshot counts are the expensive part (seconds of kernel time with
	// thousands of snapshots) — they stream in AFTER the list paints, and a
	// stale scan's counts are dropped.
	scanGen := 0
	loadCounts := func(gen int) {
		go func() {
			counts, err := SnapshotCounts(host)
			if err != nil || counts == nil {
				return
			}
			fyne.Do(func() {
				if gen != scanGen {
					return
				}
				for i := range all {
					if c, ok := counts[all[i].Name]; ok {
						all[i].Snaps = c
					} else {
						all[i].Snaps = 0
					}
				}
				for i := range visible {
					if c, ok := counts[visible[i].Name]; ok {
						visible[i].Snaps = c
					} else {
						visible[i].Snaps = 0
					}
				}
				list.Refresh()
			})
		}()
	}
	reload := func() {
		scanGen++
		gen := scanGen
		go func() {
			rows, err, pools, diag := scan()
			fyne.Do(func() {
				if gen != scanGen {
					return
				}
				applyLoad(rows, err, pools, diag)
				loadCounts(gen)
			})
		}()
	}

	// ── top: title row (status + kldload flare left, wordmark right) ──
	status := widget.NewLabelWithStyle(host.Label(), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	// Platform chip: "● OpenZFS 2.4.3-1 · Fedora Linux 44" — filled by the
	// first scan (it costs a couple of remote-able commands).
	platChip := heading("", acCyan)
	platChip.TextSize = 13
	head := container.NewHBox(status, platChip)
	// Flare: on a kldload host, zxplore lights up the extra k-command primitives
	// (boot envs, etc.) and shows a green chip. The k-commands themselves are
	// listed in the manual ("?"). Stays fully generic elsewhere.
	if kt := KldloadTools(); len(kt) > 0 {
		flare := heading(fmt.Sprintf("● kldload — %d extra tools", len(kt)), acGreen)
		flare.TextSize = 13
		head.Add(flare)
	}
	var leftHead fyne.CanvasObject = head
	// Top-right: just the version, linking to the source repo. The
	// "powered by kldload.com" credit lives on the front page footer now.
	repoU, _ := url.Parse(repoURL)
	verLink := widget.NewHyperlink("zxplore v"+versionFull(), repoU)
	verLink.Alignment = fyne.TextAlignTrailing
	titleRow := container.NewBorder(nil, nil, leftHead, verLink)

	// openManual/closeManual drive the full-window front page (built below,
	// after mainUI exists); declared here so the toolbar can reference them.
	var openManual, closeManual func()

	poolsHeader := container.NewVBox(
		heading("ZPOOLS — machine overview", acGold),
		poolsLabel,
	)
	// The lock: read-only by default, one click to arm changes (red), one to
	// relock — the GUI twin of the TUI's :rw / :ro.
	var lockBtn *widget.Button
	syncLock := func() {
		if guiRW {
			lockBtn.SetText("⚠ Read-write")
			lockBtn.Importance = widget.DangerImportance
		} else {
			lockBtn.SetText("🔒 Read-only")
			lockBtn.Importance = widget.MediumImportance
		}
		lockBtn.Refresh()
	}
	lockBtn = widget.NewButton("", func() { guiRW = !guiRW; syncLock() })

	toolbarLeft := container.NewHBox(
		widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), reload),
		widget.NewButton("Servers…", func() { showServerManager(w, func(Server) {}) }),
		widget.NewButton("Pools…", func() { showPoolManager(w, reload) }),
		lockBtn,
	)
	manualBtn := widget.NewButtonWithIcon("Manual", theme.HelpIcon(), func() { openManual() })
	toolbar := container.NewBorder(nil, nil, toolbarLeft, manualBtn)
	syncLock()
	if IsKldload() {
		toolbarLeft.Add(widget.NewButton("Boot Envs…", func() { showBootEnvManager(w) }))
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
	editBtn.Importance = widget.HighImportance // accent-filled so it reads as THE action on the pane
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
	// Enter opens the same menu, so the whole lifecycle is arrow-reachable
	// (Fyne popup menus take ↑/↓/Enter natively once shown).
	openDatasetMenu := func(pos fyne.Position) {
		if list.cursor < 0 || list.cursor >= len(visible) {
			return
		}
		m := datasetContextMenu(host, visible[list.cursor].Name, w,
			func() { reload() },
			func() { editing = true; buildRight() })
		widget.ShowPopUpMenuAtPosition(m, w.Canvas(), pos)
	}
	list.onSecondary = func(e *fyne.PointEvent) { openDatasetMenu(e.AbsolutePosition) }
	list.onEnter = func() { openDatasetMenu(fyne.NewPos(300, 220)) }

	rightHead := container.NewBorder(nil, nil, heading("DETAILS", acBlue),
		container.NewThemeOverride(editBtn, blueAccent{compactTheme{theme.DefaultTheme()}}))
	dossierArea := container.NewBorder(rightHead, nil, nil, nil, rightBody)
	snapsHead := heading("SNAPSHOTS — Enter or click to roll back / clone / hold", acPurple)
	snapsArea := container.NewBorder(snapsHead, nil, nil, nil, snapsList)
	rightPane := container.NewVSplit(card(dossierArea), card(snapsArea))
	rightPane.SetOffset(0.68) // dossier ~⅔, snapshots ~⅓

	// ── Browser tab: find above the list, list │ (dossier/editor) with a divider ──
	leftPane := container.NewBorder(search, nil, nil, nil, list)
	split := container.NewHSplit(card(leftPane), rightPane)
	split.SetOffset(0.25) // narrow list, wide dossier (Transfer stays 50/50)

	// ── tabs: hand-built colored bar — Browser blue · Transfer purple ·
	// Explorer green, with real air between the buttons (AppTabs can do
	// neither). F1/F2/F3 and clicks both land in tabSel.
	var tabSel func(int)
	switchTab := func(n fyne.KeyName) {
		if tabSel == nil {
			return
		}
		switch n {
		case fyne.KeyF1:
			tabSel(0)
		case fyne.KeyF2:
			tabSel(1)
		case fyne.KeyF3:
			tabSel(2)
		}
	}
	list.onFunc = switchTab
	snapsList.onFunc = switchTab
	// Tab hops dataset list ⇄ snapshot list (the two keyboard panes).
	list.onTab = func() { w.Canvas().Focus(snapsList) }
	snapsList.onTab = func() { w.Canvas().Focus(list) }
	// "?" opens the in-app manual from either pane.
	list.onHelp = func() { openManual() }
	snapsList.onHelp = func() { openManual() }

	explorerPage := container.NewBorder(
		container.NewBorder(nil, nil,
			heading("EXPLORER — files across every snapshot", acGreen),
			container.NewHBox(widget.NewLabel("zpool:"), explorerSel)),
		nil, nil, nil, explorerBody)
	pages := []fyne.CanvasObject{split, transferTab(w, switchTab), explorerPage}

	var tabBtns [3]*widget.Button
	mkTab := func(i int, label string, a accentPair) fyne.CanvasObject {
		b := widget.NewButton(label, func() { tabSel(i) })
		b.Importance = widget.LowImportance
		tabBtns[i] = b
		return container.NewThemeOverride(b, tabTint{compactTheme{theme.DefaultTheme()}, a})
	}
	tabGap := func() fyne.CanvasObject {
		r := canvas.NewRectangle(color.Transparent)
		r.SetMinSize(fyne.NewSize(28, 1))
		return r
	}
	tabBar := container.NewHBox(
		mkTab(0, "⌂  Browser", acBlue), tabGap(),
		mkTab(1, "⇄  Transfer", acPurple), tabGap(),
		mkTab(2, "🗁  Explorer", acGreen),
	)
	tabSel = func(i int) {
		for j, p := range pages {
			if j == i {
				p.Show()
			} else {
				p.Hide()
			}
		}
		for j, b := range tabBtns {
			if j == i {
				b.Importance = widget.HighImportance
			} else {
				b.Importance = widget.LowImportance
			}
			b.Refresh()
		}
		switch i {
		case 0:
			w.Canvas().Focus(list)
		case 2:
			if explorerFocus != nil {
				w.Canvas().Focus(explorerFocus)
			}
		}
	}
	tabs := container.NewBorder(tabBar, nil, nil, nil, container.NewStack(pages...))
	tabSel(0)

	// The right-click "Snapshot explorer" action lands here: mount the dataset
	// in the F3 tab (picker synced for local datasets) and switch to it.
	openExplorerTab = func(h Host, dataset, source string) {
		mountExplorer(h, dataset, source)
		// Sync the pool picker when the target IS a pool root; a child
		// dataset leaves it untouched (the view names the dataset anyway).
		if h.SSH == "" && !strings.ContainsRune(dataset, '/') {
			explorerSel.Selected = dataset // no OnChanged retrigger — view already mounted
			explorerSel.Refresh()
		}
		tabSel(2)
	}

	// ── bottom: tmux-style teal help bar ──
	helpBG := canvas.NewRectangle(color.NRGBA{R: 0x08, G: 0x09, B: 0x0c, A: 0xff}) // very dark steel status strip
	helpText := canvas.NewText(helpHints, color.NRGBA{R: 0xcd, G: 0xd9, B: 0xe3, A: 0xff})
	helpText.TextStyle = fyne.TextStyle{Monospace: true}
	helpText.TextSize = 12
	helpBar := container.NewStack(helpBG, helpText)

	mainUI := container.NewBorder(top, helpBar, nil, nil, tabs)

	// ── manual page: full-window zxplore(1), opened by "?" / the Manual button ──
	// Logo + wordmark header, the rendered manual, powered-by credit in the
	// footer. Dismiss with Enter/Esc/Space or the Close button. The boot cover
	// is the separate small splash below.
	pageColor := func() color.Color {
		if variantDark() {
			return color.NRGBA{R: 0x08, G: 0x09, B: 0x0c, A: 0xff}
		}
		return color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	}
	pageBG := canvas.NewRectangle(pageColor())
	repaint = append(repaint, func() { pageBG.FillColor = pageColor(); pageBG.Refresh() })
	pageLogo := canvas.NewImageFromResource(fyne.NewStaticResource("zxplore.svg", iconSVG))
	pageLogo.FillMode = canvas.ImageFillContain
	pageLogo.SetMinSize(fyne.NewSize(96, 96))
	pageTitle := heading("z x p l o r e", acGreen)
	pageTitle.TextSize = 26
	pageSub := heading("the universal ZFS console — zxplore(1)", acCyan)
	pageSub.TextSize = 13
	pageVer := heading("v"+versionFull(), acGold)
	pageVer.TextSize = 12
	pageHead := container.NewCenter(container.NewHBox(
		pageLogo, container.NewVBox(pageTitle, pageSub, pageVer)))

	// The manual body renders async (mandoc/man can take a beat) and slots in;
	// centered so the 100-column page floats mid-window instead of hugging the
	// left edge.
	manBody := widget.NewRichText()
	manBody.Wrapping = fyne.TextWrapOff
	manScroll := container.NewScroll(container.NewCenter(manBody))
	go func() {
		text := renderManual()
		fyne.Do(func() {
			manBody.Segments = manualSegments(text)
			manBody.Refresh()
		})
	}()

	siteU, _ := url.Parse(siteURL)
	powered := widget.NewHyperlink("powered by kldload.com", siteU)
	closeBtn := widget.NewButtonWithIcon("Close  ⏎", theme.ConfirmIcon(), nil)
	closeBtn.Importance = widget.HighImportance
	footer := container.NewBorder(nil, nil, powered, closeBtn, nil)

	page := container.NewStack(pageBG, container.NewBorder(
		container.NewPadded(pageHead), container.NewPadded(footer), nil, nil, manScroll))
	page.Hide() // opened on demand; boot is covered by the splash below

	// ── splash: small and clean, covers the UI while the first ZFS scan runs ──
	splashBG := canvas.NewRectangle(pageColor())
	repaint = append(repaint, func() { splashBG.FillColor = pageColor(); splashBG.Refresh() })
	splashLogo := canvas.NewImageFromResource(fyne.NewStaticResource("zxplore.svg", iconSVG))
	splashLogo.FillMode = canvas.ImageFillContain
	splashLogo.SetMinSize(fyne.NewSize(128, 128))
	splashTitle := heading("z x p l o r e", acGreen)
	splashTitle.TextSize = 26
	splashTitle.Alignment = fyne.TextAlignCenter
	splashSub := heading("the universal ZFS console", acCyan)
	splashSub.TextSize = 13
	splashSub.Alignment = fyne.TextAlignCenter
	splashVer := heading("v"+versionFull(), acGold)
	splashVer.TextSize = 12
	splashVer.Alignment = fyne.TextAlignCenter
	scanPhase := widget.NewLabelWithStyle("connecting to ZFS…",
		fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
	scanBar := widget.NewProgressBarInfinite()
	splash := container.NewStack(splashBG, container.NewCenter(container.NewVBox(
		container.NewCenter(splashLogo),
		splashTitle, splashSub, splashVer,
		container.NewCenter(container.NewGridWrap(
			fyne.NewSize(300, scanBar.MinSize().Height), scanBar)),
		scanPhase,
	)))
	w.SetContent(container.NewStack(mainUI, page, splash))

	openManual = func() {
		page.Show()
		w.Canvas().Unfocus() // bare keys fall through to the canvas handler below
	}
	closeManual = func() {
		page.Hide()
		w.Canvas().Focus(list)
	}
	closeBtn.OnTapped = func() { closeManual() }

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
	// While the manual page is up, Enter/Esc/Space dismiss it (openManual
	// unfocuses, so bare keys land here). Otherwise: Escape dismisses the open
	// dialog/menu (= cancel); F-keys switch tabs when nothing else is focused
	// (each list's navList handles them when focused).
	w.Canvas().SetOnTypedKey(func(e *fyne.KeyEvent) {
		if page.Visible() {
			switch e.Name {
			case fyne.KeyEscape, fyne.KeyReturn, fyne.KeyEnter, fyne.KeySpace:
				closeManual()
			}
			return
		}
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
	scanGen++
	firstGen := scanGen
	go func() {
		fyne.Do(func() { scanPhase.SetText("scanning ZFS…") })
		plat := HostPlatform(host)
		rows, err, pools, diag := scan()
		fyne.Do(func() {
			if plat != "" {
				platChip.Text = "● " + plat
				platChip.Refresh()
			}
			applyLoad(rows, err, pools, diag)
			scanBar.Stop()
			splash.Hide()
			w.Canvas().Focus(list)
			loadCounts(firstGen)
		})
	}()

	// Recolor the hand-colored panels/headers now the variant is resolved, and
	// again whenever the GNOME light/dark setting changes.
	a.Settings().AddListener(func(fyne.Settings) { fyne.Do(applyPalette) })
	applyPalette()

	w.ShowAndRun()
}

// gui.go — the native GUI (Fyne). Default surface for zexplore.
//
// A real window: keyboard navigation AND mouse (click, scroll, right-click),
// copy-paste, and its own icon — all native, no terminal involved. Shares the
// zfs.go engine with the --tui mode. This first cut is the browser: a dataset
// list on the left, a live dossier on the right. Transfer / favorites / actions
// / boot environments follow.
package main

import (
	_ "embed"
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

//go:embed assets/zexplore.svg
var iconSVG []byte

// compactTheme trims Fyne's default padding so the dataset list renders tight
// (single-spaced) and the whole UI is denser — matching the dossier pane and
// reading more like a pro data tool than a spacious form.
type compactTheme struct{ fyne.Theme }

func (t compactTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameInnerPadding:
		return 3
	case theme.SizeNamePadding:
		return 2
	}
	return t.Theme.Size(name)
}

// Color follows GNOME's light/dark variant (delegated) but brands the accent
// zexplore-teal and warms the dark background to a brand blue-grey so it reads
// less flat/dreary than Fyne's default grey. Light mode keeps the system look.
func (t compactTheme) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 0x5f, G: 0xc4, B: 0xbc, A: 0xff}
	case theme.ColorNameBackground:
		if v == theme.VariantDark {
			return color.NRGBA{R: 0x10, G: 0x17, B: 0x20, A: 0xff}
		}
	}
	return t.Theme.Color(name, v)
}

// setDossierText renders s as one monospace segment using the theme foreground,
// so the dossier follows GNOME's light/dark (a markdown code block would not).
func setDossierText(r *widget.RichText, s string) {
	r.Segments = []widget.RichTextSegment{
		&widget.TextSegment{Text: s, Style: widget.RichTextStyle{TextStyle: fyne.TextStyle{Monospace: true}}},
	}
	r.Refresh()
}

func runGUI() {
	a := app.NewWithID("ca.zexplore")
	a.Settings().SetTheme(compactTheme{theme.DefaultTheme()})
	a.SetIcon(fyne.NewStaticResource("zexplore.svg", iconSVG))

	host := LocalHost()
	w := a.NewWindow("zexplore — ZFS console")
	// A normal window with title bar + min/max/close. Fyne has no Maximize()
	// API, but opening larger than the work area triggers GNOME's auto-maximize
	// (on by default), so it comes up maximized with normal window controls.
	w.Resize(fyne.NewSize(1920, 1200))
	w.CenterOnScreen()

	datasets, listErr := ListDatasets(host)

	// Dossier pane (monospace via a fenced markdown block); scrollable.
	dossier := widget.NewRichText()
	dossier.Wrapping = fyne.TextWrapOff
	dossier.Scroll = fyne.ScrollVerticalOnly // scroll itself — don't wrap in an outer Scroll (they fight)

	setDossier := func(i int) {
		if i >= 0 && i < len(datasets) {
			setDossierText(dossier, Dossier(host, datasets[i].Name))
		} else {
			setDossierText(dossier, "")
		}
	}

	list := widget.NewList(
		func() int { return len(datasets) },
		func() fyne.CanvasObject { return widget.NewLabel("template") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			d := datasets[i]
			o.(*widget.Label).SetText(fmt.Sprintf("%s    %s / %s   ×%d", d.Name, d.Used, d.Refer, d.Snaps))
		},
	)
	// Two update paths, both refresh the dossier live as you move:
	//   • OnSelected    — mouse click / Space (commits a selection)
	//   • OnHighlighted — ↑/↓ arrow keys move the focus highlight (Fyne 2.8+)
	// so keyboard and mouse navigation both drive the detail pane.
	list.OnSelected = func(i widget.ListItemID) { setDossier(int(i)) }
	list.OnHighlighted = func(i widget.ListItemID) { setDossier(int(i)) }

	reload := func() {
		datasets, listErr = ListDatasets(host)
		list.Refresh()
		if listErr != nil {
			dossier.ParseMarkdown("```\ncannot list datasets:\n" + listErr.Error() +
				"\n\n(zexplore needs permission to read ZFS — run it as root, or grant\n" +
				" `zfs allow` on the pool.)\n```")
		} else if len(datasets) > 0 {
			list.Select(0)
		}
	}

	badge := ""
	if IsKldload() {
		badge = "  •  kldload"
	}
	header := widget.NewLabelWithStyle("zexplore   —   "+host.Label()+badge,
		fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	toolbar := widget.NewToolbar(
		widget.NewToolbarAction(theme.ViewRefreshIcon(), reload),
		widget.NewToolbarSeparator(),
		widget.NewToolbarSpacer(),
	)

	split := container.NewHSplit(list, dossier)
	split.SetOffset(0.42)

	top := container.NewVBox(header, toolbar)
	browser := container.NewBorder(top, nil, nil, nil, split)
	tabs := container.NewAppTabs(
		container.NewTabItem("Browser", browser),
		container.NewTabItem("Transfer", transferTab(w)),
	)
	w.SetContent(tabs)

	// Focus the list so ↑/↓/PgUp/PgDn reach it. Fyne fires OnHighlighted as the
	// arrows move the highlight (OnSelected on click/Space) — both refresh the
	// dossier, so keyboard navigation updates the detail pane live.
	w.Canvas().Focus(list)

	if listErr != nil {
		setDossierText(dossier, "cannot list datasets:\n"+listErr.Error()+
			"\n\n(zexplore needs permission to read ZFS — run it as root, or grant\n"+
			" `zfs allow` on the pool.)")
	} else if len(datasets) > 0 {
		list.Select(0)
	}
	w.ShowAndRun()
}

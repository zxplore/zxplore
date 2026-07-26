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

func runGUI() {
	a := app.NewWithID("ca.zexplore")
	a.Settings().SetTheme(compactTheme{theme.DefaultTheme()})
	a.SetIcon(fyne.NewStaticResource("zexplore.svg", iconSVG))

	host := LocalHost()
	w := a.NewWindow("zexplore — ZFS console")
	w.Resize(fyne.NewSize(1120, 720))

	datasets, listErr := ListDatasets(host)

	// Dossier pane (monospace via a fenced markdown block); scrollable.
	dossier := widget.NewRichText()
	dossier.Wrapping = fyne.TextWrapOff

	setDossier := func(i int) {
		if i >= 0 && i < len(datasets) {
			dossier.ParseMarkdown("```\n" + Dossier(host, datasets[i].Name) + "\n```")
		} else {
			dossier.ParseMarkdown("")
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
	list.OnSelected = func(i widget.ListItemID) { setDossier(int(i)) }

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

	split := container.NewHSplit(list, container.NewScroll(dossier))
	split.SetOffset(0.42)

	top := container.NewVBox(header, toolbar)
	w.SetContent(container.NewBorder(top, nil, nil, nil, split))

	if listErr != nil {
		dossier.ParseMarkdown("```\ncannot list datasets:\n" + listErr.Error() +
			"\n\n(zexplore needs permission to read ZFS — run it as root, or grant\n" +
			" `zfs allow` on the pool.)\n```")
	} else if len(datasets) > 0 {
		list.Select(0)
	}
	w.ShowAndRun()
}

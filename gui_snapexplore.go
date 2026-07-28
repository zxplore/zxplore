//go:build gui

// gui_snapexplore.go — the snapshot file explorer + zfs diff pane.
//
// THE fusion feature: browse the files of a dataset (live, or inside any
// snapshot via .zfs/snapshot), pick a file, and see it across EVERY snapshot
// that contains it — size/mtime per copy, flagged when it differs from the
// copy you're looking at. Restore any version into the live dataset
// (overwrite, or alongside as <name>.from-<snap>) — no manual cp gymnastics.
// The diff pane renders `zfs diff` between two snapshots (or snapshot ↔ live),
// colored and filterable by path. Every mutation shows its literal command
// first (the dry-run pane) and lands in the audit log.
package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const liveSourceLabel = "(live filesystem)"

// showSnapshotExplorer opens the explorer window on a dataset. initialSource
// is "" for the live filesystem, or a snapshot short name to start inside it.
func showSnapshotExplorer(h Host, dataset, initialSource string) {
	win := fyne.CurrentApp().NewWindow("Snapshot explorer — " + dataset)
	win.Resize(fyne.NewSize(1280, 800))

	var (
		mp       string // dataset mountpoint (fetched async at open)
		rel      string // current directory, relative to the dataset root
		source   = initialSource
		entries  []FileEntry
		snaps    []Snapshot // creation order — used to order the versions pane
		vers     []FileVersion
		selEntry FileEntry
		haveSel  bool
		verSel   = -1
		dirGen   = 0
		verGen   = 0
	)

	statusLbl := widget.NewLabelWithStyle("opening…", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	pathLbl := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	refreshPath := func() {
		src := "live"
		if source != "" {
			src = "@" + source
		}
		pathLbl.SetText(fmt.Sprintf("%s  [%s]  /%s", dataset, src, rel))
	}

	// up() is 1 when a synthetic ".." row leads the list.
	up := func() int {
		if rel != "" {
			return 1
		}
		return 0
	}

	// ── right: versions of the selected path across every snapshot ──
	versList := newNavList(
		func() int { return len(vers) },
		func() fyne.CanvasObject {
			return widget.NewLabelWithStyle("t", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			v := vers[i]
			mark := "≡ same"
			delta := ""
			if v.Size != selEntry.Size || v.MTime != selEntry.MTime {
				mark = "Δ DIFF"
				if !v.Dir && v.Size != selEntry.Size {
					d := v.Size - selEntry.Size
					sign := "+"
					if d < 0 {
						sign = "-"
						d = -d
					}
					delta = sign + humanBytes(d)
				}
			}
			o.(*widget.Label).SetText(fmt.Sprintf("%-6s  @%-26s %9s %8s  %s",
				mark, v.Snapshot, humanBytes(v.Size), delta, v.MTime))
		},
	)
	versList.OnHighlighted = func(i widget.ListItemID) { versList.Select(i) }
	versList.OnSelected = func(i widget.ListItemID) { versList.cursor = int(i); verSel = int(i) }
	// Enter on a version → arrow-navigable restore menu (buttons below stay
	// for the mouse).
	var doRestore func(overwrite bool)
	versList.onEnter = func() {
		if verSel < 0 || verSel >= len(vers) {
			return
		}
		showActionMenu("@"+vers[verSel].Snapshot, []menuAction{
			{"⚠ Restore over live (overwrite)…", func() { doRestore(true) }},
			{"Restore as copy…", func() { doRestore(false) }},
		}, win)
	}

	versHint := widget.NewLabel("Select a file on the left — every snapshot holding it appears here,\nflagged when its copy differs from the one you're looking at.")
	versHint.Wrapping = fyne.TextWrapWord

	loadVersions := func(e FileEntry) {
		verGen++
		gen := verGen
		verSel = -1
		vers = nil
		versList.UnselectAll()
		versList.Refresh()
		relPath := relJoin(rel, e.Name)
		go func() {
			vs, err := FileVersions(h, mp, relPath)
			fyne.Do(func() {
				if gen != verGen {
					return
				}
				// order by snapshot creation (glob order is alphabetical)
				byName := map[string]FileVersion{}
				for _, v := range vs {
					byName[v.Snapshot] = v
				}
				ordered := make([]FileVersion, 0, len(vs))
				for _, s := range snaps {
					if v, ok := byName[snapShort(s.Name)]; ok {
						ordered = append(ordered, v)
						delete(byName, v.Snapshot)
					}
				}
				for _, v := range vs { // snapshots taken since the pane opened
					if _, left := byName[v.Snapshot]; left {
						ordered = append(ordered, v)
					}
				}
				vers = ordered
				versList.Refresh()
				switch {
				case err != nil:
					statusLbl.SetText("✗ versions: " + err.Error())
				case len(vers) == 0:
					statusLbl.SetText("/" + relPath + " — in no snapshot (created since the last one?)")
				default:
					statusLbl.SetText(fmt.Sprintf("/%s — in %d of %d snapshots", relPath, len(vers), len(snaps)))
				}
				if len(vers) > 0 {
					versHint.Hide()
				} else {
					versHint.Show()
				}
			})
		}()
	}

	// ── left: directory listing (live or inside a snapshot) ──
	fileList := newNavList(
		func() int { return up() + len(entries) },
		func() fyne.CanvasObject {
			return widget.NewLabelWithStyle("t", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			l := o.(*widget.Label)
			idx := int(i) - up()
			if idx < 0 {
				l.SetText("▴ ..")
				return
			}
			e := entries[idx]
			mark, sz := "  ", ""
			if e.Dir {
				mark = "▸ "
			} else {
				sz = humanBytes(e.Size)
			}
			l.SetText(fmt.Sprintf("%s%-44s %9s  %s", mark, e.Name, sz, e.MTime))
		},
	)

	var loadDir func()
	loadDir = func() {
		refreshPath()
		dirGen++
		gen := dirGen
		base := mp
		if source != "" {
			base = mp + "/.zfs/snapshot/" + source
		}
		p := base
		if rel != "" {
			p += "/" + rel
		}
		statusLbl.SetText("… reading " + p)
		go func() {
			es, err := ListDir(h, p)
			fyne.Do(func() {
				if gen != dirGen {
					return
				}
				entries = es
				haveSel = false
				vers = nil
				verSel = -1
				versList.Refresh()
				versHint.Show()
				fileList.UnselectAll()
				fileList.cursor = -1
				fileList.Refresh()
				if err != nil {
					statusLbl.SetText("✗ " + err.Error())
				} else {
					statusLbl.SetText(fmt.Sprintf("%d entries in /%s", len(es), rel))
				}
			})
		}()
	}

	fileList.OnHighlighted = func(i widget.ListItemID) { fileList.Select(i) }
	fileList.OnSelected = func(i widget.ListItemID) {
		fileList.cursor = int(i)
		idx := int(i) - up()
		if idx < 0 || idx >= len(entries) {
			haveSel = false
			return
		}
		selEntry = entries[idx]
		haveSel = true
		loadVersions(selEntry)
	}
	openCur := func() {
		idx := fileList.cursor - up()
		if fileList.cursor >= 0 && idx < 0 { // ".."
			if j := strings.LastIndexByte(rel, '/'); j >= 0 {
				rel = rel[:j]
			} else {
				rel = ""
			}
			loadDir()
			return
		}
		if idx >= 0 && idx < len(entries) && entries[idx].Dir {
			rel = relJoin(rel, entries[idx].Name)
			loadDir()
		}
	}
	fileList.onEnter = openCur
	// Tab hops files ⇄ versions.
	fileList.onTab = func() { win.Canvas().Focus(versList) }
	versList.onTab = func() { win.Canvas().Focus(fileList) }

	// ── actions ──
	doRestore = func(overwrite bool) {
		if !haveSel || verSel < 0 || verSel >= len(vers) {
			dialog.ShowInformation("Restore",
				"Pick a file on the left, then one of its snapshot versions on the right.", win)
			return
		}
		v := vers[verSel]
		relPath := relJoin(rel, selEntry.Name)
		argv, dst := RestoreArgv(mp, v.Snapshot, relPath, v.Dir, overwrite)
		msg := fmt.Sprintf("Restore  /%s\nfrom snapshot  @%s\n→  %s\n\nRuns exactly (root):\n  %s",
			relPath, v.Snapshot, dst, strings.Join(argv, " "))
		if overwrite {
			if v.Dir {
				msg += "\n\n⚠ Merges over the live directory — files created since the\nsnapshot survive. Use rollback for a true reset."
			} else {
				msg += "\n\n⚠ Overwrites the live file."
			}
		}
		dialog.ShowConfirm("Restore from snapshot", msg, func(ok bool) {
			if !ok {
				return
			}
			go func() {
				err := RestoreFromSnapshot(h, argv)
				fyne.Do(func() {
					if err != nil {
						dialog.ShowError(err, win)
						return
					}
					dialog.ShowInformation("Restored", "Restored ✓\n"+dst, win)
					loadDir()
				})
			}()
		}, win)
	}
	restoreBtn := widget.NewButtonWithIcon("Restore over live (overwrite)…", theme.HistoryIcon(),
		func() { doRestore(true) })
	restoreBtn.Importance = widget.WarningImportance
	copyBtn := widget.NewButton("Restore as copy…", func() { doRestore(false) })
	diffBtn := widget.NewButtonWithIcon("zfs diff…", theme.ListIcon(),
		func() { showDiffDialog(h, dataset, win, "") })

	upBtn := widget.NewButtonWithIcon("Up", theme.MoveUpIcon(), func() {
		if rel != "" {
			if j := strings.LastIndexByte(rel, '/'); j >= 0 {
				rel = rel[:j]
			} else {
				rel = ""
			}
			loadDir()
		}
	})
	openBtn := widget.NewButtonWithIcon("Open", theme.NavigateNextIcon(), openCur)
	refreshBtn := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), func() { loadDir() })

	// browse source: the live tree, or transparently inside any snapshot
	sourceSel := widget.NewSelect([]string{liveSourceLabel}, nil)
	sourceSel.OnChanged = func(v string) {
		ns := ""
		if v != liveSourceLabel {
			ns = strings.TrimPrefix(v, "@")
		}
		if ns == source || mp == "" {
			return
		}
		source = ns
		loadDir() // same rel on purpose — compare this directory across time
	}

	// ── layout ──
	leftHead := container.NewBorder(nil, nil,
		dialogHeading("FILES", acBlue), container.NewHBox(upBtn, openBtn, refreshBtn))
	left := paneCard(container.NewBorder(
		container.NewVBox(leftHead, pathLbl), nil, nil, nil, fileList))

	right := paneCard(container.NewBorder(
		dialogHeading("VERSIONS — this path across every snapshot", acPurple),
		container.NewVBox(
			widget.NewSeparator(),
			container.NewHBox(restoreBtn, copyBtn, layout.NewSpacer(), diffBtn),
		),
		nil, nil,
		container.NewStack(versList, container.NewCenter(versHint))))

	split := container.NewHSplit(left, right)
	split.SetOffset(0.55)

	topBar := container.NewBorder(nil, nil,
		dialogHeading("SNAPSHOT EXPLORER", acCyan),
		container.NewHBox(widget.NewLabel("browsing:"), sourceSel))
	hint := widget.NewLabelWithStyle(
		"  hover/↑↓ = versions   Enter/Open = enter dir   pick a version → Restore   Esc close",
		fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	bottom := container.NewVBox(statusLbl, hint)

	win.SetContent(container.NewBorder(topBar, bottom, nil, nil, split))
	win.Canvas().SetOnTypedKey(func(e *fyne.KeyEvent) {
		if e.Name == fyne.KeyEscape {
			if ov := win.Canvas().Overlays().Top(); ov != nil {
				win.Canvas().Overlays().Remove(ov)
				return
			}
			win.Close()
		}
	})
	win.Show()
	win.Canvas().Focus(fileList)

	// resolve mountpoint + snapshot timeline, then land in the root dir
	go func() {
		m, err := Mountpoint(h, dataset)
		ss, _ := ListSnapshots(h, dataset)
		fyne.Do(func() {
			if err != nil {
				statusLbl.SetText("✗ " + err.Error())
				pathLbl.SetText(dataset + " — not browsable")
				return
			}
			mp = m
			snaps = ss
			opts := []string{liveSourceLabel}
			for _, s := range ss {
				opts = append(opts, "@"+snapShort(s.Name))
			}
			sourceSel.Options = opts
			if source != "" {
				sourceSel.Selected = "@" + source
			} else {
				sourceSel.Selected = liveSourceLabel
			}
			sourceSel.Refresh()
			loadDir()
		})
	}()
}

// ── zfs diff pane ────────────────────────────────────────────────────────────

// showDiffDialog opens the diff pane for a dataset: pick two snapshots (or
// snapshot → live), run, and read a colored change list filterable by path.
// presetFrom preselects the "from" snapshot (short name).
func showDiffDialog(h Host, dataset string, w fyne.Window, presetFrom string) {
	go func() {
		snaps, err := ListSnapshots(h, dataset)
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if len(snaps) == 0 {
				dialog.ShowInformation("zfs diff",
					"No snapshots of "+dataset+" yet — take one first.", w)
				return
			}
			buildDiffDialog(h, dataset, snaps, w, presetFrom)
		})
	}()
}

const liveDiffLabel = "(live filesystem)"

func buildDiffDialog(h Host, dataset string, snaps []Snapshot, w fyne.Window, presetFrom string) {
	shorts := make([]string, len(snaps))
	for i, s := range snaps {
		shorts[i] = "@" + snapShort(s.Name)
	}

	fromSel := widget.NewSelect(shorts, nil)
	if presetFrom != "" {
		fromSel.Selected = "@" + presetFrom
	} else {
		fromSel.Selected = shorts[len(shorts)-1] // newest — "what changed since"
	}
	toSel := widget.NewSelect(append([]string{liveDiffLabel}, shorts...), nil)
	toSel.Selected = liveDiffLabel

	names := func() (from, to string) {
		from = dataset + "@" + strings.TrimPrefix(fromSel.Selected, "@")
		if toSel.Selected != liveDiffLabel {
			to = dataset + "@" + strings.TrimPrefix(toSel.Selected, "@")
		}
		return
	}

	cmdLbl := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	refreshCmd := func() {
		from, to := names()
		cmdLbl.SetText("$ " + DiffCommand(from, to))
	}
	fromSel.OnChanged = func(string) { refreshCmd() }
	toSel.OnChanged = func(string) { refreshCmd() }
	refreshCmd()

	out := widget.NewRichText()
	out.Wrapping = fyne.TextWrapOff
	outScroll := container.NewScroll(out)
	sum := widget.NewLabelWithStyle("pick the range, then Run", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	filter := widget.NewEntry()
	filter.SetPlaceHolder("filter by path…")

	var rows []DiffEntry
	render := func() {
		q := strings.ToLower(strings.TrimSpace(filter.Text))
		mono := fyne.TextStyle{Monospace: true}
		seg := func(s string, cn fyne.ThemeColorName) *widget.TextSegment {
			return &widget.TextSegment{Text: s, Style: widget.RichTextStyle{Inline: true, TextStyle: mono, ColorName: cn}}
		}
		var segs []widget.RichTextSegment
		shown := 0
		for _, d := range rows {
			if q != "" && !strings.Contains(strings.ToLower(d.Path+" "+d.Extra), q) {
				continue
			}
			shown++
			var cn fyne.ThemeColorName
			var label string
			switch d.Change {
			case "+":
				cn, label = theme.ColorNameSuccess, "+ added   "
			case "-":
				cn, label = theme.ColorNameError, "- removed "
			case "M":
				cn, label = theme.ColorNameWarning, "M modified"
			case "R":
				cn, label = theme.ColorNamePrimary, "R renamed "
			default:
				cn, label = theme.ColorNameForeground, d.Change+"         "
			}
			segs = append(segs, seg(label+"  ", cn), seg(d.Path, theme.ColorNameForeground))
			if d.Extra != "" {
				segs = append(segs, seg("  →  "+d.Extra, cn))
			}
			segs = append(segs, seg("\n", theme.ColorNameForeground))
		}
		if len(rows) > 0 && shown < len(rows) {
			sum.SetText(fmt.Sprintf("%d of %d changes match \"%s\"", shown, len(rows), q))
		}
		out.Segments = segs
		out.Refresh()
	}
	filter.OnChanged = func(string) { render() }

	runBtn := widget.NewButtonWithIcon("Run", theme.MediaPlayIcon(), nil)
	runBtn.Importance = widget.HighImportance
	runBtn.OnTapped = func() {
		from, to := names()
		runBtn.Disable()
		sum.SetText("running " + DiffCommand(from, to) + " …")
		go func() {
			ds, err := SnapshotDiff(h, from, to)
			fyne.Do(func() {
				runBtn.Enable()
				if err != nil {
					sum.SetText("✗ " + err.Error())
					rows = nil
					render()
					return
				}
				rows = ds
				var m, a, r, rn int
				for _, d := range rows {
					switch d.Change {
					case "M":
						m++
					case "+":
						a++
					case "-":
						r++
					case "R":
						rn++
					}
				}
				sum.SetText(fmt.Sprintf("%d changes   ·   M %d modified · + %d added · - %d removed · R %d renamed",
					len(rows), m, a, r, rn))
				render()
			})
		}()
	}

	controls := container.NewHBox(
		widget.NewLabel("from"), fromSel,
		widget.NewLabel("→ to"), toSel,
		layout.NewSpacer(), runBtn)
	top := container.NewVBox(
		dialogHeading("ZFS DIFF — what changed between two points in time", acGold),
		controls, cmdLbl, filter)
	body := container.NewBorder(top, sum, nil, nil, outScroll)

	d := dialog.NewCustom("zfs diff — "+dataset, "Close", body, w)
	d.Resize(fyne.NewSize(1000, 660))
	d.Show()
}

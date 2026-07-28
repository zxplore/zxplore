//go:build gui

// gui_pools.go — the pool manager (F4).
//
// Turns the read-only ZPOOLS overview into action: select a pool and scrub, stop
// a scrub, trim, clear device errors, or view full `zpool status`. All mutations
// are privileged (pkexec / delegated ssh).
package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// showPoolManager acts on pools. onChange tells the main window to rescan
// after something structural (an import) changed what exists.
func showPoolManager(w fyne.Window, onChange func()) {
	host := LocalHost()
	pools, _ := ListPools(host)
	sel := -1

	var list *widget.List
	list = widget.NewList(
		func() int { return len(pools) },
		func() fyne.CanvasObject { return widget.NewLabel("t") },
		func(i widget.ListItemID, o fyne.CanvasObject) { o.(*widget.Label).SetText(pools[i]) },
	)
	list.OnSelected = func(i widget.ListItemID) { sel = int(i) }

	empty := widget.NewLabelWithStyle(
		"No pools imported.\n\nScan / Import finds exported pools on this\nmachine's disks (zpool import).",
		fyne.TextAlignCenter, fyne.TextStyle{})
	syncEmpty := func() {
		if len(pools) == 0 {
			empty.Show()
		} else {
			empty.Hide()
		}
	}
	syncEmpty()
	refreshPools := func() {
		pools, _ = ListPools(host)
		if sel >= len(pools) {
			sel = -1
		}
		list.UnselectAll()
		list.Refresh()
		syncEmpty()
	}

	runOp := func(verb string, fn func() error) {
		go func() {
			err := fn()
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, w)
				} else {
					dialog.ShowInformation(verb, verb+" ✓", w)
				}
			})
		}()
	}
	selPool := func() string {
		if sel >= 0 && sel < len(pools) {
			return pools[sel]
		}
		return ""
	}
	act := func(label string, fn func(string) error) *widget.Button {
		return widget.NewButton(label, func() {
			if p := selPool(); p != "" {
				runOp(label, func() error { return fn(p) })
			}
		})
	}

	scrubBtn := act("Scrub", func(p string) error { return ScrubPool(host, p, true) })
	stopBtn := act("Stop scrub", func(p string) error { return ScrubPool(host, p, false) })
	trimBtn := act("Trim", func(p string) error { return TrimPool(host, p) })
	clearBtn := act("Clear errors", func(p string) error { return ClearPool(host, p) })
	// Drill down: the pool dossier — vitals, BOTH space truths (zfs vs df),
	// vdev tree with error counters, one-shot iostat — plus a jump into the
	// pool's file layout via the snapshot explorer.
	drillBtn := widget.NewButton("Drill down…", func() {
		p := selPool()
		if p == "" {
			return
		}
		go func() {
			txt := PoolDossier(host, p)
			fyne.Do(func() {
				rt := widget.NewRichText()
				rt.Wrapping = fyne.TextWrapOff
				rt.Segments = dossierSegments(txt)
				browse := widget.NewButton("Browse files…", func() {
					showSnapshotExplorer(host, p, "")
				})
				body := container.NewBorder(nil, container.NewHBox(browse), nil, nil,
					container.NewScroll(rt))
				sd := dialog.NewCustom("Pool — "+p, "Close", body, w)
				sd.Resize(fyne.NewSize(1020, 700))
				sd.Show()
			})
		}()
	})
	browseBtn := widget.NewButton("Browse files…", func() {
		if p := selPool(); p != "" {
			showSnapshotExplorer(host, p, "")
		}
	})

	// Scan / Import: find exported pools on this machine's devices and import
	// one — the fallback path for "ZFS is here but no pools are imported"
	// (moved disks, rescue boots). Shows the literal command; runs privileged.
	var importBtn *widget.Button
	importBtn = widget.NewButton("Scan / Import…", func() {
		importBtn.Disable()
		go func() {
			names, err := ImportablePools(host)
			fyne.Do(func() {
				importBtn.Enable()
				if err != nil {
					dialog.ShowError(err, w)
					return
				}
				if len(names) == 0 {
					dialog.ShowInformation("Import",
						"No exported pools found on this machine's devices.", w)
					return
				}
				var pd dialog.Dialog
				box := container.NewVBox(widget.NewLabel("Exported pools found — pick one to import:"))
				for _, n := range names {
					n := n
					box.Add(widget.NewButton("Import  "+n+"   (zpool import "+n+")", func() {
						pd.Hide()
						go func() {
							e := ImportPool(host, n)
							fyne.Do(func() {
								if e != nil {
									dialog.ShowError(e, w)
									return
								}
								dialog.ShowInformation("Import", n+" imported ✓", w)
								refreshPools()
								if onChange != nil {
									onChange()
								}
							})
						}()
					}))
				}
				pd = dialog.NewCustom("Importable pools", "Cancel", box, w)
				pd.Show()
			})
		}()
	})

	buttons := container.NewHBox(drillBtn, browseBtn, scrubBtn, stopBtn, trimBtn, clearBtn, importBtn)
	body := container.NewBorder(
		widget.NewLabelWithStyle("Pools — select one, then act", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		buttons, nil, nil, container.NewStack(list, container.NewCenter(empty)))
	d := dialog.NewCustom("Pools", "Close", body, w)
	d.Resize(fyne.NewSize(720, 420))
	d.Show()
}

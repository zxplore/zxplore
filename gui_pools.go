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

func showPoolManager(w fyne.Window) {
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
	statusBtn := widget.NewButton("Status…", func() {
		p := selPool()
		if p == "" {
			return
		}
		go func() {
			txt, err := PoolStatusText(host, p)
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, w)
					return
				}
				e := widget.NewMultiLineEntry()
				e.SetText(txt)
				e.TextStyle = fyne.TextStyle{Monospace: true}
				sd := dialog.NewCustom("zpool status "+p, "Close", container.NewScroll(e), w)
				sd.Resize(fyne.NewSize(780, 520))
				sd.Show()
			})
		}()
	})

	buttons := container.NewHBox(scrubBtn, stopBtn, trimBtn, clearBtn, statusBtn)
	body := container.NewBorder(
		widget.NewLabelWithStyle("Pools — select one, then act", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		buttons, nil, nil, list)
	d := dialog.NewCustom("Pools", "Close", body, w)
	d.Resize(fyne.NewSize(720, 420))
	d.Show()
}

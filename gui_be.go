//go:build gui

// gui_be.go — the boot-environment manager.
//
// Lists the boot environments (snapshots of the active boot dataset, derived from
// the pool's bootfs), and lets you create one, roll back to one, or delete one —
// all privileged. "Activate" (boot into a different BE) is deferred: it needs the
// clone-and-set-bootfs flow, tracked separately.
package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func showBootEnvManager(w fyne.Window) {
	host := LocalHost()
	var bes []BootEnv
	sel := -1

	title := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	var list *widget.List
	list = widget.NewList(
		func() int { return len(bes) },
		func() fyne.CanvasObject { return widget.NewLabel("t") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			b := bes[i]
			short := b.Snapshot
			if j := strings.IndexByte(short, '@'); j >= 0 {
				short = short[j+1:]
			}
			o.(*widget.Label).SetText(fmt.Sprintf("%-26s %8s  %s", short, b.Used, b.Created))
		},
	)
	list.OnSelected = func(i widget.ListItemID) { sel = int(i) }

	reload := func() {
		var bd string
		var err error
		bes, bd, err = ListBootEnvs(host)
		if err != nil {
			title.SetText("Boot environments — " + err.Error())
		} else {
			title.SetText("Boot environments of " + bd + "   (snapshots = restore points)")
		}
		sel = -1
		list.UnselectAll()
		list.Refresh()
	}
	reload()

	runOp := func(verb string, fn func() error) {
		if !guiMutOK(w) {
			return
		}
		go func() {
			err := fn()
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, w)
				} else {
					dialog.ShowInformation(verb, verb+" ✓", w)
				}
				reload()
			})
		}()
	}
	selName := func() string {
		if sel >= 0 && sel < len(bes) {
			return bes[sel].Snapshot
		}
		return ""
	}

	createBtn := widget.NewButton("Create…", func() {
		e := widget.NewEntry()
		e.SetPlaceHolder("BE name")
		dialog.ShowForm("Create boot environment", "Create", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("Name", e)}, func(ok bool) {
				if ok && strings.TrimSpace(e.Text) != "" {
					runOp("boot environment", func() error { return CreateBootEnv(host, strings.TrimSpace(e.Text)) })
				}
			}, w)
	})
	rollbackBtn := widget.NewButton("Roll back", func() {
		snap := selName()
		if snap == "" {
			return
		}
		if !guiMutOK(w) {
			return
		}
		short := snap
		if j := strings.IndexByte(short, '@'); j >= 0 {
			short = short[j+1:]
		}
		confirmTyped(w, "⚠ Roll back the BOOT dataset",
			"Rolls back to\n  "+snap+"\nTakes effect on REBOOT and destroys newer boot environments.",
			short, func() {
				runOp("rollback", func() error { return RollbackBootEnv(host, snap) })
			})
	})
	rollbackBtn.Importance = widget.WarningImportance
	delBtn := widget.NewButton("Delete", func() {
		snap := selName()
		if snap == "" {
			return
		}
		if !guiMutOK(w) {
			return
		}
		short := snap
		if j := strings.IndexByte(short, '@'); j >= 0 {
			short = short[j+1:]
		}
		confirmTyped(w, "✖ Delete boot environment",
			"Permanently deletes\n  "+snap, short, func() {
				runOp("delete", func() error { return DeleteBootEnv(host, snap) })
			})
	})
	delBtn.Importance = widget.DangerImportance

	buttons := container.NewHBox(createBtn, rollbackBtn, delBtn)
	body := container.NewBorder(title, buttons, nil, nil, list)
	d := dialog.NewCustom("Boot Environments", "Close", body, w)
	d.Resize(fyne.NewSize(700, 440))
	d.Show()
}

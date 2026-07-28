//go:build gui

// gui_edit.go — the inline property editor (the ✎ Edit side of the dossier).
//
// Renders a dataset's SETTABLE properties as live controls — checkbox (on/off),
// dropdown (enum), or field (free value) — grouped like the read view. Changing
// a control applies immediately via SetProp (root, elevated with pkexec); risky
// properties confirm first. Read-only properties never appear here (they're in
// the read view). The point: to change something, you just edit the value.
package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// snapshotActionDialog is the menu shown when a snapshot is activated (Enter or
// click): roll back / clone / browse / diff / hold / release / destroy. An
// arrow-first action menu (↑/↓ + Enter, or click) — NOT a button stack, which
// the arrows can't drive. Every mutation runs privileged; destructive ones
// confirm first. onDone refreshes the caller after any action.
func snapshotActionDialog(host Host, snap string, w fyne.Window, onDone func()) {
	short := snap
	ds := snap
	if i := strings.IndexByte(snap, '@'); i >= 0 {
		short = snap[i+1:]
		ds = snap[:i]
	}
	run := func(verb string, fn func() error) {
		go func() {
			e := fn()
			fyne.Do(func() {
				if e != nil {
					dialog.ShowError(e, w)
				} else {
					dialog.ShowInformation("Done", verb+" ✓", w)
				}
				onDone()
			})
		}()
	}
	showActionMenu("@"+short, []menuAction{
		{"⚠ Roll back to this snapshot…", func() {
			dialog.ShowConfirm("Roll back",
				"Roll back to\n  "+snap+"\n\n⚠ This DESTROYS every snapshot newer than this one\n(and their clones). Continue?",
				func(ok bool) {
					if ok {
						run("rolled back", func() error { return Rollback(host, snap) })
					}
				}, w)
		}},
		{"Clone to a new dataset…", func() {
			e := widget.NewEntry()
			e.SetPlaceHolder("pool/new-dataset")
			doClone := func() {
				if strings.TrimSpace(e.Text) != "" {
					run("cloned", func() error { return Clone(host, snap, strings.TrimSpace(e.Text)) })
				}
			}
			fd := dialog.NewForm("Clone "+short, "Clone", "Cancel",
				[]*widget.FormItem{widget.NewFormItem("New dataset", e)},
				func(ok bool) {
					if ok {
						doClone()
					}
				}, w)
			e.OnSubmitted = func(string) { doClone(); fd.Hide() }
			fd.Show()
			w.Canvas().Focus(e)
		}},
		{"Browse / restore files in this snapshot…", func() {
			showSnapshotExplorer(host, ds, short)
		}},
		{"What changed since this snapshot — zfs diff…", func() {
			showDiffDialog(host, ds, w, short)
		}},
		{"Bookmark (keeps the incremental chain)…", func() {
			e := widget.NewEntry()
			e.SetText(short)
			dialog.ShowForm("Bookmark "+short, "Bookmark", "Cancel",
				[]*widget.FormItem{widget.NewFormItem("Name (→ "+ds+"#…)", e)},
				func(ok bool) {
					if ok && strings.TrimSpace(e.Text) != "" {
						run("bookmarked", func() error {
							return CreateBookmark(host, snap, strings.TrimSpace(e.Text))
						})
					}
				}, w)
		}},
		{"Hold (prevent destroy)", func() {
			run("held", func() error { return HoldSnap(host, snap) })
		}},
		{"Release hold", func() {
			run("released", func() error { return ReleaseSnap(host, snap) })
		}},
		{"✖ Destroy snapshot…", func() {
			dialog.ShowConfirm("Destroy",
				"Permanently destroy\n  "+snap+" ?", func(ok bool) {
					if ok {
						run("destroyed", func() error { return DestroySnapshot(host, snap) })
					}
				}, w)
		}},
	}, w)
}

// buildEditForm renders the settable properties of a dataset as live controls,
// grouped like the dossier. onApplied runs after any apply attempt (success,
// failure, or cancel) so the caller can rebuild the form and re-sync controls to
// the real values.
func buildEditForm(host Host, dataset string, w fyne.Window, onApplied func()) fyne.CanvasObject {
	props, err := DatasetProps(host, dataset)
	if err != nil {
		return widget.NewLabel("cannot read properties: " + err.Error())
	}
	byName := map[string]Prop{}
	for _, p := range props {
		byName[p.Name] = p
	}

	apply := func(prop, val string) {
		run := func() {
			go func() {
				e := SetProp(host, dataset, prop, val)
				fyne.Do(func() {
					if e != nil {
						dialog.ShowError(e, w)
					}
					onApplied() // rebuild → controls reflect the real (possibly rejected) value
				})
			}()
		}
		if riskyProps[prop] {
			dialog.ShowConfirm("Change "+prop,
				fmt.Sprintf("Set  %s = %s\non  %s ?\n\nApplies immediately (needs root).", prop, val, dataset),
				func(ok bool) {
					if ok {
						run()
					} else {
						onApplied() // re-sync the control back to the real value
					}
				}, w)
		} else {
			run()
		}
	}

	form := container.NewVBox(
		widget.NewLabelWithStyle("Editing  "+dataset+"   —   changes apply immediately (root)",
			fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
	)
	for _, g := range propGroups {
		var rows []fyne.CanvasObject
		for _, name := range g.props {
			if p, ok := byName[name]; ok && p.Settable {
				rows = append(rows, editRow(p, apply))
			}
		}
		if len(rows) == 0 {
			continue
		}
		form.Add(widget.NewLabelWithStyle("━━ "+g.title+" ━━", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		form.Add(container.NewGridWithColumns(2, rows...))
	}
	return container.NewScroll(form)
}

// editRow builds one "name → control" row. Callbacks attach AFTER the initial
// value is seeded, so seeding never fires a spurious apply.
func editRow(p Prop, apply func(prop, val string)) fyne.CanvasObject {
	name := widget.NewLabelWithStyle(p.Name, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	var ctrl fyne.CanvasObject
	switch p.Control.Kind {
	case "bool":
		c := widget.NewCheck("", nil)
		c.SetChecked(p.Value == "on")
		c.OnChanged = func(on bool) {
			v := "off"
			if on {
				v = "on"
			}
			if v != p.Value {
				apply(p.Name, v)
			}
		}
		ctrl = c
	case "enum":
		s := widget.NewSelect(p.Control.Options, nil)
		s.SetSelected(p.Value)
		s.OnChanged = func(v string) {
			if v != "" && v != p.Value {
				apply(p.Name, v)
			}
		}
		ctrl = s
	default:
		e := widget.NewEntry()
		e.SetText(p.Value)
		e.OnSubmitted = func(v string) {
			if v != p.Value {
				apply(p.Name, v)
			}
		}
		ctrl = e
	}
	return container.NewBorder(nil, nil, name, nil, ctrl)
}

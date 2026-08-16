//go:build gui

// =============================================================================
// gui_containers.go — the Containers tab.
//
// WHAT IT DOES, IN ORDER:
//   1. Says which engine is running and whether its layers are ZFS datasets.
//   2. Lists containers by NAME, with state, image and published ports.
//   3. Offers the four verbs worth a button, with a confirm on the one that
//      destroys something.
//   4. Lists the local images beside them.
//
// WHY IT IS IN A ZFS CONSOLE:
//   Container management on Linux is the CLI, a web UI that itself runs as a
//   container, or a terminal TUI. None of them know anything about the
//   storage underneath. On this substrate every image layer is a dataset —
//   which is why the dataset tree fills with hash-named entries that mean
//   nothing on their own. This tab is the readable half of that same
//   information, and the header states the connection outright.
//
// Notes:
//   - Nothing here mutates until a button is pressed, and `rm` asks first.
//   - The list refreshes on a timer as well as on demand: a container that
//     died while you were reading is the case you most want to see.
// =============================================================================

package main

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// containersTab builds the page. It owns its own refresh loop and returns a
// canvas object the tab bar can show and hide.
func containersTab(w fyne.Window) fyne.CanvasObject {
	engine := DetectEngine(5 * time.Second)

	header := widget.NewLabel("")
	header.Wrapping = fyne.TextWrapWord
	setHeader := func(e Engine) {
		switch {
		case !e.Found:
			header.SetText(e.Why)
		case e.OnZFS():
			header.SetText(fmt.Sprintf(
				"%s · storage driver zfs · %s — every image layer is a dataset, "+
					"so pulls are clones and layers inherit compression.",
				e.Name, e.GraphRoot))
		default:
			header.SetText(fmt.Sprintf(
				"%s · storage driver %s · %s — layers are ordinary files here, "+
					"not datasets.", e.Name, e.Driver, e.GraphRoot))
		}
	}
	setHeader(engine)

	var (
		conts    []Container
		selected = -1
	)

	contList := widget.NewList(
		func() int { return len(conts) },
		func() fyne.CanvasObject {
			name := widget.NewLabel("")
			name.TextStyle = fyne.TextStyle{Bold: true}
			state := widget.NewLabel("")
			detail := widget.NewLabel("")
			detail.TextStyle = fyne.TextStyle{Italic: true}
			return container.NewBorder(nil, nil, container.NewHBox(state, name), detail)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < 0 || id >= len(conts) {
				return
			}
			c := conts[id]
			box := o.(*fyne.Container)
			left := box.Objects[1].(*fyne.Container)
			// A glyph rather than a colour: this list is read at a glance and
			// the state is the first thing anybody wants from it.
			mark := "○"
			if c.Running() {
				mark = "●"
			}
			left.Objects[0].(*widget.Label).SetText(mark)
			left.Objects[1].(*widget.Label).SetText(c.Name)
			d := c.Image
			if c.Ports != "" {
				d += "   " + c.Ports
			}
			if c.Status != "" {
				d += "   " + c.Status
			}
			box.Objects[0].(*widget.Label).SetText(d)
		},
	)
	contList.OnSelected = func(id widget.ListItemID) { selected = int(id) }

	imgBox := container.NewVBox()

	refresh := func() {
		e := DetectEngine(5 * time.Second)
		cs, cerr := e.ListContainers(8 * time.Second)
		is, ierr := e.ListImages(8 * time.Second)
		fyne.Do(func() {
			engine = e
			setHeader(e)
			conts = cs
			contList.Refresh()

			imgBox.Objects = nil
			switch {
			case ierr != nil:
				imgBox.Add(widget.NewLabel(ierr.Error()))
			case len(is) == 0:
				imgBox.Add(widget.NewLabel("no images pulled yet"))
			default:
				for _, im := range is {
					imgBox.Add(container.NewBorder(nil, nil,
						widget.NewLabel(im.Ref()),
						widget.NewLabel(im.Size)))
				}
			}
			imgBox.Refresh()
			if cerr != nil {
				// Shown in the header rather than a dialog: a refresh loop
				// that pops a modal every few seconds is unusable.
				header.SetText(cerr.Error())
			}
		})
	}

	// current returns the selected container, or reports why not.
	current := func() (Container, bool) {
		if selected < 0 || selected >= len(conts) {
			dialog.ShowError(fmt.Errorf("pick a container first"), w)
			return Container{}, false
		}
		return conts[selected], true
	}

	act := func(action string) {
		c, ok := current()
		if !ok {
			return
		}
		do := func() {
			go func() {
				err := engine.Verb(action, c.ID, 30*time.Second)
				fyne.Do(func() {
					if err != nil {
						dialog.ShowError(err, w)
					}
				})
				refresh()
			}()
		}
		if action == "rm" {
			// The only verb here that destroys anything. A stopped container
			// still owns its writable layer — a dataset holding real space —
			// so removal is worth one question.
			dialog.ShowConfirm("Remove "+c.Name+"?",
				"The container and its writable layer are destroyed.\n"+
					"The image it came from is kept.",
				func(yes bool) {
					if yes {
						do()
					}
				}, w)
			return
		}
		do()
	}

	buttons := container.NewGridWithColumns(5,
		widget.NewButtonWithIcon("Start", theme.MediaPlayIcon(), func() { act("start") }),
		widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), func() { act("stop") }),
		widget.NewButtonWithIcon("Restart", theme.ViewRefreshIcon(), func() { act("restart") }),
		widget.NewButtonWithIcon("Logs", theme.DocumentIcon(), func() {
			c, ok := current()
			if !ok {
				return
			}
			go func() {
				out, err := engine.Logs(c.ID, 300, 15*time.Second)
				fyne.Do(func() {
					if err != nil {
						dialog.ShowError(err, w)
						return
					}
					if strings.TrimSpace(out) == "" {
						out = "(this container has written nothing to its log)"
					}
					e := widget.NewMultiLineEntry()
					e.SetText(out)
					e.Wrapping = fyne.TextWrapOff
					d := dialog.NewCustom("Logs — "+c.Name, "Close",
						container.NewScroll(e), w)
					d.Resize(fyne.NewSize(900, 600))
					d.Show()
				})
			}()
		}),
		widget.NewButtonWithIcon("Remove", theme.DeleteIcon(), func() { act("rm") }),
	)

	body := container.NewHSplit(
		card(container.NewBorder(
			heading("CONTAINERS", acGold), buttons, nil, nil, contList)),
		card(container.NewBorder(
			heading("IMAGES", acGold), nil, nil, nil, container.NewScroll(imgBox))),
	)
	body.SetOffset(0.62)

	page := container.NewBorder(
		container.NewVBox(header, widget.NewSeparator()), nil, nil, nil, body)

	// First read immediately, then keep it current. A container that exited
	// while the tab was open is exactly what somebody is looking for.
	go refresh()
	go func() {
		for range time.Tick(10 * time.Second) {
			refresh()
		}
	}()
	return page
}

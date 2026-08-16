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
	"fyne.io/fyne/v2/canvas"
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
	// The header is a STATUS LINE, never the explanation. When something is
	// wrong the detail goes in the list area and nowhere else — b38 printed
	// the same six-line permission message in the header, the containers pane
	// AND the images pane, which is three times the words and no more
	// information (fiend, 2026-08-16).
	setHeader := func(e Engine) {
		switch {
		case !e.Found:
			header.SetText(e.Name + " — not reachable from this session")
		case e.OnZFS():
			t := fmt.Sprintf("%s · storage driver zfs · %s — every image layer is "+
				"a dataset, so pulls are clones and layers inherit compression.",
				e.Name, e.GraphRoot)
			if e.ViaSudo {
				t += "  [via sudo — log out and back in to use the docker group directly]"
			}
			header.SetText(t)
		default:
			t := fmt.Sprintf("%s · storage driver %s · %s — layers are ordinary "+
				"files here, not datasets.", e.Name, e.Driver, e.GraphRoot)
			if e.ViaSudo {
				t += "  [via sudo]"
			}
			header.SetText(t)
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
			// canvas.Text, not widget.Label: a Label takes its colour from the
			// theme and cannot be told otherwise, so every row rendered the
			// same white and the state had to be read from a glyph shape
			// alone. State is the first thing anybody wants from this list.
			name := canvas.NewText("", theme.Color(theme.ColorNameForeground))
			name.TextStyle = fyne.TextStyle{Bold: true}
			name.TextSize = theme.TextSize()
			state := canvas.NewText("", theme.Color(theme.ColorNameForeground))
			state.TextSize = theme.TextSize()
			detail := widget.NewLabel("")
			detail.TextStyle = fyne.TextStyle{Italic: true}
			// A CENTER object is passed (the detail label) so the row has a
			// stretching middle. Without one, NewBorder's Objects slice is
			// just [left, right] — and indexing it as though a center were
			// there panics inside the list renderer, which Fyne swallows into
			// a blank list: containers existed, `docker ps` listed them, and
			// the tab showed nothing (2026-08-16).
			return container.NewBorder(nil, nil, container.NewHBox(state, name), nil, detail)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < 0 || id >= len(conts) {
				return
			}
			c := conts[id]
			box, ok := o.(*fyne.Container)
			// Defensive: a mis-shaped template must not take the pane down.
			if !ok || len(box.Objects) < 2 {
				return
			}
			// NewBorder appends the CENTER first, then the edges in order, so
			// Objects is [detail, leftHBox].
			detail, dOK := box.Objects[0].(*widget.Label)
			left, lOK := box.Objects[1].(*fyne.Container)
			if !dOK || !lOK || len(left.Objects) < 2 {
				return
			}
			stateT, sOK := left.Objects[0].(*canvas.Text)
			nameT, nOK := left.Objects[1].(*canvas.Text)
			if !sOK || !nOK {
				return
			}
			// Shape AND colour. The glyph alone survives a colourblind reader
			// and a screenshot; the colour is what makes the list scannable
			// without reading it.
			mark, col := "○", acRed.at()
			switch strings.ToLower(c.State) {
			case "running":
				mark, col = "●", acGreen.at()
			case "paused":
				mark, col = "◐", acGold.at()
			case "created", "restarting":
				mark, col = "◌", acGold.at()
			}
			stateT.Text, stateT.Color = mark, col
			nameT.Text = c.Name
			// The NAME carries the state colour too, so a running container
			// reads green at a glance rather than only its bullet.
			nameT.Color = col
			stateT.Refresh()
			nameT.Refresh()
			d := c.Image
			if c.Ports != "" {
				d += "   " + c.Ports
			}
			if c.Status != "" {
				d += "   " + c.Status
			}
			detail.SetText(d)
		},
	)
	contList.OnSelected = func(id widget.ListItemID) { selected = int(id) }

	imgBox := container.NewVBox()

	// A problem is shown WHERE THE LIST WOULD BE, not only in the header.
	// An empty list beside working-looking buttons is indistinguishable from
	// "you have no containers", which is how a permission error read as an
	// app with nothing in it (fiend, 2026-08-16).
	problem := widget.NewLabel("")
	problem.Wrapping = fyne.TextWrapWord
	problem.Hide()
	listArea := container.NewStack(contList, problem)

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
			case ierr != nil && !e.Found:
				// The list area already carries the reason; repeating it here
				// is three copies of one sentence.
				imgBox.Add(widget.NewLabel("—"))
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

			// Say what is wrong where the list would be. No dialog: a refresh
			// loop that pops a modal every few seconds is unusable.
			switch {
			case !e.Found:
				problem.SetText(e.Why)
				problem.Show()
				contList.Hide()
			case cerr != nil:
				problem.SetText(cerr.Error())
				problem.Show()
				contList.Hide()
			case len(cs) == 0:
				problem.SetText("No containers on this host yet.\n\n" +
					"docker run -d --name web -p 8088:80 nginx:alpine")
				problem.Show()
				contList.Hide()
			default:
				problem.Hide()
				contList.Show()
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

	logsBtn := widget.NewButtonWithIcon("Logs", theme.DocumentIcon(), func() {
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
	})
	buttons := container.NewGridWithColumns(5,
		tintBtn(widget.NewButtonWithIcon("Start", theme.MediaPlayIcon(),
			func() { act("start") }), acGreen),
		tintBtn(widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(),
			func() { act("stop") }), acRed),
		tintBtn(widget.NewButtonWithIcon("Restart", theme.ViewRefreshIcon(),
			func() { act("restart") }), acGold),
		tintBtn(logsBtn, acCyan),
		tintBtn(widget.NewButtonWithIcon("Remove", theme.DeleteIcon(),
			func() { act("rm") }), acCrimson),
	)

	// ── the estate verbs ────────────────────────────────────────────
	// These act on the STORE, not on one container: with the zfs driver the
	// store dataset carries the layers, the engine's database and the
	// volumes, so a snapshot is the whole estate and a send moves it.
	snapBtn := widget.NewButtonWithIcon("Snapshot estate", theme.StorageIcon(), func() {
		e := widget.NewEntry()
		e.SetText("before-" + time.Now().Format("20060102-1504"))
		dialog.ShowForm("Snapshot the container estate", "Snapshot", "Cancel",
			[]*widget.FormItem{{Text: "name", Widget: e}},
			func(ok bool) {
				if !ok {
					return
				}
				go func() {
					snap, err := engine.SnapshotStore(e.Text, 60*time.Second)
					fyne.Do(func() {
						if err != nil {
							dialog.ShowError(err, w)
							return
						}
						dialog.ShowInformation("Estate captured",
							"Every image layer, the engine's database and the\n"+
								"volumes, in one recursive snapshot:\n\n"+snap, w)
					})
				}()
			}, w)
	})
	rollBtn := widget.NewButtonWithIcon("Roll back…", theme.ContentUndoIcon(), func() {
		go func() {
			snaps, err := engine.ListStoreSnapshots(20 * time.Second)
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, w)
					return
				}
				if len(snaps) == 0 {
					dialog.ShowInformation("Nothing to roll back to",
						"Take a snapshot of the estate first.", w)
					return
				}
				opts := make([]string, 0, len(snaps))
				for _, sn := range snaps {
					opts = append(opts, sn.Name+"   ("+sn.Used+")")
				}
				sel := widget.NewSelect(opts, nil)
				sel.SetSelected(opts[len(opts)-1])
				dialog.ShowCustomConfirm("Roll the estate back", "Roll back", "Cancel",
					container.NewVBox(
						widget.NewLabel("Every image, container and volume returns to this "+
							"point. Snapshots newer than it are destroyed."),
						widget.NewLabel("The engine must be stopped first — this refuses "+
							"while it is running, because rolling storage out from under "+
							"an open store corrupts it."),
						sel),
					func(ok bool) {
						if !ok {
							return
						}
						tag := strings.Fields(sel.Selected)[0]
						go func() {
							err := engine.RollbackStore(tag, 120*time.Second)
							fyne.Do(func() {
								if err != nil {
									dialog.ShowError(err, w)
									return
								}
								dialog.ShowInformation("Rolled back",
									"The estate is back at "+tag+".", w)
							})
							refresh()
						}()
					}, w)
			})
		}()
	})
	replBtn := widget.NewButtonWithIcon("Replicate…", theme.MailForwardIcon(), func() {
		go func() {
			ds, ok := engine.StoreDataset(10 * time.Second)
			enc := engine.StoreEncrypted(10 * time.Second)
			snaps, _ := engine.ListStoreSnapshots(20 * time.Second)
			fyne.Do(func() {
				if !ok {
					dialog.ShowError(fmt.Errorf(
						"the container store is not on a ZFS dataset, so there is "+
							"nothing to replicate as a unit (driver is %q)", engine.Driver), w)
					return
				}
				latest := "<take a snapshot first>"
				if len(snaps) > 0 {
					latest = snaps[len(snaps)-1].Full
				}
				argv := strings.Join(ReplicateArgv(latest, "", enc), " ")
				// Shown, not run. The far side needs a host, a key and a
				// receiving dataset, and zxplore already has a Transfer
				// tab built for exactly that conversation.
				cmd := widget.NewMultiLineEntry()
				cmd.SetText(argv + " | ssh OTHER-HOST zfs recv -F " + ds)
				cmd.Wrapping = fyne.TextWrapWord
				d := dialog.NewCustom("Replicate the container estate", "Close",
					container.NewVBox(
						widget.NewLabel("One recursive stream carries every image layer, "+
							"the engine's database and the volumes."),
						widget.NewLabel(encNote(enc)),
						cmd,
						widget.NewLabel("After the first send, add -I <previous-snapshot> "+
							"and only the delta travels."),
					), w)
				d.Resize(fyne.NewSize(760, 340))
				d.Show()
			})
		}()
	})
	// Green creates, gold is the one to think about, purple is transfer —
	// the same three meanings the Browser/Transfer/Explorer tabs already use.
	storeBar := container.NewGridWithColumns(3,
		tintBtn(snapBtn, acGreen),
		tintBtn(rollBtn, acGold),
		tintBtn(replBtn, acElectric),
	)

	body := container.NewHSplit(
		card(container.NewBorder(
			heading("CONTAINERS", acGold),
			container.NewVBox(buttons, widget.NewSeparator(), storeBar),
			nil, nil, listArea)),
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

// encNote explains why the command says what it says.
func encNote(encrypted bool) string {
	if encrypted {
		return "This pool is encrypted, so the stream is raw (-w): `zfs send -R` " +
			"refuses on an encrypted dataset. The blocks travel still encrypted; " +
			"the far side needs the key to mount them, not to store them."
	}
	return "This pool is not encrypted, so the stream carries properties directly."
}

// tintBtn colours one button by what it DOES, using the same ThemeOverride
// the tab bar uses so the family keeps one visual language.
//
// The mapping is the traffic light everyone already reads without being
// told: green starts, red stops, yellow restarts. Cyan is read-only, and
// crimson — a deeper red than Stop's — marks the one verb that destroys
// rather than halts. A row of identically grey buttons makes "Remove" look
// exactly as safe as "Logs".
func tintBtn(b *widget.Button, a accentPair) fyne.CanvasObject {
	b.Importance = widget.LowImportance
	return container.NewThemeOverride(b, btnTint{compactTheme{theme.DefaultTheme()}, a})
}

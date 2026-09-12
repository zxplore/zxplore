//go:build gui

// gui_builder.go — the Builder tab (F2): design a pool from the disks the
// host has, see what it yields before anything is written, dry-run it with
// zpool's own opinion, then create it. The same canvas draws an imported
// pool's vdev tree, so a design and a running pool read the same way.
//
// Layout, top to bottom:
//   - the SHELF on the left: every whole disk, tick the ones for data; in-use
//     disks show why they are not on offer and cannot be ticked.
//   - CANDIDATES: the layouts Suggest proposes for the ticked disks — one
//     card each with badge, usable / raw / survives and the reason. "Use"
//     loads it into the layout. A custom vdev can be added by hand instead.
//   - the LAYOUT: one row per vdev, disks as chips, role sections in zpool
//     order; ✕ drops a vdev.
//   - the SUMMARY: usable, raw, what it survives, the warnings an operator
//     would say out loud, and the exact zpool create line.
//   - "See a pool": pick an imported pool and its `zpool status -P` tree is
//     drawn in the same rows, state-coloured.
//
// All engine calls are in builder.go; this file only lays them out.
package main

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// builderState is everything the tab shows, so every widget refreshes from
// one place (render) instead of each keeping its own copy.
type builderState struct {
	host   Host
	disks  []Disk
	chosen map[string]bool // Disk.Path → ticked for data
	design Design
	cands  []Candidate
	status string
}

func (b *builderState) free() []Disk {
	var out []Disk
	for _, d := range b.disks {
		if d.InUse == "" {
			out = append(out, d)
		}
	}
	return out
}

// dataDisks are the ticked disks; aux is every other free disk (Suggest turns
// the fast ones into log/cache proposals).
func (b *builderState) dataDisks() (data, aux []Disk) {
	for _, d := range b.free() {
		if b.chosen[d.Path] {
			data = append(data, d)
		} else {
			aux = append(aux, d)
		}
	}
	return
}

// unassigned are ticked disks no vdev uses yet — what "Add vdev" consumes.
func (b *builderState) unassigned() []Disk {
	used := map[string]bool{}
	for _, v := range b.design.Vdevs {
		for _, d := range v.Disks {
			used[d.Path] = true
		}
	}
	data, _ := b.dataDisks()
	var out []Disk
	for _, d := range data {
		if !used[d.Path] {
			out = append(out, d)
		}
	}
	return out
}

func diskGlyph(kind string) string {
	switch kind {
	case "nvme":
		return "▣"
	case "ssd":
		return "◧"
	case "usb":
		return "⊟"
	case "virtual":
		return "▢"
	}
	return "◫"
}

func roleAccent(role string) accentPair {
	switch role {
	case "log":
		return acPurple
	case "cache":
		return acCyan
	case "spare":
		return acGold
	case "special", "dedup":
		return acElectric
	}
	return acBlue
}

func stateAccent(state string) accentPair {
	switch state {
	case "ONLINE", "AVAIL":
		return acGreen
	case "DEGRADED", "OFFLINE", "INUSE":
		return acGold
	case "":
		return acTopic
	}
	return acRed
}

// smallText is a coloured one-liner (canvas.Text) that tracks the theme.
func smallText(s string, a accentPair, size float32, bold bool) *canvas.Text {
	t := canvas.NewText(s, a.at())
	t.TextSize = size
	t.TextStyle = fyne.TextStyle{Bold: bold}
	repaint = append(repaint, func() { t.Color = a.at(); t.Refresh() })
	return t
}

func chip(s string, a accentPair) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.StrokeColor = a.at()
	r.StrokeWidth = 1
	r.CornerRadius = 4
	repaint = append(repaint, func() { r.StrokeColor = a.at(); r.Refresh() })
	t := canvas.NewText(" "+s+" ", a.at())
	t.TextSize = 12
	t.TextStyle = fyne.TextStyle{Monospace: true}
	repaint = append(repaint, func() { t.Color = a.at(); t.Refresh() })
	return container.NewStack(r, t)
}

// gap is vertical air between sections; VBox has none of its own.
func gap(h float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(1, h))
	return r
}

// framed draws content inside an accent-stroked, transparent box of a fixed
// size — candidate cards. Transparent on purpose: a filled card in the
// theme's card colour rendered light behind white labels under the software
// painter (2026-09-05), and the operator's first words on the tab were
// "spacing is bad". A stroke never fights the text.
func framed(content fyne.CanvasObject, a accentPair, size fyne.Size) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.StrokeColor = a.at()
	r.StrokeWidth = 1
	r.CornerRadius = 6
	repaint = append(repaint, func() { r.StrokeColor = a.at(); r.Refresh() })
	_ = size
	return container.NewStack(r, container.NewPadded(container.NewPadded(content)))
}

// showMono opens a scrollable monospace dialog — the dry-run output, the
// create line, an error with context.
func showMono(w fyne.Window, title, text string) {
	l := widget.NewLabel(text)
	l.TextStyle = fyne.TextStyle{Monospace: true}
	sc := container.NewVScroll(l)
	sc.SetMinSize(fyne.NewSize(720, 360))
	dialog.NewCustom(title, "Close", sc, w).Show()
}

// builderUI is the tab plus the handles a test needs to drive it: the state,
// the tick action, the command line the operator sees, the two verbs.
type builderUI struct {
	page    fyne.CanvasObject
	shelf   *navList
	st      *builderState
	toggle  func(int)
	render  func()
	command *widget.Label
	dryRun  *widget.Button
	create  *widget.Button
	// afterCreate is the post-create state update; exported to the test so the
	// status-clobber regression has a guard that does not need a click.
	afterCreate func(Design)
}

// builderTab builds the F2 page. onPoolCreated tells the main window to
// rescan once a pool exists that did not a second ago.
func builderTab(w fyne.Window, switchTab func(fyne.KeyName), onPoolCreated func()) (fyne.CanvasObject, fyne.Focusable) {
	ui := newBuilderUI(w, switchTab, onPoolCreated)
	return ui.page, ui.shelf
}

func newBuilderUI(w fyne.Window, switchTab func(fyne.KeyName), onPoolCreated func()) *builderUI {
	b := &builderState{host: LocalHost(), chosen: map[string]bool{}, design: Design{Name: "tank", Ashift: 12, Compression: "zstd"}}

	// ── shelf ────────────────────────────────────────────────────────────
	shelfTitle := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	var shelf *navList
	var render func()
	toggle := func(i int) {
		if i < 0 || i >= len(b.disks) {
			return
		}
		d := b.disks[i]
		if d.InUse != "" {
			return
		}
		b.chosen[d.Path] = !b.chosen[d.Path]
		// A tick changes the candidate set; the layout is rebuilt from the
		// first candidate so the numbers on screen are never for a stale set.
		b.reflow()
		render()
	}
	shelf = newNavList(
		func() int { return len(b.disks) },
		func() fyne.CanvasObject { return widget.NewLabel("t") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			d := b.disks[i]
			l := o.(*widget.Label)
			box := "☐"
			if b.chosen[d.Path] {
				box = "☑"
			}
			if d.InUse != "" {
				box = "⊘"
				l.TextStyle = fyne.TextStyle{Italic: true}
				l.SetText(fmt.Sprintf("%s %s %-10s %8s  %s — %s", box, diskGlyph(d.Kind), d.Name, d.Size(), d.Model, d.InUse))
				return
			}
			l.TextStyle = fyne.TextStyle{}
			l.SetText(fmt.Sprintf("%s %s %-10s %8s  %s  [%s]", box, diskGlyph(d.Kind), d.Name, d.Size(), d.Model, d.Kind))
		},
	)
	shelf.OnSelected = func(i widget.ListItemID) {
		toggle(int(i))
		shelf.Unselect(i) // a tick, not a selection — the next click must land again
	}
	shelf.onEnter = func() { toggle(shelf.cursor) }
	shelf.onFunc = switchTab
	rescan := func() {
		disks, err := ListDisks(b.host)
		if err != nil {
			b.status = "✗ " + err.Error()
			b.disks = nil
		} else {
			b.disks = disks
			b.status = fmt.Sprintf("%d disks, %d free", len(disks), len(b.free()))
		}
		for p := range b.chosen { // drop ticks for disks that went away
			found := false
			for _, d := range disks {
				if d.Path == p && d.InUse == "" {
					found = true
				}
			}
			if !found {
				delete(b.chosen, p)
			}
		}
		b.reflow()
		render()
	}
	shelfButtons := container.NewHBox(
		widget.NewButton("Rescan", rescan),
		widget.NewButton("Tick all free", func() {
			for _, d := range b.free() {
				b.chosen[d.Path] = true
			}
			b.reflow()
			render()
		}),
		widget.NewButton("Clear", func() {
			b.chosen = map[string]bool{}
			b.design.Vdevs = nil
			b.cands = nil
			render()
		}),
	)
	shelfPane := container.NewPadded(container.NewBorder(
		container.NewVBox(heading("SHELF — tick the disks for data", acCyan), shelfTitle, gap(4)),
		container.NewVBox(gap(6), shelfButtons), nil, nil, shelf))

	// ── pool options ─────────────────────────────────────────────────────
	// The selects fire OnChanged from SetSelected during construction,
	// before render exists — hence the guard (nil deref under the test driver).
	safeRender := func() {
		if render != nil {
			render()
		}
	}
	name := widget.NewEntry()
	name.SetText(b.design.Name)
	name.OnChanged = func(s string) { b.design.Name = strings.TrimSpace(s); safeRender() }
	ashift := widget.NewSelect([]string{"auto", "9 (512B)", "12 (4K)", "13 (8K)"}, func(s string) {
		b.design.Ashift = 0
		if n, err := strconv.Atoi(strings.Fields(s)[0]); err == nil {
			b.design.Ashift = n
		}
		safeRender()
	})
	ashift.SetSelected("12 (4K)")
	compression := widget.NewSelect([]string{"zstd", "lz4", "gzip", "off", "pool default"}, func(s string) {
		if s == "pool default" {
			s = ""
		}
		b.design.Compression = s
		safeRender()
	})
	compression.SetSelected("zstd")
	nameBox := container.NewGridWrap(fyne.NewSize(220, name.MinSize().Height), name)
	options := container.NewHBox(widget.NewLabel("Pool name"), nameBox, gap(1), widget.NewLabel("ashift"), ashift, gap(1), widget.NewLabel("compression"), compression)

	// ── candidates ───────────────────────────────────────────────────────
	// A wrapping grid, not a horizontal scroll: every candidate box is on
	// screen at once ("more spread out so you can see all of the boxes").
	candBox := container.NewGridWrap(fyne.NewSize(310, 300))
	badgeAccent := func(badge string) accentPair {
		switch badge {
		case "RECOMMENDED":
			return acGreen
		case "HIGH IOPS", "FAST RESILVER":
			return acCyan
		case "MAX REDUNDANCY":
			return acBlue
		case "LOW REDUNDANCY":
			return acGold
		}
		return acRed
	}

	// ── custom vdev ──────────────────────────────────────────────────────
	kindSel := widget.NewSelect(vdevKinds, nil)
	kindSel.SetSelected("mirror")
	roleSel := widget.NewSelect(vdevRoles, nil)
	roleSel.SetSelected("data")
	draidData := widget.NewEntry()
	draidData.SetPlaceHolder("dRAID data/group")
	draidSpares := widget.NewEntry()
	draidSpares.SetPlaceHolder("dRAID spares")
	draidDataBox := container.NewGridWrap(fyne.NewSize(150, draidData.MinSize().Height), draidData)
	draidSparesBox := container.NewGridWrap(fyne.NewSize(120, draidSpares.MinSize().Height), draidSpares)
	addVdev := widget.NewButton("Add vdev from unassigned ticks", func() {
		disks := b.unassigned()
		if len(disks) == 0 {
			b.status = "tick some disks on the shelf that no vdev uses yet"
			render()
			return
		}
		v := Vdev{Kind: kindSel.Selected, Role: roleSel.Selected, Disks: disks}
		if v.isDraid() {
			v.DraidData, _ = strconv.Atoi(strings.TrimSpace(draidData.Text))
			v.DraidSpares, _ = strconv.Atoi(strings.TrimSpace(draidSpares.Text))
			if v.DraidData == 0 {
				if dd, sp, ok := draidLayout(len(disks), v.parity()); ok {
					v.DraidData, v.DraidSpares = dd, sp
				}
			}
		}
		b.design.Vdevs = append(b.design.Vdevs, v)
		render()
	})
	custom := container.NewHBox(widget.NewLabel("Custom vdev:"), kindSel, roleSel, draidDataBox, draidSparesBox, gap(1), addVdev,
		widget.NewButton("Clear layout", func() { b.design.Vdevs = nil; render() }))

	// ── layout + summary ─────────────────────────────────────────────────
	layoutBox := container.NewVBox()
	summary := widget.NewLabel("")
	summary.Wrapping = fyne.TextWrapWord
	warnBox := container.NewVBox()
	command := widget.NewLabel("")
	command.TextStyle = fyne.TextStyle{Monospace: true}
	command.Wrapping = fyne.TextWrapWord
	statusLbl := widget.NewLabel("")

	dryRun := widget.NewButton("Dry run (zpool create -n)", func() {
		d := b.design
		if err := d.Validate(); err != nil {
			dialog.ShowError(err, w)
			return
		}
		go func() {
			out, err := DryRun(b.host, d)
			fyne.Do(func() {
				if err != nil {
					showMono(w, "Dry run — zpool refused", out+"\n"+err.Error())
					return
				}
				showMono(w, "Dry run — what zpool would build", out)
			})
		}()
	})
	// afterCreate is the success half of a create, out of the button closure so
	// a test can run the sequence.
	//
	// ORDER MATTERS: rescan() ends by overwriting b.status with its own
	// "N disks, M free" and rendering that, so the success line has to be set
	// AFTER it or it is destroyed before it is ever drawn. It used to be set
	// before, which is why void came up on fiend with seven disks in it and the
	// UI said nothing at all (2026-09-12). Only the failure path was visible,
	// because that one is a dialog. TestCreateFeedbackSurvivesRescan guards it.
	afterCreate := func(final Design) {
		b.design.Vdevs = nil
		b.chosen = map[string]bool{}
		rescan()
		b.status = "✓ pool " + final.Name + " created"
		render()
		onPoolCreated()
	}
	create := widget.NewButton("Create pool…", func() {
		d := b.design
		if err := d.Validate(); err != nil {
			dialog.ShowError(err, w)
			return
		}
		// HISTORY: this used confirmTyped, the retype-the-target-name gate the
		// destroy verbs use. On a CREATE the target does not exist yet, so the
		// only dialog mentioning a pool name was demanding the DEFAULT name
		// back: type what you actually wanted the pool called and it answered
		// `name mismatch — expected "tank"`, with no way forward (fiend,
		// 2026-09-12, five 8TB disks sat unpooled). Naming and acknowledging
		// are two questions, so they get two fields.
		confirmCreate(w, d, func(final Design) {
			b.design.Name = final.Name
			name.SetText(final.Name)
			go func() {
				err := CreatePool(b.host, final)
				fyne.Do(func() {
					if err != nil {
						showMono(w, "zpool create failed", err.Error())
						return
					}
					afterCreate(final)
					// Read the pool back and show what landed. A dialog,
					// because erasing seven disks deserves more than a status
					// line, and the topology is proof the pool is there rather
					// than a claim that zpool exited 0.
					go func() {
						t, terr := PoolTopology(b.host, final.Name)
						fyne.Do(func() {
							if terr != nil {
								showMono(w, "pool "+final.Name+" created",
									"zpool create reported success, but reading the pool back failed:\n\n"+terr.Error())
								return
							}
							showMono(w, "pool "+final.Name+" created", t.Flatten())
						})
					}()
				})
			}()
		})
	})
	create.Importance = widget.HighImportance
	actions := container.NewHBox(dryRun, create)

	// ── see a pool ───────────────────────────────────────────────────────
	poolTree := container.NewVBox()
	poolSel := widget.NewSelect(nil, nil)
	poolSel.PlaceHolder = "pick a pool"
	drawTopo := func(n *TopoNode) {
		poolTree.Objects = nil
		var walk func(x *TopoNode, depth int)
		walk = func(x *TopoNode, depth int) {
			indent := canvas.NewRectangle(color.Transparent)
			indent.SetMinSize(fyne.NewSize(float32(18*depth), 1))
			label := x.Name
			if x.Kind() != "disk" && x.Kind() != "pool" {
				label = strings.ToUpper(x.Kind()) + "  " + x.Name
			}
			row := []fyne.CanvasObject{indent, chip(label, roleAccentFor(x)), smallText(x.State, stateAccent(x.State), 12, true)}
			if x.Read != "" {
				row = append(row, smallText(fmt.Sprintf("r/w/c %s/%s/%s", x.Read, x.Write, x.Cksum), acTopic, 11, false))
			}
			if x.Note != "" {
				row = append(row, smallText(x.Note, acGold, 11, false))
			}
			poolTree.Add(container.NewHBox(row...))
			for _, c := range x.Children {
				walk(c, depth+1)
			}
		}
		walk(n, 0)
		poolTree.Refresh()
	}
	poolSel.OnChanged = func(p string) {
		if p == "" {
			return
		}
		go func() {
			t, err := PoolTopology(b.host, p)
			fyne.Do(func() {
				if err != nil {
					poolTree.Objects = []fyne.CanvasObject{widget.NewLabel("✗ " + err.Error())}
					poolTree.Refresh()
					return
				}
				drawTopo(t)
			})
		}()
	}
	refreshPools := func() {
		if names, err := ListPools(b.host); err == nil {
			poolSel.Options = names
			poolSel.Refresh()
		}
	}
	seePool := container.NewBorder(container.NewHBox(heading("SEE A POOL — the vdev tree of one that exists", acGreen), poolSel, widget.NewButton("Refresh", func() { refreshPools(); poolSel.OnChanged(poolSel.Selected) })), nil, nil, nil, poolTree)

	// ── render: everything on the right from b ───────────────────────────
	render = func() {
		shelfTitle.SetText(b.status)
		shelf.Refresh()
		if strings.HasPrefix(b.status, "✓") || strings.HasPrefix(b.status, "✗") || strings.HasPrefix(b.status, "tick") {
			statusLbl.SetText(b.status)
		} else {
			statusLbl.SetText("")
		}

		candBox.Objects = nil
		for i := range b.cands {
			c := b.cands[i]
			_, ft := c.Design.FaultTolerance()
			why := widget.NewLabel(c.Why)
			why.Wrapping = fyne.TextWrapWord
			surv := widget.NewLabel("survives " + ft)
			surv.Wrapping = fyne.TextWrapWord
			use := widget.NewButton("Use", func() {
				b.design.Vdevs = append([]Vdev(nil), c.Design.Vdevs...)
				render()
			})
			use.Importance = widget.MediumImportance
			body := container.NewVBox(
				smallText(c.Badge, badgeAccent(c.Badge), 11, true),
				widget.NewLabelWithStyle(c.Label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				widget.NewLabel(fmt.Sprintf("usable ≈ %s  ·  raw %s", fmtBytesDec(c.Design.Usable()), fmtBytesDec(c.Design.Raw()))),
				surv,
				why)
			card := container.NewBorder(nil, use, nil, nil, body)
			candBox.Add(framed(card, badgeAccent(c.Badge), fyne.Size{}))
		}
		if len(b.cands) == 0 {
			candBox.Add(widget.NewLabel("Tick disks on the shelf — the candidate layouts appear here, one box per layout."))
		}
		candBox.Refresh()

		layoutBox.Objects = nil
		for i := range b.design.Vdevs {
			v := b.design.Vdevs[i]
			idx := i
			a := roleAccent(v.Role)
			row := []fyne.CanvasObject{container.NewPadded(chip(strings.ToUpper(v.Role), a)), smallText(v.Label(), a, 12, true), gap(1)}
			for _, d := range v.Disks {
				row = append(row, container.NewPadded(chip(diskGlyph(d.Kind)+" "+d.Name, acTopic)))
			}
			if v.Role == "data" || v.Role == "" {
				row = append(row, gap(1), smallText("≈ "+fmtBytesDec(v.Usable())+" usable", acGreen, 12, false))
			}
			rm := widget.NewButton("✕", func() {
				b.design.Vdevs = append(b.design.Vdevs[:idx], b.design.Vdevs[idx+1:]...)
				render()
			})
			rm.Importance = widget.LowImportance
			row = append(row, rm)
			layoutBox.Add(container.NewHBox(row...))
			layoutBox.Add(gap(4))
		}
		if len(b.design.Vdevs) == 0 {
			layoutBox.Add(widget.NewLabel("No vdevs yet — use a candidate above, or add a custom vdev."))
		}
		if un := b.unassigned(); len(un) > 0 {
			names := make([]string, 0, len(un))
			for _, d := range un {
				names = append(names, d.Name)
			}
			layoutBox.Add(smallText("ticked but not in any vdev: "+strings.Join(names, " "), acGold, 12, false))
		}
		layoutBox.Refresh()

		d := b.design
		_, ft := d.FaultTolerance()
		summary.SetText(fmt.Sprintf("usable ≈ %s   ·   raw %s   ·   survives %s   ·   %d data vdev(s)",
			fmtBytesDec(d.Usable()), fmtBytesDec(d.Raw()), ft, len(d.dataVdevs())))
		warnBox.Objects = nil
		for _, s := range d.Warnings() {
			warnBox.Add(smallText("! "+s, acGold, 12, false))
		}
		warnBox.Refresh()
		if err := d.Validate(); err != nil {
			command.SetText("— " + err.Error())
			dryRun.Disable()
			create.Disable()
		} else {
			command.SetText(d.Command())
			dryRun.Enable()
			create.Enable()
		}
	}

	right := container.NewVBox(
		options,
		gap(18),
		heading("CANDIDATES — what these disks could be", acCyan),
		gap(8),
		candBox,
		gap(12),
		custom,
		gap(22),
		widget.NewSeparator(),
		gap(12),
		heading("LAYOUT", acBlue),
		gap(8),
		layoutBox,
		gap(22),
		widget.NewSeparator(),
		gap(12),
		heading("SUMMARY", acGreen),
		gap(8),
		summary,
		warnBox,
		gap(8),
		command,
		gap(10),
		container.NewHBox(actions, gap(1), statusLbl),
		gap(22),
		widget.NewSeparator(),
		gap(12),
		seePool,
		gap(20),
	)
	rightScroll := container.NewVScroll(container.NewPadded(right))

	page := container.NewHSplit(shelfPane, rightScroll)
	page.SetOffset(0.34)

	rescan()
	refreshPools()
	return &builderUI{page: container.NewBorder(nil, nil, nil, nil, page), shelf: shelf, st: b, toggle: toggle, render: render, command: command, dryRun: dryRun, create: create, afterCreate: afterCreate}
}

// reflow recomputes the candidates for the current ticks and loads the first
// one as the layout, so the screen always shows numbers for THESE disks.
func (b *builderState) reflow() {
	data, aux := b.dataDisks()
	b.cands = Suggest(b.design.Name, data, aux)
	b.design.Vdevs = nil
	if len(b.cands) > 0 {
		b.design.Vdevs = append([]Vdev(nil), b.cands[0].Design.Vdevs...)
	}
}

// roleAccentFor colours an existing pool's node by what it is.
func roleAccentFor(n *TopoNode) accentPair {
	switch n.Kind() {
	case "logs":
		return acPurple
	case "cache":
		return acCyan
	case "spares":
		return acGold
	case "special", "dedup":
		return acElectric
	case "mirror", "raidz1", "raidz2", "raidz3", "draid1", "draid2", "draid3":
		return acBlue
	case "replacing":
		return acRed
	}
	return acTopic
}

// ─── the create gate ────────────────────────────────────────────────────────
// Creating a pool is two decisions an operator makes at once: what it is
// called, and the fact that every member disk is about to be overwritten.
// confirmTyped can only ask the second one, and it asks it by demanding a name
// that does not exist yet, which is how the name prompt came to reject every
// name (see the call site). So the create path gets its own dialog.

// confirmCreate asks for the pool name, shows the exact zpool line and the
// disks it consumes, and gates on a fixed acknowledgement word. onOK runs only
// if the name validates AND the acknowledgement matches; it receives the design
// with the name as finally typed, so the caller never has to re-read a widget.
func confirmCreate(w fyne.Window, d Design, onOK func(Design)) {
	const ack = createAck

	nameEnt := widget.NewEntry()
	nameEnt.SetText(d.Name)
	ackEnt := widget.NewEntry()
	ackEnt.SetPlaceHolder(ack)

	cmd := widget.NewLabel(d.Command())
	cmd.TextStyle = fyne.TextStyle{Monospace: true}
	cmd.Wrapping = fyne.TextWrapWord

	// Every disk in the design, not just the data vdevs: a cache or log member
	// is overwritten exactly as thoroughly as a raidz member.
	var disks []string
	for _, v := range d.Vdevs {
		for _, disk := range v.Disks {
			disks = append(disks, fmt.Sprintf("  %-10s %8s  %s", disk.Name, disk.Size(), disk.Model))
		}
	}
	diskLbl := widget.NewLabel(strings.Join(disks, "\n"))
	diskLbl.TextStyle = fyne.TextStyle{Monospace: true}

	body := container.NewVBox(
		widget.NewLabel("Pool name:"),
		nameEnt,
		widget.NewLabel(fmt.Sprintf("usable ≈ %s of %s raw", fmtBytesDec(d.Usable()), fmtBytesDec(d.Raw()))),
		cmd,
	)
	if ws := d.Warnings(); len(ws) > 0 {
		warn := widget.NewLabel("! " + strings.Join(ws, "\n! "))
		warn.Wrapping = fyne.TextWrapWord
		body.Add(warn)
	}
	body.Add(widget.NewLabel(fmt.Sprintf("This ERASES %d disk(s) and everything on them:", len(disks))))
	body.Add(diskLbl)
	body.Add(widget.NewLabel("Type  " + ack + "  to confirm:"))
	body.Add(ackEnt)

	dlg := dialog.NewCustomConfirm("Create pool", "Create", "Cancel",
		container.NewVScroll(body), func(ok bool) {
			if !ok {
				return
			}
			final, err := createGate(d, nameEnt.Text, ackEnt.Text)
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			onOK(final)
		}, w)
	dlg.Resize(fyne.NewSize(640, 560))
	dlg.Show()
	w.Canvas().Focus(nameEnt)
}

// createAck is the word the create gate wants typed back. It is deliberately
// not the pool name: the gate that asks for a name back belongs to the destroy
// verbs, where the name already exists.
const createAck = "ERASE"

// createGate is the create dialog's decision, with no widgets in it: given the
// design, the name as typed and the acknowledgement as typed, it returns the
// design to build or the error to show. Nothing is created on an error.
func createGate(d Design, typedName, typedAck string) (Design, error) {
	final := d
	final.Name = strings.TrimSpace(typedName)
	// Validate again: the name is editable in the dialog, so this is the last
	// place a bad one can be caught before zpool sees it.
	if err := final.Validate(); err != nil {
		return final, err
	}
	if strings.TrimSpace(typedAck) != createAck {
		n := 0
		for _, v := range d.Vdevs {
			n += len(v.Disks)
		}
		return final, fmt.Errorf("type %s to confirm that %d disk(s) will be erased", createAck, n)
	}
	return final, nil
}

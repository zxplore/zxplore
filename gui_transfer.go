//go:build gui

// gui_transfer.go — the GUI transfer view.
//
// Left pane = the LOCAL filesystem (source). Right pane = the TARGET, connected
// to a remote via "Connect…" ([user@]host:pool/path). Pick a source on the left
// and (optionally) a destination on the right, then "Replicate →" sends the
// source's latest snapshot under the destination — incremental when a common
// snapshot exists. ZFS send/recv needs root and the GUI runs as the user, so the
// transfer is elevated via pkexec (a polkit prompt), keeping the GUI unprivileged.
//
// The target listing is SCOPED to the connected pool/path (ListSubtree), so a
// delegated user (e.g. one holding only `zfs allow receive` on tank/backups)
// still sees the target — a full `zfs list` would be denied.
package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

type xferPane struct {
	side     string // "LEFT" / "RIGHT" — pins the pane in the operator's head
	host     Host
	location string // "" = list all datasets; else list this path + descendants
	datasets []Dataset
	list     *navList
	sel      int // effective selection (-1 = none)
	title    *widget.Label
}

func newXferPane(side string, switchTab func(fyne.KeyName)) *xferPane {
	p := &xferPane{side: side, sel: -1}
	p.title = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	p.list = newNavList(
		func() int { return len(p.datasets) },
		func() fyne.CanvasObject { return widget.NewLabel("t") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			d := p.datasets[i]
			o.(*widget.Label).SetText(fmt.Sprintf("%s   %s/%s  ×%d", d.Name, d.Used, d.Refer, d.Snaps))
		},
	)
	p.list.onFunc = switchTab // F1/F2 switch tabs even when a pane is focused
	// One selection: OnSelected records it; OnHighlighted (↑/↓ and hover)
	// forwards into Select so the blue bar moves with the arrows too.
	p.list.OnSelected = func(i widget.ListItemID) { p.sel = int(i) }
	p.list.OnHighlighted = func(i widget.ListItemID) { p.list.Select(i) }
	return p
}

func (p *xferPane) reload() {
	var ds []Dataset
	var err error
	if p.location == "" {
		ds, err = ListDatasets(p.host)
	} else {
		ds, err = ListSubtree(p.host, p.location)
	}
	p.datasets = ds
	if p.sel >= len(p.datasets) {
		p.sel = -1
	}
	p.list.Refresh()
	// Say WHERE this pane is, unambiguously: LOCAL vs REMOTE user@host.
	where := "● LOCAL — this machine"
	if p.host.SSH != "" {
		where = "● REMOTE — " + p.host.SSH
	}
	label := p.side + "  " + where
	if p.location != "" {
		label += "   " + p.location
	}
	if err != nil {
		label += "    ✗ " + err.Error()
	}
	p.title.SetText(label)
}

func (p *xferPane) selectedName() string {
	if p.sel >= 0 && p.sel < len(p.datasets) {
		return p.datasets[p.sel].Name
	}
	return ""
}

// destination is the chosen dest: the selected dataset, else the connected root.
func (p *xferPane) destination() string {
	if d := p.selectedName(); d != "" {
		return d
	}
	return p.location
}

func (p *xferPane) view(onLocal, onConnect func()) fyne.CanvasObject {
	buttons := container.NewHBox(
		widget.NewButton("Local", onLocal),
		widget.NewButton("Connect…", onConnect),
		widget.NewButton("Refresh", func() { p.reload() }),
	)
	head := container.NewBorder(nil, nil, p.title, buttons)
	return container.NewBorder(head, nil, nil, nil, p.list)
}

func transferTab(w fyne.Window, switchTab func(fyne.KeyName)) fyne.CanvasObject {
	left := newXferPane("LEFT", switchTab) // starts on the local machine
	left.reload()
	right := newXferPane("RIGHT", switchTab)
	right.title.SetText("RIGHT  ○ not connected — click Local or Connect…")

	// Connect a pane to Local or a saved server (via the server manager). Both
	// panes are connectable, so a remote→remote (FXP-style) replication works.
	goLocal := func(p *xferPane) func() {
		return func() {
			p.host = LocalHost()
			p.location = ""
			p.sel = -1
			p.reload()
		}
	}
	connectVia := func(p *xferPane) func() {
		return func() {
			showServerManager(w, func(s Server) {
				p.host = s.toHost()
				p.location = s.Path
				p.sel = -1
				p.reload()
			})
		}
	}

	replicate := func(src, dst *xferPane) {
		s := src.selectedName()
		if s == "" {
			dialog.ShowInformation("Replicate", "Select a SOURCE dataset first.", w)
			return
		}
		d := dst.destination()
		if d == "" {
			dialog.ShowInformation("Replicate", "Choose a destination — connect the target pane (and/or select a dataset in it).", w)
			return
		}
		snap := ""
		if snaps, _ := ListSnapshots(src.host, s); len(snaps) > 0 {
			snap = snaps[len(snaps)-1].Name
		} else if sn, err := SnapshotNow(src.host, s, "zx-"+time.Now().Format("20060102-150405")); err == nil {
			snap = sn
		} else {
			dialog.ShowError(fmt.Errorf("snapshot failed: %v", err), w)
			return
		}
		leaf := s
		if i := strings.LastIndexByte(s, '/'); i >= 0 {
			leaf = s[i+1:]
		}
		dstPath := d + "/" + leaf
		pipeline := ReplicatePipeline(src.host, snap, dst.host, dstPath)
		dialog.ShowConfirm("Replicate",
			fmt.Sprintf("Send\n  %s\nto\n  %s:%s\n\nRuns exactly (root):\n  %s", snap, dst.host.Label(), dstPath, pipeline),
			func(ok bool) {
				if !ok {
					return
				}
				go func() {
					out, err := exec.Command("pkexec", "sh", "-c", pipeline).CombinedOutput()
					fyne.Do(func() {
						if err != nil {
							dialog.ShowError(fmt.Errorf("replicate failed: %v\n%s", err, string(out)), w)
							return
						}
						dst.reload()
						dialog.ShowInformation("Replicate", "✓ replicated → "+dst.host.Label()+":"+dstPath, w)
					})
				}()
			}, w)
	}

	// Tab hops between the two panes.
	left.list.onTab = func() { w.Canvas().Focus(right.list) }
	right.list.onTab = func() { w.Canvas().Focus(left.list) }

	btnLR := widget.NewButton("Replicate left → right", func() { replicate(left, right) })
	btnRL := widget.NewButton("Replicate right → left", func() { replicate(right, left) })
	bar := container.NewCenter(container.NewHBox(btnLR, widget.NewLabel("        "), btnRL))

	split := container.NewHSplit(
		left.view(goLocal(left), connectVia(left)),
		right.view(goLocal(right), connectVia(right)),
	)
	split.SetOffset(0.5)
	return container.NewBorder(nil, bar, nil, nil, split)
}

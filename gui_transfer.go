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
			snaps := ""
			if d.Snaps >= 0 {
				snaps = fmt.Sprintf("  ×%d", d.Snaps)
			}
			o.(*widget.Label).SetText(fmt.Sprintf("%s   %s/%s%s", d.Name, d.Used, d.Refer, snaps))
		},
	)
	p.list.onFunc = switchTab // F1/F2 switch tabs even when a pane is focused
	// One selection: OnSelected records it. Arrows select via keyNavSelect;
	// mouse TRAVEL does not — source/destination are locked choices, and a
	// stray hover must never retarget a replication. Click to select.
	p.list.OnSelected = func(i widget.ListItemID) { p.sel = int(i) }
	p.list.keyNavSelect()
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

	var replicate func(src, dst *xferPane)

	// offerGrant explains a permission failure on a remote end and, with the
	// account's sudo password (used once, on stdin, never stored), delegates
	// the exact zfs allow set that side needs — then retries the replication.
	offerGrant := func(p *xferPane, dataset, perms, role string, retry func()) {
		u := p.host.User()
		if u == "" {
			u = "the connected user"
		}
		grant := GrantCommand(u, perms, dataset)
		pw := widget.NewPasswordEntry()
		pw.SetPlaceHolder("sudo password for " + u + " on " + p.host.Label())
		var gd dialog.Dialog
		runGrant := func() {
			go func() {
				err := GrantReplicationPerms(p.host, u, perms, dataset, pw.Text)
				fyne.Do(func() {
					if err != nil {
						dialog.ShowError(err, w)
						return
					}
					retry()
				})
			}()
		}
		pw.OnSubmitted = func(string) { gd.Hide(); runGrant() }
		body := container.NewVBox(
			widget.NewLabel("ZFS refused: "+u+" isn't delegated to "+role+" on "+p.host.Label()+".\n\nGrant it? Runs exactly (as root, once):\n  sudo "+grant+"\n\nDescendant datasets inherit the grant."),
			pw,
		)
		gd = dialog.NewCustomConfirm("Replication permissions — "+p.host.Label(), "Grant & retry", "Cancel", body,
			func(ok bool) {
				if ok {
					runGrant()
				}
			}, w)
		gd.Resize(fyne.NewSize(620, 260))
		gd.Show()
		w.Canvas().Focus(pw)
	}

	replicate = func(src, dst *xferPane) {
		if !guiMutOK(w) {
			return
		}
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
			if src.host.SSH != "" && needsElevation(err.Error()) {
				offerGrant(src, s, ReplSendPerms, "snapshot/send "+s,
					func() { replicate(src, dst) })
				return
			}
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
			fmt.Sprintf("Send\n  %s\nto\n  %s:%s\n\nRuns exactly (root):\n  %s\n\nOwnership travels inside the stream as numeric UIDs — the replica is\nbit-exact and readonly; no matching account is needed on the target.",
				snap, dst.host.Label(), dstPath, pipeline),
			func(ok bool) {
				if !ok {
					return
				}
				go func() {
					// RunReplicate — not a raw pkexec — so the pipeline is
					// audit-logged like every other mutation.
					err := RunReplicate(pipeline)
					fyne.Do(func() {
						if err != nil {
							// A permission refusal on a remote end is a missing
							// zfs allow — offer the grant and retry.
							if needsElevation(err.Error()) {
								if src.host.SSH != "" && strings.Contains(err.Error(), "send") {
									offerGrant(src, s, ReplSendPerms, "snapshot/send "+s,
										func() { replicate(src, dst) })
									return
								}
								if dst.host.SSH != "" {
									offerGrant(dst, d, ReplRecvPerms, "receive into "+d,
										func() { replicate(src, dst) })
									return
								}
								if src.host.SSH != "" {
									offerGrant(src, s, ReplSendPerms, "snapshot/send "+s,
										func() { replicate(src, dst) })
									return
								}
							}
							dialog.ShowError(err, w)
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

	// Same lifted card panels as the Browser/Explorer panes, so every tab
	// reads as one console.
	split := container.NewHSplit(
		card(left.view(goLocal(left), connectVia(left))),
		card(right.view(goLocal(right), connectVia(right))),
	)
	split.SetOffset(0.5)
	return container.NewBorder(nil, bar, nil, nil, split)
}

//go:build gui

// gui_context.go — the right-click / context menu on a dataset.
//
// One gesture for the whole ZFS lifecycle: snapshot, clone/duplicate, replicate
// or back up to a server, create a boot environment, roll back, edit properties,
// destroy. Every mutation runs privileged (pkexec / delegated ssh); destructive
// ones confirm first. This is what makes zxplore feel like a real console — the
// filesystem as a right-click-managed solution.
package main

import (
	"fmt"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"strings"
	"time"
)

// latestOrNewSnapshot returns the newest snapshot of a dataset, taking one if it
// has none (so clone/replicate always have a source).
func latestOrNewSnapshot(h Host, dataset string) (string, error) {
	if snaps, _ := ListSnapshots(h, dataset); len(snaps) > 0 {
		return snaps[len(snaps)-1].Name, nil
	}
	return SnapshotNow(h, dataset, "zx-"+time.Now().Format("20060102-150405"))
}

func datasetLeaf(dataset string) string {
	if i := strings.LastIndexByte(dataset, '/'); i >= 0 {
		return dataset[i+1:]
	}
	return dataset
}

// datasetContextMenu builds the right-click menu for a dataset. refresh reloads
// the views after any change; onEdit flips the detail pane into edit mode.
func datasetContextMenu(h Host, dataset string, w fyne.Window, refresh, onEdit func()) *fyne.Menu {
	// runOp runs a privileged op off the UI thread, reports the result, refreshes.
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
				refresh()
			})
		}()
	}
	promptName := func(title, label, def string, do func(string)) {
		e := widget.NewEntry()
		e.SetText(def)
		dialog.ShowForm(title, "OK", "Cancel", []*widget.FormItem{widget.NewFormItem(label, e)},
			func(ok bool) {
				if ok && strings.TrimSpace(e.Text) != "" {
					do(strings.TrimSpace(e.Text))
				}
			}, w)
	}

	explore := fyne.NewMenuItem("Snapshot explorer — browse / restore files…", func() {
		openExplorerTab(h, dataset, "")
	})
	diffItem := fyne.NewMenuItem("What changed — zfs diff…", func() {
		showDiffDialog(h, dataset, w, "")
	})
	bookmarks := fyne.NewMenuItem("Bookmarks…", func() {
		go func() {
			bms, err := ListBookmarks(h, dataset)
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, w)
					return
				}
				if len(bms) == 0 {
					dialog.ShowInformation("Bookmarks",
						"No bookmarks on "+dataset+" yet.\n\nBookmark a snapshot (snapshot menu) to keep an\nincremental base that costs no space — then the\nsnapshot itself can be pruned.", w)
					return
				}
				var acts []menuAction
				for _, b := range bms {
					b := b
					short := b.Name
					if i := strings.IndexByte(short, '#'); i >= 0 {
						short = short[i:]
					}
					acts = append(acts, menuAction{short + "   (" + b.Creation + ")  — destroy…", func() {
						dialog.ShowConfirm("Destroy bookmark", "Destroy "+b.Name+" ?\n\n(If it is the last common base with a replica,\nthe next send becomes a FULL send.)", func(ok bool) {
							if ok {
								runOp("destroy bookmark", func() error { return DestroyBookmark(h, b.Name) })
							}
						}, w)
					}})
				}
				showActionMenu("Bookmarks — "+dataset, acts, w)
			})
		}()
	})
	snapNow := fyne.NewMenuItem("Snapshot now…", func() {
		promptName("Snapshot "+dataset, "Name", "snap-"+time.Now().Format("20060102-150405"), func(n string) {
			runOp("snapshot", func() error { _, e := SnapshotNow(h, dataset, n); return e })
		})
	})
	clone := fyne.NewMenuItem("Clone / duplicate…", func() {
		promptName("Clone "+dataset, "New dataset", dataset+"-copy", func(target string) {
			runOp("clone", func() error {
				snap, err := latestOrNewSnapshot(h, dataset)
				if err != nil {
					return err
				}
				return Clone(h, snap, target)
			})
		})
	})
	replicate := fyne.NewMenuItem("Replicate / back up to…", func() {
		showServerManager(w, func(s Server) {
			dstPath := s.Path + "/" + datasetLeaf(dataset)
			dialog.ShowConfirm("Replicate",
				fmt.Sprintf("Send  %s\nto  %s:%s ?", dataset, s.sshTarget(), dstPath), func(ok bool) {
					if !ok {
						return
					}
					// A replication is the longest thing this tool does —
					// 789G at ~1.2GB/s is eleven minutes — and it used to run
					// behind a dialog that appeared only when it FINISHED. zfs
					// prints a progress line every second the whole time; the
					// window simply never showed it (reported 2026-08-26).
					//
					// NewCustomWithoutButtons + widget.ProgressBar rather than
					// dialog.NewProgress: the latter is deprecated in fyne 2.8
					// and its own doc says to build it this way.
					bar := widget.NewProgressBar()
					bar.Min, bar.Max = 0, 1
					status := widget.NewLabel("starting…")
					pipe := ReplicatePipeline(h, "", s.toHost(), dstPath)
					head := container.NewVBox(
						widget.NewLabel(fmt.Sprintf("%s  →  %s:%s", dataset, s.sshTarget(), dstPath)))
					if IsRawSend(pipe) {
						lock := widget.NewLabel("🔒  encrypted end to end — raw send, key never loaded")
						lock.TextStyle = fyne.TextStyle{Bold: true}
						head.Add(lock)
					}
					prog := dialog.NewCustomWithoutButtons("Replicating",
						container.NewVBox(head, bar, status),
						w)
					prog.Show()

					go func() {
						snap, err := latestOrNewSnapshot(h, dataset)
						if err == nil {
							err = RunReplicateProgress(
								ReplicatePipeline(h, snap, s.toHost(), dstPath),
								func(sent, total int64) {
									// Until the size line lands the total is 0.
									// Show bytes moved rather than divide by it.
									fyne.Do(func() {
										if total > 0 {
											bar.SetValue(float64(sent) / float64(total))
											status.SetText(fmt.Sprintf("%s of %s  (%.1f%%)",
												humanBytes(sent), humanBytes(total),
												100*float64(sent)/float64(total)))
										} else {
											status.SetText(humanBytes(sent) + " sent")
										}
									})
								})
						}
						fyne.Do(func() {
							prog.Hide()
							if err != nil {
								dialog.ShowError(err, w)
							} else {
								dialog.ShowInformation("replicate", "replicate ✓", w)
							}
							refresh()
						})
					}()
				}, w)
		})
	})
	be := fyne.NewMenuItem("Create boot environment…", func() {
		promptName("Create boot environment", "BE name", "", func(n string) {
			runOp("boot environment", func() error { return CreateBootEnv(h, n) })
		})
	})
	rollback := fyne.NewMenuItem("Roll back to latest snapshot", func() {
		snaps, _ := ListSnapshots(h, dataset)
		if len(snaps) == 0 {
			dialog.ShowInformation("Rollback", "No snapshots to roll back to.", w)
			return
		}
		snap := snaps[len(snaps)-1].Name
		if !guiMutOK(w) {
			return
		}
		confirmTyped(w, "⚠ Roll back "+dataset,
			"Rolls back to\n  "+snap+"\nand DESTROYS every newer snapshot (and their clones).",
			dataset, func() {
				runOp("rollback", func() error { return Rollback(h, snap) })
			})
	})
	edit := fyne.NewMenuItem("Edit properties", func() {
		if onEdit != nil {
			onEdit()
		}
	})
	destroy := fyne.NewMenuItem("Destroy…", func() {
		if !guiMutOK(w) {
			return
		}
		confirmTyped(w, "✖ Destroy "+dataset,
			"Recursively destroys the dataset, its children,\nand every snapshot of them.",
			dataset, func() {
				runOp("destroy", func() error { return DestroyDataset(h, dataset) })
			})
	})

	newChild := fyne.NewMenuItem("Create child dataset…", func() {
		promptName("New dataset under "+dataset, "Name", "", func(n string) {
			runOp("create dataset", func() error { return CreateDataset(h, dataset+"/"+n, "") })
		})
	})
	newVol := fyne.NewMenuItem("Create volume (zvol)…", func() {
		nameE := widget.NewEntry()
		nameE.SetPlaceHolder("name")
		sizeE := widget.NewEntry()
		sizeE.SetPlaceHolder("size e.g. 10G")
		dialog.ShowForm("New volume under "+dataset, "Create", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("Name", nameE), widget.NewFormItem("Size", sizeE)},
			func(ok bool) {
				if ok && strings.TrimSpace(nameE.Text) != "" && strings.TrimSpace(sizeE.Text) != "" {
					runOp("create volume", func() error {
						return CreateDataset(h, dataset+"/"+strings.TrimSpace(nameE.Text), strings.TrimSpace(sizeE.Text))
					})
				}
			}, w)
	})
	renameItem := fyne.NewMenuItem("Rename…", func() {
		promptName("Rename "+dataset, "New full name", dataset, func(n string) {
			runOp("rename", func() error { return RenameDataset(h, dataset, n) })
		})
	})
	mountItem := fyne.NewMenuItem("Mount", func() {
		runOp("mount", func() error { return SetMounted(h, dataset, true) })
	})
	unmountItem := fyne.NewMenuItem("Unmount", func() {
		runOp("unmount", func() error { return SetMounted(h, dataset, false) })
	})

	unlock := fyne.NewMenuItem("Unlock (load key)…", func() {
		pw := widget.NewPasswordEntry()
		dialog.ShowForm("Unlock "+dataset, "Unlock", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("Passphrase", pw)}, func(ok bool) {
				if ok {
					runOp("unlock", func() error { return LoadKey(h, dataset, pw.Text) })
				}
			}, w)
	})
	lock := fyne.NewMenuItem("Lock (unload key)", func() {
		runOp("lock", func() error { return UnloadKey(h, dataset) })
	})
	changeKey := fyne.NewMenuItem("Change passphrase…", func() {
		pw := widget.NewPasswordEntry()
		dialog.ShowForm("Change passphrase for "+dataset, "Change", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("New passphrase", pw)}, func(ok bool) {
				if ok && pw.Text != "" {
					runOp("change passphrase", func() error { return ChangeKey(h, dataset, pw.Text) })
				}
			}, w)
	})
	newEnc := fyne.NewMenuItem("Create encrypted child…", func() {
		nameE := widget.NewEntry()
		nameE.SetPlaceHolder("name")
		pw := widget.NewPasswordEntry()
		dialog.ShowForm("New encrypted dataset under "+dataset, "Create", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("Name", nameE), widget.NewFormItem("Passphrase", pw)}, func(ok bool) {
				if ok && strings.TrimSpace(nameE.Text) != "" && pw.Text != "" {
					runOp("create encrypted", func() error {
						return CreateEncrypted(h, dataset+"/"+strings.TrimSpace(nameE.Text), pw.Text)
					})
				}
			}, w)
	})
	enc := fyne.NewMenuItem("Encryption", nil)
	enc.ChildMenu = fyne.NewMenu("", unlock, lock, changeKey, newEnc)

	return fyne.NewMenu("",
		newChild, newVol, renameItem, mountItem, unmountItem, enc,
		fyne.NewMenuItemSeparator(),
		snapNow, explore, diffItem, bookmarks, clone, replicate, be,
		fyne.NewMenuItemSeparator(), rollback, edit,
		fyne.NewMenuItemSeparator(), destroy)
}

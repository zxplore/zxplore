//go:build gui

// gui_sync.go — the Auto Sync tab: scheduled replication you can see.
//
// The point of this tab is VISIBILITY. A backup that runs on a timer is
// invisible by construction: it works for months, then silently stops, and
// nobody notices until a restore. So every row answers the three questions
// that matter — is it scheduled, when does it run next, and what is the
// newest thing that actually arrived — rather than just letting you create
// jobs and hope.
//
// Jobs are pulls: this host fetches FROM the source. A compromised source
// therefore holds no credentials here and cannot reach the archive.
package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func syncTab(w fyne.Window) fyne.CanvasObject {
	list := container.NewVBox()
	var refresh func()

	runJob := func(j SyncJob) {
		prog := dialog.NewCustom("Auto Sync — "+j.Name, "Close",
			widget.NewLabel("Running "+j.Name+" …\nThis window can be closed; the job continues."), w)
		prog.Show()
		go func() {
			out, err := runSyncJobNow(j)
			fyne.Do(func() {
				prog.Hide()
				body := widget.NewMultiLineEntry()
				body.SetText(out)
				body.Wrapping = fyne.TextWrapWord
				title := "Auto Sync — " + j.Name
				if err != nil {
					title += "  (FAILED)"
				}
				d := dialog.NewCustom(title, "Close", container.NewVScroll(body), w)
				d.Resize(fyne.NewSize(760, 420))
				d.Show()
				refresh()
			})
		}()
	}

	editJob := func(existing *SyncJob) {
		j := SyncJob{Schedule: "*-*-* 03:00:00", Recursive: true, MaxRuntime: "4h", User: "root"}
		if existing != nil {
			j = *existing
		}
		name := widget.NewEntry()
		name.SetText(j.Name)
		host := widget.NewEntry()
		host.SetText(j.Host)
		host.SetPlaceHolder("10.100.10.146")
		user := widget.NewEntry()
		user.SetText(j.User)
		user.SetPlaceHolder("root")
		key := widget.NewEntry()
		key.SetText(j.KeyPath)
		key.SetPlaceHolder("/root/.ssh/id_ed25519")
		src := widget.NewEntry()
		src.SetText(j.Source)
		src.SetPlaceHolder("rpool")
		dst := widget.NewEntry()
		dst.SetText(j.Target)
		dst.SetPlaceHolder("rpool/backup/<host>")
		sched := widget.NewEntry()
		sched.SetText(j.Schedule)
		excl := widget.NewEntry()
		excl.SetText(j.Exclude)
		excl.SetPlaceHolder("containers/storage")
		cap := widget.NewEntry()
		cap.SetText(j.runtimeCap())
		rec := widget.NewCheck("", func(bool) {})
		rec.SetChecked(j.Recursive)

		// The inventory is the source of truth for connections; this picker
		// copies one in rather than making you retype a host and key path
		// that the server manager already knows and has tested.
		//
		// It COPIES rather than references on purpose. A job that stored only
		// a name would resolve against whichever registry the reader has, and
		// the timer runs as root with a different HOME than the GUI — a job
		// that works when you press Run now and fails silently at 03:00.
		saved := LoadClients()
		pickNames := make([]string, 0, len(saved))
		for _, sv := range saved {
			pickNames = append(pickNames, sv.Name)
		}
		pick := widget.NewSelect(pickNames, func(sel string) {
			for _, sv := range saved {
				if sv.Name != sel {
					continue
				}
				host.SetText(sv.Host)
				user.SetText(sv.User)
				key.SetText(sv.KeyPath)
				if sv.Path != "" && strings.TrimSpace(src.Text) == "" {
					src.SetText(sv.Path)
				}
				if strings.TrimSpace(dst.Text) == "" {
					dst.SetText("rpool/backup/" + sv.Name)
				}
				if strings.TrimSpace(name.Text) == "" {
					name.SetText(sv.Name + "-nightly")
				}
			}
		})
		pick.PlaceHolder = "— pick a saved server —"
		manage := widget.NewButton("Manage clients…", func() {
			showClientManager(w, func(Server) {})
		})

		form := widget.NewForm(
			widget.NewFormItem("From inventory", container.NewBorder(nil, nil, nil, manage, pick)),
			widget.NewFormItem("Job name", name),
			widget.NewFormItem("Source host", host),
			widget.NewFormItem("SSH user", user),
			widget.NewFormItem("SSH key", key),
			widget.NewFormItem("Source dataset", src),
			widget.NewFormItem("Target here", dst),
			widget.NewFormItem("Recursive", rec),
			widget.NewFormItem("Exclude (regex)", excl),
			widget.NewFormItem("Schedule (OnCalendar)", sched),
			widget.NewFormItem("Max runtime", cap),
		)
		d := dialog.NewCustomConfirm("Auto Sync job", "Save", "Cancel", form, func(ok bool) {
			if !ok {
				return
			}
			nj := SyncJob{
				Name: strings.TrimSpace(name.Text), Server: strings.TrimSpace(host.Text),
				Host: strings.TrimSpace(host.Text), User: strings.TrimSpace(user.Text),
				KeyPath: strings.TrimSpace(key.Text), Source: strings.TrimSpace(src.Text),
				Target: strings.TrimSpace(dst.Text), Schedule: strings.TrimSpace(sched.Text),
				Recursive: rec.Checked, Exclude: strings.TrimSpace(excl.Text),
				MaxRuntime: strings.TrimSpace(cap.Text),
			}
			if nj.Name == "" || nj.Host == "" || nj.Source == "" || nj.Target == "" {
				dialog.ShowError(fmt.Errorf("name, host, source and target are all required"), w)
				return
			}
			if err := SaveSyncJobs(UpsertSyncJob(LoadSyncJobs(), nj)); err != nil {
				dialog.ShowError(fmt.Errorf("saving %s: %v\n\nThe job list is system-wide so the "+
					"root-run timer can read it; saving needs root.", syncJobsPath(), err), w)
				return
			}
			refresh()
		}, w)
		d.Resize(fyne.NewSize(560, 460))
		d.Show()
	}

	// jobFor finds the backup job covering a client, matched on HOST rather
	// than on a name: names are labels an operator retypes, the address is
	// what actually identifies the machine.
	jobFor := func(sv Server, jobs []SyncJob) *SyncJob {
		for i := range jobs {
			if jobs[i].hostAddr() == sv.Host {
				return &jobs[i]
			}
		}
		return nil
	}

	// jobCard renders one configured backup: is it scheduled, when next, and
	// what actually arrived — the three questions a timer cannot answer by
	// existing.
	jobCard := func(j SyncJob) fyne.CanvasObject {
		st := SyncStatus(j)
		state := "NOT SCHEDULED"
		if st.Enabled {
			state = "enabled"
		}
		lines := []string{
			st.Summary(),
			fmt.Sprintf("%s · %s → %s · schedule %s", state, j.Source, j.Target, j.Schedule),
		}
		if st.NextRun != "" && st.NextRun != "0" {
			lines = append(lines, "next run:    "+st.NextRun)
		}
		if st.NewestHere != "" {
			lines = append(lines, "newest here: "+st.NewestHere)
		} else {
			lines = append(lines, "newest here: nothing has arrived yet")
		}
		detail := widget.NewLabelWithStyle(strings.Join(lines, "\n"),
			fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})

		jj := j
		btnRun := widget.NewButton("Run now", func() { runJob(jj) })
		btnEdit := widget.NewButton("Edit…", func() { e := jj; editJob(&e) })
		btnCmd := widget.NewButton("Show command", func() {
			body := widget.NewMultiLineEntry()
			txt := strings.Join(SyncCommand(jj, jj.server()), " ")
			if odd := MixedEncryption(jj.server().toHost(), jj.Source); len(odd) > 0 {
				txt += fmt.Sprintf("\n\nWARNING: %d dataset(s) under %s differ in encryption from its "+
					"root. One syncoid run has ONE send mode, so these cannot be carried by this job "+
					"and need their own:\n  %s", len(odd), jj.Source, strings.Join(odd, "\n  "))
			}
			body.SetText(txt)
			body.Wrapping = fyne.TextWrapWord
			d := dialog.NewCustom("Command — "+jj.Name, "Close", container.NewVScroll(body), w)
			d.Resize(fyne.NewSize(760, 300))
			d.Show()
		})
		sched := widget.NewButton("Install timer", func() {
			if st.Enabled {
				if err := RemoveSyncJob(jj); err != nil {
					dialog.ShowError(err, w)
					return
				}
			} else if err := InstallSyncJob(jj, jj.server()); err != nil {
				dialog.ShowError(err, w)
				return
			}
			refresh()
		})
		if st.Enabled {
			sched.SetText("Remove timer")
		}
		btnDel := widget.NewButton("Delete job", func() {
			dialog.ShowConfirm("Delete job", "Remove \""+jj.Name+"\" and its timer?", func(ok bool) {
				if !ok {
					return
				}
				_ = RemoveSyncJob(jj)
				if err := SaveSyncJobs(DeleteSyncJob(LoadSyncJobs(), jj.Name)); err != nil {
					dialog.ShowError(err, w)
					return
				}
				refresh()
			}, w)
		})
		return container.NewVBox(detail, container.NewHBox(btnRun, sched, btnCmd, btnEdit, btnDel))
	}

	refresh = func() {
		list.RemoveAll()
		jobs := LoadSyncJobs()
		clients := LoadClients()

		if len(clients) == 0 && len(jobs) == 0 {
			list.Add(widget.NewLabelWithStyle(
				"No clients yet.\n\nA client is a machine this host backs up. Add one under "+
					"Clients… — generate or paste a key, authorize it with a one-time password, "+
					"test the connection — then give it a backup here.",
				fyne.TextAlignLeading, fyne.TextStyle{Italic: true}))
			list.Refresh()
			return
		}

		// One row per CLIENT. The estate view: every machine in the inventory,
		// and whether it is actually protected.
		covered := map[string]bool{}
		for _, sv := range clients {
			svv := sv
			head := widget.NewLabelWithStyle(
				fmt.Sprintf("%s      %s", sv.Name, sv.sshTarget()),
				fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			var bodyObj fyne.CanvasObject
			if j := jobFor(sv, jobs); j != nil {
				covered[j.Name] = true
				bodyObj = jobCard(*j)
			} else {
				bodyObj = container.NewVBox(
					widget.NewLabelWithStyle("NOT BACKED UP — no job covers this client",
						fyne.TextAlignLeading, fyne.TextStyle{Monospace: true}),
					container.NewHBox(widget.NewButton("Back up this host…", func() {
						j := SyncJob{
							Name: svv.Name + "-nightly", Server: svv.Name, Host: svv.Host,
							User: svv.User, Port: svv.Port, KeyPath: svv.KeyPath,
							Source: "rpool", Target: "rpool/backup/" + svv.Name,
							Schedule: "*-*-* 03:00:00", Recursive: true, MaxRuntime: "4h",
						}
						editJob(&j)
					})))
			}
			list.Add(card(container.NewVBox(head, bodyObj)))
		}

		// Jobs whose client is not in the inventory still have to be visible,
		// or deleting a server would hide a running timer.
		for i := range jobs {
			if covered[jobs[i].Name] {
				continue
			}
			j := jobs[i]
			head := widget.NewLabelWithStyle(
				fmt.Sprintf("%s      %s   (not in the server inventory)", j.Name, j.label()),
				fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			list.Add(card(container.NewVBox(head, jobCard(j))))
		}
		list.Refresh()
	}
	refresh()

	top := container.NewBorder(nil, nil,
		heading("AUTO SYNC — scheduled replication", acCyan),
		container.NewHBox(
			widget.NewButton("Clients…", func() { showClientManager(w, func(Server) {}) }),
			widget.NewButton("Add job…", func() { editJob(nil) }),
			widget.NewButton("Refresh", func() { refresh() }),
		))
	foot := widget.NewLabelWithStyle(
		"Jobs PULL: this host fetches from the source, so a compromised source holds no credentials here. "+
			"Timers are systemd units; on a host without systemd zxplore prints the crontab line instead.",
		fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	return container.NewBorder(top, foot, nil, nil, container.NewVScroll(list))
}

// hostAddr is the address this job actually connects to, after any legacy
// name resolution. Matching a job to a client uses it rather than the raw
// field, or a job written before jobs carried their own connection shows as
// orphaned beside a client that reads NOT BACKED UP — both wrong, same host.
func (j SyncJob) hostAddr() string { return j.server().Host }

// runSyncJobNow is the GUI's entry point. Same code as the timer runs — there
// is exactly one implementation, so "Run now" and 03:00 cannot behave
// differently.
func runSyncJobNow(j SyncJob) (string, error) {
	out, rc := runSyncJob(j)
	if rc != 0 {
		return out, fmt.Errorf("job %s failed (exit %d)", j.Name, rc)
	}
	return out, nil
}

// tui_shares.go — the shares view (F5): what this host actually serves, and
// which dataset each export comes out of.
//
// The question this answers that nothing else here could: "is tank/media
// reachable from outside, and by whom". `zfs list` knows the dataset,
// `exportfs` knows the export, and until now nothing joined them -- so the
// operator did the mountpoint matching in their head across three tools.
//
// Two things sit above the list on purpose.
//
// The DAEMON STATE, because a correct export on a dead server is the failure
// worth catching: every config file reads right and nothing is served. That
// line is first so it is seen before the shares it invalidates.
//
// The SOURCES THAT COULD NOT BE READ, because "no SMB shares" and "Samba is
// not installed" are different facts and a blank list says both. A storage box
// commonly runs one or two of NFS/SMB/iSCSI, never all three, and silence
// about the others would make this view lie by omission.
//
// Read-only. Creating and removing exports changes what the machine lets
// strangers reach, and that belongs behind :rw and a typed confirmation, the
// way destructive dataset verbs already are -- not in the first cut.
package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type sharesView struct {
	host    Host
	shares  []Share
	svcs    []ShareService
	missing []string
	cursor  int
	status  string
}

func newSharesView(h Host) *sharesView {
	s := &sharesView{host: h}
	s.reload()
	return s
}

func (s *sharesView) reload() {
	shares, missing, err := ListShares(s.host)
	s.shares, s.missing = shares, missing
	s.svcs = ShareServices(s.host)
	if s.cursor >= len(shares) {
		s.cursor = len(shares) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
	// An error here means the dataset join failed, not that the shares are
	// wrong -- they are still listed, just without a dataset column. Say which
	// half is missing rather than reporting the whole view as broken.
	if err != nil {
		s.status = "✗ " + err.Error()
		return
	}
	nfs, smb, iscsi := 0, 0, 0
	for _, sh := range shares {
		switch sh.Kind {
		case "nfs":
			nfs++
		case "smb":
			smb++
		case "iscsi":
			iscsi++
		}
	}
	s.status = fmt.Sprintf("%d shares — %d nfs, %d smb, %d iscsi", len(shares), nfs, smb, iscsi)
}

func (s *sharesView) current() (Share, bool) {
	if s.cursor >= 0 && s.cursor < len(s.shares) {
		return s.shares[s.cursor], true
	}
	return Share{}, false
}

// serviceLine renders the daemon state across all three kinds in one row.
// A kind whose daemon is dead while shares of that kind exist is the case
// worth shouting about, so it is marked rather than merely listed.
func (s *sharesView) serviceLine() string {
	served := map[string]bool{}
	for _, sh := range s.shares {
		served[sh.Kind] = true
	}
	var parts []string
	for _, svc := range s.svcs {
		switch {
		case svc.Active:
			parts = append(parts, okStyle.Render(svc.Kind+" "+svc.State))
		case svc.State == "not-installed":
			parts = append(parts, dimStyle.Render(svc.Kind+" absent"))
		case served[svc.Kind]:
			// Configured shares with nothing serving them.
			parts = append(parts, alertStyle.Render(svc.Kind+" "+svc.State+" — SHARES NOT SERVED"))
		default:
			parts = append(parts, dimStyle.Render(svc.Kind+" "+svc.State))
		}
	}
	return strings.Join(parts, "   ")
}

func (s *sharesView) view(width, height int) string {
	bodyH := height - 3
	if bodyH < 4 {
		bodyH = 4
	}
	var b strings.Builder
	b.WriteString(hostStyle.Render("SHARES — "+s.host.Label()) + "\n")
	b.WriteString("  " + s.serviceLine() + "\n\n")

	if len(s.shares) == 0 {
		b.WriteString(dimStyle.Render("  (nothing is being served from this host)\n"))
	} else {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %-6s %-22s %-24s %s\n",
			"KIND", "NAME", "DATASET", "CLIENTS")))
	}
	for i, sh := range s.shares {
		ds := sh.Dataset
		if ds == "" {
			// Worth naming explicitly on a ZFS box: this export is coming off
			// something that is not a dataset at all.
			ds = "— not zfs —"
		}
		line := fmt.Sprintf("  %-6s %-22s %-24s %s",
			sh.Kind, truncate(sh.Name, 22), truncate(ds, 24), truncate(sh.Clients, 28))
		if i == s.cursor {
			line = cursorStyle.Render(padRight(line, width-6))
		}
		b.WriteString(line + "\n")
	}

	if len(s.missing) > 0 {
		b.WriteString("\n" + dimStyle.Render("  not read: "+strings.Join(s.missing, ", ")))
	}

	body := paneFocus.Width(width - 2).Height(bodyH).Render(b.String())
	status := footerStyle.Render(" " + truncate(s.status, width-2))
	return lipgloss.JoinVertical(lipgloss.Left, body, status)
}

// detail is the full record for one share, for the pager.
func (s *sharesView) detail(sh Share) string {
	var b strings.Builder
	fmt.Fprintf(&b, "kind      %s\n", sh.Kind)
	fmt.Fprintf(&b, "name      %s\n", sh.Name)
	fmt.Fprintf(&b, "path      %s\n", sh.Path)
	if sh.Dataset != "" {
		fmt.Fprintf(&b, "dataset   %s\n", sh.Dataset)
	} else {
		fmt.Fprintf(&b, "dataset   (none — this path is not on a ZFS filesystem)\n")
	}
	fmt.Fprintf(&b, "clients   %s\n", sh.Clients)
	if sh.Options != "" {
		fmt.Fprintf(&b, "options   %s\n", sh.Options)
	}
	for _, svc := range s.svcs {
		if svc.Kind == sh.Kind {
			unit := svc.Unit
			if unit == "" {
				unit = "(no unit found)"
			}
			fmt.Fprintf(&b, "\nserved by %s — %s\n", unit, svc.State)
			if !svc.Active {
				b.WriteString("\nThis share is configured but its daemon is not active,\n" +
					"so nothing is actually reachable through it.\n")
			}
		}
	}
	return b.String()
}

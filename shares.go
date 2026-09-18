// shares.go — what this machine is actually SERVING, and which dataset each
// export comes out of.
//
// The half a ZFS console has always been missing here. zxplore could tell you
// everything about a pool and nothing about whether anyone outside the machine
// could reach it: `zfs list` knows the dataset, `exportfs` knows the export,
// and nothing joined the two. An operator asking "is tank/media actually
// shared, and to whom" had to read three tools and do the mountpoint matching
// in their head.
//
// Sources, each optional and each reported when missing rather than silently
// skipped (a storage host runs some of these, rarely all):
//   - `exportfs -v`            NFS exports and their per-client options
//   - `testparm -s`            the effective Samba config, including defaults
//     and registry shares, which reading smb.conf by
//     hand does not give you
//   - `targetcli ls /backstores/block`   iSCSI block backstores and their
//     backing device, which for us is usually a zvol
//
// THE JOIN IS THE POINT. Every share is matched back to the dataset behind it
// by longest mountpoint prefix, so `/tank/media/movies` resolves to the
// dataset `tank/media` when that is where the filesystem actually starts --
// not to `tank`, and not to a dataset whose NAME merely looks similar. An
// iSCSI backstore pointing at /dev/zvol/tank/lun0 resolves by zvol path
// instead. A share with no ZFS behind it says so, because that is worth
// knowing on a ZFS box.
//
// Read-only by construction. Creating and removing exports touches the
// security boundary of the machine, and that belongs behind the same
// read-write gate and confirmation the restore path already has, not in a
// first cut.
package main

import (
	"fmt"
	"sort"
	"strings"
)

// Share is one thing this host serves, and the dataset it comes from.
type Share struct {
	Kind    string // "nfs" | "smb" | "iscsi"
	Name    string // export path, SMB share name, or backstore name
	Path    string // filesystem path or backing device
	Dataset string // ZFS dataset behind it; "" when there is none
	Clients string // NFS: allowed clients. SMB: valid users/guest. iSCSI: "".
	Options string // whatever the source says about how it is served
}

// ShareService is the daemon behind a kind of share: is it even running?
// A configured export on a dead daemon is the failure this catches -- it looks
// correct in every config file and serves nothing.
type ShareService struct {
	Kind   string
	Unit   string
	State  string // active | inactive | failed | not-installed | unknown
	Active bool
}

// parseExportfs turns `exportfs -v` into shares.
//
// The format wraps: a long export path puts the client on the NEXT line,
// indented. Treating every line as self-contained loses exactly the exports
// with the longest paths, which on a ZFS box are the nested datasets most
// likely to be shared.
//
//	/tank/media   	10.0.0.0/24(rw,sync,no_subtree_check)
//	/tank/a/very/long/path
//			*(ro,sync)
func parseExportfs(out string) []Share {
	var shares []Share
	var pending string // an export path still waiting for its client line

	flush := func(path, spec string) {
		client, opts := spec, ""
		if i := strings.Index(spec, "("); i >= 0 {
			client = strings.TrimSpace(spec[:i])
			opts = strings.Trim(spec[i:], "()")
		}
		if client == "" {
			client = "<world>"
		}
		shares = append(shares, Share{
			Kind: "nfs", Name: path, Path: path,
			Clients: client, Options: opts,
		})
	}

	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if indented {
			// Continuation: the client for the path we saw last.
			if pending != "" {
				flush(pending, fields[0])
				pending = ""
			}
			continue
		}
		if len(fields) == 1 {
			// A path whose client is on the next line.
			pending = fields[0]
			continue
		}
		pending = ""
		flush(fields[0], fields[1])
	}
	return shares
}

// parseTestparm turns `testparm -s` output into SMB shares.
//
// testparm rather than smb.conf because it prints the EFFECTIVE config:
// includes resolved, registry shares merged, and defaults filled in. A share
// defined in the registry is invisible in smb.conf and very much visible to
// clients.
//
// [global] and the built-in [printers]/[print$] are skipped: they are not
// storage and listing them buries the shares that are.
func parseTestparm(out string) []Share {
	var shares []Share
	section, path, valid, ro := "", "", "", ""

	emit := func() {
		if section == "" || section == "global" ||
			section == "printers" || section == "print$" {
			return
		}
		if path == "" {
			return // a section with no path serves nothing
		}
		clients := valid
		if clients == "" {
			clients = "<any authenticated>"
		}
		opts := ""
		if ro == "yes" {
			opts = "read only"
		}
		shares = append(shares, Share{
			Kind: "smb", Name: section, Path: path,
			Clients: clients, Options: opts,
		})
	}

	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			emit()
			section = strings.Trim(t, "[]")
			path, valid, ro = "", "", ""
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "path":
			path = strings.TrimSpace(v)
		case "valid users":
			valid = strings.TrimSpace(v)
		case "read only":
			ro = strings.ToLower(strings.TrimSpace(v))
		}
	}
	emit()
	return shares
}

// parseTargetcliBlock turns `targetcli ls /backstores/block` into shares.
//
//	o- block ........................ [Storage Objects: 2]
//	  o- lun0 ....... [/dev/zvol/tank/lun0 (8.0GiB) write-thru activated]
//	  o- lun1 ....... [/dev/sdb (1.0TiB) write-thru deactivated]
//
// The name and the backing device are what matter; the dots are padding.
func parseTargetcliBlock(out string) []Share {
	var shares []Share
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "o- ") {
			continue
		}
		t = strings.TrimPrefix(t, "o- ")
		name, rest, ok := strings.Cut(t, " ")
		if !ok || name == "block" {
			continue
		}
		i := strings.Index(rest, "[")
		if i < 0 {
			continue
		}
		inner := strings.Trim(rest[i:], "[]")
		dev := inner
		if f := strings.Fields(inner); len(f) > 0 {
			dev = f[0]
		}
		opts := ""
		if strings.Contains(inner, "deactivated") {
			opts = "deactivated"
		}
		shares = append(shares, Share{
			Kind: "iscsi", Name: name, Path: dev, Options: opts,
		})
	}
	return shares
}

// attachDatasets resolves each share back to the dataset behind it.
//
// LONGEST MOUNTPOINT WINS. With tank mounted at /tank and tank/media at
// /tank/media, an export of /tank/media/movies belongs to tank/media -- the
// deepest filesystem that actually contains the path. Sorting by mountpoint
// length and taking the first match is what makes that true; matching in
// `zfs list` order would attribute it to whichever came first.
//
// Volumes are matched by their zvol device path instead, because an iSCSI
// backstore points at /dev/zvol/<name> and has no mountpoint at all.
func attachDatasets(shares []Share, ds []Dataset) []Share {
	type mp struct{ path, name string }
	var mounts []mp
	zvol := map[string]string{}

	for _, d := range ds {
		if d.Type == "volume" {
			zvol["/dev/zvol/"+d.Name] = d.Name
			continue
		}
		if d.Mountpoint == "" || d.Mountpoint == "-" || d.Mountpoint == "none" {
			continue
		}
		mounts = append(mounts, mp{d.Mountpoint, d.Name})
	}
	sort.Slice(mounts, func(i, j int) bool {
		return len(mounts[i].path) > len(mounts[j].path)
	})

	for i := range shares {
		p := shares[i].Path
		if name, ok := zvol[p]; ok {
			shares[i].Dataset = name
			continue
		}
		for _, m := range mounts {
			if p == m.path || strings.HasPrefix(p, strings.TrimSuffix(m.path, "/")+"/") {
				shares[i].Dataset = m.name
				break
			}
		}
	}
	return shares
}

// ListShares reads every source this host has and joins the results to the
// datasets behind them.
//
// A source whose tool is absent is SKIPPED, not fatal: a machine serving NFS
// and nothing else is a perfectly normal storage host, and failing the whole
// listing because targetcli is not installed would make the feature useless
// exactly where it is most wanted. What the caller gets back alongside the
// shares is the list of sources that could not be read, so "no SMB shares" and
// "Samba is not installed" never look the same.
func ListShares(h Host) ([]Share, []string, error) {
	var shares []Share
	var missing []string

	if out, err := run(h.command("exportfs", "-v")); err == nil {
		shares = append(shares, parseExportfs(out)...)
	} else {
		missing = append(missing, "nfs (exportfs not available)")
	}

	if out, err := run(h.command("testparm", "-s")); err == nil {
		shares = append(shares, parseTestparm(out)...)
	} else {
		missing = append(missing, "smb (testparm not available)")
	}

	if out, err := run(h.command("targetcli", "ls", "/backstores/block")); err == nil {
		shares = append(shares, parseTargetcliBlock(out)...)
	} else {
		missing = append(missing, "iscsi (targetcli not available)")
	}

	ds, err := ListDatasets(h)
	if err != nil {
		// The shares are still worth returning; they just will not carry a
		// dataset. Say which half failed rather than dropping everything.
		return shares, missing, fmt.Errorf("shares listed, datasets not: %w", err)
	}
	return attachDatasets(shares, ds), missing, nil
}

// shareUnits maps a share kind to the units that might serve it. Both spellings
// appear: nfs-server on RPM, nfs-kernel-server on Debian, and the same split
// runs through the rest -- which is exactly the class of mistake that shipped a
// storage profile with no NFS at all.
var shareUnits = map[string][]string{
	"nfs":   {"nfs-server", "nfs-kernel-server"},
	"smb":   {"smbd", "smb"},
	"iscsi": {"target", "tgt", "rtslib-fb-targetctl"},
}

// ShareServices reports whether the daemon behind each kind is actually
// running. A correct export on a dead daemon serves nothing and looks fine in
// every config file, which is the failure worth surfacing next to the shares.
func ShareServices(h Host) []ShareService {
	kinds := make([]string, 0, len(shareUnits))
	for k := range shareUnits {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	out := make([]ShareService, 0, len(kinds))
	for _, kind := range kinds {
		svc := ShareService{Kind: kind, State: "not-installed"}
		for _, unit := range shareUnits[kind] {
			state, err := run(h.command("systemctl", "is-active", unit))
			state = strings.TrimSpace(state)
			// is-active exits non-zero for anything but "active", so the error
			// is not the signal -- the word it printed is. An absent unit says
			// "inactive" or "unknown" with no output on some versions.
			if state == "" && err != nil {
				continue
			}
			if state == "unknown" {
				continue
			}
			svc.Unit, svc.State, svc.Active = unit, state, state == "active"
			break
		}
		out = append(out, svc)
	}
	return out
}

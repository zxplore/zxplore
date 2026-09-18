package main

import "testing"

// The wrapped-line case is the one worth having a test for: exportfs puts the
// client on the next line when the path is long, and a parser that reads each
// line independently loses exactly the deeply-nested exports a ZFS box has.
func TestParseExportfs(t *testing.T) {
	const out = `/tank/media    	10.0.0.0/24(rw,sync,no_subtree_check)
/tank/a/very/long/export/path
		*(ro,sync,no_subtree_check)
/srv/plain	192.168.1.5(rw)
`
	got := parseExportfs(out)
	if len(got) != 3 {
		t.Fatalf("want 3 exports, got %d: %+v", len(got), got)
	}
	for _, tc := range []struct {
		i                     int
		path, client, options string
	}{
		{0, "/tank/media", "10.0.0.0/24", "rw,sync,no_subtree_check"},
		{1, "/tank/a/very/long/export/path", "*", "ro,sync,no_subtree_check"},
		{2, "/srv/plain", "192.168.1.5", "rw"},
	} {
		g := got[tc.i]
		if g.Kind != "nfs" {
			t.Errorf("[%d] kind = %q, want nfs", tc.i, g.Kind)
		}
		if g.Path != tc.path {
			t.Errorf("[%d] path = %q, want %q", tc.i, g.Path, tc.path)
		}
		if g.Clients != tc.client {
			t.Errorf("[%d] client = %q, want %q", tc.i, g.Clients, tc.client)
		}
		if g.Options != tc.options {
			t.Errorf("[%d] options = %q, want %q", tc.i, g.Options, tc.options)
		}
	}
}

func TestParseTestparm(t *testing.T) {
	const out = `[global]
	workgroup = WORKGROUP
	server role = standalone server

[media]
	path = /tank/media
	read only = yes
	valid users = anthony

[printers]
	path = /var/spool/samba

[scratch]
	path = /tank/scratch
`
	got := parseTestparm(out)
	if len(got) != 2 {
		t.Fatalf("want 2 shares (global and printers skipped), got %d: %+v", len(got), got)
	}
	if got[0].Name != "media" || got[0].Path != "/tank/media" {
		t.Errorf("first share = %+v", got[0])
	}
	if got[0].Options != "read only" {
		t.Errorf("read only not carried: %+v", got[0])
	}
	if got[0].Clients != "anthony" {
		t.Errorf("valid users = %q, want anthony", got[0].Clients)
	}
	// A share with no "valid users" is open to any authenticated user, and
	// saying so is more useful than an empty column.
	if got[1].Clients != "<any authenticated>" {
		t.Errorf("scratch clients = %q", got[1].Clients)
	}
}

func TestParseTargetcliBlock(t *testing.T) {
	const out = `o- block ............................... [Storage Objects: 2]
  o- lun0 ......... [/dev/zvol/tank/lun0 (8.0GiB) write-thru activated]
  o- lun1 ................ [/dev/sdb (1.0TiB) write-thru deactivated]
`
	got := parseTargetcliBlock(out)
	if len(got) != 2 {
		t.Fatalf("want 2 backstores, got %d: %+v", len(got), got)
	}
	if got[0].Name != "lun0" || got[0].Path != "/dev/zvol/tank/lun0" {
		t.Errorf("lun0 = %+v", got[0])
	}
	if got[0].Options != "" {
		t.Errorf("lun0 is activated, want no option note, got %q", got[0].Options)
	}
	if got[1].Options != "deactivated" {
		t.Errorf("lun1 should be flagged deactivated, got %q", got[1].Options)
	}
}

// The join is the whole point of the feature, and the nesting case is where a
// naive implementation gets it wrong: /tank/media/movies must resolve to
// tank/media, not to tank, even though tank's mountpoint also prefixes it.
func TestAttachDatasetsLongestMountpointWins(t *testing.T) {
	ds := []Dataset{
		{Name: "tank", Type: "filesystem", Mountpoint: "/tank"},
		{Name: "tank/media", Type: "filesystem", Mountpoint: "/tank/media"},
		{Name: "tank/lun0", Type: "volume", Mountpoint: "-"},
		{Name: "rpool/none", Type: "filesystem", Mountpoint: "none"},
	}
	shares := []Share{
		{Kind: "nfs", Path: "/tank/media/movies"},
		{Kind: "nfs", Path: "/tank/other"},
		{Kind: "iscsi", Path: "/dev/zvol/tank/lun0"},
		{Kind: "smb", Path: "/srv/not-zfs"},
	}
	got := attachDatasets(shares, ds)

	if got[0].Dataset != "tank/media" {
		t.Errorf("nested path resolved to %q, want tank/media", got[0].Dataset)
	}
	if got[1].Dataset != "tank" {
		t.Errorf("/tank/other resolved to %q, want tank", got[1].Dataset)
	}
	if got[2].Dataset != "tank/lun0" {
		t.Errorf("zvol backstore resolved to %q, want tank/lun0", got[2].Dataset)
	}
	// A share with nothing ZFS behind it must stay empty rather than being
	// attributed to whichever dataset happened to sort first.
	if got[3].Dataset != "" {
		t.Errorf("non-ZFS path claimed dataset %q", got[3].Dataset)
	}
}

// A mountpoint must not match a sibling that merely shares a prefix:
// /tank/media must not swallow /tank/media-archive.
func TestAttachDatasetsNoPrefixBleed(t *testing.T) {
	ds := []Dataset{
		{Name: "tank", Type: "filesystem", Mountpoint: "/tank"},
		{Name: "tank/media", Type: "filesystem", Mountpoint: "/tank/media"},
	}
	got := attachDatasets([]Share{{Kind: "nfs", Path: "/tank/media-archive"}}, ds)
	if got[0].Dataset != "tank" {
		t.Errorf("/tank/media-archive resolved to %q, want tank", got[0].Dataset)
	}
}

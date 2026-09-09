// zfs_rawsend_test.go — guard for the raw-send decision.
//
// HISTORY: 2026-09-08. Two bugs in one evening, both silent:
//  1. The original code probed `encryption` and skipped -w when the probe
//     returned its ERROR value, so a failed probe sent PLAINTEXT to the
//     backup target — the one thing raw sending exists to prevent.
//  2. Over-correcting to "always raw" broke the real onyx<-fiend topology:
//     -w on an unencrypted source is -Lec, and its embedded-data feature is
//     refused by an encrypted receive. It did not fail cleanly either — the
//     receive died while the sender's pv waited on stdin, so it hung.
//
// The measured matrix (real pools, 2026-09-08); only one cell is illegal:
//
//	src plain -> dst plain : raw OK   plain OK
//	src plain -> dst ENC   : raw FAIL plain OK
//	src ENC   -> dst plain : raw OK   plain OK
//	src ENC   -> dst ENC   : raw OK   plain OK
package main

import (
	"strings"
	"testing"
)

func TestRawSendDecision(t *testing.T) {
	// A zfs that answers only encryption probes; everything else is empty,
	// which is also how a probe FAILURE looks to the caller.
	const enc = `echo "zfs $*" >> "$ZX_CMDLOG"
case "$*" in
"get -H -o value encryption src/enc")   echo aes-256-gcm ;;
"get -H -o value encryption src/plain") echo off ;;
"get -H -o value encryption dst/enc")   echo aes-256-gcm ;;
"get -H -o value encryption dst/plain") echo off ;;
*) exit 0 ;;
esac`

	cases := []struct {
		name, src, dst string
		wantRaw        bool
	}{
		{"plain into plain stays raw (legal, and compresses)", "src/plain@s", "dst/plain/new", true},
		{"plain into ENCRYPTED must drop raw (the illegal cell)", "src/plain@s", "dst/enc/new", false},
		{"encrypted source is always raw, else the key must be loaded", "src/enc@s", "dst/plain/new", true},
		{"encrypted into encrypted is raw", "src/enc@s", "dst/enc/new", true},
		{"UNKNOWN source fails CLOSED to raw, never plaintext", "src/gone@s", "dst/enc/new", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newMock(t)
			m.script("zfs", enc)
			m.script("zpool", zpoolFixture)
			got := strings.Contains(ReplicatePipeline(Host{}, c.src, Host{}, c.dst), " -w ")
			if got != c.wantRaw {
				t.Fatalf("raw=%v, want %v — %s -> %s", got, c.wantRaw, c.src, c.dst)
			}
		})
	}
}

// The target usually does not exist on a first replication, so the decision
// must read the nearest EXISTING ancestor, which is what a new child inherits.
func TestInheritedEncryptionWalksUp(t *testing.T) {
	const enc = `echo "zfs $*" >> "$ZX_CMDLOG"
case "$*" in
"get -H -o value encryption pool/backup") echo aes-256-gcm ;;
*) exit 0 ;;
esac`
	m := newMock(t)
	m.script("zfs", enc)
	if got := inheritedEncryption(Host{}, "pool/backup/host/deep/child"); got != "aes-256-gcm" {
		t.Fatalf("inheritedEncryption = %q, want aes-256-gcm from the ancestor", got)
	}
	if got := inheritedEncryption(Host{}, "other/tree"); got != "" {
		t.Fatalf("unreadable path should report %q, got %q", "", got)
	}
}

// RESTORE inverts the raw rule when the archive's key is loaded here.
//
// HISTORY: 2026-09-09. fiend's root is `encryption off`; its copy on onyx
// inherited onyx's aes-256-gcm. Restoring it RAW preserved that wrapping key,
// so the restored dataset became its own encryption root with keystatus
// unavailable — a rebuilt machine unable to mount its own filesystem without
// the backup server's passphrase. Sending decrypted instead let the target
// apply its own policy, and the copy came back mountable with the kernel
// intact.
func TestRestoreOfEncryptedArchivePrefersDecrypted(t *testing.T) {
	const enc = `echo "zfs $*" >> "$ZX_CMDLOG"
case "$*" in
"get -H -o value encryption archive/root")  echo aes-256-gcm ;;
"get -H -o value keystatus archive/root")   echo available ;;
"get -H -o value encryption locked/root")   echo aes-256-gcm ;;
"get -H -o value keystatus locked/root")    echo unavailable ;;
"get -H -o value encryption dst")           echo off ;;
*) exit 0 ;;
esac`
	t.Run("key available: decrypted, so the target owns the policy", func(t *testing.T) {
		m := newMock(t)
		m.script("zfs", enc)
		p := RestorePipeline(Host{}, "archive/root@s", Host{}, "dst/new")
		if strings.Contains(p, " -w ") {
			t.Errorf("must NOT raw-send a restorable archive; got %s", p)
		}
		// ZFS refuses -p on an encrypted send that is not raw.
		if strings.Contains(p, " -p ") {
			t.Errorf("-p is illegal without -w on an encrypted source; got %s", p)
		}
		if !strings.Contains(p, "-x readonly") {
			t.Errorf("a restored root must not come back readonly; got %s", p)
		}
	})
	t.Run("key unavailable: raw is the only option", func(t *testing.T) {
		m := newMock(t)
		m.script("zfs", enc)
		p := RestorePipeline(Host{}, "locked/root@s", Host{}, "dst/new")
		if !strings.Contains(p, " -w ") {
			t.Errorf("without the key the send must be raw; got %s", p)
		}
	})
}

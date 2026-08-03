// zxplore-api — host-side ZFS-transaction daemon ("instant rollback as a function").
//
// WHAT
//
//	A small host daemon that lets a guest VM (or an app inside it) snapshot,
//	roll back, and commit *its own* zvol — without ever handing the guest ZFS
//	control of the host. The guest asks; the host verifies scope and acts.
//
// WHAT IT DOES, IN ORDER (per request)
//
//  1. Accept one JSON line on a listening socket (AF_VSOCK for real guests,
//     AF_UNIX for host-local ops/testing, TCP token-only).
//  2. Resolve the CALLER -> a VM name:
//     - vsock: peer CID -> the libvirt domain whose <vsock> CID matches.
//     - unix : SO_PEERCRED uid 0 (the host operator) may name the VM
//     directly, or present a per-VM token (token -> VM map).
//  3. Resolve + SCOPE the target zvol to that VM's subtree only
//     (ZXPLORE_VM_BASE/<vm>[-<zvol>]); refuse anything outside it.
//  4. Do the op: begin (snapshot), rollback (zfs rollback -r), commit
//     (destroy the snapshot), or list (open txns).
//  5. Append an audit line and answer JSON.
//
// WHY
//
//	ZFS snapshots are copy-on-write and ~instant, so "snapshot -> do a risky
//	thing -> roll back or commit" becomes a real transaction primitive: a DB
//	migration undone in milliseconds, ephemeral CI resetting to a golden
//	state, an agent sandbox that reverts every experiment. The host owns the
//	zvol, so mediation is the only safe way to expose that to a guest.
//
// HARD CONSTRAINT (callers must honour)
//
//	You cannot `zfs rollback` a zvol the guest is writing live — it corrupts
//	the mounted FS. Two supported patterns (guest side, see zxplore-txn):
//	  - data-zvol : app data on a SEPARATE zvol; quiesce -> unmount ->
//	                rollback -> remount -> restart. No reboot.
//	  - boot-env  : snapshot the OS zvol; "rollback" = reboot onto it.
//	This daemon does the ZFS op only and refuses a rollback of a zvol that is
//	currently attached to a running domain unless force, as a footgun guard.
//
// INPUTS / OUTPUTS
//
//	Env : ZXPLORE_VM_BASE   (default rpool/vms)             zvol subtree for VMs
//	      ZXPLORE_TXN_STATE (default /var/lib/zxplore)      txn + token state
//	      ZXPLORE_AUDIT     (default /var/log/zxplore/txn.log)
//	Args: --unix PATH | --vsock PORT | --tcp HOST:PORT (repeatable listeners)
//	Sock: newline-delimited JSON request -> newline-delimited JSON reply
//
// NOTES / INVARIANTS
//
//   - Scope is a hard boundary: a resolved zvol MUST equal <base>/<vm> or
//     start with <base>/<vm>- (a data disk) or <base>/<vm>/. No exceptions.
//   - Snapshots created here are zxplore-txn-<epoch>-<rand>; sanoid's
//     autosnap_* are never touched.
//   - HISTORY: this was Python through 1.1.0 (prototype: stdlib-only socket
//     daemon). Ported to Go so it ships static on every platform zxplore
//     targets — FreeBSD base has no interpreter — and so it lives inside the
//     project's test + release machinery. The wire protocol is unchanged.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zxplore/zxplore/txn"
)

var (
	vmBase   = env("ZXPLORE_VM_BASE", "rpool/vms")
	stateDir = env("ZXPLORE_TXN_STATE", "/var/lib/zxplore")
	auditLog = env("ZXPLORE_AUDIT", "/var/log/zxplore/txn.log")

	txnRe  = regexp.MustCompile(`^zxplore-txn-\d+-[0-9a-f]{6,}$`)
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	cidRe  = regexp.MustCompile(`<cid[^>]*\baddress=['"](\d+)['"]`)
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// apiError is a request-level failure; its message goes back to the caller.
type apiError struct{ msg string }

func (e *apiError) Error() string { return e.msg }
func errf(format string, a ...any) error {
	return &apiError{fmt.Sprintf(format, a...)}
}

// zfs runs `zfs <args>` and returns stdout, failing loud with stderr.
func zfs(args ...string) (string, error) {
	cmd := exec.Command("zfs", args...)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = "failed"
		}
		return "", errf("zfs %s: %s", strings.Join(args, " "), msg)
	}
	return string(out), nil
}

// audit appends one tab-separated line. Best-effort: a broken audit log must
// never mask the operation it was trying to record.
func audit(vm, caller, op, target, result string) {
	line := strings.Join([]string{
		time.Now().Format("2006-01-02T15:04:05-0700"),
		caller, vm, op, target, result,
	}, "\t") + "\n"
	if err := os.MkdirAll(filepath.Dir(auditLog), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "zxplore-api: audit mkdir failed: %v\n", err)
		return
	}
	f, err := os.OpenFile(auditLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "zxplore-api: audit write failed: %v\n", err)
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// ─── caller identity — the trust boundary ────────────────────────────────

func virshRunning() []string {
	out, err := exec.Command("virsh", "-q", "list", "--name", "--state-running").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// vmForVsockCID finds the running libvirt domain whose virtio <vsock> CID
// matches the connecting guest.
func vmForVsockCID(cid uint32) string {
	for _, name := range virshRunning() {
		xml, err := exec.Command("virsh", "dumpxml", name).Output()
		if err != nil {
			continue
		}
		if m := cidRe.FindSubmatch(xml); m != nil {
			var got uint32
			if _, err := fmt.Sscanf(string(m[1]), "%d", &got); err == nil && got == cid {
				return name
			}
		}
	}
	return ""
}

// vmForToken maps a per-VM secret to its VM. Tokens live at
// STATE_DIR/tokens/<vm> (0600); comparison is constant-time.
func vmForToken(token string) string {
	entries, err := os.ReadDir(filepath.Join(stateDir, "tokens"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(stateDir, "tokens", e.Name()))
		if err != nil {
			continue
		}
		want := strings.TrimSpace(string(b))
		if want != "" && subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1 {
			return e.Name()
		}
	}
	return ""
}

// ─── scope + transaction state ───────────────────────────────────────────

// resolveZvol maps (vm, optional data-disk name) to a fully-qualified zvol
// and proves it lives inside that VM's own subtree. This is the scope check
// everything else depends on.
func resolveZvol(vm, zvol string) (string, error) {
	base := vmBase + "/" + vm
	target := base
	switch zvol {
	case "", "root", "os":
	default:
		if !nameRe.MatchString(zvol) {
			return "", errf("illegal zvol name: %q", zvol)
		}
		target = base + "-" + zvol
	}
	if !(target == base || strings.HasPrefix(target, base+"-") || strings.HasPrefix(target, base+"/")) {
		return "", errf("scope violation: %s is outside %s", target, base)
	}
	out, err := zfs("get", "-H", "-o", "value", "type", target)
	if err != nil {
		return "", err
	}
	if typ := strings.TrimSpace(out); typ != "volume" {
		if typ == "" {
			typ = "missing"
		}
		return "", errf("%s is not a zvol (type=%s)", target, typ)
	}
	return target, nil
}

func txnDir(vm string) (string, error) {
	d := filepath.Join(stateDir, "txns", vm)
	return d, os.MkdirAll(d, 0o700)
}

// zvolInUse reports whether a running domain currently has this zvol
// attached — rolling that back would corrupt the guest FS.
func zvolInUse(zvol string) bool {
	dev := "/dev/zvol/" + zvol
	for _, name := range virshRunning() {
		xml, err := exec.Command("virsh", "dumpxml", name).Output()
		if err != nil {
			continue
		}
		s := string(xml)
		if strings.Contains(s, dev) || strings.Contains(s, "'"+zvol+"'") || strings.Contains(s, `"`+zvol+`"`) {
			return true
		}
	}
	return false
}

func loadTxn(vm, id string) (*txn.TxnRecord, error) {
	if !txnRe.MatchString(id) {
		return nil, errf("illegal txn id: %q", id)
	}
	d, err := txnDir(vm)
	if err != nil {
		return nil, errf("txn state: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(d, id))
	if err != nil {
		return nil, errf("no such open transaction: %s", id)
	}
	var rec txn.TxnRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, errf("corrupt txn record %s: %v", id, err)
	}
	return &rec, nil
}

// ─── operations ──────────────────────────────────────────────────────────

func opBegin(vm, caller string, req *txn.Request) (*txn.Reply, error) {
	target, err := resolveZvol(vm, req.Zvol)
	if err != nil {
		return nil, err
	}
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, errf("entropy: %v", err)
	}
	id := fmt.Sprintf("zxplore-txn-%d-%s", time.Now().Unix(), hex.EncodeToString(rnd[:]))
	snap := target + "@" + id
	if _, err := zfs("snapshot", snap); err != nil {
		return nil, err
	}
	note := req.Note
	if len(note) > 200 {
		note = note[:200]
	}
	rec := txn.TxnRecord{
		Txn: id, VM: vm, Zvol: target, Snapshot: snap,
		Opened: float64(time.Now().UnixNano()) / 1e9, Note: note,
	}
	d, err := txnDir(vm)
	if err != nil {
		return nil, errf("txn state: %v", err)
	}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(d, id), b, 0o600); err != nil {
		return nil, errf("txn state: %v", err)
	}
	audit(vm, caller, "begin", snap, "ok")
	return &txn.Reply{OK: true, Txn: id, Snapshot: snap, Zvol: target}, nil
}

func opRollback(vm, caller string, req *txn.Request) (*txn.Reply, error) {
	rec, err := loadTxn(vm, req.Txn)
	if err != nil {
		return nil, err
	}
	if zvolInUse(rec.Zvol) && !req.Force {
		audit(vm, caller, "rollback", rec.Snapshot, "refused-in-use")
		return nil, errf("%s is attached to a running VM; quiesce+detach first "+
			"or pass force=true (data-zvol pattern), or use boot-env rollback", rec.Zvol)
	}
	if _, err := zfs("rollback", "-r", rec.Snapshot); err != nil {
		return nil, err
	}
	audit(vm, caller, "rollback", rec.Snapshot, "ok")
	return &txn.Reply{OK: true, Txn: rec.Txn, Snapshot: rec.Snapshot, RolledBack: true}, nil
}

func opCommit(vm, caller string, req *txn.Request) (*txn.Reply, error) {
	rec, err := loadTxn(vm, req.Txn)
	if err != nil {
		return nil, err
	}
	if _, err := zfs("destroy", rec.Snapshot); err != nil {
		return nil, err
	}
	d, _ := txnDir(vm)
	_ = os.Remove(filepath.Join(d, rec.Txn))
	audit(vm, caller, "commit", rec.Snapshot, "ok")
	return &txn.Reply{OK: true, Txn: rec.Txn, Committed: true}, nil
}

func opList(vm, _ string, _ *txn.Request) (*txn.Reply, error) {
	d, err := txnDir(vm)
	if err != nil {
		return nil, errf("txn state: %v", err)
	}
	entries, _ := os.ReadDir(d)
	out := []txn.TxnRecord{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(d, e.Name()))
		if err != nil {
			continue
		}
		var rec txn.TxnRecord
		if json.Unmarshal(b, &rec) == nil {
			out = append(out, rec)
		}
	}
	return &txn.Reply{OK: true, VM: vm, Txns: out}, nil
}

var ops = map[string]func(string, string, *txn.Request) (*txn.Reply, error){
	"begin": opBegin, "rollback": opRollback, "commit": opCommit, "list": opList,
}

// ─── request handling ────────────────────────────────────────────────────

// identity is what the daemon decided about a caller before any op runs.
type identity struct{ vm, caller string }

// identify maps a connection to a VM. Everything downstream is scoped to
// whatever this returns, so it is deliberately strict.
func identify(peerCID int64, unixUID int, req *txn.Request) (*identity, error) {
	if peerCID >= 0 {
		vm := vmForVsockCID(uint32(peerCID))
		if vm == "" {
			return nil, errf("vsock CID %d maps to no running VM", peerCID)
		}
		return &identity{vm, fmt.Sprintf("vsock:%d", peerCID)}, nil
	}
	if req.Token != "" {
		vm := vmForToken(req.Token)
		if vm == "" {
			return nil, errf("invalid token")
		}
		return &identity{vm, "token"}, nil
	}
	if unixUID == 0 {
		if req.VM == "" || !nameRe.MatchString(req.VM) {
			return nil, errf("root unix caller must supply a valid 'vm'")
		}
		return &identity{req.VM, "operator"}, nil
	}
	return nil, errf("unauthenticated: token required")
}

// serve handles one connection: read a request, act, write a reply.
func serve(rw io.ReadWriteCloser, peerCID int64, unixUID int) {
	defer rw.Close()
	reply := &txn.Reply{}
	var req txn.Request
	err := txn.ReadJSON(rw, &req)
	switch {
	case err != nil:
		reply = &txn.Reply{OK: false, Error: fmt.Sprintf("bad request: %v", err)}
	default:
		fn, ok := ops[req.Op]
		if !ok {
			reply = &txn.Reply{OK: false, Error: fmt.Sprintf(
				"unknown op: %q (want begin|rollback|commit|list)", req.Op)}
			break
		}
		id, ierr := identify(peerCID, unixUID, &req)
		if ierr != nil {
			reply = &txn.Reply{OK: false, Error: ierr.Error()}
			break
		}
		out, oerr := fn(id.vm, id.caller, &req)
		if oerr != nil {
			var ae *apiError
			if !errors.As(oerr, &ae) {
				// Never leak internals to a guest; log locally instead.
				fmt.Fprintf(os.Stderr, "zxplore-api: unexpected: %v\n", oerr)
				oerr = errf("internal error")
			}
			reply = &txn.Reply{OK: false, Error: oerr.Error()}
			break
		}
		reply = out
	}
	_ = txn.WriteJSON(rw, reply)
}

func serveNet(l net.Listener, label string) {
	fmt.Fprintf(os.Stderr, "zxplore-api: listening on %s\n", label)
	for {
		c, err := l.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "zxplore-api: accept on %s failed: %v\n", label, err)
			continue
		}
		uid := txn.PeerUID(c)
		go func() {
			_ = c.SetDeadline(time.Now().Add(15 * time.Second))
			serve(c, -1, uid)
		}()
	}
}

func serveVsock(l *txn.VsockListener) {
	fmt.Fprintf(os.Stderr, "zxplore-api: listening on %s\n", l.Label)
	for {
		c, err := l.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "zxplore-api: accept on %s failed: %v\n", l.Label, err)
			continue
		}
		go func() {
			_ = c.SetDeadline(time.Now().Add(15 * time.Second))
			serve(c, int64(c.PeerCID), -1)
		}()
	}
}

func listenUnix(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// root/operator only on the host side.
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func main() {
	unixPath := flag.String("unix", "", "listen on an AF_UNIX socket")
	vsockPort := flag.Int("vsock", 0, "listen on AF_VSOCK PORT")
	tcpAddr := flag.String("tcp", "", "listen on TCP HOST:PORT (token-only)")
	flag.Parse()

	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "zxplore-api: state dir: %v\n", err)
		os.Exit(1)
	}

	started := 0
	start := func(err error, what string) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "zxplore-api: %s: %v\n", what, err)
			return
		}
		started++
	}

	if *unixPath != "" {
		l, err := listenUnix(*unixPath)
		start(err, "unix listener")
		if err == nil {
			go serveNet(l, "unix:"+*unixPath)
		}
	}
	if *vsockPort != 0 {
		l, err := txn.ListenVsock(uint32(*vsockPort))
		start(err, "vsock listener")
		if err == nil {
			go serveVsock(l)
		}
	}
	if *tcpAddr != "" {
		l, err := net.Listen("tcp", *tcpAddr)
		start(err, "tcp listener")
		if err == nil {
			go serveNet(l, "tcp:"+*tcpAddr)
		}
	}
	if *unixPath == "" && *vsockPort == 0 && *tcpAddr == "" {
		// Sane default: unix socket for host ops + vsock 9455 for guests.
		l, err := listenUnix("/run/zxplore-api.sock")
		start(err, "unix listener")
		if err == nil {
			go serveNet(l, "unix:/run/zxplore-api.sock")
		}
		vl, verr := txn.ListenVsock(txn.DefaultPort)
		start(verr, "vsock listener")
		if verr == nil {
			go serveVsock(vl)
		}
	}
	if started == 0 {
		fmt.Fprintln(os.Stderr, "zxplore-api: no listeners started — exiting")
		os.Exit(1)
	}
	select {} // serve until killed
}

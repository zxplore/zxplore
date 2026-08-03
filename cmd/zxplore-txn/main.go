// zxplore-txn — guest-side ZFS transaction client ("instant rollback as a function").
//
// WHAT
//
//	The app-facing half of the zxplore transaction API. Runs INSIDE a guest VM
//	and asks the host daemon (zxplore-api) to snapshot / roll back / commit
//	this VM's own zvol. Turns ZFS copy-on-write into a transaction primitive.
//
// WHAT IT DOES, IN ORDER
//
//  1. Connect to the host: vsock CID 2 (default), or a unix socket / TCP host.
//  2. Authenticate: send the per-VM token from /etc/zxplore/token if present.
//  3. Run one op — begin | rollback | commit | list — and print the reply
//     (begin prints the txn id to stdout so a script can capture it).
//  4. For the data-zvol rollback pattern, optionally quiesce the guest side:
//     -unmount DIR unmounts before the host rollback; -dev DEV remounts after.
//     (You cannot roll back a live-mounted FS — see zxplore-api.)
//
// WHY
//
//	So an application can bracket a risky operation with a guaranteed, ~instant
//	undo — exit status is 0 on success, non-zero on failure, so it composes
//	with && / || like any Unix tool:
//
//	    txn=$(zxplore-txn begin -zvol data) || exit 1
//	    psql < migration.sql && ./smoke-test || zxplore-txn rollback "$txn" -unmount /data
//	    zxplore-txn commit "$txn"
//
// INPUTS / OUTPUTS
//
//	Files : /etc/zxplore/token  (per-VM secret, injected at VM deploy; optional
//	                            on vsock, where the host identifies us by CID)
//	Args  : see -h. Transport defaults to vsock CID 2 port 9455.
//	Stdout: the txn id (begin) or the reply; stderr: human errors.
//
// NOTES
//
//   - Boot-env (whole-VM) rollback isn't done here: that's "reboot onto the
//     snapshot", a host/VM-manager action, not an in-guest FS remount.
//   - Static by design (CGO_ENABLED=0): one file that drops into any guest,
//     including images with no interpreter. That is why this was ported from
//     the 1.x Python client — same wire protocol, no runtime dependency.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/zxplore/zxplore/txn"
)

const tokenFile = "/etc/zxplore/token"

type opts struct {
	cid, port          int
	unixPath, tcpAddr  string
	vm                 string
	jsonOut            bool
	zvol, note         string
	unmountDir, devArg string
	force              bool
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "zxplore-txn: "+format+"\n", a...)
	os.Exit(1)
}

// dial opens the chosen transport to the host daemon.
func dial(o *opts) (io.ReadWriteCloser, error) {
	const timeout = 30 * time.Second
	switch {
	case o.unixPath != "":
		c, err := net.DialTimeout("unix", o.unixPath, timeout)
		if err == nil {
			_ = c.SetDeadline(time.Now().Add(timeout))
		}
		return c, err
	case o.tcpAddr != "":
		c, err := net.DialTimeout("tcp", o.tcpAddr, timeout)
		if err == nil {
			_ = c.SetDeadline(time.Now().Add(timeout))
		}
		return c, err
	default:
		return txn.DialVsock(uint32(o.cid), uint32(o.port), timeout)
	}
}

// call sends one request and returns the reply, attaching the VM token when
// one exists (vsock guests are identified by CID and need none).
func call(o *opts, req *txn.Request) (*txn.Reply, error) {
	if b, err := os.ReadFile(tokenFile); err == nil {
		req.Token = strings.TrimSpace(string(b))
	}
	if o.vm != "" {
		req.VM = o.vm
	}
	c, err := dial(o)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := txn.WriteJSON(c, req); err != nil {
		return nil, err
	}
	var rep txn.Reply
	if err := txn.ReadJSON(c, &rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

// run executes a guest-side quiesce command (umount/mount), failing loud.
func run(args ...string) {
	cmd := exec.Command(args[0], args[1:]...)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		die("%s: %s", strings.Join(args, " "), strings.TrimSpace(errBuf.String()))
	}
}

func printJSON(v any, indent bool) {
	var b []byte
	if indent {
		b, _ = json.MarshalIndent(v, "", "  ")
	} else {
		b, _ = json.Marshal(v)
	}
	fmt.Println(string(b))
}

func usage() {
	fmt.Fprint(os.Stderr, `zxplore-txn — guest client for host-mediated ZFS transactions

  zxplore-txn begin    [-zvol NAME] [-note TEXT]
  zxplore-txn rollback <txn> [-unmount DIR] [-dev DEV] [-force]
  zxplore-txn commit   <txn>
  zxplore-txn list

Transport (default vsock CID 2 port 9455):
  -cid N          vsock host CID
  -port N         vsock/host port
  -unix PATH      use a unix socket instead of vsock
  -tcp HOST:PORT  use TCP instead of vsock
  -vm NAME        name the VM explicitly (operator/root-unix only)
  -json           print the raw JSON reply

Exit status is 0 on success, non-zero on failure, so it composes with && / ||.
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(64)
	}
	op := os.Args[1]
	if op == "-h" || op == "--help" || op == "help" {
		usage()
		return
	}

	o := &opts{cid: txn.HostCID, port: txn.DefaultPort}
	fs := flag.NewFlagSet(op, flag.ExitOnError)
	fs.Usage = usage
	fs.IntVar(&o.cid, "cid", o.cid, "vsock host CID")
	fs.IntVar(&o.port, "port", o.port, "vsock/host port")
	fs.StringVar(&o.unixPath, "unix", "", "use a unix socket")
	fs.StringVar(&o.tcpAddr, "tcp", "", "use TCP HOST:PORT")
	fs.StringVar(&o.vm, "vm", "", "name the VM explicitly")
	fs.BoolVar(&o.jsonOut, "json", false, "print the raw JSON reply")
	switch op {
	case "begin":
		fs.StringVar(&o.zvol, "zvol", "", "data-disk name (default: the OS disk)")
		fs.StringVar(&o.note, "note", "", "free-text note stored with the txn")
	case "rollback":
		fs.StringVar(&o.unmountDir, "unmount", "", "unmount DIR before rollback")
		fs.StringVar(&o.devArg, "dev", "", "remount DEV at DIR after rollback")
		fs.BoolVar(&o.force, "force", false, "allow rollback of an attached zvol")
	case "commit", "list":
	default:
		usage()
		os.Exit(64)
	}

	// Positional txn id may appear before or after flags.
	args := os.Args[2:]
	var positional string
	if op == "rollback" || op == "commit" {
		var rest []string
		for _, a := range args {
			if positional == "" && !strings.HasPrefix(a, "-") {
				positional = a
				continue
			}
			rest = append(rest, a)
		}
		args = rest
	}
	_ = fs.Parse(args)
	if (op == "rollback" || op == "commit") && positional == "" {
		die("%s needs a transaction id", op)
	}

	var rep *txn.Reply
	var err error
	switch op {
	case "begin":
		rep, err = call(o, &txn.Request{Op: "begin", Zvol: o.zvol, Note: o.note})
		if err == nil && rep.OK {
			if o.jsonOut {
				printJSON(rep, false)
			} else {
				fmt.Println(rep.Txn)
			}
			return
		}
	case "rollback":
		// data-zvol pattern: quiesce in the guest, roll back on the host,
		// then remount — the only safe way to roll back live app data.
		if o.unmountDir != "" {
			run("umount", o.unmountDir)
		}
		rep, err = call(o, &txn.Request{Op: "rollback", Txn: positional, Force: o.force})
		if err == nil && rep.OK {
			if o.unmountDir != "" && o.devArg != "" {
				run("mount", o.devArg, o.unmountDir)
			}
			if o.jsonOut {
				printJSON(rep, false)
			} else {
				fmt.Printf("rolled back to %s\n", rep.Snapshot)
			}
			return
		}
	case "commit":
		rep, err = call(o, &txn.Request{Op: "commit", Txn: positional})
		if err == nil && rep.OK {
			if o.jsonOut {
				printJSON(rep, false)
			} else {
				fmt.Printf("committed %s\n", positional)
			}
			return
		}
	case "list":
		rep, err = call(o, &txn.Request{Op: "list"})
		if err == nil && rep.OK {
			if o.jsonOut {
				printJSON(rep, true)
			} else {
				for _, t := range rep.Txns {
					fmt.Printf("%s\t%s\t%s\n", t.Txn, t.Zvol, t.Note)
				}
			}
			return
		}
	}

	switch {
	case err != nil:
		die("%v", err)
	case rep != nil && rep.Error != "":
		die("%s", rep.Error)
	default:
		die("no reply")
	}
}

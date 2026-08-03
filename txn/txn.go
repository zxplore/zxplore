// Package txn is the wire protocol and transports shared by the zxplore
// transaction API: the host daemon (cmd/zxplore-api) and the in-guest client
// (cmd/zxplore-txn).
//
// PROTOCOL (unchanged from the 1.x Python implementation — guests in the
// field speak it): one newline-delimited JSON request per connection, one
// newline-delimited JSON reply. Ops: begin | rollback | commit | list.
// Replies always carry ok:true|false; failures carry error.
//
// TRANSPORTS: AF_VSOCK (real guests; the host identifies the caller by its
// CID), AF_UNIX (host-local operator/testing; the daemon reads SO_PEERCRED),
// and TCP (token-only). vsock is spoken through raw fds wrapped in os.File —
// the net package has no AF_VSOCK support, and os.File gives us working
// deadlines on any pollable fd.
package txn

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultPort is the vsock port the daemon listens on and the client dials.
const DefaultPort = 9455

// HostCID is the well-known vsock CID of the host, seen from inside a guest.
const HostCID = 2

// AnyCID (VMADDR_CID_ANY) binds a listener to every guest CID.
const AnyCID = uint32(0xFFFFFFFF)

// Request is one API call. Fields are optional per-op; the daemon validates.
type Request struct {
	Op    string `json:"op"`
	Token string `json:"token,omitempty"`
	VM    string `json:"vm,omitempty"`
	Zvol  string `json:"zvol,omitempty"`
	Note  string `json:"note,omitempty"`
	Txn   string `json:"txn,omitempty"`
	Force bool   `json:"force,omitempty"`
}

// Reply is one API answer. Extra per-op fields ride in the same object, so
// this mirrors the Python dict shape exactly.
type Reply struct {
	OK         bool        `json:"ok"`
	Error      string      `json:"error,omitempty"`
	Txn        string      `json:"txn,omitempty"`
	Snapshot   string      `json:"snapshot,omitempty"`
	Zvol       string      `json:"zvol,omitempty"`
	RolledBack bool        `json:"rolled_back,omitempty"`
	Committed  bool        `json:"committed,omitempty"`
	VM         string      `json:"vm,omitempty"`
	Txns       []TxnRecord `json:"txns,omitempty"`
}

// TxnRecord is the on-disk state of one open transaction (STATE_DIR/txns/<vm>/<id>).
type TxnRecord struct {
	Txn      string  `json:"txn"`
	VM       string  `json:"vm"`
	Zvol     string  `json:"zvol"`
	Snapshot string  `json:"snapshot"`
	Opened   float64 `json:"opened"`
	Note     string  `json:"note,omitempty"`
}

// maxLine caps a request so a hostile guest cannot exhaust host memory.
const maxLine = 64 << 10

// ReadJSON reads one newline-delimited JSON value into v.
func ReadJSON(r io.Reader, v any) error {
	br := bufio.NewReaderSize(io.LimitReader(r, maxLine), 4096)
	line, err := br.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return err
	}
	return json.Unmarshal(line, v)
}

// WriteJSON writes v as one newline-delimited JSON line.
func WriteJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ─── vsock ───────────────────────────────────────────────────────────────
// Raw AF_VSOCK fds wrapped in os.File: the net package cannot speak vsock,
// but os.File Read/Write/SetDeadline work on any pollable fd.

// VsockConn is an accepted vsock connection plus the peer's CID — the
// daemon's entire notion of "who is calling" on that transport.
type VsockConn struct {
	*os.File
	PeerCID uint32
}

// VsockListener accepts guest connections on a vsock port.
type VsockListener struct {
	fd    int
	Label string
}

// ListenVsock binds VMADDR_CID_ANY:port. Fails clearly when the kernel has
// no vsock support rather than pretending to listen.
func ListenVsock(port uint32) (*VsockListener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w (kernel without AF_VSOCK?)", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: AnyCID, Port: port}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock bind :%d: %w", port, err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return &VsockListener{fd: fd, Label: fmt.Sprintf("vsock:*:%d", port)}, nil
}

// Accept blocks for the next guest connection.
func (l *VsockListener) Accept() (*VsockConn, error) {
	nfd, sa, err := unix.Accept(l.fd)
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(nfd)
	var cid uint32
	if vm, ok := sa.(*unix.SockaddrVM); ok {
		cid = vm.CID
	}
	return &VsockConn{File: os.NewFile(uintptr(nfd), "vsock"), PeerCID: cid}, nil
}

// Close stops accepting.
func (l *VsockListener) Close() error { return unix.Close(l.fd) }

// DialVsock connects to the host daemon from inside a guest.
func DialVsock(cid uint32, port uint32, timeout time.Duration) (*os.File, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w (no vsock in this guest? use --unix/--tcp)", err)
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock connect %d:%d: %w", cid, port, err)
	}
	f := os.NewFile(uintptr(fd), "vsock")
	_ = f.SetDeadline(time.Now().Add(timeout))
	return f, nil
}

// ─── peer credentials ────────────────────────────────────────────────────

// PeerUID returns the uid on the far end of an AF_UNIX connection, or -1.
// The daemon uses this to let root-on-unix (the operator) name a VM directly.
func PeerUID(c net.Conn) int {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return -1
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1
	}
	uid := -1
	_ = raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			uid = int(cred.Uid)
		}
	})
	return uid
}

// servers.go — the saved-server store + SSH key setup (the "WinSCP sessions" of
// zxplore). A Server is a named connection (host/port/user/path) authenticated by
// an SSH KEY. Key-first by design: you paste, pick, or generate a key, then use
// your password ONCE to install the public key on the server (ssh-copy-id style).
// Passwords are never stored — only the server record + a path to the key.
//
// Persisted as JSON under the operator's config dir (stdlib only); keys live
// beside it in keys/ at 0600. Same engine (Host) drives local, remote, and
// remote→remote (FXP-style) replication.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Server is one saved connection.
type Server struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Port    int    `json:"port,omitempty"`    // 0 = 22
	User    string `json:"user,omitempty"`    // "" = current user
	Path    string `json:"path,omitempty"`    // pool[/dataset] to land on
	KeyPath string `json:"keyPath,omitempty"` // identity file for this server
	Jump    string `json:"jump,omitempty"`    // ProxyJump chain "[user@]host[,…]" (bastion)
}

func (s Server) sshTarget() string {
	if s.User != "" {
		return s.User + "@" + s.Host
	}
	return s.Host
}

// toHost turns a Server into the connection the ZFS engine uses.
func (s Server) toHost() Host {
	return Host{SSH: s.sshTarget(), Port: s.Port, KeyPath: s.KeyPath, Jump: s.Jump}
}

// ── persistence ──────────────────────────────────────────────────────────────

func zxploreConfigDir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "zxplore")
}

func serversPath() string { return filepath.Join(zxploreConfigDir(), "servers.json") }
func keysDir() string     { return filepath.Join(zxploreConfigDir(), "keys") }

// LoadServers reads saved servers (nil if none / unreadable).
func LoadServers() []Server {
	data, err := os.ReadFile(serversPath())
	if err != nil {
		return nil
	}
	var list []Server
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	return list
}

// SaveServers writes the list atomically (temp + rename), 0600.
func SaveServers(list []Server) error {
	p := serversPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// UpsertServer replaces the entry with the same Name, or appends it.
func UpsertServer(list []Server, s Server) []Server {
	out := make([]Server, 0, len(list)+1)
	replaced := false
	for _, e := range list {
		if e.Name == s.Name {
			out, replaced = append(out, s), true
		} else {
			out = append(out, e)
		}
	}
	if !replaced {
		out = append(out, s)
	}
	return out
}

// DeleteServer drops the entry with the given Name.
func DeleteServer(list []Server, name string) []Server {
	out := make([]Server, 0, len(list))
	for _, e := range list {
		if e.Name != name {
			out = append(out, e)
		}
	}
	return out
}

// ── key setup ────────────────────────────────────────────────────────────────

// keyPathFor is the standard identity-file path for a server name.
func keyPathFor(name string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' || r == filepath.Separator {
			return '_'
		}
		return r
	}, name)
	if safe == "" {
		safe = "server"
	}
	return filepath.Join(keysDir(), safe)
}

// GenerateKey creates a fresh ed25519 keypair for a server and returns its path.
func GenerateKey(name string) (string, error) {
	if err := os.MkdirAll(keysDir(), 0o700); err != nil {
		return "", err
	}
	kp := keyPathFor(name)
	_ = os.Remove(kp)
	_ = os.Remove(kp + ".pub")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "zxplore-"+name, "-f", kp)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh-keygen: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return kp, nil
}

// SavePastedKey writes a pasted private key to the keys dir (0600), returning its
// path. The matching public key is derived on demand by PublicKey.
func SavePastedKey(name, pem string) (string, error) {
	if err := os.MkdirAll(keysDir(), 0o700); err != nil {
		return "", err
	}
	kp := keyPathFor(name)
	if !strings.HasSuffix(pem, "\n") {
		pem += "\n"
	}
	if err := os.WriteFile(kp, []byte(pem), 0o600); err != nil {
		return "", err
	}
	_ = os.Remove(kp + ".pub") // force re-derivation
	return kp, nil
}

// PublicKey returns the OpenSSH public key line for a key path — from the .pub
// if present, else derived from the private key via ssh-keygen -y.
func PublicKey(keyPath string) (string, error) {
	if b, err := os.ReadFile(keyPath + ".pub"); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	out, err := exec.Command("ssh-keygen", "-y", "-f", keyPath).Output()
	if err != nil {
		return "", fmt.Errorf("derive public key from %s: %v", keyPath, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// AuthorizeKey appends the server's public key to the remote's authorized_keys
// using a ONE-TIME password. Pure Go (golang.org/x/crypto/ssh) — no sshpass,
// no password on any process's argv or environment, used only for this dial
// and never stored. After this, key auth works and no password is needed.
func AuthorizeKey(s Server, password string) error {
	pub, err := PublicKey(s.KeyPath)
	if err != nil {
		return err
	}
	port := s.Port
	if port == 0 {
		port = 22
	}
	user := s.User
	if user == "" {
		user = os.Getenv("USER")
	}
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.Password(password)},
		// accept-new, like the ssh CLI: first contact records the host key in
		// known_hosts; a CHANGED key refuses — this leg carries a PASSWORD, so
		// blind trust here would hand it to a man-in-the-middle.
		HostKeyCallback: hostKeyAcceptNew(),
		Timeout:         10 * time.Second,
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(s.Host, strconv.Itoa(port)), cfg)
	if err != nil {
		return fmt.Errorf("authorize key on %s: %v", s.sshTarget(), err)
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("authorize key on %s: %v", s.sshTarget(), err)
	}
	defer sess.Close()
	sess.Stdin = strings.NewReader(pub + "\n")
	// append via stdin so the key material never hits a shell-quoted argv
	out, err := sess.CombinedOutput(
		"umask 077; mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys && sort -u ~/.ssh/authorized_keys -o ~/.ssh/authorized_keys")
	if err != nil {
		return fmt.Errorf("authorize key on %s: %v: %s", s.sshTarget(), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetupServer makes a server connectable in ONE step: ensures a key exists
// (generating a fresh ed25519 when none is set), checks whether that key
// already works, and only if it doesn't, uses the one-time password to
// install it — then verifies ZFS is actually reachable. Returns the server
// with its (possibly new) KeyPath so the caller can persist it. The password
// is used at most once and never stored; "" is fine when the key might
// already be authorized.
func SetupServer(s Server, password string) (Server, error) {
	if s.KeyPath == "" {
		kp, err := GenerateKey(s.Name)
		if err != nil {
			return s, err
		}
		s.KeyPath = kp
	}
	if err := TestServer(s); err == nil {
		return s, nil // key already authorized, ZFS visible — nothing to do
	}
	if password == "" {
		return s, fmt.Errorf("the key isn't authorized on %s yet — the one-time password is needed to install it", s.sshTarget())
	}
	if err := AuthorizeKey(s, password); err != nil {
		return s, err
	}
	return s, TestServer(s)
}

// hostKeyAcceptNew mirrors OpenSSH's StrictHostKeyChecking=accept-new for the
// in-process ssh client: a host already in ~/.ssh/known_hosts must present the
// SAME key (a changed key is refused — likely MITM); an unknown host is
// recorded on first contact. Keeping the pin in the standard known_hosts file
// means the later `ssh` CLI legs (engine, replication) verify against the very
// key this bootstrap saw.
func hostKeyAcceptNew() ssh.HostKeyCallback {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".ssh", "known_hosts")
	return func(hostport string, remote net.Addr, key ssh.PublicKey) error {
		if check, err := knownhosts.New(path); err == nil {
			err := check(hostport, remote, key)
			if err == nil {
				return nil // known and matching
			}
			var ke *knownhosts.KeyError
			if errors.As(err, &ke) && len(ke.Want) > 0 {
				return fmt.Errorf("host key for %s CHANGED — possible man-in-the-middle; verify the server and update %s", hostport, path)
			}
			// otherwise: unknown host — record it below
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("record host key: %v", err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("record host key: %v", err)
		}
		defer f.Close()
		_, err = fmt.Fprintln(f, knownhosts.Line([]string{hostport}, key))
		return err
	}
}

// TestServer verifies the key works AND ZFS is reachable: it runs `zfs list` on
// the server (scoped to Path when set). A nil return means "ready to use".
func TestServer(s Server) error {
	h := s.toHost()
	args := []string{"list", "-H", "-o", "name"}
	if s.Path != "" {
		args = append(args, "-d", "0", s.Path)
	}
	_, err := run(h.command("zfs", args...))
	return friendlySSH(err, s)
}

// friendlySSH turns a raw ssh auth failure into the NEXT STEP. Connections
// are key-first by design (BatchMode, no tty): the password is only ever
// used by "Authorize on server", once, to install the key.
func friendlySSH(err error, s Server) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Too many authentication failures"):
		return fmt.Errorf("%v\n\nYour ssh-agent offered too many keys before the right one.\nAssign a key to this server (Generate key / Use key file),\nthen click “Authorize on server”.", err)
	case strings.Contains(msg, "Permission denied"):
		return fmt.Errorf("%v\n\nThe key isn't authorized on %s yet.\nClick “Authorize on server (password)” — the password is used once\nto install the key and never stored. Panes always connect by key;\ninteractive password login is not used.", err, s.sshTarget())
	}
	return err
}

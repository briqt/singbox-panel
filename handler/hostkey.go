package handler

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyStore pins each node's SSH host key on first contact and verifies it
// on every later connection (trust on first use).
//
// The panel logs into every node as root, pushes the sing-box config — which
// carries every user's UUID and the Reality private key — and runs install and
// restart commands, all over the public internet. Accepting any host key means
// anything able to intercept that path can impersonate a node and be handed the
// full credential set. TOFU closes that for every connection after the first;
// the first one happens during operator-driven node setup.
//
// The file is plain OpenSSH known_hosts format so it can be inspected, edited,
// or seeded with ssh-keyscan using familiar tools.
type hostKeyStore struct {
	path string
	mu   sync.Mutex
}

func newHostKeyStore(dataDir string) *hostKeyStore {
	return &hostKeyStore{path: filepath.Join(dataDir, "known_hosts")}
}

// ErrHostKeyMismatch reports that a node presented a different key than the one
// pinned earlier. It is deliberately fatal: a rebuilt node and a
// man-in-the-middle look identical to the panel, so an operator has to say
// which one it is.
type ErrHostKeyMismatch struct {
	Host string
	Path string
}

func (e *ErrHostKeyMismatch) Error() string {
	return fmt.Sprintf(
		"host key for %s does not match the pinned key in %s. "+
			"If this node was rebuilt, remove its line (ssh-keygen -f %s -R %s) and reconnect; "+
			"otherwise the connection is being intercepted",
		e.Host, e.Path, e.Path, e.Host)
}

// callback returns an ssh.HostKeyCallback bound to this store.
func (s *hostKeyStore) callback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		s.mu.Lock()
		defer s.mu.Unlock()

		if err := s.ensureFile(); err != nil {
			return err
		}

		// Re-read on every verification: dials are rare now that the poller
		// pools connections, and this keeps the store correct when an operator
		// edits the file underneath a running panel.
		verify, err := knownhosts.New(s.path)
		if err != nil {
			return fmt.Errorf("read known_hosts: %w", err)
		}

		switch err := verify(hostname, remote, key); {
		case err == nil:
			return nil
		case isUnknownHost(err):
			return s.pin(hostname, remote, key)
		default:
			var keyErr *knownhosts.KeyError
			if errors.As(err, &keyErr) {
				return &ErrHostKeyMismatch{Host: hostname, Path: s.path}
			}
			return err
		}
	}
}

// isUnknownHost distinguishes "never seen this host" from "seen it with a
// different key". knownhosts reports both as *KeyError; only the mismatch case
// carries the keys it knows about.
func isUnknownHost(err error) bool {
	var keyErr *knownhosts.KeyError
	return errors.As(err, &keyErr) && len(keyErr.Want) == 0
}

// pin appends the host key, trusting it for the first time.
func (s *hostKeyStore) pin(hostname string, remote net.Addr, key ssh.PublicKey) error {
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open known_hosts: %w", err)
	}
	defer f.Close()

	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("pin host key: %w", err)
	}
	return nil
}

func (s *hostKeyStore) ensureFile() error {
	if _, err := os.Stat(s.path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// forget removes every pinned key for a host, so the next connection re-pins.
// Used when a node is deliberately rebuilt.
func (s *hostKeyStore) forget(hostname string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	want := knownhosts.Normalize(hostname)
	var kept []byte
	for _, line := range knownHostsLines(data) {
		if hostMatchesLine(line, want) {
			continue
		}
		kept = append(kept, line...)
		kept = append(kept, '\n')
	}
	return os.WriteFile(s.path, kept, 0o600)
}

// knownHostsLines splits the file into lines. Named apart from setup.go's
// string-based splitLines, which serves a different caller.
func knownHostsLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// hostMatchesLine reports whether a known_hosts line pins the given normalized
// host. Hashed entries are left alone: the panel never writes them, so a hashed
// line came from an operator and is not ours to rewrite.
func hostMatchesLine(line []byte, want string) bool {
	trimmed := string(line)
	if trimmed == "" || trimmed[0] == '#' || trimmed[0] == '|' {
		return false
	}
	_, hosts, _, _, _, err := ssh.ParseKnownHosts(line)
	if err != nil {
		return false
	}
	for _, h := range hosts {
		if h == want {
			return true
		}
	}
	return false
}

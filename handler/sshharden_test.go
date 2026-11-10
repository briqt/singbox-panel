package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strconv"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/briqt/singbox-panel/model"
)

// startTestSSHD runs an in-process SSH server. passwordSeen counts every
// password the server receives, which is the property the probe must never
// trigger.
func startTestSSHD(t *testing.T, allowPassword bool) (*model.Node, *atomic.Int32) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	var passwordSeen atomic.Int32
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, errProbeOnly
		},
	}
	if allowPassword {
		cfg.PasswordCallback = func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			passwordSeen.Add(1)
			return nil, errProbeOnly
		}
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				ssh.NewServerConn(conn, cfg)
				conn.Close()
			}()
		}
	}()
	port, _ := strconv.Atoi(ln.Addr().(*net.TCPAddr).AddrPort().String()[len("127.0.0.1:"):])
	return &model.Node{Name: "t", Host: "127.0.0.1", Port: port, SSHUser: "root"}, &passwordSeen
}

// TestProbeDetectsPasswordWithoutSendingIt is the falsification test for the
// hardening read-back: against a server that still accepts passwords the probe
// must say so, and the server must never have received a password.
func TestProbeDetectsPasswordWithoutSendingIt(t *testing.T) {
	node, seen := startTestSSHD(t, true)
	h := &ConfigHandler{DataDir: t.TempDir()}
	offered, err := h.probeOfferedAuth(node)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !offered.Password {
		t.Fatal("server offers password auth, probe reported it does not")
	}
	if n := seen.Load(); n != 0 {
		t.Fatalf("probe sent %d password attempt(s); it must never send credentials", n)
	}
}

func TestProbeReportsKeyOnlyServer(t *testing.T) {
	node, _ := startTestSSHD(t, false)
	h := &ConfigHandler{DataDir: t.TempDir()}
	offered, err := h.probeOfferedAuth(node)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if offered.Password || offered.KeyboardInteractive {
		t.Fatalf("key-only server reported as offering %+v", offered)
	}
}

func TestProbeUnreachableIsNotEvidence(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().(*net.TCPAddr)
	ln.Close() // nothing listens here now
	h := &ConfigHandler{DataDir: t.TempDir()}
	_, err := h.probeOfferedAuth(&model.Node{Host: "127.0.0.1", Port: addr.Port, SSHUser: "root"})
	if err == nil {
		t.Fatal("a refused connection must not be read as 'no password offered'")
	}
}

func TestIneffectiveSSHDByValue(t *testing.T) {
	good := parseSSHDT("port 22\npubkeyauthentication yes\npasswordauthentication no\nkbdinteractiveauthentication no\npermitrootlogin without-password\n")
	if bad := ineffectiveSSHD(good); len(bad) != 0 {
		t.Fatalf("hardened output flagged: %v", bad)
	}
	// Each value broken in turn must be caught, by value not substring:
	// "no" is a prefix of nothing here, but "yes" vs "no" and a missing key are
	// both what a stray earlier drop-in produces.
	for _, broken := range []string{
		"pubkeyauthentication yes\npasswordauthentication yes\nkbdinteractiveauthentication no\npermitrootlogin without-password\n",
		"pubkeyauthentication yes\npasswordauthentication no\nkbdinteractiveauthentication yes\npermitrootlogin without-password\n",
		"pubkeyauthentication yes\npasswordauthentication no\nkbdinteractiveauthentication no\npermitrootlogin yes\n",
		"pubkeyauthentication yes\npasswordauthentication no\npermitrootlogin without-password\n",
		// The laxcg image: keys off. Hardening must never be reported done here.
		"pubkeyauthentication no\npasswordauthentication no\nkbdinteractiveauthentication no\npermitrootlogin without-password\n",
	} {
		if bad := ineffectiveSSHD(parseSSHDT(broken)); len(bad) != 1 {
			t.Fatalf("expected exactly one ineffective key for %q, got %v", broken, bad)
		}
	}
}

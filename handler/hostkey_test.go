package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func testKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("wrap key: %v", err)
	}
	return signer
}

func testAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 22}
}

func TestHostKeyPinsOnFirstUse(t *testing.T) {
	store := newHostKeyStore(t.TempDir())
	key := testKey(t)

	if err := store.callback()("10.0.0.1:22", testAddr(), key); err != nil {
		t.Fatalf("first contact must be trusted, got %v", err)
	}
	// Second contact with the same key must still pass, now from the file.
	if err := store.callback()("10.0.0.1:22", testAddr(), key); err != nil {
		t.Fatalf("pinned key must verify, got %v", err)
	}

	data, err := os.ReadFile(filepath.Join(store.path))
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	// OpenSSH omits the default port and brackets only non-default ones, so the
	// pinned line reads "10.0.0.1", not "10.0.0.1:22". Assert the normalized
	// form so the file stays readable by ssh-keygen -R.
	if want := knownhosts.Normalize("10.0.0.1:22"); !strings.Contains(string(data), want+" ") {
		t.Fatalf("known_hosts lacks normalized host %q:\n%s", want, data)
	}
	if !strings.Contains(string(data), "ssh-ed25519") {
		t.Fatalf("known_hosts lacks the key type:\n%s", data)
	}
}

// The panel's forget() and OpenSSH's ssh-keygen -R must agree on how a host is
// written, otherwise the recovery instruction in ErrHostKeyMismatch is wrong.
func TestHostKeyNormalizationMatchesOpenSSH(t *testing.T) {
	cases := map[string]string{
		"10.0.0.1:22":   "10.0.0.1",        // default port is implicit
		"10.0.0.1:2222": "[10.0.0.1]:2222", // non-default port is bracketed
	}
	for in, want := range cases {
		if got := knownhosts.Normalize(in); got != want {
			t.Fatalf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHostKeyRejectsMismatch is the falsification test for this guard: break
// exactly what it exists to catch (a node answering with a different key) and
// prove it refuses. Without this, "the pinning code runs" would be mistaken for
// "impersonation is blocked".
func TestHostKeyRejectsMismatch(t *testing.T) {
	store := newHostKeyStore(t.TempDir())
	genuine, impostor := testKey(t), testKey(t)

	if err := store.callback()("10.0.0.1:22", testAddr(), genuine); err != nil {
		t.Fatalf("pin genuine key: %v", err)
	}

	err := store.callback()("10.0.0.1:22", testAddr(), impostor)
	if err == nil {
		t.Fatal("a different host key MUST be rejected; the guard is not working")
	}
	var mismatch *ErrHostKeyMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("want ErrHostKeyMismatch, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "ssh-keygen") {
		t.Fatalf("mismatch error must tell the operator how to recover, got: %v", err)
	}
}

func TestHostKeyDistinguishesHosts(t *testing.T) {
	store := newHostKeyStore(t.TempDir())
	keyA, keyB := testKey(t), testKey(t)

	if err := store.callback()("10.0.0.1:22", testAddr(), keyA); err != nil {
		t.Fatalf("pin host A: %v", err)
	}
	// A different host is a different pin, not a mismatch.
	if err := store.callback()("10.0.0.2:22", testAddr(), keyB); err != nil {
		t.Fatalf("second host must pin independently, got %v", err)
	}
	// The same port-less/port-ful distinction matters too.
	if err := store.callback()("10.0.0.1:2222", testAddr(), keyB); err != nil {
		t.Fatalf("different port must pin independently, got %v", err)
	}
}

func TestHostKeyForgetAllowsRepin(t *testing.T) {
	store := newHostKeyStore(t.TempDir())
	old, rebuilt := testKey(t), testKey(t)

	if err := store.callback()("10.0.0.1:22", testAddr(), old); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := store.callback()("10.0.0.1:22", testAddr(), rebuilt); err == nil {
		t.Fatal("expected mismatch before forget")
	}

	if err := store.forget("10.0.0.1:22"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if err := store.callback()("10.0.0.1:22", testAddr(), rebuilt); err != nil {
		t.Fatalf("after forget the rebuilt node must re-pin, got %v", err)
	}
}

func TestHostKeyForgetKeepsOtherHosts(t *testing.T) {
	store := newHostKeyStore(t.TempDir())
	keyA, keyB := testKey(t), testKey(t)
	store.callback()("10.0.0.1:22", testAddr(), keyA)
	store.callback()("10.0.0.2:22", testAddr(), keyB)

	if err := store.forget("10.0.0.1:22"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	// The untouched host must still verify against its original key.
	if err := store.callback()("10.0.0.2:22", testAddr(), keyB); err != nil {
		t.Fatalf("forget removed an unrelated host: %v", err)
	}
	// And must still reject an impostor.
	if err := store.callback()("10.0.0.2:22", testAddr(), keyA); err == nil {
		t.Fatal("unrelated host lost its pin after forget")
	}
}

func TestHostKeyForgetMissingFile(t *testing.T) {
	store := newHostKeyStore(t.TempDir())
	if err := store.forget("10.0.0.1:22"); err != nil {
		t.Fatalf("forget on a fresh store must be a no-op, got %v", err)
	}
}

func TestHostKeyConcurrent(t *testing.T) {
	store := newHostKeyStore(t.TempDir())
	keys := make([]ssh.PublicKey, 16)
	for i := range keys {
		keys[i] = testKey(t)
	}

	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			host := "10.0.1." + string(rune('a'+i)) + ":22"
			store.callback()(host, testAddr(), keys[i])
		}(i)
	}
	wg.Wait() // -race proves the file writes are serialized
}

func TestHostKeyFilePermissions(t *testing.T) {
	store := newHostKeyStore(t.TempDir())
	store.callback()("10.0.0.1:22", testAddr(), testKey(t))

	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("known_hosts perm = %o, want 600", perm)
	}
}

// A ConfigHandler without DataDir must not try to write pins; unit tests
// elsewhere construct it that way.
func TestConfigHandlerWithoutDataDirSkipsPinning(t *testing.T) {
	h := &ConfigHandler{}
	if h.hostKeyCallback() == nil {
		t.Fatal("callback must never be nil")
	}
	if err := h.ForgetHostKey(nil); err != nil {
		t.Fatalf("ForgetHostKey without DataDir must be a no-op, got %v", err)
	}
}

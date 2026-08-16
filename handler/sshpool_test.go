package handler

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/briqt/singbox-panel/model"
)

// A real *ssh.Client cannot be constructed without a server, so these tests
// drive the dial function and the pool's bookkeeping. The contract under test:
// get never returns (nil, nil), a failed dial is not cached, and a node whose
// connection parameters changed does not keep using the old connection.

func TestSSHPoolRejectsNilClient(t *testing.T) {
	pool := newSSHPool(func(*model.Node) (*ssh.Client, error) { return nil, nil })

	client, err := pool.get(&model.Node{ID: 1, Host: "10.0.0.1", Port: 22, SSHUser: "root"})
	if err == nil {
		t.Fatal("a dialer returning no client must produce an error, not a nil client")
	}
	if client != nil {
		t.Fatal("client must be nil when err is set")
	}
	pool.mu.Lock()
	n := len(pool.entries)
	pool.mu.Unlock()
	if n != 0 {
		t.Fatalf("pool cached %d entries after a bad dial, want 0", n)
	}
}

func TestSSHPoolPropagatesDialError(t *testing.T) {
	want := errors.New("no route to host")
	pool := newSSHPool(func(*model.Node) (*ssh.Client, error) { return nil, want })

	if _, err := pool.get(&model.Node{ID: 7}); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	pool.mu.Lock()
	n := len(pool.entries)
	pool.mu.Unlock()
	if n != 0 {
		t.Fatalf("failed dial cached %d entries, want 0", n)
	}
}

func TestSSHPoolRedialsAfterTargetChange(t *testing.T) {
	var dialed []string
	pool := newSSHPool(func(n *model.Node) (*ssh.Client, error) {
		dialed = append(dialed, sshTarget(n))
		return nil, errors.New("dial stub")
	})
	// Seed a pooled entry as if the node had been reachable at its old address.
	pool.entries[3] = &sshPoolEntry{target: "root@10.0.0.1:22"}

	moved := &model.Node{ID: 3, Host: "10.0.0.9", Port: 22, SSHUser: "root"}
	pool.get(moved)

	if len(dialed) != 1 || dialed[0] != "root@10.0.0.9:22" {
		t.Fatalf("dialed = %v, want one dial to the new address", dialed)
	}
	pool.mu.Lock()
	_, still := pool.entries[3]
	pool.mu.Unlock()
	if still {
		t.Fatal("stale entry must be dropped when the target changes")
	}
}

func TestSSHPoolDropIsIdempotent(t *testing.T) {
	pool := newSSHPool(func(*model.Node) (*ssh.Client, error) { return nil, nil })
	pool.drop(42)
	pool.drop(42) // must not panic on a node that was never pooled
	pool.Close()
	pool.Close()
}

func TestSSHPoolConcurrentGet(t *testing.T) {
	pool := newSSHPool(func(*model.Node) (*ssh.Client, error) {
		return nil, errors.New("dial stub")
	})
	node := &model.Node{ID: 5, Host: "10.0.0.5", Port: 22, SSHUser: "root"}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool.get(node)
			pool.drop(node.ID)
		}()
	}
	wg.Wait() // -race proves the locking is sound
}

func TestSSHAliveNilClient(t *testing.T) {
	if sshAlive(nil) {
		t.Fatal("sshAlive(nil) must be false")
	}
}

func TestSSHTargetDistinguishesConnections(t *testing.T) {
	base := model.Node{SSHUser: "root", Host: "10.0.0.1", Port: 22}
	seen := map[string]string{"base": sshTarget(&base)}

	otherUser := base
	otherUser.SSHUser = "briqt"
	otherHost := base
	otherHost.Host = "10.0.0.2"
	otherPort := base
	otherPort.Port = 2222

	for name, n := range map[string]model.Node{"user": otherUser, "host": otherHost, "port": otherPort} {
		if got := sshTarget(&n); got == seen["base"] {
			t.Fatalf("changing %s must change the target, got %q for both", name, got)
		}
	}

	// IPv6 literals must survive verbatim so the dialer can bracket them.
	if got := sshTarget(&model.Node{SSHUser: "root", Host: "2001:db8::1", Port: 22}); !strings.Contains(got, "2001:db8::1") {
		t.Fatalf("sshTarget lost the IPv6 host: %q", got)
	}
}

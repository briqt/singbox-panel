package handler

import (
	"errors"
	"strconv"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/briqt/singbox-panel/model"
)

// sshPool keeps one long-lived SSH connection per node for the high-frequency
// caller (the traffic poller dials every enabled node once a minute).
//
// Reconnecting each cycle is not just a wasted handshake: on the node side every
// SSH login starts a systemd user manager and every logout tears it down, so a
// one-minute poll interval writes ~26k journal entries per week per node and
// fills auth.log with successful-login noise. Holding the connection open
// removes both.
//
// Interactive, operator-triggered work (push config, install, upgrade) keeps
// using ConfigHandler.sshConnect: those run rarely and are better off with a
// fresh connection whose failure is reported straight back to the caller.
type sshPool struct {
	dial func(*model.Node) (*ssh.Client, error)

	mu      sync.Mutex
	entries map[int]*sshPoolEntry
}

type sshPoolEntry struct {
	client *ssh.Client
	// target pins the connection parameters this client was dialed with. When a
	// node is edited (new host, port, or SSH user) the cached client still points
	// at the old machine, so the fingerprint mismatch forces a redial.
	target string
}

func newSSHPool(dial func(*model.Node) (*ssh.Client, error)) *sshPool {
	return &sshPool{dial: dial, entries: make(map[int]*sshPoolEntry)}
}

func sshTarget(node *model.Node) string {
	return node.SSHUser + "@" + node.Host + ":" + strconv.Itoa(node.Port)
}

// get returns a live connection for the node, reusing the pooled one when it is
// still healthy. The caller must not Close the returned client; call drop on
// failure so the next get redials.
func (p *sshPool) get(node *model.Node) (*ssh.Client, error) {
	target := sshTarget(node)

	p.mu.Lock()
	entry, ok := p.entries[node.ID]
	p.mu.Unlock()

	if ok {
		if entry.target == target && sshAlive(entry.client) {
			return entry.client, nil
		}
		// Stale target or dead transport: discard before dialing again.
		p.drop(node.ID)
	}

	client, err := p.dial(node)
	if err != nil {
		return nil, err
	}
	// Never let a nil client into the pool: callers dereference the result, so a
	// silent nil would surface later as a panic far from the cause.
	if client == nil {
		return nil, errors.New("ssh dial returned no client")
	}

	p.mu.Lock()
	// A concurrent get may have already stored a client; keep the first one and
	// close ours so the pool never leaks a connection.
	if existing, dup := p.entries[node.ID]; dup && existing.target == target {
		p.mu.Unlock()
		client.Close()
		return existing.client, nil
	}
	p.entries[node.ID] = &sshPoolEntry{client: client, target: target}
	p.mu.Unlock()

	return client, nil
}

// drop closes and forgets the node's pooled connection.
func (p *sshPool) drop(nodeID int) {
	p.mu.Lock()
	entry, ok := p.entries[nodeID]
	delete(p.entries, nodeID)
	p.mu.Unlock()

	if ok && entry.client != nil {
		entry.client.Close()
	}
}

// Close tears the whole pool down. Used on shutdown and by tests.
func (p *sshPool) Close() {
	p.mu.Lock()
	entries := p.entries
	p.entries = make(map[int]*sshPoolEntry)
	p.mu.Unlock()

	for _, entry := range entries {
		if entry.client != nil {
			entry.client.Close()
		}
	}
}

// sshAlive probes the transport with the OpenSSH keepalive request. A dead or
// half-open connection fails here instead of surfacing as a confusing error in
// the middle of a stats query.
func sshAlive(client *ssh.Client) bool {
	if client == nil {
		return false
	}
	_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

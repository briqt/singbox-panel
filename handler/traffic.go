package handler

import (
	"log"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/briqt/singbox-panel/model"
)

const (
	// pollInterval is measured between cycle starts, not between end and start,
	// so a slow cycle does not push every later cycle further out.
	pollInterval = 60 * time.Second
	// pollConcurrency bounds how many nodes are polled at once. Each poll is one
	// gRPC round trip over an already-open SSH connection, so a small pool keeps
	// the whole fleet inside one cycle without opening a burst of channels.
	pollConcurrency = 8
	// pollTimeout caps a single node's poll. A node that is up at the TCP level
	// but wedged would otherwise hold its slot until the SSH layer gives up.
	pollTimeout = 30 * time.Second
)

type TrafficPoller struct {
	Nodes   *model.NodeStore
	Users   *model.UserStore
	Traffic *model.TrafficStore
	Config  *ConfigHandler

	pool *sshPool

	// health remembers the last outcome per node so a node that is down logs
	// once on the way down and once on the way back up, instead of once a minute
	// forever.
	healthMu sync.Mutex
	health   map[int]bool
}

func (p *TrafficPoller) Start() {
	if p.pool == nil {
		p.pool = newSSHPool(func(node *model.Node) (*ssh.Client, error) {
			return p.Config.sshConnect(node)
		})
	}
	if p.health == nil {
		p.health = make(map[int]bool)
	}
	go p.loop()
	go p.retentionLoop()
}

// Close releases the pooled SSH connections. Safe to call on a poller that was
// never started.
func (p *TrafficPoller) Close() {
	if p.pool != nil {
		p.pool.Close()
	}
}

// retentionLoop keeps usage bounded to the last RetentionMonths calendar
// months; the stats API refuses to query past that same boundary.
func (p *TrafficPoller) retentionLoop() {
	for {
		if n, err := p.Traffic.Prune(); err == nil && n > 0 {
			log.Printf("traffic: pruned %d samples recorded before %s", n, p.Traffic.RetentionStart())
		}
		time.Sleep(24 * time.Hour)
	}
}

func (p *TrafficPoller) loop() {
	time.Sleep(15 * time.Second)
	p.pollAll()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for range ticker.C {
		p.pollAll()
	}
}

func (p *TrafficPoller) pollAll() {
	nodes, err := p.Nodes.ListEnabled()
	if err != nil {
		log.Printf("traffic: list nodes: %v", err)
		return
	}

	// The user table is the same for every node, so read it once per cycle
	// instead of once per node.
	users, err := p.Users.List()
	if err != nil {
		log.Printf("traffic: list users: %v", err)
		return
	}
	nameToUser := make(map[string]int, len(users))
	for _, u := range users {
		nameToUser[u.Name] = u.ID
	}

	sem := make(chan struct{}, pollConcurrency)
	var wg sync.WaitGroup
	for _, node := range nodes {
		if node.ProxyType != "singbox" {
			continue
		}
		wg.Add(1)
		go func(node model.Node) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p.pollNodeGuarded(node, nameToUser)
		}(node)
	}
	wg.Wait()
}

// pollNodeGuarded runs one node's poll under a deadline. On timeout the pooled
// connection is dropped: the in-flight goroutine unwinds on its own once the SSH
// layer errors out, and the next cycle starts from a fresh dial.
func (p *TrafficPoller) pollNodeGuarded(node model.Node, nameToUser map[string]int) {
	done := make(chan error, 1)
	go func() { done <- p.pollNode(node, nameToUser) }()

	select {
	case err := <-done:
		p.recordHealth(node, err)
	case <-time.After(pollTimeout):
		p.pool.drop(node.ID)
		p.recordHealth(node, errPollTimeout{node.Name})
	}
}

type errPollTimeout struct{ node string }

func (e errPollTimeout) Error() string { return "poll timed out after " + pollTimeout.String() }

// recordHealth logs only on transitions so a permanently unreachable node does
// not produce one line per minute.
func (p *TrafficPoller) recordHealth(node model.Node, err error) {
	healthy := err == nil

	p.healthMu.Lock()
	prev, seen := p.health[node.ID]
	p.health[node.ID] = healthy
	p.healthMu.Unlock()

	switch {
	case !healthy && (!seen || prev):
		log.Printf("traffic: node %s unhealthy: %v", node.Name, err)
	case healthy && seen && !prev:
		log.Printf("traffic: node %s recovered", node.Name)
	}
}

// pollNode reads exact per-user uplink/downlink counters from the node's
// v2ray_api StatsService and attributes them to the matching user. Using
// reset=true, each poll returns the delta since the previous poll, so no
// baseline tracking is needed and a sing-box restart simply starts a fresh
// counter from zero.
func (p *TrafficPoller) pollNode(node model.Node, nameToUser map[string]int) error {
	client, err := p.pool.get(&node)
	if err != nil {
		return err
	}

	stats, err := queryUserStats(client, true)
	if err != nil {
		// The connection is the most likely culprit; force a redial next cycle
		// rather than retrying forever over a broken transport.
		p.pool.drop(node.ID)
		return err
	}
	if len(stats) == 0 {
		return nil
	}

	var users, up, down int64
	for name, t := range stats {
		if t.Up == 0 && t.Down == 0 {
			continue
		}
		userID, ok := nameToUser[name]
		if !ok {
			continue
		}
		p.Users.AddTraffic(userID, t.Up, t.Down)
		p.Traffic.Record(node.ID, userID, t.Up, t.Down)
		users++
		up += t.Up
		down += t.Down
	}

	// One line per node per cycle instead of one line per user per node: with a
	// handful of users on a handful of nodes the old form was the panel's own
	// largest log producer.
	if users > 0 {
		log.Printf("traffic: %s %d user(s) +%d↑ +%d↓", node.Name, users, up, down)
	}
	return nil
}

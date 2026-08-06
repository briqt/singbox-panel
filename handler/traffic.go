package handler

import (
	"log"
	"time"

	"github.com/briqt/singbox-panel/model"
)

type TrafficPoller struct {
	Nodes   *model.NodeStore
	Users   *model.UserStore
	Traffic *model.TrafficStore
	Config  *ConfigHandler
}

func (p *TrafficPoller) Start() {
	go p.loop()
	go p.retentionLoop()
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
	for {
		p.pollAll()
		time.Sleep(60 * time.Second)
	}
}

func (p *TrafficPoller) pollAll() {
	nodes, err := p.Nodes.ListEnabled()
	if err != nil {
		return
	}
	for _, node := range nodes {
		if node.ProxyType != "singbox" {
			continue
		}
		p.pollNode(node)
	}
}

// pollNode reads exact per-user uplink/downlink counters from the node's
// v2ray_api StatsService and attributes them to the matching user. Using
// reset=true, each poll returns the delta since the previous poll, so no
// baseline tracking is needed and a sing-box restart simply starts a fresh
// counter from zero.
func (p *TrafficPoller) pollNode(node model.Node) {
	client, err := p.Config.sshConnect(&node)
	if err != nil {
		return
	}
	defer client.Close()

	stats, err := queryUserStats(client, true)
	if err != nil || len(stats) == 0 {
		return
	}

	users, err := p.Users.List()
	if err != nil {
		return
	}
	nameToUser := make(map[string]int, len(users))
	for _, u := range users {
		nameToUser[u.Name] = u.ID
	}

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
		log.Printf("traffic: %s@%s +%d↑ +%d↓", name, node.Name, t.Up, t.Down)
	}
}

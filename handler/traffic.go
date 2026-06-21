package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/briqt/singbox-panel/model"
)

type TrafficPoller struct {
	Nodes  *model.NodeStore
	Users  *model.UserStore
	Config *ConfigHandler

	mu       sync.Mutex
	lastSeen map[string][2]int64 // nodeKey -> [lastUp, lastDown]
}

func (p *TrafficPoller) Start() {
	p.lastSeen = make(map[string][2]int64)
	go p.loop()
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

type clashConnsResp struct {
	UploadTotal   int64       `json:"uploadTotal"`
	DownloadTotal int64       `json:"downloadTotal"`
	Connections   []clashConn `json:"connections"`
}

type clashConn struct {
	Upload   int64             `json:"upload"`
	Download int64             `json:"download"`
	Metadata map[string]string `json:"metadata"`
}

func (p *TrafficPoller) pollNode(node model.Node) {
	client, err := p.Config.sshConnect(&node)
	if err != nil {
		return
	}
	defer client.Close()

	out, err := sshRun(client, "curl -s http://127.0.0.1:9090/connections 2>/dev/null")
	if err != nil || out == "" || out[0] != '{' {
		return
	}

	var resp clashConnsResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return
	}

	nodeKey := fmt.Sprintf("node_%d", node.ID)
	p.mu.Lock()
	prev := p.lastSeen[nodeKey]
	p.lastSeen[nodeKey] = [2]int64{resp.UploadTotal, resp.DownloadTotal}
	p.mu.Unlock()

	// First poll for this node — just record baseline
	if prev[0] == 0 && prev[1] == 0 {
		return
	}

	deltaUp := resp.UploadTotal - prev[0]
	deltaDown := resp.DownloadTotal - prev[1]

	// Negative delta = service restarted, use current total as delta
	if deltaUp < 0 {
		deltaUp = resp.UploadTotal
	}
	if deltaDown < 0 {
		deltaDown = resp.DownloadTotal
	}
	if deltaUp == 0 && deltaDown == 0 {
		return
	}

	// Count active connections per sourceIP to estimate per-user traffic
	// Map sourceIP to user via node access control
	ipConns := map[string][2]int64{} // sourceIP -> [upload, download] from active connections
	var totalConnUp, totalConnDown int64
	for _, c := range resp.Connections {
		ip := c.Metadata["sourceIP"]
		if ip == "" {
			continue
		}
		cur := ipConns[ip]
		cur[0] += c.Upload
		cur[1] += c.Download
		ipConns[ip] = cur
		totalConnUp += c.Upload
		totalConnDown += c.Download
	}

	// Get users with access to this node
	accessIDs, _ := p.Config.Access.UsersForNode(node.ID)
	if len(accessIDs) == 0 {
		return
	}

	users, _ := p.Users.ListEnabled()
	accessUsers := make([]model.User, 0)
	for _, u := range users {
		for _, aid := range accessIDs {
			if u.ID == aid {
				accessUsers = append(accessUsers, u)
				break
			}
		}
	}

	if len(accessUsers) == 0 {
		return
	}

	// Simple allocation: if only 1 user has access, all traffic goes to them.
	// If multiple users, split proportionally by active connection bytes (or equally if no connections).
	if len(accessUsers) == 1 {
		u := accessUsers[0]
		p.Users.AddTraffic(u.ID, deltaUp, deltaDown)
		p.Nodes.RecordTraffic(node.ID, u.ID, deltaUp, deltaDown)
		log.Printf("traffic: %s@%s +%d↑ +%d↓", u.Name, node.Name, deltaUp, deltaDown)
		return
	}

	// Multiple users: split delta proportionally based on connection traffic
	if totalConnUp+totalConnDown > 0 {
		for _, u := range accessUsers {
			// We can't map sourceIP to user without extra info, so split evenly
			share := int64(1)
			total := int64(len(accessUsers))
			userUp := deltaUp * share / total
			userDown := deltaDown * share / total
			if userUp > 0 || userDown > 0 {
				p.Users.AddTraffic(u.ID, userUp, userDown)
				p.Nodes.RecordTraffic(node.ID, u.ID, userUp, userDown)
			}
		}
	} else {
		// No active connections but had traffic delta — split evenly
		n := int64(len(accessUsers))
		for _, u := range accessUsers {
			p.Users.AddTraffic(u.ID, deltaUp/n, deltaDown/n)
			p.Nodes.RecordTraffic(node.ID, u.ID, deltaUp/n, deltaDown/n)
		}
	}
	log.Printf("traffic: %s total +%d↑ +%d↓ (split %d users)", node.Name, deltaUp, deltaDown, len(accessUsers))
}

package model

import (
	"database/sql"
	"encoding/json"
)

type Node struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	SSHUser    string `json:"ssh_user"`
	ProxyType  string `json:"proxy_type"`
	ConfigPath string `json:"config_path"`
	SingboxBin string `json:"singbox_bin"`
	AgentToken string `json:"agent_token"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"created_at"`
}

type NodeInbound struct {
	ID       int             `json:"id"`
	NodeID   int             `json:"node_id"`
	Tag      string          `json:"tag"`
	Protocol string          `json:"protocol"`
	Port     int             `json:"port"`
	Settings json.RawMessage `json:"settings"`
	Enabled  bool            `json:"enabled"`
}

type NodeWithInbounds struct {
	Node
	Inbounds []NodeInbound `json:"inbounds"`
}

type NodeStore struct {
	DB *sql.DB
}

func (s *NodeStore) List() ([]Node, error) {
	rows, err := s.DB.Query(`SELECT id, name, host, port, ssh_user, proxy_type, config_path, singbox_bin, agent_token, enabled, created_at FROM nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		var n Node
		var enabled int
		if err := rows.Scan(&n.ID, &n.Name, &n.Host, &n.Port, &n.SSHUser, &n.ProxyType, &n.ConfigPath, &n.SingboxBin, &n.AgentToken, &enabled, &n.CreatedAt); err != nil {
			return nil, err
		}
		n.Enabled = enabled == 1
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (s *NodeStore) Get(id int) (*Node, error) {
	var n Node
	var enabled int
	err := s.DB.QueryRow(`SELECT id, name, host, port, ssh_user, proxy_type, config_path, singbox_bin, agent_token, enabled, created_at FROM nodes WHERE id = ?`, id).
		Scan(&n.ID, &n.Name, &n.Host, &n.Port, &n.SSHUser, &n.ProxyType, &n.ConfigPath, &n.SingboxBin, &n.AgentToken, &enabled, &n.CreatedAt)
	if err != nil {
		return nil, err
	}
	n.Enabled = enabled == 1
	return &n, nil
}

func (s *NodeStore) GetWithInbounds(id int) (*NodeWithInbounds, error) {
	node, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	inbounds, err := s.ListInbounds(id)
	if err != nil {
		return nil, err
	}
	return &NodeWithInbounds{Node: *node, Inbounds: inbounds}, nil
}

type CreateNodeReq struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	SSHUser    string `json:"ssh_user"`
	ProxyType  string `json:"proxy_type"`
	ConfigPath string `json:"config_path"`
	SingboxBin string `json:"singbox_bin"`
}

func (s *NodeStore) Create(req CreateNodeReq) (*Node, error) {
	if req.Port == 0 {
		req.Port = 22
	}
	if req.SSHUser == "" {
		req.SSHUser = "root"
	}
	if req.ProxyType == "" {
		req.ProxyType = "singbox"
	}
	if req.ConfigPath == "" {
		req.ConfigPath = "/etc/v2ray-agent/sing-box/conf/config.json"
	}
	if req.SingboxBin == "" {
		req.SingboxBin = "/etc/v2ray-agent/sing-box/sing-box"
	}
	token := generateToken()
	res, err := s.DB.Exec(`INSERT INTO nodes (name, host, port, ssh_user, proxy_type, config_path, singbox_bin, agent_token) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		req.Name, req.Host, req.Port, req.SSHUser, req.ProxyType, req.ConfigPath, req.SingboxBin, token)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(int(id))
}

type UpdateNodeReq struct {
	Name       *string `json:"name"`
	Host       *string `json:"host"`
	Port       *int    `json:"port"`
	Enabled    *bool   `json:"enabled"`
	ConfigPath *string `json:"config_path"`
	SingboxBin *string `json:"singbox_bin"`
}

func (s *NodeStore) Update(id int, req UpdateNodeReq) (*Node, error) {
	if req.Name != nil {
		s.DB.Exec(`UPDATE nodes SET name = ? WHERE id = ?`, *req.Name, id)
	}
	if req.Host != nil {
		s.DB.Exec(`UPDATE nodes SET host = ? WHERE id = ?`, *req.Host, id)
	}
	if req.Port != nil {
		s.DB.Exec(`UPDATE nodes SET port = ? WHERE id = ?`, *req.Port, id)
	}
	if req.Enabled != nil {
		enabled := 0
		if *req.Enabled {
			enabled = 1
		}
		s.DB.Exec(`UPDATE nodes SET enabled = ? WHERE id = ?`, enabled, id)
	}
	if req.ConfigPath != nil {
		s.DB.Exec(`UPDATE nodes SET config_path = ? WHERE id = ?`, *req.ConfigPath, id)
	}
	if req.SingboxBin != nil {
		s.DB.Exec(`UPDATE nodes SET singbox_bin = ? WHERE id = ?`, *req.SingboxBin, id)
	}
	return s.Get(id)
}

func (s *NodeStore) Delete(id int) error {
	_, err := s.DB.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	return err
}

func (s *NodeStore) ListInbounds(nodeID int) ([]NodeInbound, error) {
	rows, err := s.DB.Query(`SELECT id, node_id, tag, protocol, port, settings, enabled FROM node_inbounds WHERE node_id = ? ORDER BY id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var inbounds []NodeInbound
	for rows.Next() {
		var ib NodeInbound
		var enabled int
		var settings string
		if err := rows.Scan(&ib.ID, &ib.NodeID, &ib.Tag, &ib.Protocol, &ib.Port, &settings, &enabled); err != nil {
			return nil, err
		}
		ib.Enabled = enabled == 1
		ib.Settings = json.RawMessage(settings)
		inbounds = append(inbounds, ib)
	}
	return inbounds, nil
}

type CreateInboundReq struct {
	Tag      string          `json:"tag"`
	Protocol string          `json:"protocol"`
	Port     int             `json:"port"`
	Settings json.RawMessage `json:"settings"`
}

func (s *NodeStore) CreateInbound(nodeID int, req CreateInboundReq) (*NodeInbound, error) {
	settings := req.Settings
	if len(settings) == 0 {
		settings = json.RawMessage("{}")
	}
	res, err := s.DB.Exec(`INSERT INTO node_inbounds (node_id, tag, protocol, port, settings) VALUES (?, ?, ?, ?, ?)`,
		nodeID, req.Tag, req.Protocol, req.Port, string(settings))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	var ib NodeInbound
	var enabled int
	var settingsStr string
	err = s.DB.QueryRow(`SELECT id, node_id, tag, protocol, port, settings, enabled FROM node_inbounds WHERE id = ?`, id).
		Scan(&ib.ID, &ib.NodeID, &ib.Tag, &ib.Protocol, &ib.Port, &settingsStr, &enabled)
	if err != nil {
		return nil, err
	}
	ib.Enabled = enabled == 1
	ib.Settings = json.RawMessage(settingsStr)
	return &ib, nil
}

func (s *NodeStore) DeleteInbound(id int) error {
	_, err := s.DB.Exec(`DELETE FROM node_inbounds WHERE id = ?`, id)
	return err
}

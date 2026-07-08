package model

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

type Node struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Domain      string `json:"domain"`
	SSHUser     string `json:"ssh_user"`
	SSHPassword string `json:"ssh_password,omitempty"`
	ProxyType   string `json:"proxy_type"`
	ConfigPath  string `json:"config_path"`
	SingboxBin  string `json:"singbox_bin"`
	AgentToken  string `json:"agent_token"`
	Enabled     bool   `json:"enabled"`
	SortOrder   int    `json:"sort_order"`
	CreatedAt   string `json:"created_at"`
}

type NodeInbound struct {
	ID        int             `json:"id"`
	NodeID    int             `json:"node_id"`
	Tag       string          `json:"tag"`
	Protocol  string          `json:"protocol"`
	Port      int             `json:"port"`
	Settings  json.RawMessage `json:"settings"`
	Enabled   bool            `json:"enabled"`
	SortOrder int             `json:"sort_order"`
}

type NodeWithInbounds struct {
	Node
	Inbounds []NodeInbound `json:"inbounds"`
}

type NodeStore struct {
	DB *sql.DB
}

func (s *NodeStore) List() ([]Node, error) {
	rows, err := s.DB.Query(`SELECT id, name, host, port, domain, ssh_user, COALESCE(ssh_password,''), proxy_type, config_path, singbox_bin, agent_token, enabled, COALESCE(sort_order,0), created_at FROM nodes ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		var n Node
		var enabled int
		if err := rows.Scan(&n.ID, &n.Name, &n.Host, &n.Port, &n.Domain, &n.SSHUser, &n.SSHPassword, &n.ProxyType, &n.ConfigPath, &n.SingboxBin, &n.AgentToken, &enabled, &n.SortOrder, &n.CreatedAt); err != nil {
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
	err := s.DB.QueryRow(`SELECT id, name, host, port, domain, ssh_user, COALESCE(ssh_password,''), proxy_type, config_path, singbox_bin, agent_token, enabled, COALESCE(sort_order,0), created_at FROM nodes WHERE id = ?`, id).
		Scan(&n.ID, &n.Name, &n.Host, &n.Port, &n.Domain, &n.SSHUser, &n.SSHPassword, &n.ProxyType, &n.ConfigPath, &n.SingboxBin, &n.AgentToken, &enabled, &n.SortOrder, &n.CreatedAt)
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
	Domain     string `json:"domain"`
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
	res, err := s.DB.Exec(`INSERT INTO nodes (name, host, port, domain, ssh_user, proxy_type, config_path, singbox_bin, agent_token) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.Name, req.Host, req.Port, req.Domain, req.SSHUser, req.ProxyType, req.ConfigPath, req.SingboxBin, token)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(int(id))
}

type UpdateNodeReq struct {
	Name        *string `json:"name"`
	Host        *string `json:"host"`
	Port        *int    `json:"port"`
	Domain      *string `json:"domain"`
	SSHPassword *string `json:"ssh_password"`
	SSHUser     *string `json:"ssh_user"`
	ProxyType   *string `json:"proxy_type"`
	Enabled     *bool   `json:"enabled"`
	ConfigPath  *string `json:"config_path"`
	SingboxBin  *string `json:"singbox_bin"`
	SortOrder   *int    `json:"sort_order"`
}

func (s *NodeStore) Update(id int, req UpdateNodeReq) (*Node, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if req.Name != nil {
		if _, err := tx.Exec(`UPDATE nodes SET name = ? WHERE id = ?`, *req.Name, id); err != nil {
			return nil, err
		}
	}
	if req.Host != nil {
		if _, err := tx.Exec(`UPDATE nodes SET host = ? WHERE id = ?`, *req.Host, id); err != nil {
			return nil, err
		}
	}
	if req.Port != nil {
		if _, err := tx.Exec(`UPDATE nodes SET port = ? WHERE id = ?`, *req.Port, id); err != nil {
			return nil, err
		}
	}
	if req.Domain != nil {
		if _, err := tx.Exec(`UPDATE nodes SET domain = ? WHERE id = ?`, *req.Domain, id); err != nil {
			return nil, err
		}
	}
	if req.SSHPassword != nil {
		if _, err := tx.Exec(`UPDATE nodes SET ssh_password = ? WHERE id = ?`, *req.SSHPassword, id); err != nil {
			return nil, err
		}
	}
	if req.SSHUser != nil {
		if _, err := tx.Exec(`UPDATE nodes SET ssh_user = ? WHERE id = ?`, *req.SSHUser, id); err != nil {
			return nil, err
		}
	}
	if req.ProxyType != nil {
		if _, err := tx.Exec(`UPDATE nodes SET proxy_type = ? WHERE id = ?`, *req.ProxyType, id); err != nil {
			return nil, err
		}
	}
	if req.Enabled != nil {
		enabled := 0
		if *req.Enabled {
			enabled = 1
		}
		if _, err := tx.Exec(`UPDATE nodes SET enabled = ? WHERE id = ?`, enabled, id); err != nil {
			return nil, err
		}
	}
	if req.ConfigPath != nil {
		if _, err := tx.Exec(`UPDATE nodes SET config_path = ? WHERE id = ?`, *req.ConfigPath, id); err != nil {
			return nil, err
		}
	}
	if req.SingboxBin != nil {
		if _, err := tx.Exec(`UPDATE nodes SET singbox_bin = ? WHERE id = ?`, *req.SingboxBin, id); err != nil {
			return nil, err
		}
	}
	if req.SortOrder != nil {
		if _, err := tx.Exec(`UPDATE nodes SET sort_order = ? WHERE id = ?`, *req.SortOrder, id); err != nil {
			return nil, err
		}
	}
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = ?`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(id)
}

func (s *NodeStore) Delete(id int) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM node_inbounds WHERE node_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_access WHERE node_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM traffic_logs WHERE node_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM nodes WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *NodeStore) ListInbounds(nodeID int) ([]NodeInbound, error) {
	rows, err := s.DB.Query(`SELECT id, node_id, tag, protocol, port, settings, enabled, COALESCE(sort_order,0) FROM node_inbounds WHERE node_id = ? ORDER BY sort_order, id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var inbounds []NodeInbound
	for rows.Next() {
		var ib NodeInbound
		var enabled int
		var settings string
		if err := rows.Scan(&ib.ID, &ib.NodeID, &ib.Tag, &ib.Protocol, &ib.Port, &settings, &enabled, &ib.SortOrder); err != nil {
			return nil, err
		}
		ib.Enabled = enabled == 1
		ib.Settings = json.RawMessage(settings)
		inbounds = append(inbounds, ib)
	}
	return inbounds, rows.Err()
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
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetInbound(int(id))
}

func (s *NodeStore) GetInbound(id int) (*NodeInbound, error) {
	var ib NodeInbound
	var enabled int
	var settingsStr string
	err := s.DB.QueryRow(`SELECT id, node_id, tag, protocol, port, settings, enabled, COALESCE(sort_order,0) FROM node_inbounds WHERE id = ?`, id).
		Scan(&ib.ID, &ib.NodeID, &ib.Tag, &ib.Protocol, &ib.Port, &settingsStr, &enabled, &ib.SortOrder)
	if err != nil {
		return nil, err
	}
	ib.Enabled = enabled == 1
	ib.Settings = json.RawMessage(settingsStr)
	return &ib, nil
}

func (s *NodeStore) UpdateInbound(id int, req CreateInboundReq) (*NodeInbound, error) {
	settings := req.Settings
	if len(settings) == 0 {
		settings = json.RawMessage("{}")
	}
	result, err := s.DB.Exec(`UPDATE node_inbounds SET tag = ?, protocol = ?, port = ?, settings = ? WHERE id = ?`,
		req.Tag, req.Protocol, req.Port, string(settings), id)
	if err != nil {
		return nil, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if updated != 1 {
		return nil, sql.ErrNoRows
	}
	return s.GetInbound(id)
}

func (s *NodeStore) RestoreInbound(inbound NodeInbound) (*NodeInbound, error) {
	enabled := 0
	if inbound.Enabled {
		enabled = 1
	}
	_, err := s.DB.Exec(`INSERT INTO node_inbounds (id, node_id, tag, protocol, port, settings, enabled, sort_order) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inbound.ID, inbound.NodeID, inbound.Tag, inbound.Protocol, inbound.Port, string(inbound.Settings), enabled, inbound.SortOrder)
	if err != nil {
		return nil, err
	}
	return s.GetInbound(inbound.ID)
}

func (s *NodeStore) DeleteInbound(id int) error {
	result, err := s.DB.Exec(`DELETE FROM node_inbounds WHERE id = ?`, id)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *NodeStore) GetByToken(token string) (*Node, error) {
	if token == "" {
		return nil, sql.ErrNoRows
	}
	var n Node
	var enabled int
	err := s.DB.QueryRow(`SELECT id, name, host, port, domain, ssh_user, COALESCE(ssh_password,''), proxy_type, config_path, singbox_bin, agent_token, enabled, COALESCE(sort_order,0), created_at FROM nodes WHERE agent_token = ?`, token).
		Scan(&n.ID, &n.Name, &n.Host, &n.Port, &n.Domain, &n.SSHUser, &n.SSHPassword, &n.ProxyType, &n.ConfigPath, &n.SingboxBin, &n.AgentToken, &enabled, &n.SortOrder, &n.CreatedAt)
	if err != nil {
		return nil, err
	}
	n.Enabled = enabled == 1
	return &n, nil
}

func (s *NodeStore) RecordTraffic(nodeID, userID int, up, down int64) {
	s.DB.Exec(`INSERT INTO traffic_logs (node_id, user_id, bytes_up, bytes_down) VALUES (?, ?, ?, ?)`,
		nodeID, userID, up, down)
}

func (s *NodeStore) GetTrafficSum(nodeID int) (up, down int64) {
	s.DB.QueryRow(`SELECT COALESCE(SUM(bytes_up),0), COALESCE(SUM(bytes_down),0) FROM traffic_logs WHERE node_id = ?`, nodeID).
		Scan(&up, &down)
	return
}

// PruneTrafficLogs deletes traffic_logs rows older than the given number of
// days. The stats history endpoint never queries beyond 90 days, so anything
// older is unreadable and only grows the database; this keeps it bounded.
func (s *NodeStore) PruneTrafficLogs(days int) (int64, error) {
	res, err := s.DB.Exec(`DELETE FROM traffic_logs WHERE recorded_at < datetime('now', ?)`, fmt.Sprintf("-%d days", days))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

type ReorderItem struct {
	ID        int `json:"id"`
	SortOrder int `json:"sort_order"`
}

func (s *NodeStore) ReorderNodes(items []ReorderItem) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		result, err := tx.Exec(`UPDATE nodes SET sort_order = ? WHERE id = ?`, item.SortOrder, item.ID)
		if err != nil {
			return err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if updated != 1 {
			return sql.ErrNoRows
		}
	}
	return tx.Commit()
}

func (s *NodeStore) ReorderInbounds(nodeID int, items []ReorderItem) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		result, err := tx.Exec(`UPDATE node_inbounds SET sort_order = ? WHERE id = ? AND node_id = ?`, item.SortOrder, item.ID, nodeID)
		if err != nil {
			return err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if updated != 1 {
			return sql.ErrNoRows
		}
	}
	return tx.Commit()
}

func (s *NodeStore) ListEnabled() ([]Node, error) {
	rows, err := s.DB.Query(`SELECT id, name, host, port, domain, ssh_user, COALESCE(ssh_password,''), proxy_type, config_path, singbox_bin, agent_token, enabled, COALESCE(sort_order,0), created_at FROM nodes WHERE enabled = 1 ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		var n Node
		var enabled int
		if err := rows.Scan(&n.ID, &n.Name, &n.Host, &n.Port, &n.Domain, &n.SSHUser, &n.SSHPassword, &n.ProxyType, &n.ConfigPath, &n.SingboxBin, &n.AgentToken, &enabled, &n.SortOrder, &n.CreatedAt); err != nil {
			return nil, err
		}
		n.Enabled = enabled == 1
		nodes = append(nodes, n)
	}
	return nodes, nil
}

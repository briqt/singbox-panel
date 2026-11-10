package model

import (
	"database/sql"
	"fmt"
	"sort"
)

type AccessStore struct {
	DB *sql.DB
}

// Grant 是兼容接口，仅授权节点当前已有的入站。
// 它不创建节点级授权，未来新增的入站不会继承权限。
func (s *AccessStore) Grant(userID, nodeID int) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateAccessUser(tx, userID); err != nil {
		return err
	}
	inboundIDs, err := inboundIDsForNodes(tx, []int{nodeID})
	if err != nil {
		return err
	}
	for _, id := range inboundIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO user_inbound_access (user_id, inbound_id) VALUES (?, ?)`, userID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GrantAll 显式授权所有已有入站的快照，不包含未来新增的入站。
func (s *AccessStore) GrantAll(userID int) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateAccessUser(tx, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO user_inbound_access (user_id, inbound_id) SELECT ?, id FROM node_inbounds`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *AccessStore) GrantInbound(userID, inboundID int) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateAccessUser(tx, userID); err != nil {
		return err
	}
	if err := validateAccessInbounds(tx, []int{inboundID}); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO user_inbound_access (user_id, inbound_id) VALUES (?, ?)`, userID, inboundID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *AccessStore) Revoke(userID, nodeID int) error {
	_, err := s.DB.Exec(`DELETE FROM user_inbound_access WHERE user_id = ? AND inbound_id IN (SELECT id FROM node_inbounds WHERE node_id = ?)`, userID, nodeID)
	return err
}

func (s *AccessStore) RevokeInbound(userID, inboundID int) error {
	_, err := s.DB.Exec(`DELETE FROM user_inbound_access WHERE user_id = ? AND inbound_id = ?`, userID, inboundID)
	return err
}

func (s *AccessStore) RevokeAll(userID int) error {
	_, err := s.DB.Exec(`DELETE FROM user_inbound_access WHERE user_id = ?`, userID)
	return err
}

// Replace 是兼容接口，在替换用户授权的同一事务内，
// 为所选节点当前已有的入站生成授权快照。
func (s *AccessStore) Replace(userID int, nodeIDs []int) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	inboundIDs, err := inboundIDsForNodes(tx, nodeIDs)
	if err != nil {
		return err
	}
	if err := replaceInboundAccess(tx, userID, inboundIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *AccessStore) ReplaceInbounds(userID int, inboundIDs []int) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceInboundAccess(tx, userID, inboundIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceInboundAccess(tx *sql.Tx, userID int, inboundIDs []int) error {
	if err := validateAccessUser(tx, userID); err != nil {
		return err
	}
	if err := validateAccessInbounds(tx, inboundIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_inbound_access WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, id := range uniqueSortedIDs(inboundIDs) {
		if _, err := tx.Exec(`INSERT INTO user_inbound_access (user_id, inbound_id) VALUES (?, ?)`, userID, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *AccessStore) ListInboundIDs(userID int) ([]int, error) {
	rows, err := s.DB.Query(`SELECT inbound_id FROM user_inbound_access WHERE user_id = ? ORDER BY inbound_id`, userID)
	if err != nil {
		return nil, err
	}
	return scanAccessIDs(rows)
}

// ListNodeIDs 从入站授权推导节点列表，不能用作鉴权。
func (s *AccessStore) ListNodeIDs(userID int) ([]int, error) {
	rows, err := s.DB.Query(`SELECT DISTINCT ib.node_id FROM user_inbound_access ua JOIN node_inbounds ib ON ib.id = ua.inbound_id WHERE ua.user_id = ? ORDER BY ib.node_id`, userID)
	if err != nil {
		return nil, err
	}
	return scanAccessIDs(rows)
}

// NodeIDsForInbounds 将精确的入站变更集合映射为需要同步的节点。
// 入站不存在时必须报错，不能静默跳过同步。
func (s *AccessStore) NodeIDsForInbounds(inboundIDs []int) ([]int, error) {
	var nodeIDs []int
	for _, id := range uniqueSortedIDs(inboundIDs) {
		var nodeID int
		if err := s.DB.QueryRow(`SELECT node_id FROM node_inbounds WHERE id = ?`, id).Scan(&nodeID); err != nil {
			return nil, fmt.Errorf("node for inbound %d: %w", id, err)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	return uniqueSortedIDs(nodeIDs), nil
}

// HasAccess 保留旧的节点级信息查询接口。
// 服务端鉴权必须改用 UsersForInbound 或 ListInboundIDs。
func (s *AccessStore) HasAccess(userID, nodeID int) bool {
	var count int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM user_inbound_access ua JOIN node_inbounds ib ON ib.id = ua.inbound_id WHERE ua.user_id = ? AND ib.node_id = ?`, userID, nodeID).Scan(&count)
	return err == nil && count > 0
}

func (s *AccessStore) UsersForInbound(inboundID int) ([]int, error) {
	rows, err := s.DB.Query(`SELECT u.id FROM user_inbound_access ua JOIN users u ON ua.user_id = u.id WHERE ua.inbound_id = ? AND u.enabled = 1 AND (u.expire_at = '' OR u.expire_at > datetime('now')) AND (u.traffic_limit_bytes = 0 OR u.traffic_used_bytes < u.traffic_limit_bytes) ORDER BY u.id`, inboundID)
	if err != nil {
		return nil, err
	}
	return scanAccessIDs(rows)
}

// UsersForNode 仅供兼容性信息查询，不能用它填充节点全部入站的用户列表，
// 因为每条入站都有独立的授权。
func (s *AccessStore) UsersForNode(nodeID int) ([]int, error) {
	rows, err := s.DB.Query(`SELECT DISTINCT u.id FROM user_inbound_access ua JOIN node_inbounds ib ON ib.id = ua.inbound_id JOIN users u ON ua.user_id = u.id WHERE ib.node_id = ? AND u.enabled = 1 AND (u.expire_at = '' OR u.expire_at > datetime('now')) AND (u.traffic_limit_bytes = 0 OR u.traffic_used_bytes < u.traffic_limit_bytes) ORDER BY u.id`, nodeID)
	if err != nil {
		return nil, err
	}
	return scanAccessIDs(rows)
}

func (s *AccessStore) CountForNode(nodeID int) (int, error) {
	var count int
	err := s.DB.QueryRow(`SELECT COUNT(DISTINCT ua.user_id) FROM user_inbound_access ua JOIN node_inbounds ib ON ib.id = ua.inbound_id WHERE ib.node_id = ?`, nodeID).Scan(&count)
	return count, err
}

func scanAccessIDs(rows *sql.Rows) ([]int, error) {
	defer rows.Close()
	ids := []int{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func validateAccessUser(tx *sql.Tx, userID int) error {
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("user %d not found", userID)
	}
	return nil
}

func validateAccessInbounds(tx *sql.Tx, inboundIDs []int) error {
	for _, id := range inboundIDs {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM node_inbounds WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if id <= 0 || exists == 0 {
			return fmt.Errorf("inbound %d not found", id)
		}
	}
	return nil
}

func inboundIDsForNodes(tx *sql.Tx, nodeIDs []int) ([]int, error) {
	var inboundIDs []int
	for _, id := range nodeIDs {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = ?`, id).Scan(&exists); err != nil {
			return nil, err
		}
		if id <= 0 || exists == 0 {
			return nil, fmt.Errorf("node %d not found", id)
		}
	}
	for _, id := range uniqueSortedIDs(nodeIDs) {
		rows, err := tx.Query(`SELECT id FROM node_inbounds WHERE node_id = ? ORDER BY id`, id)
		if err != nil {
			return nil, err
		}
		ids, err := scanAccessIDs(rows)
		if err != nil {
			return nil, err
		}
		inboundIDs = append(inboundIDs, ids...)
	}
	return uniqueSortedIDs(inboundIDs), nil
}

func uniqueSortedIDs(ids []int) []int {
	seen := make(map[int]struct{}, len(ids))
	result := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Ints(result)
	return result
}

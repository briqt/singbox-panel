package model

import (
	"database/sql"
	"fmt"
	"sort"
)

type AccessStore struct {
	DB *sql.DB
}

func (s *AccessStore) Grant(userID, nodeID int) error {
	if err := s.validateUserAndNode(userID, nodeID); err != nil {
		return err
	}
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO user_access (user_id, node_id) VALUES (?, ?)`, userID, nodeID)
	return err
}

func (s *AccessStore) GrantAll(userID int) error {
	if err := s.validateUser(userID); err != nil {
		return err
	}
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO user_access (user_id, node_id) SELECT ?, id FROM nodes`, userID)
	return err
}

func (s *AccessStore) Revoke(userID, nodeID int) error {
	_, err := s.DB.Exec(`DELETE FROM user_access WHERE user_id = ? AND node_id = ?`, userID, nodeID)
	return err
}

func (s *AccessStore) RevokeAll(userID int) error {
	_, err := s.DB.Exec(`DELETE FROM user_access WHERE user_id = ?`, userID)
	return err
}

// Replace atomically replaces all node assignments for a user.
func (s *AccessStore) Replace(userID int, nodeIDs []int) error {
	if err := s.validateUser(userID); err != nil {
		return err
	}
	nodeIDs = uniqueSortedIDs(nodeIDs)

	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, nodeID := range nodeIDs {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = ?`, nodeID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("node %d not found", nodeID)
		}
	}
	if _, err := tx.Exec(`DELETE FROM user_access WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, nodeID := range nodeIDs {
		if _, err := tx.Exec(`INSERT INTO user_access (user_id, node_id) VALUES (?, ?)`, userID, nodeID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *AccessStore) ListNodeIDs(userID int) ([]int, error) {
	rows, err := s.DB.Query(`SELECT node_id FROM user_access WHERE user_id = ? ORDER BY node_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *AccessStore) HasAccess(userID, nodeID int) bool {
	var count int
	s.DB.QueryRow(`SELECT COUNT(*) FROM user_access WHERE user_id = ? AND node_id = ?`, userID, nodeID).Scan(&count)
	return count > 0
}

func (s *AccessStore) UsersForNode(nodeID int) ([]int, error) {
	rows, err := s.DB.Query(`SELECT user_id FROM user_access ua JOIN users u ON ua.user_id = u.id WHERE ua.node_id = ? AND u.enabled = 1 AND (u.expire_at = '' OR u.expire_at > datetime('now')) AND (u.traffic_limit_bytes = 0 OR u.traffic_used_bytes < u.traffic_limit_bytes)`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *AccessStore) CountForNode(nodeID int) (int, error) {
	var count int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM user_access WHERE node_id = ?`, nodeID).Scan(&count)
	return count, err
}

func (s *AccessStore) validateUser(userID int) error {
	var exists int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("user %d not found", userID)
	}
	return nil
}

func (s *AccessStore) validateUserAndNode(userID, nodeID int) error {
	if err := s.validateUser(userID); err != nil {
		return err
	}
	var exists int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = ?`, nodeID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("node %d not found", nodeID)
	}
	return nil
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

package model

import "database/sql"

type AccessStore struct {
	DB *sql.DB
}

func (s *AccessStore) Grant(userID, nodeID int) error {
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO user_access (user_id, node_id) VALUES (?, ?)`, userID, nodeID)
	return err
}

func (s *AccessStore) GrantAll(userID int) error {
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

func (s *AccessStore) ListNodeIDs(userID int) ([]int, error) {
	rows, err := s.DB.Query(`SELECT node_id FROM user_access WHERE user_id = ?`, userID)
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
	return ids, nil
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
	return ids, nil
}

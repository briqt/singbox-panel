package model

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	UUID              string `json:"uuid"`
	SubToken          string `json:"sub_token"`
	Password          string `json:"-"`
	Enabled           bool   `json:"enabled"`
	TrafficLimitBytes int64  `json:"traffic_limit_bytes"`
	TrafficUsedBytes  int64  `json:"traffic_used_bytes"`
	TrafficUpBytes    int64  `json:"traffic_up_bytes"`
	TrafficDownBytes  int64  `json:"traffic_down_bytes"`
	TrafficResetDay   int    `json:"traffic_reset_day"`
	TrafficLastReset  string `json:"traffic_last_reset,omitempty"`
	ExpireAt          string `json:"expire_at"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type UserStore struct {
	DB *sql.DB
}

type userUpdateExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func (s *UserStore) List() ([]User, error) {
	rows, err := s.DB.Query(`SELECT id, name, uuid, sub_token, enabled, traffic_limit_bytes, traffic_used_bytes, COALESCE(traffic_up_bytes,0), COALESCE(traffic_down_bytes,0), COALESCE(traffic_reset_day,0), COALESCE(traffic_last_reset,''), expire_at, created_at, updated_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var enabled int
		if err := rows.Scan(&u.ID, &u.Name, &u.UUID, &u.SubToken, &enabled, &u.TrafficLimitBytes, &u.TrafficUsedBytes, &u.TrafficUpBytes, &u.TrafficDownBytes, &u.TrafficResetDay, &u.TrafficLastReset, &u.ExpireAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.Enabled = enabled == 1
		users = append(users, u)
	}
	return users, nil
}

func (s *UserStore) Get(id int) (*User, error) {
	var u User
	var enabled int
	err := s.DB.QueryRow(`SELECT id, name, uuid, sub_token, enabled, traffic_limit_bytes, traffic_used_bytes, COALESCE(traffic_up_bytes,0), COALESCE(traffic_down_bytes,0), COALESCE(traffic_reset_day,0), COALESCE(traffic_last_reset,''), expire_at, created_at, updated_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Name, &u.UUID, &u.SubToken, &enabled, &u.TrafficLimitBytes, &u.TrafficUsedBytes, &u.TrafficUpBytes, &u.TrafficDownBytes, &u.TrafficResetDay, &u.TrafficLastReset, &u.ExpireAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	u.Enabled = enabled == 1
	return &u, nil
}

func (s *UserStore) GetBySubToken(token string) (*User, error) {
	var u User
	var enabled int
	err := s.DB.QueryRow(`SELECT id, name, uuid, sub_token, enabled, traffic_limit_bytes, traffic_used_bytes, COALESCE(traffic_up_bytes,0), COALESCE(traffic_down_bytes,0), COALESCE(traffic_reset_day,0), COALESCE(traffic_last_reset,''), expire_at, created_at, updated_at FROM users WHERE sub_token = ?`, token).
		Scan(&u.ID, &u.Name, &u.UUID, &u.SubToken, &enabled, &u.TrafficLimitBytes, &u.TrafficUsedBytes, &u.TrafficUpBytes, &u.TrafficDownBytes, &u.TrafficResetDay, &u.TrafficLastReset, &u.ExpireAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	u.Enabled = enabled == 1
	return &u, nil
}

func (s *UserStore) GetByUsername(username string) (*User, error) {
	var u User
	var enabled int
	err := s.DB.QueryRow(`SELECT id, name, uuid, sub_token, COALESCE(password,''), enabled, traffic_limit_bytes, traffic_used_bytes, COALESCE(traffic_up_bytes,0), COALESCE(traffic_down_bytes,0), COALESCE(traffic_reset_day,0), COALESCE(traffic_last_reset,''), expire_at, created_at, updated_at FROM users WHERE name = ?`, username).
		Scan(&u.ID, &u.Name, &u.UUID, &u.SubToken, &u.Password, &enabled, &u.TrafficLimitBytes, &u.TrafficUsedBytes, &u.TrafficUpBytes, &u.TrafficDownBytes, &u.TrafficResetDay, &u.TrafficLastReset, &u.ExpireAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	u.Enabled = enabled == 1
	return &u, nil
}

type CreateUserReq struct {
	Name              string `json:"name"`
	UUID              string `json:"uuid"`
	TrafficLimitBytes int64  `json:"traffic_limit_bytes"`
	ExpireAt          string `json:"expire_at"`
}

func (s *UserStore) Create(req CreateUserReq) (*User, error) {
	if req.UUID == "" {
		req.UUID = uuid.NewString()
	}
	subToken := generateToken()
	now := time.Now().UTC().Format(time.DateTime)
	res, err := s.DB.Exec(`INSERT INTO users (name, uuid, sub_token, traffic_limit_bytes, traffic_reset_day, expire_at, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
		req.Name, req.UUID, subToken, req.TrafficLimitBytes, req.ExpireAt, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(int(id))
}

func (s *UserStore) CreateWithPassword(username, passwordHash string) (*User, error) {
	uid := uuid.NewString()
	subToken := generateToken()
	now := time.Now().UTC().Format(time.DateTime)
	res, err := s.DB.Exec(`INSERT INTO users (name, uuid, sub_token, password, traffic_reset_day, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, ?, ?)`,
		username, uid, subToken, passwordHash, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(int(id))
}

type UpdateUserReq struct {
	Name              *string `json:"name"`
	Enabled           *bool   `json:"enabled"`
	TrafficLimitBytes *int64  `json:"traffic_limit_bytes"`
	TrafficResetDay   *int    `json:"traffic_reset_day"`
	ExpireAt          *string `json:"expire_at"`
}

func (s *UserStore) Update(id int, req UpdateUserReq) (*User, error) {
	if err := applyUserUpdate(s.DB, id, req); err != nil {
		return nil, err
	}
	return s.Get(id)
}

// UpdateWithAccess updates user properties and node assignments in one
// transaction, preventing a partially saved edit.
func (s *UserStore) UpdateWithAccess(id int, req UpdateUserReq, nodeIDs []int) (*User, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, sql.ErrNoRows
	}

	nodeIDs = uniqueSortedIDs(nodeIDs)
	for _, nodeID := range nodeIDs {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = ?`, nodeID).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			return nil, fmt.Errorf("node %d not found", nodeID)
		}
	}
	if err := applyUserUpdate(tx, id, req); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM user_access WHERE user_id = ?`, id); err != nil {
		return nil, err
	}
	for _, nodeID := range nodeIDs {
		if _, err := tx.Exec(`INSERT INTO user_access (user_id, node_id) VALUES (?, ?)`, id, nodeID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(id)
}

func applyUserUpdate(exec userUpdateExecer, id int, req UpdateUserReq) error {
	now := time.Now().UTC().Format(time.DateTime)
	if req.Name != nil {
		if _, err := exec.Exec(`UPDATE users SET name = ?, updated_at = ? WHERE id = ?`, *req.Name, now, id); err != nil {
			return err
		}
	}
	if req.Enabled != nil {
		enabled := 0
		if *req.Enabled {
			enabled = 1
		}
		if _, err := exec.Exec(`UPDATE users SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, now, id); err != nil {
			return err
		}
	}
	if req.TrafficLimitBytes != nil {
		if _, err := exec.Exec(`UPDATE users SET traffic_limit_bytes = ?, updated_at = ? WHERE id = ?`, *req.TrafficLimitBytes, now, id); err != nil {
			return err
		}
	}
	if req.TrafficResetDay != nil {
		if _, err := exec.Exec(`UPDATE users SET traffic_reset_day = ?, updated_at = ? WHERE id = ?`, *req.TrafficResetDay, now, id); err != nil {
			return err
		}
	}
	if req.ExpireAt != nil {
		if _, err := exec.Exec(`UPDATE users SET expire_at = ?, updated_at = ? WHERE id = ?`, *req.ExpireAt, now, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *UserStore) Delete(id int) error {
	_, err := s.DB.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

func (s *UserStore) ListEnabled() ([]User, error) {
	rows, err := s.DB.Query(`SELECT id, name, uuid, sub_token, enabled, traffic_limit_bytes, traffic_used_bytes, COALESCE(traffic_up_bytes,0), COALESCE(traffic_down_bytes,0), COALESCE(traffic_reset_day,0), COALESCE(traffic_last_reset,''), expire_at, created_at, updated_at FROM users WHERE enabled = 1 AND (expire_at = '' OR expire_at > datetime('now')) ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var enabled int
		if err := rows.Scan(&u.ID, &u.Name, &u.UUID, &u.SubToken, &enabled, &u.TrafficLimitBytes, &u.TrafficUsedBytes, &u.TrafficUpBytes, &u.TrafficDownBytes, &u.TrafficResetDay, &u.TrafficLastReset, &u.ExpireAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.Enabled = enabled == 1
		users = append(users, u)
	}
	return users, nil
}

func (s *UserStore) AddTraffic(userID int, up, down int64) {
	s.DB.Exec(`UPDATE users SET traffic_used_bytes = traffic_used_bytes + ?, traffic_up_bytes = traffic_up_bytes + ?, traffic_down_bytes = traffic_down_bytes + ?, updated_at = datetime('now') WHERE id = ?`, up+down, up, down, userID)
}

func (s *UserStore) ResetTraffic(userID int) error {
	_, err := s.DB.Exec(`UPDATE users SET traffic_used_bytes = 0, traffic_up_bytes = 0, traffic_down_bytes = 0, updated_at = datetime('now') WHERE id = ?`, userID)
	return err
}

func (s *UserStore) ResetSubToken(userID int) (string, error) {
	token := generateToken()
	_, err := s.DB.Exec(`UPDATE users SET sub_token = ?, updated_at = datetime('now') WHERE id = ?`, token, userID)
	return token, err
}

// CheckTrafficReset performs lazy reset: if user has a reset_day configured
// and the current month's reset date has passed without a reset, clear traffic.
func (s *UserStore) CheckTrafficReset(u *User) {
	if u.TrafficResetDay <= 0 || u.TrafficResetDay > 28 {
		return
	}
	now := time.Now().UTC()
	resetDate := time.Date(now.Year(), now.Month(), u.TrafficResetDay, 0, 0, 0, 0, time.UTC)
	if now.Before(resetDate) {
		// Haven't reached reset day this month yet — check previous month
		resetDate = resetDate.AddDate(0, -1, 0)
	}
	expected := resetDate.Format("2006-01-02")
	if u.TrafficLastReset >= expected {
		return
	}
	// Reset needed
	s.DB.Exec(`UPDATE users SET traffic_used_bytes = 0, traffic_up_bytes = 0, traffic_down_bytes = 0, traffic_last_reset = ?, updated_at = datetime('now') WHERE id = ?`, expected, u.ID)
	u.TrafficUsedBytes = 0
	u.TrafficUpBytes = 0
	u.TrafficDownBytes = 0
	u.TrafficLastReset = expected
}

func (s *UserStore) IsActive(u *User) bool {
	if !u.Enabled {
		return false
	}
	if u.ExpireAt != "" {
		now := time.Now().UTC().Format(time.DateTime)
		if u.ExpireAt < now {
			return false
		}
	}
	if u.TrafficLimitBytes > 0 && u.TrafficUsedBytes >= u.TrafficLimitBytes {
		return false
	}
	return true
}

func generateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

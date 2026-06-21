package model

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	UUID              string `json:"uuid"`
	SubToken          string `json:"sub_token"`
	Enabled           bool   `json:"enabled"`
	TrafficLimitBytes int64  `json:"traffic_limit_bytes"`
	TrafficUsedBytes  int64  `json:"traffic_used_bytes"`
	ExpireAt          string `json:"expire_at"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type UserStore struct {
	DB *sql.DB
}

func (s *UserStore) List() ([]User, error) {
	rows, err := s.DB.Query(`SELECT id, name, uuid, sub_token, enabled, traffic_limit_bytes, traffic_used_bytes, expire_at, created_at, updated_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var enabled int
		if err := rows.Scan(&u.ID, &u.Name, &u.UUID, &u.SubToken, &enabled, &u.TrafficLimitBytes, &u.TrafficUsedBytes, &u.ExpireAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
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
	err := s.DB.QueryRow(`SELECT id, name, uuid, sub_token, enabled, traffic_limit_bytes, traffic_used_bytes, expire_at, created_at, updated_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Name, &u.UUID, &u.SubToken, &enabled, &u.TrafficLimitBytes, &u.TrafficUsedBytes, &u.ExpireAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	u.Enabled = enabled == 1
	return &u, nil
}

func (s *UserStore) GetBySubToken(token string) (*User, error) {
	var u User
	var enabled int
	err := s.DB.QueryRow(`SELECT id, name, uuid, sub_token, enabled, traffic_limit_bytes, traffic_used_bytes, expire_at, created_at, updated_at FROM users WHERE sub_token = ?`, token).
		Scan(&u.ID, &u.Name, &u.UUID, &u.SubToken, &enabled, &u.TrafficLimitBytes, &u.TrafficUsedBytes, &u.ExpireAt, &u.CreatedAt, &u.UpdatedAt)
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
	res, err := s.DB.Exec(`INSERT INTO users (name, uuid, sub_token, traffic_limit_bytes, expire_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
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
	res, err := s.DB.Exec(`INSERT INTO users (name, uuid, sub_token, password, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, ?)`,
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
	ExpireAt          *string `json:"expire_at"`
}

func (s *UserStore) Update(id int, req UpdateUserReq) (*User, error) {
	now := time.Now().UTC().Format(time.DateTime)
	if req.Name != nil {
		s.DB.Exec(`UPDATE users SET name = ?, updated_at = ? WHERE id = ?`, *req.Name, now, id)
	}
	if req.Enabled != nil {
		enabled := 0
		if *req.Enabled {
			enabled = 1
		}
		s.DB.Exec(`UPDATE users SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, now, id)
	}
	if req.TrafficLimitBytes != nil {
		s.DB.Exec(`UPDATE users SET traffic_limit_bytes = ?, updated_at = ? WHERE id = ?`, *req.TrafficLimitBytes, now, id)
	}
	if req.ExpireAt != nil {
		s.DB.Exec(`UPDATE users SET expire_at = ?, updated_at = ? WHERE id = ?`, *req.ExpireAt, now, id)
	}
	return s.Get(id)
}

func (s *UserStore) Delete(id int) error {
	_, err := s.DB.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

func (s *UserStore) ListEnabled() ([]User, error) {
	rows, err := s.DB.Query(`SELECT id, name, uuid, sub_token, enabled, traffic_limit_bytes, traffic_used_bytes, expire_at, created_at, updated_at FROM users WHERE enabled = 1 AND (expire_at = '' OR expire_at > datetime('now')) ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var enabled int
		if err := rows.Scan(&u.ID, &u.Name, &u.UUID, &u.SubToken, &enabled, &u.TrafficLimitBytes, &u.TrafficUsedBytes, &u.ExpireAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.Enabled = enabled == 1
		users = append(users, u)
	}
	return users, nil
}

func (s *UserStore) AddTraffic(userID int, bytes int64) {
	s.DB.Exec(`UPDATE users SET traffic_used_bytes = traffic_used_bytes + ?, updated_at = datetime('now') WHERE id = ?`, bytes, userID)
}

func (s *UserStore) ResetTraffic(userID int) {
	s.DB.Exec(`UPDATE users SET traffic_used_bytes = 0, updated_at = datetime('now') WHERE id = ?`, userID)
}

func (s *UserStore) ResetSubToken(userID int) (string, error) {
	token := generateToken()
	_, err := s.DB.Exec(`UPDATE users SET sub_token = ?, updated_at = datetime('now') WHERE id = ?`, token, userID)
	return token, err
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

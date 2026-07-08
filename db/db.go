package db

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func Open(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "panel.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	db.Exec(`ALTER TABLE nodes ADD COLUMN domain TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE users ADD COLUMN password TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE nodes ADD COLUMN ssh_password TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE users ADD COLUMN traffic_reset_day INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE users ADD COLUMN traffic_last_reset TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE nodes ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE node_inbounds ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE users ADD COLUMN traffic_up_bytes INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE users ADD COLUMN traffic_down_bytes INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE users ADD COLUMN note TEXT NOT NULL DEFAULT ''`)
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	uuid TEXT NOT NULL UNIQUE,
	sub_token TEXT NOT NULL UNIQUE,
	enabled INTEGER NOT NULL DEFAULT 1,
	traffic_limit_bytes INTEGER NOT NULL DEFAULT 0,
	traffic_used_bytes INTEGER NOT NULL DEFAULT 0,
	expire_at TEXT DEFAULT '',
	created_at TEXT DEFAULT (datetime('now')),
	updated_at TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS nodes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	host TEXT NOT NULL,
	port INTEGER NOT NULL DEFAULT 22,
	domain TEXT NOT NULL DEFAULT '',
	ssh_user TEXT NOT NULL DEFAULT 'root',
	proxy_type TEXT NOT NULL DEFAULT 'singbox',
	config_path TEXT NOT NULL DEFAULT '/etc/v2ray-agent/sing-box/conf/config.json',
	singbox_bin TEXT NOT NULL DEFAULT '/etc/v2ray-agent/sing-box/sing-box',
	agent_token TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS node_inbounds (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	node_id INTEGER NOT NULL,
	tag TEXT NOT NULL DEFAULT '',
	protocol TEXT NOT NULL,
	port INTEGER NOT NULL,
	settings TEXT NOT NULL DEFAULT '{}',
	enabled INTEGER NOT NULL DEFAULT 1,
	FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS traffic_logs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	node_id INTEGER NOT NULL,
	bytes_up INTEGER NOT NULL DEFAULT 0,
	bytes_down INTEGER NOT NULL DEFAULT 0,
	recorded_at TEXT DEFAULT (datetime('now')),
	FOREIGN KEY (user_id) REFERENCES users(id),
	FOREIGN KEY (node_id) REFERENCES nodes(id)
);

CREATE INDEX IF NOT EXISTS idx_traffic_user ON traffic_logs(user_id, recorded_at);
CREATE INDEX IF NOT EXISTS idx_traffic_node ON traffic_logs(node_id, recorded_at);

CREATE TABLE IF NOT EXISTS user_access (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	node_id INTEGER NOT NULL,
	UNIQUE(user_id, node_id),
	FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
	FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);
`

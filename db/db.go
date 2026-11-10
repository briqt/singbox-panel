package db

import (
	"database/sql"
	"fmt"
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

const inboundAccessMigration = "user_inbound_access_v1"

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schema); err != nil {
		return err
	}
	for _, column := range []struct {
		table, name, definition string
	}{
		{"nodes", "domain", "TEXT NOT NULL DEFAULT ''"},
		{"users", "password", "TEXT NOT NULL DEFAULT ''"},
		{"nodes", "ssh_password", "TEXT NOT NULL DEFAULT ''"},
		{"users", "traffic_reset_day", "INTEGER NOT NULL DEFAULT 0"},
		{"users", "traffic_last_reset", "TEXT NOT NULL DEFAULT ''"},
		{"nodes", "sort_order", "INTEGER NOT NULL DEFAULT 0"},
		{"node_inbounds", "sort_order", "INTEGER NOT NULL DEFAULT 0"},
		{"users", "traffic_up_bytes", "INTEGER NOT NULL DEFAULT 0"},
		{"users", "traffic_down_bytes", "INTEGER NOT NULL DEFAULT 0"},
		{"users", "note", "TEXT NOT NULL DEFAULT ''"},
		{"node_inbounds", "name", "TEXT NOT NULL DEFAULT ''"},
		{"node_inbounds", "outbound", "TEXT NOT NULL DEFAULT '{}'"},
		{"node_inbounds", "custom", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := addColumnIfMissing(tx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}

	var migrated int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, inboundAccessMigration).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 0 {
		// 仅迁移一次当前入站的授权快照，包含已禁用的入站。此后不得读取
		// user_access 补授权，否则会复活已撤销的权限，或把权限静默授予未来新建的入站。
		if _, err := tx.Exec(`INSERT OR IGNORE INTO user_inbound_access (user_id, inbound_id)
			SELECT ua.user_id, ib.id FROM user_access ua
			JOIN users u ON u.id = ua.user_id
			JOIN node_inbounds ib ON ib.node_id = ua.node_id`); err != nil {
			return fmt.Errorf("migrate inbound access: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name) VALUES (?)`, inboundAccessMigration); err != nil {
			return fmt.Errorf("mark inbound access migration: %w", err)
		}
	}
	return tx.Commit()
}

// 先检查表结构，而不是忽略 ALTER 的错误：重启时列已存在属于正常情况，
// 数据库只读或其他迁移失败则必须报错。
func addColumnIfMissing(tx *sql.Tx, table, name, definition string) error {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var columnName, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		found = found || columnName == name
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, name, err)
	}
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
	config_path TEXT NOT NULL DEFAULT '/etc/sing-box/config.json',
	singbox_bin TEXT NOT NULL DEFAULT '/usr/local/bin/sing-box',
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
CREATE INDEX IF NOT EXISTS idx_traffic_recorded ON traffic_logs(recorded_at);

CREATE TABLE IF NOT EXISTS user_access (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	node_id INTEGER NOT NULL,
	UNIQUE(user_id, node_id),
	FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
	FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS user_inbound_access (
	user_id INTEGER NOT NULL,
	inbound_id INTEGER NOT NULL,
	UNIQUE(user_id, inbound_id),
	FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
	FOREIGN KEY (inbound_id) REFERENCES node_inbounds(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_user_inbound_access_inbound ON user_inbound_access(inbound_id);

CREATE TABLE IF NOT EXISTS schema_migrations (
	name TEXT PRIMARY KEY,
	applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

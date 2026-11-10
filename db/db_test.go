package db

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func legacyAccessDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	database, err := sql.Open("sqlite", filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	// schema 的基础表仍描述迁移前的列。移除两个新表，
	// 模拟真实的旧数据库，而不是在新结构上直接写入授权。
	for _, statement := range []string{
		schema,
		`DROP TABLE user_inbound_access`,
		`DROP TABLE schema_migrations`,
		`INSERT INTO users (id, name, uuid, sub_token) VALUES (1, 'legacy', 'original-uuid', 'original-token')`,
		`INSERT INTO nodes (id, name, host) VALUES (1, 'one', 'host-one'), (2, 'two', 'host-two')`,
		`INSERT INTO node_inbounds (id, node_id, protocol, port, enabled) VALUES (10, 1, 'vless', 443, 1), (11, 1, 'hysteria2', 8443, 0), (20, 2, 'trojan', 9443, 1)`,
		`INSERT INTO user_access (user_id, node_id) VALUES (1, 1)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return database, dir
}

func migratedInboundIDs(t *testing.T, database *sql.DB) []int {
	t.Helper()
	rows, err := database.Query(`SELECT inbound_id FROM user_inbound_access WHERE user_id = 1 ORDER BY inbound_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := []int{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestInboundAccessMigrationSnapshotsExistingInboundsOnlyOnce(t *testing.T) {
	database, dir := legacyAccessDatabase(t)
	if err := migrate(database); err != nil {
		t.Fatal(err)
	}
	if got := migratedInboundIDs(t, database); !reflect.DeepEqual(got, []int{10, 11}) {
		t.Fatalf("migration must include all existing inbounds of the granted node, got %v", got)
	}
	var name, outbound string
	var custom int
	if err := database.QueryRow(`SELECT name, outbound, custom FROM node_inbounds WHERE id = 10`).Scan(&name, &outbound, &custom); err != nil {
		t.Fatal(err)
	}
	if name != "" || outbound != "{}" || custom != 0 {
		t.Fatalf("new column defaults: name=%q outbound=%q custom=%d", name, outbound, custom)
	}
	for _, statement := range []string{
		`DELETE FROM user_inbound_access WHERE user_id = 1 AND inbound_id = 10`,
		`INSERT INTO node_inbounds (id, node_id, protocol, port) VALUES (12, 1, 'trojan', 10443)`,
		// 即便旧授权仍被保留或修改，也绝不能用它兜底补授权。
		`INSERT INTO user_access (user_id, node_id) VALUES (1, 2)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := migratedInboundIDs(t, reopened); !reflect.DeepEqual(got, []int{11}) {
		t.Fatalf("restart resurrected revoked or future grants: %v", got)
	}
	var uuid, token string
	if err := reopened.QueryRow(`SELECT uuid, sub_token FROM users WHERE id = 1`).Scan(&uuid, &token); err != nil {
		t.Fatal(err)
	}
	if uuid != "original-uuid" || token != "original-token" {
		t.Fatalf("migration changed credentials: uuid=%q token=%q", uuid, token)
	}
	var markers, legacyGrants int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, inboundAccessMigration).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM user_access WHERE user_id = 1`).Scan(&legacyGrants); err != nil {
		t.Fatal(err)
	}
	if markers != 1 || legacyGrants != 2 {
		t.Fatalf("marker=%d retained legacy grants=%d", markers, legacyGrants)
	}
}

func TestInboundAccessMigrationRollsBackCopyAndColumnsOnMarkerFailure(t *testing.T) {
	database, _ := legacyAccessDatabase(t)
	if _, err := database.Exec(`CREATE TABLE schema_migrations (name TEXT PRIMARY KEY);
		CREATE TRIGGER reject_migration BEFORE INSERT ON schema_migrations
		BEGIN SELECT RAISE(ABORT, 'marker failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err == nil || !strings.Contains(err.Error(), "marker failure") {
		t.Fatalf("migration must report marker failure, got %v", err)
	}
	var tables, columns, markers int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'user_inbound_access'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('node_inbounds') WHERE name IN ('name', 'outbound', 'custom')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if tables != 0 || columns != 0 || markers != 0 {
		t.Fatalf("failed migration partially committed: tables=%d columns=%d markers=%d", tables, columns, markers)
	}
	if _, err := database.Exec(`DROP TRIGGER reject_migration`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatal(err)
	}
	if got := migratedInboundIDs(t, database); !reflect.DeepEqual(got, []int{10, 11}) {
		t.Fatalf("retry failed to migrate exact grants: %v", got)
	}
}

func TestAddColumnDoesNotIgnoreUnexpectedMigrationErrors(t *testing.T) {
	database, _ := legacyAccessDatabase(t)
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := addColumnIfMissing(tx, "node_inbounds", "name", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatal(err)
	}
	if err := addColumnIfMissing(tx, "node_inbounds", "name", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("existing column is not an error: %v", err)
	}
	if err := addColumnIfMissing(tx, "node_inbounds", "outbound", "TEXT NOT NULL DEFAULT ("); err == nil {
		t.Fatal("invalid outbound column migration was silently ignored")
	}
}

func TestInboundAccessSchemaEnforcesUniqueAndForeignKeys(t *testing.T) {
	database, _ := legacyAccessDatabase(t)
	database.SetMaxOpenConns(1)
	if err := migrate(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO user_inbound_access (user_id, inbound_id) VALUES (1, 10)`,
		`INSERT INTO user_inbound_access (user_id, inbound_id) VALUES (999, 10)`,
		`INSERT INTO user_inbound_access (user_id, inbound_id) VALUES (1, 999)`,
	} {
		if _, err := database.Exec(statement); err == nil {
			t.Fatalf("invalid grant accepted: %s", statement)
		}
	}
}

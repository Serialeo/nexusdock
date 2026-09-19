package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEnsureSchemaIsIdempotentAndPersistent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nexus.db")
	db, err := OpenSQLite(ctx, path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatalf("second ensure failed: %v", err)
	}
	for _, table := range []string{"agentdock_devices", "projects", "project_deployments", "project_deployment_removals", "agentdock_pairing_codes", "agentdock_tool_contracts", "agentdock_ui_resources", "agentdock_published_tool_contracts", "agentdock_published_tool_variants", "oauth_clients", "oauth_authorization_codes", "oauth_grants", "oauth_refresh_token_history", "runtime_ai_settings", "stage3_prompt_overrides", "runtime_ai_setting_secrets", "stage3_proposal_audit"} {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("%s missing: %v", table, err)
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE tasks(id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	var removedTable string
	err = db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='tasks'`).Scan(&removedTable)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unused table should be removed, err=%v table=%q", err, removedTable)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id, username, created_at, updated_at) VALUES('u1', 'alice', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = OpenSQLite(ctx, path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var username string
	if err := db.QueryRowContext(ctx, `SELECT username FROM users WHERE id = 'u1'`).Scan(&username); err != nil {
		t.Fatal(err)
	}
	if username != "alice" {
		t.Fatalf("username = %q, want alice", username)
	}
}

func TestEnsureSchemaUpgradesDatabaseWithoutRewritingExistingTables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nexus-upgrade.db")
	db, err := OpenSQLite(ctx, path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE stage3_prompt_overrides`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id, username, created_at, updated_at) VALUES('legacy-user', 'legacy', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatalf("upgrade ensure failed: %v", err)
	}
	var stage3Table string
	if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='stage3_prompt_overrides'`).Scan(&stage3Table); err != nil {
		t.Fatalf("upgrade did not recreate stage3_prompt_overrides: %v", err)
	}
	var username string
	if err := db.QueryRowContext(ctx, `SELECT username FROM users WHERE id='legacy-user'`).Scan(&username); err != nil || username != "legacy" {
		t.Fatalf("legacy row did not survive upgrade: username=%q err=%v", username, err)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(runtime_ai_settings)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "system_prompt" || name == "stage3_system_prompt" {
			t.Fatalf("upgrade unexpectedly altered runtime_ai_settings with %q", name)
		}
	}
}

func TestEnsureSchemaAddsNodeSessionProjectColumnsAfterLegacyTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "node-session-upgrade.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE agentdock_devices (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE projects (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, orchestration_policy TEXT NOT NULL DEFAULT '',
		revision INTEGER NOT NULL DEFAULT 1, enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,name,created_at,updated_at) VALUES('legacy-project','Legacy','now','now')`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	var kind string
	var nodeID any
	if err := db.QueryRowContext(ctx, `SELECT kind,node_id FROM projects WHERE id='legacy-project'`).Scan(&kind, &nodeID); err != nil {
		t.Fatal(err)
	}
	if kind != "project" || nodeID != nil {
		t.Fatalf("legacy project migrated to kind=%q node_id=%#v", kind, nodeID)
	}
	var indexName string
	if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='index' AND name='idx_projects_node_session'`).Scan(&indexName); err != nil {
		t.Fatalf("node session unique index missing: %v", err)
	}
}

func TestProjectSchemaUpgradePreservesUnrelatedControlPlaneData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "project-upgrade.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	// Model a database from immediately before T04 by removing only the new
	// Project tables while retaining unrelated user, node, and encrypted secret data.
	for _, table := range []string{"project_deployment_removals", "project_deployments", "projects"} {
		if _, err := db.ExecContext(ctx, `DROP TABLE `+table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id, username, created_at, updated_at) VALUES('project-upgrade-user', 'alice', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agentdock_devices(
		id, device_id, name, enabled, version, protocol_version, os, arch, capabilities_json, tool_contract_hash, created_at, updated_at
	) VALUES('node-preserve', 'device_preserve_12345678', 'PreserveNode', 1, '1', '4', 'linux', 'amd64', '[]', '', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO runtime_ai_setting_secrets(name, ciphertext, updated_at) VALUES('embedding_api_key', ?, 'now')`, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}

	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatalf("Project schema upgrade failed: %v", err)
	}
	for _, table := range []string{"projects", "project_deployments", "project_deployment_removals"} {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("Project table %s missing after upgrade: %v", table, err)
		}
	}
	var username, nodeName string
	if err := db.QueryRowContext(ctx, `SELECT username FROM users WHERE id='project-upgrade-user'`).Scan(&username); err != nil || username != "alice" {
		t.Fatalf("administrator data changed during Project upgrade: username=%q err=%v", username, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT name FROM agentdock_devices WHERE id='node-preserve'`).Scan(&nodeName); err != nil || nodeName != "PreserveNode" {
		t.Fatalf("node identity changed during Project upgrade: name=%q err=%v", nodeName, err)
	}
	var ciphertext []byte
	if err := db.QueryRowContext(ctx, `SELECT ciphertext FROM runtime_ai_setting_secrets WHERE name='embedding_api_key'`).Scan(&ciphertext); err != nil || !reflect.DeepEqual(ciphertext, []byte{1, 2, 3, 4}) {
		t.Fatalf("encrypted secret changed during Project upgrade: ciphertext=%v err=%v", ciphertext, err)
	}
}

func TestEnsureSchemaDropsLegacyInstructionTablesAndPreservesUnrelatedData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "legacy-prompt.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE runtime_ai_settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agentdock_devices(id, device_id, name, enabled, created_at, updated_at) VALUES('node_legacy', 'device_legacy', 'Legacy', 1, 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	legacyStatements := []string{
		`CREATE TABLE agentdock_node_instructions (node_id TEXT PRIMARY KEY, instructions TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY (node_id) REFERENCES agentdock_devices(id) ON DELETE CASCADE)`,
		`CREATE TABLE nexus_instructions (singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1), instructions TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE runtime_ai_settings (
			singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
			embedding_enabled INTEGER NOT NULL CHECK (embedding_enabled IN (0, 1)), embedding_endpoint TEXT NOT NULL, embedding_model TEXT NOT NULL,
			embedding_timeout_seconds INTEGER NOT NULL, stage3_enabled INTEGER NOT NULL CHECK (stage3_enabled IN (0, 1)), stage3_endpoint TEXT NOT NULL,
			stage3_model TEXT NOT NULL, stage3_timeout_seconds INTEGER NOT NULL, stage3_interval_minutes INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`INSERT INTO agentdock_node_instructions(node_id, instructions, updated_at) VALUES('node_legacy', 'node legacy text', '2026-01-01T00:00:00Z')`,
		`INSERT INTO nexus_instructions(singleton_id, instructions, updated_at) VALUES(1, 'global legacy text', '2026-01-01T00:00:00Z')`,
		`INSERT INTO runtime_ai_settings(singleton_id, embedding_enabled, embedding_endpoint, embedding_model, embedding_timeout_seconds, stage3_enabled, stage3_endpoint, stage3_model, stage3_timeout_seconds, stage3_interval_minutes, updated_at) VALUES(1, 0, '', 'legacy-embedding', 30, 0, '', 'legacy-stage3', 60, 360, '2026-01-01T00:00:00Z')`,
	}
	for _, statement := range legacyStatements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("legacy fixture: %v\n%s", err, statement)
		}
	}
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatalf("remove legacy instruction tables: %v", err)
	}
	for _, table := range []string{"agentdock_node_instructions", "nexus_instructions"} {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("legacy table %s still exists: name=%q err=%v", table, name, err)
		}
	}
	var nodeName string
	if err := db.QueryRowContext(ctx, `SELECT name FROM agentdock_devices WHERE id='node_legacy'`).Scan(&nodeName); err != nil || nodeName != "Legacy" {
		t.Fatalf("node identity changed while retiring legacy prompt tables: name=%q err=%v", nodeName, err)
	}
	var reviewNode string
	var settingsRevision int64
	var model string
	if err := db.QueryRowContext(ctx, `SELECT stage3_review_node_id, revision, stage3_model FROM runtime_ai_settings WHERE singleton_id=1`).Scan(&reviewNode, &settingsRevision, &model); err != nil {
		t.Fatal(err)
	}
	if reviewNode != "" || settingsRevision != 1 || model != "legacy-stage3" {
		t.Fatalf("legacy runtime settings migration = review=%q revision=%d model=%q", reviewNode, settingsRevision, model)
	}
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatalf("second migration pass: %v", err)
	}
}

func TestTxManagerRollsBackOnError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := OpenSQLite(ctx, ":memory:", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE values_test(value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("stop")
	err = NewTxManager(db).WithinTx(ctx, nil, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO values_test(value) VALUES('x')`); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM values_test`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want rollback to 0", count)
	}
}

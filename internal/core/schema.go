package core

import (
	"context"
	"database/sql"
	"fmt"
)

// 当前控制面表。历史 Task/Run/设备表不再创建，启动时若还在就丢掉。
var currentSchema = []string{
	`CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS auth_tokens (
    id TEXT PRIMARY KEY,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user', 'agent', 'device', 'system')),
    subject_id TEXT NOT NULL,
    token_kind TEXT NOT NULL CHECK (token_kind IN ('session', 'agent_token', 'device_token', 'system_token')),
    token_hash TEXT NOT NULL UNIQUE,
    scopes_json TEXT NOT NULL,
    issued_at TEXT NOT NULL,
    expires_at TEXT,
    revoked_at TEXT,
    revoked_by_type TEXT,
    revoked_by_id TEXT
)`,
	`CREATE INDEX IF NOT EXISTS idx_auth_tokens_subject ON auth_tokens(subject_type, subject_id)`,
	`CREATE INDEX IF NOT EXISTS idx_auth_tokens_active ON auth_tokens(token_hash, revoked_at, expires_at)`,
	`CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    occurred_at TEXT NOT NULL,
    actor_type TEXT NOT NULL CHECK (actor_type IN ('user', 'agent', 'device', 'system')),
    actor_id TEXT NOT NULL,
    action TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    result TEXT NOT NULL,
    risk TEXT NOT NULL DEFAULT 'low',
    approval TEXT NOT NULL DEFAULT 'not_required',
    run_id TEXT,
    request_id TEXT,
    metadata_json TEXT NOT NULL DEFAULT '{}'
)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_events_time ON audit_events(occurred_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_events_object ON audit_events(object_type, object_id, occurred_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_events_actor ON audit_events(actor_type, actor_id, occurred_at DESC)`,
	`CREATE TRIGGER IF NOT EXISTS audit_events_no_update
BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events are append-only');
END`,
	`CREATE TRIGGER IF NOT EXISTS audit_events_no_delete
BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events are append-only');
END`,
	`CREATE TABLE IF NOT EXISTS user_credentials (
    user_id TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    password_algorithm TEXT NOT NULL,
    must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0, 1)),
    password_changed_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
)`,
	`CREATE TABLE IF NOT EXISTS user_sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    csrf_salt TEXT NOT NULL,
    remember_me INTEGER NOT NULL DEFAULT 0 CHECK (remember_me IN (0, 1)),
    ip_prefix TEXT NOT NULL DEFAULT '',
    user_agent_summary TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    idle_expires_at TEXT NOT NULL,
    absolute_expires_at TEXT NOT NULL,
    revoked_at TEXT,
    revoke_reason TEXT,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
)`,
	`CREATE INDEX IF NOT EXISTS idx_user_sessions_user_active
    ON user_sessions(user_id, revoked_at, absolute_expires_at)`,
	`CREATE INDEX IF NOT EXISTS idx_user_sessions_token
    ON user_sessions(token_hash, revoked_at)`,
	`CREATE TABLE IF NOT EXISTS oauth_clients (
    id TEXT PRIMARY KEY,
    client_name TEXT NOT NULL DEFAULT '',
    redirect_uris_json TEXT NOT NULL,
    grant_types_json TEXT NOT NULL,
    response_types_json TEXT NOT NULL,
    token_endpoint_auth_method TEXT NOT NULL DEFAULT 'none' CHECK (token_endpoint_auth_method = 'none'),
    created_at TEXT NOT NULL,
    last_used_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS oauth_authorization_codes (
    code_hash TEXT PRIMARY KEY,
    client_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    resource TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT 'mcp',
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at TEXT,
    grant_id TEXT,
    FOREIGN KEY (client_id) REFERENCES oauth_clients(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
)`,
	`CREATE INDEX IF NOT EXISTS idx_oauth_authorization_codes_expiry
    ON oauth_authorization_codes(expires_at, used_at)`,
	`CREATE TABLE IF NOT EXISTS oauth_grants (
    id TEXT PRIMARY KEY,
    client_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    resource TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT 'mcp',
    access_token_hash TEXT NOT NULL UNIQUE,
    refresh_token_hash TEXT NOT NULL UNIQUE,
    access_expires_at TEXT NOT NULL,
    refresh_expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    revoked_at TEXT,
    FOREIGN KEY (client_id) REFERENCES oauth_clients(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
)`,
	`CREATE INDEX IF NOT EXISTS idx_oauth_grants_access
    ON oauth_grants(access_token_hash, revoked_at, access_expires_at)`,
	`CREATE INDEX IF NOT EXISTS idx_oauth_grants_refresh
    ON oauth_grants(refresh_token_hash, revoked_at, refresh_expires_at)`,
	`CREATE INDEX IF NOT EXISTS idx_oauth_grants_user
    ON oauth_grants(user_id, revoked_at)`,
	`CREATE TABLE IF NOT EXISTS oauth_refresh_token_history (
    token_hash TEXT PRIMARY KEY,
    grant_id TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    FOREIGN KEY (grant_id) REFERENCES oauth_grants(id) ON DELETE CASCADE
)`,
	`CREATE INDEX IF NOT EXISTS idx_oauth_refresh_token_history_expiry
    ON oauth_refresh_token_history(expires_at)`,
	`CREATE TABLE IF NOT EXISTS login_throttles (
    key_type TEXT NOT NULL CHECK (key_type IN ('account', 'ip')),
    key_value TEXT NOT NULL,
    failures INTEGER NOT NULL DEFAULT 0 CHECK (failures >= 0),
    blocked_until TEXT,
    last_failed_at TEXT NOT NULL,
    PRIMARY KEY (key_type, key_value)
)`,
	`CREATE TABLE IF NOT EXISTS agentdock_devices (
    id TEXT PRIMARY KEY,
    device_id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    full_access INTEGER NOT NULL DEFAULT 0 CHECK (full_access IN (0, 1)),
    version TEXT NOT NULL DEFAULT '',
    protocol_version TEXT NOT NULL DEFAULT '',
    os TEXT NOT NULL DEFAULT '',
    arch TEXT NOT NULL DEFAULT '',
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    tool_contract_hash TEXT NOT NULL DEFAULT '',
    last_seen_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    orchestration_policy TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_projects_name ON projects(name COLLATE NOCASE, id)`,
	`CREATE TABLE IF NOT EXISTS project_deployments (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    working_folder TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT '',
    purpose TEXT NOT NULL DEFAULT '',
    files_permission TEXT NOT NULL CHECK (files_permission IN ('none', 'read_only', 'read_write')),
    shell_enabled INTEGER NOT NULL DEFAULT 0 CHECK (shell_enabled IN (0, 1)),
    browser_enabled INTEGER NOT NULL DEFAULT 0 CHECK (browser_enabled IN (0, 1)),
    dynamic_mcp_enabled INTEGER NOT NULL DEFAULT 0 CHECK (dynamic_mcp_enabled IN (0, 1)),
    acp_enabled INTEGER NOT NULL DEFAULT 0 CHECK (acp_enabled IN (0, 1)),
    desired_revision INTEGER NOT NULL DEFAULT 1 CHECK (desired_revision >= 1),
    applied_revision INTEGER NOT NULL DEFAULT 0 CHECK (applied_revision >= 0),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    apply_status TEXT NOT NULL DEFAULT 'pending' CHECK (apply_status IN ('draft', 'pending', 'applied', 'failed', 'disabled')),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (node_id) REFERENCES agentdock_devices(id) ON DELETE RESTRICT,
    UNIQUE(project_id, node_id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_project_deployments_project ON project_deployments(project_id, id)`,
	`CREATE INDEX IF NOT EXISTS idx_project_deployments_node_apply ON project_deployments(node_id, enabled, desired_revision, applied_revision)`,
	`CREATE TABLE IF NOT EXISTS project_deployment_removals (
    deployment_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    removed_at TEXT NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (node_id) REFERENCES agentdock_devices(id) ON DELETE CASCADE
)`,
	`CREATE INDEX IF NOT EXISTS idx_project_deployment_removals_node ON project_deployment_removals(node_id, removed_at, deployment_id)`,
	`CREATE TABLE IF NOT EXISTS work_sessions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    owner_key TEXT NOT NULL,
    client_request_id TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    project_revision TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('preparing', 'ready', 'running', 'completed', 'partial', 'failed', 'cancelled')),
    context_revision TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(owner_key, client_request_id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_work_sessions_owner_project ON work_sessions(owner_key, project_id, updated_at, id)`,
	`CREATE TABLE IF NOT EXISTS work_targets (
    id TEXT PRIMARY KEY,
    work_session_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    deployment_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    cwd_rel TEXT NOT NULL DEFAULT '.',
    deployment_revision TEXT NOT NULL,
    context_revision TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('preparing', 'ready', 'running', 'idle', 'unavailable', 'context_error', 'revoked')),
    permissions_json TEXT NOT NULL,
    prompt_json TEXT NOT NULL,
    prompt_scopes_json TEXT NOT NULL DEFAULT '[]',
    source_provenance_json TEXT NOT NULL DEFAULT '{"kind":"unknown","repository_root":"","head":"","branch":"","detached":false,"unborn":false,"dirty":false}',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (work_session_id) REFERENCES work_sessions(id) ON DELETE CASCADE,
    UNIQUE(work_session_id, deployment_id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_work_targets_session ON work_targets(work_session_id, id)`,
	`CREATE INDEX IF NOT EXISTS idx_work_targets_node_status ON work_targets(node_id, status, updated_at, id)`,
	`CREATE TABLE IF NOT EXISTS work_continuations (
    work_session_id TEXT PRIMARY KEY,
    document_json BLOB NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (work_session_id) REFERENCES work_sessions(id) ON DELETE CASCADE
)`,
	`CREATE TABLE IF NOT EXISTS command_source_receipts (
    node_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    work_session_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    command_session_id TEXT NOT NULL,
    outcome_hash TEXT NOT NULL,
    outcome_json BLOB NOT NULL,
    received_at TEXT NOT NULL,
    eligible INTEGER NOT NULL CHECK (eligible IN (0, 1, 2)),
    PRIMARY KEY(node_id, event_id),
    UNIQUE(node_id, command_session_id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_command_receipts_session ON command_source_receipts(work_session_id, target_id, command_session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_command_receipts_node_retention ON command_source_receipts(node_id, eligible, received_at)`,
	`CREATE TABLE IF NOT EXISTS project_context_deliveries (
    work_session_id TEXT NOT NULL,
    target_id TEXT NOT NULL DEFAULT '',
    context_revision TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('returned', 'host_consumed')),
    returned_at TEXT NOT NULL,
    host_consumed_at TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY(work_session_id, target_id),
    FOREIGN KEY (work_session_id) REFERENCES work_sessions(id) ON DELETE CASCADE
)`,
	`CREATE INDEX IF NOT EXISTS idx_project_context_deliveries_status ON project_context_deliveries(status, updated_at, work_session_id, target_id)`,
	`CREATE TABLE IF NOT EXISTS agentdock_pairing_codes (
    id TEXT PRIMARY KEY,
    code_hash TEXT NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    used_at TEXT,
    created_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS agentdock_tool_contracts (
    node_id TEXT PRIMARY KEY,
    descriptors_json TEXT NOT NULL DEFAULT '[]',
    updated_at TEXT NOT NULL,
    FOREIGN KEY (node_id) REFERENCES agentdock_devices(id) ON DELETE CASCADE
)`,
	`CREATE TABLE IF NOT EXISTS agentdock_ui_resources (
    node_id TEXT PRIMARY KEY,
    resources_json TEXT NOT NULL DEFAULT '[]',
    updated_at TEXT NOT NULL,
    FOREIGN KEY (node_id) REFERENCES agentdock_devices(id) ON DELETE CASCADE
)`,
	`CREATE TABLE IF NOT EXISTS agentdock_bridge_capabilities (
    node_id TEXT PRIMARY KEY,
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    updated_at TEXT NOT NULL,
    FOREIGN KEY (node_id) REFERENCES agentdock_devices(id) ON DELETE CASCADE
)`,
	`CREATE TABLE IF NOT EXISTS agentdock_published_tool_contracts (
    tool_name TEXT PRIMARY KEY,
    descriptor_json TEXT NOT NULL,
    source_node_id TEXT NOT NULL DEFAULT '',
    source_version TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS agentdock_published_tool_variants (
    tool_name TEXT NOT NULL,
    semantic_hash TEXT NOT NULL,
    PRIMARY KEY (tool_name, semantic_hash),
    FOREIGN KEY (tool_name) REFERENCES agentdock_published_tool_contracts(tool_name) ON DELETE CASCADE
)`,
	`CREATE TABLE IF NOT EXISTS mcp_settings (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    mcp_apps_enabled INTEGER NOT NULL DEFAULT 1 CHECK (mcp_apps_enabled IN (0, 1)),
    updated_at TEXT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_agentdock_pairing_codes_active
    ON agentdock_pairing_codes(code_hash, used_at, expires_at)`,
	`CREATE TABLE IF NOT EXISTS runtime_ai_settings (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    embedding_enabled INTEGER NOT NULL CHECK (embedding_enabled IN (0, 1)),
    embedding_endpoint TEXT NOT NULL,
    embedding_model TEXT NOT NULL,
    embedding_timeout_seconds INTEGER NOT NULL CHECK (embedding_timeout_seconds BETWEEN 1 AND 300),
    stage3_enabled INTEGER NOT NULL CHECK (stage3_enabled IN (0, 1)),
    stage3_endpoint TEXT NOT NULL,
    stage3_model TEXT NOT NULL,
    stage3_timeout_seconds INTEGER NOT NULL CHECK (stage3_timeout_seconds BETWEEN 1 AND 300),
    stage3_interval_minutes INTEGER NOT NULL CHECK (stage3_interval_minutes BETWEEN 60 AND 10080),
    stage3_review_node_id TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    updated_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS stage3_prompt_overrides (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    prompt TEXT NOT NULL,
    updated_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS runtime_ai_setting_secrets (
    name TEXT PRIMARY KEY CHECK (name IN ('embedding_api_key', 'stage3_api_key')),
    ciphertext BLOB NOT NULL,
    updated_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS stage3_proposal_audit (
    id TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    target_node_id TEXT NOT NULL,
    candidate_type TEXT NOT NULL,
    candidate_scope TEXT NOT NULL,
    candidate_device TEXT NOT NULL,
    canonical_key TEXT NOT NULL,
    source_nodes_json TEXT NOT NULL,
    evidence_refs_json TEXT NOT NULL,
    rationale TEXT NOT NULL,
    result TEXT NOT NULL CHECK (result IN ('proposed', 'skipped', 'rejected')),
    error_code TEXT NOT NULL DEFAULT ''
)`,
	`CREATE INDEX IF NOT EXISTS idx_stage3_proposal_audit_created ON stage3_proposal_audit(created_at DESC)`,
}

var unusedTables = []string{
	"agentdock_node_instructions",
	"nexus_instructions",
	"agentdock_node_secrets",
	"agentdock_nodes",
	"run_verifications",
	"run_evidence",
	"run_steps",
	"runs",
	"skills",
	"tasks",
	"agents",
	"device_commands_v1",
	"device_heartbeats",
	"device_enrollment_tokens",
	"device_records",
	"devices",
	"schema_migrations",
}

func EnsureSchema(ctx context.Context, db *sql.DB) error {
	for _, statement := range currentSchema {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	for _, migration := range []struct {
		table, column, definition string
	}{
		{"runtime_ai_settings", "stage3_review_node_id", "TEXT NOT NULL DEFAULT ''"},
		{"runtime_ai_settings", "revision", "INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1)"},
		{"agentdock_devices", "full_access", "INTEGER NOT NULL DEFAULT 0 CHECK (full_access IN (0, 1))"},
		{"work_targets", "source_provenance_json", `TEXT NOT NULL DEFAULT '{"kind":"none","repository_root":"","head":"","branch":"","detached":false,"unborn":false,"dirty":false}'`},
	} {
		if err := ensureColumn(ctx, db, migration.table, migration.column, migration.definition); err != nil {
			return err
		}
	}
	for _, name := range unusedTables {
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+name); err != nil {
			return fmt.Errorf("drop unused table %s: %w", name, err)
		}
	}
	return nil
}

func ensureColumn(ctx context.Context, db *sql.DB, table, column, definition string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("inspect table %s: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("inspect table %s columns: %w", table, err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close table %s inspection: %w", table, err)
	}
	if found {
		return nil
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition); err != nil {
		return fmt.Errorf("migrate %s.%s: %w", table, column, err)
	}
	return nil
}

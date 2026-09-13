package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/core"
)

var (
	ErrWorkSessionNotFound = errors.New("WorkSession 不存在")
	ErrWorkTargetNotFound  = errors.New("WorkSession Target 不存在")
)

type WorkSessionRequestConflictError struct {
	ClientRequestID string
	WorkSessionID   string
}

func (e *WorkSessionRequestConflictError) Error() string {
	return "client_request_id 已绑定到不同的 Project 打开请求"
}

type WorkSession struct {
	ID              string                     `json:"work_session_id"`
	ProjectID       string                     `json:"project_id"`
	OwnerKey        string                     `json:"-"`
	ClientRequestID string                     `json:"client_request_id"`
	RequestHash     string                     `json:"-"`
	ProjectRevision string                     `json:"project_revision"`
	Status          protocol.WorkSessionStatus `json:"status"`
	ContextRevision string                     `json:"context_revision"`
	CreatedAt       time.Time                  `json:"created_at"`
	UpdatedAt       time.Time                  `json:"updated_at"`
}

type WorkTarget struct {
	Target       protocol.WorkTarget            `json:"target"`
	PromptScopes []protocol.PromptScopeRevision `json:"prompt_scopes"`
	LastError    string                         `json:"last_error,omitempty"`
	CreatedAt    time.Time                      `json:"created_at"`
	UpdatedAt    time.Time                      `json:"updated_at"`
}

func (s *Store) BeginWorkSession(ctx context.Context, ownerKey, projectID, clientRequestID, requestHash, projectRevision string) (WorkSession, bool, error) {
	if s == nil || s.db == nil {
		return WorkSession{}, false, errors.New("Project store 未初始化")
	}
	ownerKey = strings.TrimSpace(ownerKey)
	projectID = strings.TrimSpace(projectID)
	clientRequestID = strings.TrimSpace(clientRequestID)
	requestHash = strings.TrimSpace(requestHash)
	projectRevision = strings.TrimSpace(projectRevision)
	if ownerKey == "" || projectID == "" || clientRequestID == "" || requestHash == "" || projectRevision == "" {
		return WorkSession{}, false, invalid("WorkSession owner/project/client_request_id/request_hash/project_revision 不能为空")
	}
	id, err := core.NewID("ws")
	if err != nil {
		return WorkSession{}, false, fmt.Errorf("生成 WorkSession ID: %w", err)
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `INSERT INTO work_sessions(
		id, project_id, owner_key, client_request_id, request_hash, project_revision, status, context_revision, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, '', ?, ?)
	ON CONFLICT(owner_key, client_request_id) DO NOTHING`,
		id, projectID, ownerKey, clientRequestID, requestHash, projectRevision, string(protocol.WorkSessionPreparing), formatTime(now), formatTime(now))
	if err != nil {
		return WorkSession{}, false, fmt.Errorf("创建 WorkSession: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return WorkSession{}, false, fmt.Errorf("读取 WorkSession 创建结果: %w", err)
	}
	if rows > 0 {
		created, err := s.GetWorkSession(ctx, ownerKey, id)
		return created, true, err
	}
	existing, err := s.getWorkSessionByRequest(ctx, ownerKey, clientRequestID)
	if err != nil {
		return WorkSession{}, false, err
	}
	if existing.ProjectID != projectID || existing.RequestHash != requestHash {
		return WorkSession{}, false, &WorkSessionRequestConflictError{ClientRequestID: clientRequestID, WorkSessionID: existing.ID}
	}
	return existing, false, nil
}

func (s *Store) GetWorkSession(ctx context.Context, ownerKey, id string) (WorkSession, error) {
	if s == nil || s.db == nil {
		return WorkSession{}, errors.New("Project store 未初始化")
	}
	return scanWorkSession(s.db.QueryRowContext(ctx, `SELECT id, project_id, owner_key, client_request_id, request_hash, project_revision, status, context_revision, created_at, updated_at
		FROM work_sessions WHERE id = ? AND owner_key = ?`, strings.TrimSpace(id), strings.TrimSpace(ownerKey)))
}

func (s *Store) ListProjectWorkSessions(ctx context.Context, projectID string, limit int) ([]WorkSession, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("Project store 未初始化")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, invalid("Project ID 不能为空")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, project_id, owner_key, client_request_id, request_hash, project_revision, status, context_revision, created_at, updated_at
		FROM work_sessions WHERE project_id = ? ORDER BY updated_at DESC, id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("列出 Project WorkSessions: %w", err)
	}
	defer rows.Close()
	items := make([]WorkSession, 0)
	for rows.Next() {
		item, scanErr := scanWorkSession(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 Project WorkSessions: %w", err)
	}
	return items, nil
}

func (s *Store) GetProjectWorkSession(ctx context.Context, projectID, id string) (WorkSession, error) {
	if s == nil || s.db == nil {
		return WorkSession{}, errors.New("Project store 未初始化")
	}
	item, err := scanWorkSession(s.db.QueryRowContext(ctx, `SELECT id, project_id, owner_key, client_request_id, request_hash, project_revision, status, context_revision, created_at, updated_at
		FROM work_sessions WHERE id = ? AND project_id = ?`, strings.TrimSpace(id), strings.TrimSpace(projectID)))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkSession{}, ErrWorkSessionNotFound
	}
	return item, err
}

func (s *Store) getWorkSessionByRequest(ctx context.Context, ownerKey, clientRequestID string) (WorkSession, error) {
	item, err := scanWorkSession(s.db.QueryRowContext(ctx, `SELECT id, project_id, owner_key, client_request_id, request_hash, project_revision, status, context_revision, created_at, updated_at
		FROM work_sessions WHERE owner_key = ? AND client_request_id = ?`, strings.TrimSpace(ownerKey), strings.TrimSpace(clientRequestID)))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkSession{}, ErrWorkSessionNotFound
	}
	return item, err
}

func (s *Store) SetWorkSessionState(ctx context.Context, ownerKey, id string, status protocol.WorkSessionStatus, contextRevision string) (WorkSession, error) {
	if !validWorkSessionStatus(status) {
		return WorkSession{}, invalid("WorkSession status 无效")
	}
	now := formatTime(time.Now().UTC())
	result, err := s.db.ExecContext(ctx, `UPDATE work_sessions SET status = ?, context_revision = ?, updated_at = ? WHERE id = ? AND owner_key = ?`,
		string(status), strings.TrimSpace(contextRevision), now, strings.TrimSpace(id), strings.TrimSpace(ownerKey))
	if err != nil {
		return WorkSession{}, fmt.Errorf("更新 WorkSession 状态: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return WorkSession{}, err
	}
	if rows == 0 {
		return WorkSession{}, ErrWorkSessionNotFound
	}
	return s.GetWorkSession(ctx, ownerKey, id)
}

func (s *Store) UpdateWorkSessionContext(ctx context.Context, ownerKey, id, projectRevision string, status protocol.WorkSessionStatus, contextRevision string) (WorkSession, error) {
	if !validWorkSessionStatus(status) || strings.TrimSpace(projectRevision) == "" {
		return WorkSession{}, invalid("WorkSession project revision/status 无效")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE work_sessions SET project_revision = ?, status = ?, context_revision = ?, updated_at = ? WHERE id = ? AND owner_key = ?`,
		strings.TrimSpace(projectRevision), string(status), strings.TrimSpace(contextRevision), formatTime(time.Now().UTC()), strings.TrimSpace(id), strings.TrimSpace(ownerKey))
	if err != nil {
		return WorkSession{}, fmt.Errorf("刷新 WorkSession context: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return WorkSession{}, err
	}
	if rows == 0 {
		return WorkSession{}, ErrWorkSessionNotFound
	}
	return s.GetWorkSession(ctx, ownerKey, id)
}

func (s *Store) PutWorkTarget(ctx context.Context, ownerKey string, item WorkTarget) (WorkTarget, error) {
	if _, err := s.GetWorkSession(ctx, ownerKey, item.Target.WorkSessionID); err != nil {
		return WorkTarget{}, err
	}
	if strings.TrimSpace(item.Target.ID) == "" {
		id, err := core.NewID("target")
		if err != nil {
			return WorkTarget{}, fmt.Errorf("生成 Target ID: %w", err)
		}
		item.Target.ID = id
	}
	if !validTargetStatus(item.Target.Status) {
		return WorkTarget{}, invalid("Target status 无效")
	}
	permissionsJSON, err := json.Marshal(item.Target.Permissions)
	if err != nil {
		return WorkTarget{}, fmt.Errorf("编码 Target permissions: %w", err)
	}
	promptJSON, err := json.Marshal(item.Target.Prompt)
	if err != nil {
		return WorkTarget{}, fmt.Errorf("编码 Target Prompt: %w", err)
	}
	promptScopesJSON, err := json.Marshal(item.PromptScopes)
	if err != nil {
		return WorkTarget{}, fmt.Errorf("编码 Target Prompt scopes: %w", err)
	}
	if item.Target.SourceProvenance.Kind == "" {
		// Older persisted/test Targets predate source provenance. Preserve them as
		// explicitly uninspected until the next Project context refresh.
		item.Target.SourceProvenance = protocol.SourceProvenance{Kind: protocol.SourceProvenanceUnknown}
	}
	if err := item.Target.SourceProvenance.Validate(); err != nil {
		return WorkTarget{}, invalid("Target source provenance 无效: " + err.Error())
	}
	sourceProvenanceJSON, err := json.Marshal(item.Target.SourceProvenance)
	if err != nil {
		return WorkTarget{}, fmt.Errorf("编码 Target source provenance: %w", err)
	}
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	_, err = s.db.ExecContext(ctx, `INSERT INTO work_targets(
		id, work_session_id, project_id, deployment_id, node_id, cwd_rel, deployment_revision, context_revision, status,
		permissions_json, prompt_json, prompt_scopes_json, source_provenance_json, last_error, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		cwd_rel=excluded.cwd_rel, deployment_revision=excluded.deployment_revision, context_revision=excluded.context_revision,
		status=excluded.status, permissions_json=excluded.permissions_json, prompt_json=excluded.prompt_json,
		prompt_scopes_json=excluded.prompt_scopes_json, source_provenance_json=excluded.source_provenance_json,
		last_error=excluded.last_error, updated_at=excluded.updated_at`,
		item.Target.ID, item.Target.WorkSessionID, item.Target.ProjectID, item.Target.DeploymentID, item.Target.NodeID, item.Target.CWDRel,
		item.Target.DeploymentRevision, item.Target.ContextRevision, string(item.Target.Status), string(permissionsJSON), string(promptJSON), string(promptScopesJSON), string(sourceProvenanceJSON),
		boundedError(item.LastError, 2048), formatTime(item.CreatedAt), formatTime(item.UpdatedAt))
	if err != nil {
		return WorkTarget{}, fmt.Errorf("保存 WorkSession Target: %w", err)
	}
	return s.GetWorkTarget(ctx, ownerKey, item.Target.WorkSessionID, item.Target.ID)
}

func (s *Store) GetWorkTarget(ctx context.Context, ownerKey, workSessionID, targetID string) (WorkTarget, error) {
	row := s.db.QueryRowContext(ctx, `SELECT t.id, t.work_session_id, t.project_id, t.deployment_id, t.node_id, t.cwd_rel,
		t.deployment_revision, t.context_revision, t.status, t.permissions_json, t.prompt_json, t.prompt_scopes_json, t.source_provenance_json,
		t.last_error, t.created_at, t.updated_at
		FROM work_targets t JOIN work_sessions s ON s.id = t.work_session_id
		WHERE t.id = ? AND t.work_session_id = ? AND s.owner_key = ?`, strings.TrimSpace(targetID), strings.TrimSpace(workSessionID), strings.TrimSpace(ownerKey))
	item, err := scanWorkTarget(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkTarget{}, ErrWorkTargetNotFound
	}
	return item, err
}

func (s *Store) ListWorkTargets(ctx context.Context, ownerKey, workSessionID string) ([]WorkTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.work_session_id, t.project_id, t.deployment_id, t.node_id, t.cwd_rel,
		t.deployment_revision, t.context_revision, t.status, t.permissions_json, t.prompt_json, t.prompt_scopes_json, t.source_provenance_json,
		t.last_error, t.created_at, t.updated_at
		FROM work_targets t JOIN work_sessions s ON s.id = t.work_session_id
		WHERE t.work_session_id = ? AND s.owner_key = ? ORDER BY t.id`, strings.TrimSpace(workSessionID), strings.TrimSpace(ownerKey))
	if err != nil {
		return nil, fmt.Errorf("列出 WorkSession Targets: %w", err)
	}
	defer rows.Close()
	items := make([]WorkTarget, 0)
	for rows.Next() {
		item, err := scanWorkTarget(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListProjectWorkTargets(ctx context.Context, projectID, workSessionID string) ([]WorkTarget, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("Project store 未初始化")
	}
	projectID = strings.TrimSpace(projectID)
	workSessionID = strings.TrimSpace(workSessionID)
	if projectID == "" || workSessionID == "" {
		return nil, invalid("Project ID 和 WorkSession ID 不能为空")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.work_session_id, t.project_id, t.deployment_id, t.node_id, t.cwd_rel,
		t.deployment_revision, t.context_revision, t.status, t.permissions_json, t.prompt_json, t.prompt_scopes_json, t.source_provenance_json,
		t.last_error, t.created_at, t.updated_at
		FROM work_targets t JOIN work_sessions s ON s.id = t.work_session_id
		WHERE t.work_session_id = ? AND t.project_id = ? AND s.project_id = ? ORDER BY t.id`, strings.TrimSpace(workSessionID), strings.TrimSpace(projectID), strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("列出 Project WorkSession Targets: %w", err)
	}
	defer rows.Close()
	items := make([]WorkTarget, 0)
	for rows.Next() {
		item, scanErr := scanWorkTarget(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 Project WorkSession Targets: %w", err)
	}
	return items, nil
}

func (s *Store) FindWorkTargetByDeployment(ctx context.Context, ownerKey, workSessionID, deploymentID string) (WorkTarget, error) {
	row := s.db.QueryRowContext(ctx, `SELECT t.id, t.work_session_id, t.project_id, t.deployment_id, t.node_id, t.cwd_rel,
		t.deployment_revision, t.context_revision, t.status, t.permissions_json, t.prompt_json, t.prompt_scopes_json, t.source_provenance_json,
		t.last_error, t.created_at, t.updated_at
		FROM work_targets t JOIN work_sessions s ON s.id = t.work_session_id
		WHERE t.work_session_id = ? AND t.deployment_id = ? AND s.owner_key = ?`, strings.TrimSpace(workSessionID), strings.TrimSpace(deploymentID), strings.TrimSpace(ownerKey))
	item, err := scanWorkTarget(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkTarget{}, ErrWorkTargetNotFound
	}
	return item, err
}

// RevokeTargetsForDeployment invalidates every non-revoked WorkTarget that was
// prepared from one Deployment. The DB state is changed before callers attempt
// the best-effort Bridge revoke so an offline node can never leave Nexus routing
// an old Target as executable.
func (s *Store) RevokeTargetsForDeployment(ctx context.Context, deploymentID, reason string) ([]WorkTarget, error) {
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return nil, invalid("Deployment ID 不能为空")
	}
	return s.revokeTargets(ctx, "deployment_id = ?", []any{deploymentID}, reason, false)
}

// RevokeTargetsForProject invalidates every active Target in a Project and
// marks its WorkSessions cancelled. This is used when the Project itself is
// disabled or deleted; preserving the rows keeps historical routing evidence
// without preserving execution authority.
func (s *Store) RevokeTargetsForProject(ctx context.Context, projectID, reason string) ([]WorkTarget, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, invalid("Project ID 不能为空")
	}
	return s.revokeTargets(ctx, "project_id = ?", []any{projectID}, reason, true)
}

func (s *Store) revokeTargets(ctx context.Context, predicate string, args []any, reason string, cancelSessions bool) ([]WorkTarget, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("Project store 未初始化")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("开始撤销 Project Targets: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `SELECT id, work_session_id, project_id, deployment_id, node_id, cwd_rel,
		deployment_revision, context_revision, status, permissions_json, prompt_json, prompt_scopes_json, source_provenance_json,
		last_error, created_at, updated_at FROM work_targets WHERE ` + predicate + ` AND status <> ? ORDER BY work_session_id, id`
	queryArgs := append(append([]any(nil), args...), string(protocol.TargetRevoked))
	rows, err := tx.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("读取待撤销 Project Targets: %w", err)
	}
	items := make([]WorkTarget, 0)
	sessionIDs := make(map[string]struct{})
	for rows.Next() {
		item, scanErr := scanWorkTarget(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		items = append(items, item)
		sessionIDs[item.Target.WorkSessionID] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return items, nil
	}

	now := formatTime(time.Now().UTC())
	update := `UPDATE work_targets SET status = ?, last_error = ?, updated_at = ? WHERE ` + predicate + ` AND status <> ?`
	updateArgs := []any{string(protocol.TargetRevoked), boundedError(reason, 2048), now}
	updateArgs = append(updateArgs, args...)
	updateArgs = append(updateArgs, string(protocol.TargetRevoked))
	if _, err := tx.ExecContext(ctx, update, updateArgs...); err != nil {
		return nil, fmt.Errorf("标记 Project Targets revoked: %w", err)
	}

	for sessionID := range sessionIDs {
		status := protocol.WorkSessionCancelled
		if !cancelSessions {
			status, err = workSessionStatusAfterRevocation(ctx, tx, sessionID)
			if err != nil {
				return nil, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE work_sessions SET status = ?, updated_at = ? WHERE id = ?`, string(status), now, sessionID); err != nil {
			return nil, fmt.Errorf("更新撤销后的 WorkSession 状态: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交 Project Target 撤销: %w", err)
	}
	for i := range items {
		items[i].Target.Status = protocol.TargetRevoked
		items[i].LastError = boundedError(reason, 2048)
	}
	return items, nil
}

func workSessionStatusAfterRevocation(ctx context.Context, tx *sql.Tx, workSessionID string) (protocol.WorkSessionStatus, error) {
	var total, ready int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN status IN (?, ?, ?) THEN 1 ELSE 0 END), 0)
		FROM work_targets WHERE work_session_id = ?`, string(protocol.TargetReady), string(protocol.TargetIdle), string(protocol.TargetRunning), workSessionID).Scan(&total, &ready); err != nil {
		return "", fmt.Errorf("统计撤销后的 WorkSession Targets: %w", err)
	}
	switch {
	case total > 0 && ready == total:
		return protocol.WorkSessionReady, nil
	case ready > 0:
		return protocol.WorkSessionPartial, nil
	default:
		return protocol.WorkSessionFailed, nil
	}
}

func scanWorkSession(row scanner) (WorkSession, error) {
	var item WorkSession
	var status, created, updated string
	if err := row.Scan(&item.ID, &item.ProjectID, &item.OwnerKey, &item.ClientRequestID, &item.RequestHash, &item.ProjectRevision,
		&status, &item.ContextRevision, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorkSession{}, ErrWorkSessionNotFound
		}
		return WorkSession{}, err
	}
	item.Status = protocol.WorkSessionStatus(status)
	var err error
	item.CreatedAt, err = parseTime(created)
	if err != nil {
		return WorkSession{}, err
	}
	item.UpdatedAt, err = parseTime(updated)
	return item, err
}

func scanWorkTarget(row scanner) (WorkTarget, error) {
	var item WorkTarget
	var status, permissionsJSON, promptJSON, promptScopesJSON, sourceProvenanceJSON, created, updated string
	if err := row.Scan(&item.Target.ID, &item.Target.WorkSessionID, &item.Target.ProjectID, &item.Target.DeploymentID, &item.Target.NodeID,
		&item.Target.CWDRel, &item.Target.DeploymentRevision, &item.Target.ContextRevision, &status, &permissionsJSON, &promptJSON, &promptScopesJSON, &sourceProvenanceJSON,
		&item.LastError, &created, &updated); err != nil {
		return WorkTarget{}, err
	}
	item.Target.Status = protocol.TargetStatus(status)
	if err := json.Unmarshal([]byte(permissionsJSON), &item.Target.Permissions); err != nil {
		return WorkTarget{}, fmt.Errorf("解析 Target permissions: %w", err)
	}
	if err := json.Unmarshal([]byte(promptJSON), &item.Target.Prompt); err != nil {
		return WorkTarget{}, fmt.Errorf("解析 Target Prompt: %w", err)
	}
	if err := json.Unmarshal([]byte(promptScopesJSON), &item.PromptScopes); err != nil {
		return WorkTarget{}, fmt.Errorf("解析 Target Prompt scopes: %w", err)
	}
	if err := json.Unmarshal([]byte(sourceProvenanceJSON), &item.Target.SourceProvenance); err != nil {
		return WorkTarget{}, fmt.Errorf("解析 Target source provenance: %w", err)
	}
	if err := item.Target.SourceProvenance.Validate(); err != nil {
		return WorkTarget{}, fmt.Errorf("校验 Target source provenance: %w", err)
	}
	var err error
	item.CreatedAt, err = parseTime(created)
	if err != nil {
		return WorkTarget{}, err
	}
	item.UpdatedAt, err = parseTime(updated)
	return item, err
}

func validWorkSessionStatus(status protocol.WorkSessionStatus) bool {
	switch status {
	case protocol.WorkSessionPreparing, protocol.WorkSessionReady, protocol.WorkSessionRunning,
		protocol.WorkSessionCompleted, protocol.WorkSessionPartial, protocol.WorkSessionFailed, protocol.WorkSessionCancelled:
		return true
	default:
		return false
	}
}

func validTargetStatus(status protocol.TargetStatus) bool {
	switch status {
	case protocol.TargetPreparing, protocol.TargetReady, protocol.TargetRunning, protocol.TargetIdle,
		protocol.TargetUnavailable, protocol.TargetContextError, protocol.TargetRevoked:
		return true
	default:
		return false
	}
}

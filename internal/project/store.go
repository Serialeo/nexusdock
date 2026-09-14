package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/core"
)

var (
	ErrProjectNotFound    = errors.New("Project 不存在")
	ErrDeploymentNotFound = errors.New("Deployment 不存在")
	ErrNodeNotFound       = errors.New("AgentDock Node 不存在")
	ErrRevisionConflict   = errors.New("revision conflict")
	ErrDuplicateNode      = errors.New("Project 已关联该节点")
)

type RevisionConflictError struct {
	Resource string
	Current  string
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("%s revision conflict; current=%s", e.Resource, e.Current)
}
func (e *RevisionConflictError) Unwrap() error { return ErrRevisionConflict }

type ValidationError struct{ Message string }

func (e ValidationError) Error() string { return e.Message }

func invalid(message string) error { return ValidationError{Message: message} }

type Project struct {
	ID                  string       `json:"id"`
	Name                string       `json:"name"`
	OrchestrationPolicy string       `json:"orchestration_policy"`
	Revision            string       `json:"revision"`
	Enabled             bool         `json:"enabled"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
	Deployments         []Deployment `json:"deployments,omitempty"`
}

type Deployment struct {
	ID              string                         `json:"id"`
	ProjectID       string                         `json:"project_id"`
	NodeID          string                         `json:"node_id"`
	WorkingFolder   string                         `json:"working_folder"`
	Role            string                         `json:"role"`
	Purpose         string                         `json:"purpose"`
	Permissions     protocol.DeploymentPermissions `json:"permissions"`
	DesiredRevision string                         `json:"desired_revision"`
	AppliedRevision string                         `json:"applied_revision"`
	Enabled         bool                           `json:"enabled"`
	ApplyStatus     string                         `json:"apply_status"`
	LastError       string                         `json:"last_error,omitempty"`
	CreatedAt       time.Time                      `json:"created_at"`
	UpdatedAt       time.Time                      `json:"updated_at"`
}

type Removal struct {
	DeploymentID string    `json:"deployment_id"`
	ProjectID    string    `json:"project_id"`
	NodeID       string    `json:"node_id"`
	RemovedAt    time.Time `json:"removed_at"`
	LastError    string    `json:"last_error,omitempty"`
}

type CreateProjectInput struct {
	Name                string
	OrchestrationPolicy string
	Enabled             bool
}

type UpdateProjectInput struct {
	ExpectedRevision    string
	Name                string
	OrchestrationPolicy string
	Enabled             bool
}

type CreateDeploymentInput struct {
	ProjectID     string
	NodeID        string
	WorkingFolder string
	Role          string
	Purpose       string
	Permissions   protocol.DeploymentPermissions
	Enabled       bool
}

type UpdateDeploymentInput struct {
	ExpectedRevision string
	WorkingFolder    string
	Role             string
	Purpose          string
	Permissions      protocol.DeploymentPermissions
	Enabled          bool
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("Project 数据库不能为空")
	}
	return &Store{db: db, now: time.Now}, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, orchestration_policy, revision, enabled, created_at, updated_at
		FROM projects ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("列出 Projects: %w", err)
	}
	defer rows.Close()
	items := make([]Project, 0)
	for rows.Next() {
		item, scanErr := scanProject(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 Projects: %w", err)
	}
	return items, nil
}

func (s *Store) GetProject(ctx context.Context, id string) (Project, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Project{}, invalid("Project ID 不能为空")
	}
	item, err := scanProject(s.db.QueryRowContext(ctx, `SELECT id, name, orchestration_policy, revision, enabled, created_at, updated_at FROM projects WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, err
	}
	deployments, err := s.ListDeployments(ctx, id)
	if err != nil {
		return Project{}, err
	}
	item.Deployments = deployments
	return item, nil
}

func (s *Store) CreateProject(ctx context.Context, input CreateProjectInput) (Project, error) {
	name, policy, err := validateProjectFields(input.Name, input.OrchestrationPolicy)
	if err != nil {
		return Project{}, err
	}
	id, err := core.NewID("project")
	if err != nil {
		return Project{}, err
	}
	now := s.now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO projects(id, name, orchestration_policy, revision, enabled, created_at, updated_at)
		VALUES(?, ?, ?, 1, ?, ?, ?)`, id, name, policy, boolInt(input.Enabled), formatTime(now), formatTime(now))
	if err != nil {
		return Project{}, fmt.Errorf("创建 Project: %w", err)
	}
	return s.GetProject(ctx, id)
}

func (s *Store) UpdateProject(ctx context.Context, id string, input UpdateProjectInput) (Project, error) {
	id = strings.TrimSpace(id)
	expected, err := parseRevision(input.ExpectedRevision)
	if err != nil {
		return Project{}, err
	}
	name, policy, err := validateProjectFields(input.Name, input.OrchestrationPolicy)
	if err != nil {
		return Project{}, err
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE projects
		SET name = ?, orchestration_policy = ?, enabled = ?, revision = revision + 1, updated_at = ?
		WHERE id = ? AND revision = ?`, name, policy, boolInt(input.Enabled), formatTime(now), id, expected)
	if err != nil {
		return Project{}, fmt.Errorf("更新 Project: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Project{}, s.projectConflictOrNotFound(ctx, id)
	}
	return s.GetProject(ctx, id)
}

func (s *Store) DeleteProject(ctx context.Context, id, expectedRevision string) ([]Deployment, error) {
	id = strings.TrimSpace(id)
	expected, err := parseRevision(expectedRevision)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("开始删除 Project 事务: %w", err)
	}
	defer tx.Rollback()
	deployments, err := listDeploymentsDB(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	removedAt := s.now().UTC()
	for _, deployment := range deployments {
		if err := stageRemoval(ctx, tx, deployment, removedAt); err != nil {
			return nil, err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ? AND revision = ?`, id, expected)
	if err != nil {
		return nil, fmt.Errorf("删除 Project: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		var current int
		err := tx.QueryRowContext(ctx, `SELECT revision FROM projects WHERE id = ?`, id).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrProjectNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("读取 Project revision: %w", err)
		}
		return nil, &RevisionConflictError{Resource: "project", Current: revisionString(current)}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交删除 Project: %w", err)
	}
	return deployments, nil
}

func (s *Store) ListDeployments(ctx context.Context, projectID string) ([]Deployment, error) {
	return listDeploymentsDB(ctx, s.db, strings.TrimSpace(projectID))
}

func (s *Store) ListDeploymentsForNode(ctx context.Context, nodeID string) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx, deploymentSelect+` WHERE node_id = ? ORDER BY project_id, id`, strings.TrimSpace(nodeID))
	if err != nil {
		return nil, fmt.Errorf("列出 Node Deployments: %w", err)
	}
	return scanDeployments(rows)
}

func (s *Store) GetDeployment(ctx context.Context, projectID, deploymentID string) (Deployment, error) {
	item, err := scanDeployment(s.db.QueryRowContext(ctx, deploymentSelect+` WHERE project_id = ? AND id = ?`, strings.TrimSpace(projectID), strings.TrimSpace(deploymentID)))
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrDeploymentNotFound
	}
	return item, err
}

func (s *Store) CreateDeployment(ctx context.Context, input CreateDeploymentInput) (Deployment, error) {
	projectID := strings.TrimSpace(input.ProjectID)
	nodeID := strings.TrimSpace(input.NodeID)
	working, role, purpose, permissions, err := validateDeploymentFields(input.WorkingFolder, input.Role, input.Purpose, input.Permissions)
	if err != nil {
		return Deployment{}, err
	}
	if projectID == "" || nodeID == "" {
		return Deployment{}, invalid("project_id 和 node_id 不能为空")
	}
	var projectExists, nodeExists int
	var nodeOS string
	if err := s.db.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM projects WHERE id = ?),
		EXISTS(SELECT 1 FROM agentdock_devices WHERE id = ?),
		COALESCE((SELECT os FROM agentdock_devices WHERE id = ?), '')`, projectID, nodeID, nodeID).Scan(&projectExists, &nodeExists, &nodeOS); err != nil {
		return Deployment{}, fmt.Errorf("校验 Deployment 关联: %w", err)
	}
	if projectExists == 0 {
		return Deployment{}, ErrProjectNotFound
	}
	if nodeExists == 0 {
		return Deployment{}, ErrNodeNotFound
	}
	if err := validateWorkingFolderForOS(working, nodeOS); err != nil {
		return Deployment{}, err
	}
	id, err := core.NewID("deployment")
	if err != nil {
		return Deployment{}, err
	}
	now := s.now().UTC()
	status := "pending"
	_, err = s.db.ExecContext(ctx, `INSERT INTO project_deployments(
		id, project_id, node_id, working_folder, role, purpose, files_permission, computer_permission,
		shell_enabled, browser_enabled, dynamic_mcp_enabled, acp_enabled,
		desired_revision, applied_revision, enabled, apply_status, last_error, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0, ?, ?, '', ?, ?)`,
		id, projectID, nodeID, working, role, purpose, string(permissions.Files), string(permissions.Computer),
		boolInt(permissions.Shell), boolInt(permissions.Browser), boolInt(permissions.DynamicMCP), boolInt(permissions.ACP),
		boolInt(input.Enabled), status, formatTime(now), formatTime(now))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			return Deployment{}, ErrDuplicateNode
		}
		return Deployment{}, fmt.Errorf("创建 Deployment: %w", err)
	}
	return s.GetDeployment(ctx, projectID, id)
}

func (s *Store) UpdateDeployment(ctx context.Context, projectID, id string, input UpdateDeploymentInput) (Deployment, error) {
	expected, err := parseRevision(input.ExpectedRevision)
	if err != nil {
		return Deployment{}, err
	}
	working, role, purpose, permissions, err := validateDeploymentFields(input.WorkingFolder, input.Role, input.Purpose, input.Permissions)
	if err != nil {
		return Deployment{}, err
	}
	var nodeOS string
	err = s.db.QueryRowContext(ctx, `SELECT n.os
		FROM project_deployments d
		JOIN agentdock_devices n ON n.id = d.node_id
		WHERE d.project_id = ? AND d.id = ?`, strings.TrimSpace(projectID), strings.TrimSpace(id)).Scan(&nodeOS)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, s.deploymentConflictOrNotFound(ctx, projectID, id)
	}
	if err != nil {
		return Deployment{}, fmt.Errorf("读取 Deployment Node 平台: %w", err)
	}
	if err := validateWorkingFolderForOS(working, nodeOS); err != nil {
		return Deployment{}, err
	}
	status := "pending"
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE project_deployments SET
		working_folder = ?, role = ?, purpose = ?, files_permission = ?, computer_permission = ?, shell_enabled = ?, browser_enabled = ?, dynamic_mcp_enabled = ?, acp_enabled = ?,
		desired_revision = desired_revision + 1, enabled = ?, apply_status = ?, last_error = '', updated_at = ?
		WHERE project_id = ? AND id = ? AND desired_revision = ?`,
		working, role, purpose, string(permissions.Files), string(permissions.Computer), boolInt(permissions.Shell), boolInt(permissions.Browser), boolInt(permissions.DynamicMCP), boolInt(permissions.ACP),
		boolInt(input.Enabled), status, formatTime(now), strings.TrimSpace(projectID), strings.TrimSpace(id), expected)
	if err != nil {
		return Deployment{}, fmt.Errorf("更新 Deployment: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Deployment{}, s.deploymentConflictOrNotFound(ctx, projectID, id)
	}
	return s.GetDeployment(ctx, projectID, id)
}

func (s *Store) DeleteDeployment(ctx context.Context, projectID, id, expectedRevision string) (Deployment, error) {
	expected, err := parseRevision(expectedRevision)
	if err != nil {
		return Deployment{}, err
	}
	projectID = strings.TrimSpace(projectID)
	id = strings.TrimSpace(id)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Deployment{}, fmt.Errorf("开始删除 Deployment 事务: %w", err)
	}
	defer tx.Rollback()
	current, err := scanDeployment(tx.QueryRowContext(ctx, deploymentSelect+` WHERE project_id = ? AND id = ?`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrDeploymentNotFound
	}
	if err != nil {
		return Deployment{}, err
	}
	if current.DesiredRevision != revisionString(expected) {
		return Deployment{}, &RevisionConflictError{Resource: "deployment", Current: current.DesiredRevision}
	}
	if err := stageRemoval(ctx, tx, current, s.now().UTC()); err != nil {
		return Deployment{}, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM project_deployments WHERE project_id = ? AND id = ? AND desired_revision = ?`, projectID, id, expected)
	if err != nil {
		return Deployment{}, fmt.Errorf("删除 Deployment: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Deployment{}, &RevisionConflictError{Resource: "deployment", Current: current.DesiredRevision}
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, fmt.Errorf("提交删除 Deployment: %w", err)
	}
	return current, nil
}

// RecordApplyResult only updates the exact desired revision that produced the RPC.
// A late response from an older revision can never mark a newer desired state applied.
func (s *Store) RecordApplyResult(ctx context.Context, projectID, id, desiredRevision string, applyErr error) (Deployment, error) {
	desired, err := parseRevision(desiredRevision)
	if err != nil {
		return Deployment{}, err
	}
	status := "applied"
	lastError := ""
	appliedRevision := desired
	if applyErr != nil {
		status = "failed"
		lastError = boundedError(applyErr.Error(), 4096)
		appliedRevision = -1
	}
	now := s.now().UTC()
	var result sql.Result
	if appliedRevision >= 0 {
		result, err = s.db.ExecContext(ctx, `UPDATE project_deployments SET applied_revision = ?, apply_status = CASE WHEN enabled = 1 THEN 'applied' ELSE 'disabled' END, last_error = ?, updated_at = ?
			WHERE project_id = ? AND id = ? AND desired_revision = ?`, desired, lastError, formatTime(now), strings.TrimSpace(projectID), strings.TrimSpace(id), desired)
	} else {
		result, err = s.db.ExecContext(ctx, `UPDATE project_deployments SET apply_status = ?, last_error = ?, updated_at = ?
			WHERE project_id = ? AND id = ? AND desired_revision = ?`, status, lastError, formatTime(now), strings.TrimSpace(projectID), strings.TrimSpace(id), desired)
	}
	if err != nil {
		return Deployment{}, fmt.Errorf("记录 Deployment apply 结果: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		// Desired state changed while the Node RPC was in flight. Preserve the newer pending state.
		return s.GetDeployment(ctx, projectID, id)
	}
	return s.GetDeployment(ctx, projectID, id)
}

func (s *Store) MarkPending(ctx context.Context, projectID, id, desiredRevision, message string) (Deployment, error) {
	desired, err := parseRevision(desiredRevision)
	if err != nil {
		return Deployment{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE project_deployments SET apply_status = 'pending', last_error = ?, updated_at = ?
		WHERE project_id = ? AND id = ? AND desired_revision = ?`, boundedError(message, 4096), formatTime(s.now().UTC()), strings.TrimSpace(projectID), strings.TrimSpace(id), desired)
	if err != nil {
		return Deployment{}, fmt.Errorf("标记 Deployment pending: %w", err)
	}
	return s.GetDeployment(ctx, projectID, id)
}

func (s *Store) ListPendingRemovalsForNode(ctx context.Context, nodeID string) ([]Removal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT deployment_id, project_id, node_id, removed_at, last_error
		FROM project_deployment_removals WHERE node_id = ? ORDER BY removed_at, deployment_id`, strings.TrimSpace(nodeID))
	if err != nil {
		return nil, fmt.Errorf("列出 Deployment removals: %w", err)
	}
	defer rows.Close()
	items := make([]Removal, 0)
	for rows.Next() {
		var item Removal
		var removedAt string
		if err := rows.Scan(&item.DeploymentID, &item.ProjectID, &item.NodeID, &removedAt, &item.LastError); err != nil {
			return nil, fmt.Errorf("读取 Deployment removal: %w", err)
		}
		item.RemovedAt, err = parseTime(removedAt)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 Deployment removals: %w", err)
	}
	return items, nil
}

func (s *Store) RecordRemovalResult(ctx context.Context, deploymentID string, removeErr error) error {
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return invalid("deployment_id 不能为空")
	}
	if removeErr == nil {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM project_deployment_removals WHERE deployment_id = ?`, deploymentID); err != nil {
			return fmt.Errorf("清除 Deployment removal: %w", err)
		}
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE project_deployment_removals SET last_error = ? WHERE deployment_id = ?`, boundedError(removeErr.Error(), 4096), deploymentID); err != nil {
		return fmt.Errorf("记录 Deployment removal 失败: %w", err)
	}
	return nil
}

func stageRemoval(ctx context.Context, db core.DBTX, deployment Deployment, removedAt time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO project_deployment_removals(deployment_id, project_id, node_id, removed_at, last_error)
		VALUES(?, ?, ?, ?, '')
		ON CONFLICT(deployment_id) DO UPDATE SET project_id = excluded.project_id, node_id = excluded.node_id, removed_at = excluded.removed_at, last_error = ''`,
		deployment.ID, deployment.ProjectID, deployment.NodeID, formatTime(removedAt))
	if err != nil {
		return fmt.Errorf("记录 Deployment removal: %w", err)
	}
	return nil
}

func (s *Store) projectConflictOrNotFound(ctx context.Context, id string) error {
	var revision int
	err := s.db.QueryRowContext(ctx, `SELECT revision FROM projects WHERE id = ?`, strings.TrimSpace(id)).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProjectNotFound
	}
	if err != nil {
		return fmt.Errorf("读取 Project revision: %w", err)
	}
	return &RevisionConflictError{Resource: "project", Current: revisionString(revision)}
}

func (s *Store) deploymentConflictOrNotFound(ctx context.Context, projectID, id string) error {
	var revision int
	err := s.db.QueryRowContext(ctx, `SELECT desired_revision FROM project_deployments WHERE project_id = ? AND id = ?`, strings.TrimSpace(projectID), strings.TrimSpace(id)).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDeploymentNotFound
	}
	if err != nil {
		return fmt.Errorf("读取 Deployment revision: %w", err)
	}
	return &RevisionConflictError{Resource: "deployment", Current: revisionString(revision)}
}

const deploymentSelect = `SELECT id, project_id, node_id, working_folder, role, purpose, files_permission, computer_permission,
	shell_enabled, browser_enabled, dynamic_mcp_enabled, acp_enabled, desired_revision, applied_revision,
	enabled, apply_status, last_error, created_at, updated_at FROM project_deployments`

func listDeploymentsDB(ctx context.Context, db core.DBTX, projectID string) ([]Deployment, error) {
	rows, err := db.QueryContext(ctx, deploymentSelect+` WHERE project_id = ? ORDER BY id`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("列出 Deployments: %w", err)
	}
	return scanDeployments(rows)
}

func scanDeployments(rows *sql.Rows) ([]Deployment, error) {
	defer rows.Close()
	items := make([]Deployment, 0)
	for rows.Next() {
		item, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 Deployments: %w", err)
	}
	return items, nil
}

type scanner interface{ Scan(...any) error }

func scanProject(row scanner) (Project, error) {
	var item Project
	var revision, enabled int
	var created, updated string
	if err := row.Scan(&item.ID, &item.Name, &item.OrchestrationPolicy, &revision, &enabled, &created, &updated); err != nil {
		return Project{}, err
	}
	item.Revision = revisionString(revision)
	item.Enabled = enabled != 0
	var err error
	item.CreatedAt, err = parseTime(created)
	if err != nil {
		return Project{}, err
	}
	item.UpdatedAt, err = parseTime(updated)
	return item, err
}

func scanDeployment(row scanner) (Deployment, error) {
	var item Deployment
	var files, computer string
	var shell, browser, dynamicMCP, acp, desired, applied, enabled int
	var created, updated string
	if err := row.Scan(&item.ID, &item.ProjectID, &item.NodeID, &item.WorkingFolder, &item.Role, &item.Purpose, &files, &computer,
		&shell, &browser, &dynamicMCP, &acp, &desired, &applied, &enabled, &item.ApplyStatus, &item.LastError, &created, &updated); err != nil {
		return Deployment{}, err
	}
	item.Permissions = protocol.DeploymentPermissions{Files: protocol.FileCapability(files), Computer: protocol.ComputerPermission(computer), Shell: shell != 0, Browser: browser != 0, DynamicMCP: dynamicMCP != 0, ACP: acp != 0}
	item.DesiredRevision = revisionString(desired)
	if applied > 0 {
		item.AppliedRevision = revisionString(applied)
	}
	item.Enabled = enabled != 0
	var err error
	item.CreatedAt, err = parseTime(created)
	if err != nil {
		return Deployment{}, err
	}
	item.UpdatedAt, err = parseTime(updated)
	return item, err
}

func validateProjectFields(name, policy string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", invalid("Project 名称不能为空")
	}
	return name, policy, nil
}

func validateDeploymentFields(working, role, purpose string, permissions protocol.DeploymentPermissions) (string, string, string, protocol.DeploymentPermissions, error) {
	working = strings.TrimSpace(working)
	if strings.ContainsRune(working, '\x00') {
		return "", "", "", permissions, invalid("working_folder 不能包含 NUL")
	}
	if err := permissions.Validate(); err != nil {
		return "", "", "", permissions, invalid("permissions 无效: " + err.Error())
	}
	return working, role, purpose, permissions, nil
}

func validateWorkingFolderForOS(working, nodeOS string) error {
	working = strings.TrimSpace(working)
	if working == "" {
		return nil
	}
	nodeOS = strings.ToLower(strings.TrimSpace(nodeOS))
	posixAbsolute := strings.HasPrefix(working, "/")
	windowsAbsolute := isWindowsAbsolutePath(working)
	valid := false
	switch nodeOS {
	case "linux", "darwin":
		valid = posixAbsolute
	case "windows":
		valid = windowsAbsolute
	default:
		// A paired-but-offline Node may not have reported its OS yet. Preserve the
		// user's native path verbatim, but still reject relative paths.
		valid = posixAbsolute || windowsAbsolute
	}
	if !valid {
		if nodeOS == "" {
			nodeOS = "unknown"
		}
		return invalid(fmt.Sprintf("working_folder 必须是目标 Node (%s) 的原生绝对路径", nodeOS))
	}
	return nil
}

func isWindowsAbsolutePath(value string) bool {
	if len(value) >= 3 && isASCIIAlpha(value[0]) && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		return true
	}
	if len(value) < 5 || !((value[0] == '\\' && value[1] == '\\') || (value[0] == '/' && value[1] == '/')) {
		return false
	}
	// UNC requires both a non-empty server and share component. Accept either
	// slash style without normalizing casing or separators.
	rest := value[2:]
	separator := func(b byte) bool { return b == '\\' || b == '/' }
	serverEnd := -1
	for i := 0; i < len(rest); i++ {
		if separator(rest[i]) {
			serverEnd = i
			break
		}
	}
	if serverEnd <= 0 || serverEnd+1 >= len(rest) {
		return false
	}
	share := rest[serverEnd+1:]
	for i := 0; i < len(share); i++ {
		if separator(share[i]) {
			return i > 0
		}
	}
	return share != ""
}

func isASCIIAlpha(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}

func parseRevision(value string) (int, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "rev-") {
		return 0, invalid("revision 必须使用 rev-N 格式")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(value, "rev-"))
	if err != nil || n < 1 {
		return 0, invalid("revision 必须使用 rev-N 格式")
	}
	return n, nil
}

func revisionString(value int) string { return fmt.Sprintf("rev-%d", value) }
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("解析 Project 时间: %w", err)
	}
	return parsed, nil
}
func boundedError(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}

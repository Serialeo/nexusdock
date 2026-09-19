package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/agentdock"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

const projectNodeApplyTimeout = 15 * time.Second

type projectCreateRequest struct {
	Name                string `json:"name"`
	OrchestrationPolicy string `json:"orchestration_policy"`
	Enabled             *bool  `json:"enabled,omitempty"`
}

type projectUpdateRequest struct {
	ExpectedRevision    string `json:"expected_revision"`
	Name                string `json:"name"`
	OrchestrationPolicy string `json:"orchestration_policy"`
	Enabled             bool   `json:"enabled"`
}

type projectDeleteRequest struct {
	ExpectedRevision string `json:"expected_revision"`
}

type deploymentCreateRequest struct {
	NodeID        string                         `json:"node_id"`
	WorkingFolder string                         `json:"working_folder"`
	Role          string                         `json:"role"`
	Purpose       string                         `json:"purpose"`
	Permissions   protocol.DeploymentPermissions `json:"permissions"`
	Enabled       *bool                          `json:"enabled,omitempty"`
}

type deploymentUpdateRequest struct {
	ExpectedRevision string                         `json:"expected_revision"`
	WorkingFolder    string                         `json:"working_folder"`
	Role             string                         `json:"role"`
	Purpose          string                         `json:"purpose"`
	Permissions      protocol.DeploymentPermissions `json:"permissions"`
	Enabled          bool                           `json:"enabled"`
}

type deploymentDeleteRequest struct {
	ExpectedRevision string `json:"expected_revision"`
}

func (s *Server) registerProjectRoutes(mux *http.ServeMux, protected func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /v1/sessions/node", protected(s.nodeSessionList))
	mux.HandleFunc("GET /v1/sessions/node/{workSessionID}", protected(s.nodeSessionGet))
	mux.HandleFunc("GET /v1/projects", protected(s.projectList))
	mux.HandleFunc("POST /v1/projects", protected(s.projectCreate))
	mux.HandleFunc("GET /v1/projects/{projectID}", protected(s.projectGet))
	mux.HandleFunc("PUT /v1/projects/{projectID}", protected(s.projectUpdate))
	mux.HandleFunc("DELETE /v1/projects/{projectID}", protected(s.projectDelete))
	mux.HandleFunc("GET /v1/projects/{projectID}/deployments", protected(s.deploymentList))
	mux.HandleFunc("POST /v1/projects/{projectID}/deployments", protected(s.deploymentCreate))
	mux.HandleFunc("GET /v1/projects/{projectID}/deployments/{deploymentID}", protected(s.deploymentGet))
	mux.HandleFunc("PUT /v1/projects/{projectID}/deployments/{deploymentID}", protected(s.deploymentUpdate))
	mux.HandleFunc("DELETE /v1/projects/{projectID}/deployments/{deploymentID}", protected(s.deploymentDelete))
	mux.HandleFunc("POST /v1/projects/{projectID}/deployments/{deploymentID}/apply", protected(s.deploymentApply))
	mux.HandleFunc("GET /v1/projects/{projectID}/deployments/{deploymentID}/prompt", protected(s.projectPromptPreview))
	mux.HandleFunc("PUT /v1/projects/{projectID}/deployments/{deploymentID}/prompt", protected(s.projectPromptWrite))
	mux.HandleFunc("GET /v1/projects/{projectID}/sessions", protected(s.projectSessionList))
	mux.HandleFunc("GET /v1/projects/{projectID}/sessions/{workSessionID}", protected(s.projectSessionGet))
}

func (s *Server) projectList(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	items, err := s.projects.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PROJECT_LIST_FAILED", "无法读取 Projects")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "projects": items, "count": len(items)})
}

func (s *Server) projectGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	item, err := s.projects.GetUserProject(r.Context(), r.PathValue("projectID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project": item})
}

func (s *Server) projectCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	var request projectCreateRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	item, err := s.projects.CreateProject(r.Context(), projectstore.CreateProjectInput{Name: request.Name, OrchestrationPolicy: request.OrchestrationPolicy, Enabled: enabled})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "project": item})
}

func (s *Server) projectUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	var request projectUpdateRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if _, err := s.projects.GetUserProject(r.Context(), r.PathValue("projectID")); err != nil {
		writeProjectError(w, err)
		return
	}
	item, err := s.projects.UpdateProject(r.Context(), r.PathValue("projectID"), projectstore.UpdateProjectInput{
		ExpectedRevision: request.ExpectedRevision, Name: request.Name, OrchestrationPolicy: request.OrchestrationPolicy, Enabled: request.Enabled,
	})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if !item.Enabled {
		revoked, revokeErr := s.projects.RevokeTargetsForProject(r.Context(), item.ID, "Project disabled")
		if revokeErr != nil {
			writeError(w, http.StatusInternalServerError, "PROJECT_TARGET_REVOKE_FAILED", revokeErr.Error())
			return
		}
		s.revokeProjectTargetsOnNodes(r.Context(), revoked)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project": item})
}

func (s *Server) projectDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	var request projectDeleteRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	projectID := r.PathValue("projectID")
	if _, err := s.projects.GetUserProject(r.Context(), projectID); err != nil {
		writeProjectError(w, err)
		return
	}
	deployments, err := s.projects.DeleteProject(r.Context(), projectID, request.ExpectedRevision)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	revoked, revokeErr := s.projects.RevokeTargetsForProject(r.Context(), projectID, "Project deleted")
	if revokeErr != nil {
		writeError(w, http.StatusInternalServerError, "PROJECT_TARGET_REVOKE_FAILED", revokeErr.Error())
		return
	}
	s.revokeProjectTargetsOnNodes(r.Context(), revoked)
	for _, deployment := range deployments {
		s.tryRemoveProjectDeployment(r.Context(), deployment)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project_id": projectID, "deleted": true, "deployment_removals": len(deployments)})
}

func (s *Server) deploymentGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	if _, err := s.projects.GetUserProject(r.Context(), r.PathValue("projectID")); err != nil {
		writeProjectError(w, err)
		return
	}
	item, err := s.projects.GetDeployment(r.Context(), r.PathValue("projectID"), r.PathValue("deploymentID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	item = s.projectDeploymentEffectiveView(r.Context(), item)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployment": item})
}

func (s *Server) deploymentList(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	if _, err := s.projects.GetUserProject(r.Context(), r.PathValue("projectID")); err != nil {
		writeProjectError(w, err)
		return
	}
	items, err := s.projects.ListDeployments(r.Context(), r.PathValue("projectID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	for i := range items {
		items[i] = s.projectDeploymentEffectiveView(r.Context(), items[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployments": items, "count": len(items)})
}

func (s *Server) deploymentCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	var request deploymentCreateRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	request.Permissions.FullAccess = false
	item, err := s.projects.CreateDeployment(r.Context(), projectstore.CreateDeploymentInput{
		ProjectID: r.PathValue("projectID"), NodeID: request.NodeID, WorkingFolder: request.WorkingFolder,
		Role: request.Role, Purpose: request.Purpose, Permissions: request.Permissions, Enabled: enabled,
	})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	item = s.tryApplyProjectDeployment(r.Context(), item)
	item = s.projectDeploymentEffectiveView(r.Context(), item)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "deployment": item})
}

func (s *Server) deploymentUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	var request deploymentUpdateRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if _, err := s.projects.GetUserProject(r.Context(), r.PathValue("projectID")); err != nil {
		writeProjectError(w, err)
		return
	}
	request.Permissions.FullAccess = false
	item, err := s.projects.UpdateDeployment(r.Context(), r.PathValue("projectID"), r.PathValue("deploymentID"), projectstore.UpdateDeploymentInput{
		ExpectedRevision: request.ExpectedRevision, WorkingFolder: request.WorkingFolder, Role: request.Role,
		Purpose: request.Purpose, Permissions: request.Permissions, Enabled: request.Enabled,
	})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	revoked, revokeErr := s.projects.RevokeTargetsForDeployment(r.Context(), item.ID, "Deployment configuration changed")
	if revokeErr != nil {
		writeError(w, http.StatusInternalServerError, "PROJECT_TARGET_REVOKE_FAILED", revokeErr.Error())
		return
	}
	s.revokeProjectTargetsOnNodes(r.Context(), revoked)
	item = s.tryApplyProjectDeployment(r.Context(), item)
	item = s.projectDeploymentEffectiveView(r.Context(), item)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployment": item})
}

func (s *Server) deploymentDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	var request deploymentDeleteRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	projectID := r.PathValue("projectID")
	deploymentID := r.PathValue("deploymentID")
	if _, err := s.projects.GetUserProject(r.Context(), projectID); err != nil {
		writeProjectError(w, err)
		return
	}
	item, err := s.projects.DeleteDeployment(r.Context(), projectID, deploymentID, request.ExpectedRevision)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	revoked, revokeErr := s.projects.RevokeTargetsForDeployment(r.Context(), item.ID, "Deployment deleted")
	if revokeErr != nil {
		writeError(w, http.StatusInternalServerError, "PROJECT_TARGET_REVOKE_FAILED", revokeErr.Error())
		return
	}
	s.revokeProjectTargetsOnNodes(r.Context(), revoked)
	s.tryRemoveProjectDeployment(r.Context(), item)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployment_id": deploymentID, "deleted": true})
}

func (s *Server) deploymentApply(w http.ResponseWriter, r *http.Request) {
	if !s.requireProjectStore(w) {
		return
	}
	if _, err := s.projects.GetUserProject(r.Context(), r.PathValue("projectID")); err != nil {
		writeProjectError(w, err)
		return
	}
	item, err := s.projects.GetDeployment(r.Context(), r.PathValue("projectID"), r.PathValue("deploymentID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	item = s.tryApplyProjectDeployment(r.Context(), item)
	item = s.projectDeploymentEffectiveView(r.Context(), item)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployment": item})
}

func (s *Server) projectDeploymentEffectiveView(ctx context.Context, deployment projectstore.Deployment) projectstore.Deployment {
	deployment.Permissions.FullAccess = false
	if s.agentDock == nil {
		return deployment
	}
	node, err := s.agentDock.Get(ctx, deployment.NodeID)
	if err == nil {
		deployment.Permissions.FullAccess = node.FullAccess
	}
	return deployment
}

func (s *Server) requireProjectStore(w http.ResponseWriter) bool {
	if s.projects == nil {
		writeError(w, http.StatusServiceUnavailable, "PROJECT_STORE_UNAVAILABLE", "Project 存储不可用")
		return false
	}
	return true
}

func (s *Server) tryApplyProjectDeployment(ctx context.Context, deployment projectstore.Deployment) projectstore.Deployment {
	if s.projects == nil {
		return deployment
	}
	unlock, lockErr := s.acquireOperation(ctx, operationLockKey{scope: "deployment_apply", primary: deployment.ID})
	if lockErr != nil {
		return deployment
	}
	defer unlock()
	current, currentErr := s.projects.GetDeployment(ctx, deployment.ProjectID, deployment.ID)
	if currentErr != nil {
		return deployment
	}
	if current.DesiredRevision != deployment.DesiredRevision {
		return current
	}
	deployment = current
	if s.agentDock == nil || s.agentDockHub == nil {
		updated, err := s.projects.MarkPending(ctx, deployment.ProjectID, deployment.ID, deployment.DesiredRevision, "AgentDock 连接服务不可用")
		if err == nil {
			return updated
		}
		return deployment
	}
	node, err := s.agentDock.Get(ctx, deployment.NodeID)
	if err != nil {
		updated, recordErr := s.projects.RecordApplyResult(ctx, deployment.ProjectID, deployment.ID, deployment.DesiredRevision, err)
		if recordErr == nil {
			return updated
		}
		return deployment
	}
	if !node.Enabled {
		updated, recordErr := s.projects.RecordApplyResult(ctx, deployment.ProjectID, deployment.ID, deployment.DesiredRevision, agentdock.ErrNodeDisabled)
		if recordErr == nil {
			return updated
		}
		return deployment
	}
	if strings.TrimSpace(node.ProtocolVersion) != "" && !node.IsCurrent() {
		err := fmt.Errorf("AgentDock node protocol %q is not Bridge v%s", node.ProtocolVersion, agentdock.ConnectionProtocolVersion)
		updated, recordErr := s.projects.RecordApplyResult(ctx, deployment.ProjectID, deployment.ID, deployment.DesiredRevision, err)
		if recordErr == nil {
			return updated
		}
		return deployment
	}
	if !s.agentDockHub.Online(node.ID) {
		updated, pendingErr := s.projects.MarkPending(ctx, deployment.ProjectID, deployment.ID, deployment.DesiredRevision, agentdock.ErrNodeOffline.Error())
		if pendingErr == nil {
			return updated
		}
		return deployment
	}
	if !node.IsCurrent() {
		updated, recordErr := s.projects.RecordApplyResult(ctx, deployment.ProjectID, deployment.ID, deployment.DesiredRevision, errors.New("AgentDock node did not negotiate Bridge v4"))
		if recordErr == nil {
			return updated
		}
		return deployment
	}

	applyStatus := protocol.DeploymentApplyApplied
	if !deployment.Enabled {
		applyStatus = protocol.DeploymentApplyDisabled
	}
	effectivePermissions := deployment.Permissions
	effectivePermissions.FullAccess = node.FullAccess
	payload := protocol.Deployment{
		ID: deployment.ID, ProjectID: deployment.ProjectID, NodeID: deployment.NodeID,
		WorkingFolder: deployment.WorkingFolder, Role: deployment.Role, Purpose: deployment.Purpose,
		Permissions: effectivePermissions, DesiredRevision: deployment.DesiredRevision, AppliedRevision: deployment.DesiredRevision,
		Enabled: deployment.Enabled, ApplyStatus: applyStatus,
	}
	invokeCtx, cancel := context.WithTimeout(ctx, projectNodeApplyTimeout)
	result, invokeErr := s.agentDockHub.Invoke(invokeCtx, node.ID, protocol.OperationProjectDeploymentApply, payload)
	cancel()
	if invokeErr == nil {
		invokeErr = validateProjectDeploymentApplyResult(result, deployment, effectivePermissions)
	}
	updated, recordErr := s.projects.RecordApplyResult(ctx, deployment.ProjectID, deployment.ID, deployment.DesiredRevision, invokeErr)
	if recordErr != nil {
		if s.logger != nil {
			s.logger.Warn("记录 Project Deployment apply 结果失败", "deployment_id", deployment.ID, "error", recordErr)
		}
		return deployment
	}
	return updated
}

func (s *Server) tryRemoveProjectDeployment(ctx context.Context, deployment projectstore.Deployment) {
	if s.projects == nil {
		return
	}
	if s.agentDock == nil || s.agentDockHub == nil {
		_ = s.projects.RecordRemovalResult(ctx, deployment.ID, errors.New("AgentDock 连接服务不可用"))
		return
	}
	node, err := s.agentDock.Get(ctx, deployment.NodeID)
	if err != nil {
		_ = s.projects.RecordRemovalResult(ctx, deployment.ID, err)
		return
	}
	if strings.TrimSpace(node.ProtocolVersion) != "" && !node.IsCurrent() {
		_ = s.projects.RecordRemovalResult(ctx, deployment.ID, fmt.Errorf("AgentDock node protocol %q is not Bridge v%s", node.ProtocolVersion, agentdock.ConnectionProtocolVersion))
		return
	}
	if !s.agentDockHub.Online(node.ID) {
		_ = s.projects.RecordRemovalResult(ctx, deployment.ID, agentdock.ErrNodeOffline)
		return
	}
	if !node.IsCurrent() {
		_ = s.projects.RecordRemovalResult(ctx, deployment.ID, errors.New("AgentDock node did not negotiate Bridge v4"))
		return
	}
	invokeCtx, cancel := context.WithTimeout(ctx, projectNodeApplyTimeout)
	result, invokeErr := s.agentDockHub.Invoke(invokeCtx, node.ID, protocol.OperationProjectDeploymentRemove, protocol.ProjectDeploymentRemoveRequest{DeploymentID: deployment.ID})
	cancel()
	if invokeErr == nil {
		invokeErr = validateProjectDeploymentRemoveResult(result, deployment.ID)
	}
	if err := s.projects.RecordRemovalResult(ctx, deployment.ID, invokeErr); err != nil && s.logger != nil {
		s.logger.Warn("记录 Project Deployment removal 结果失败", "deployment_id", deployment.ID, "error", err)
	}
}

func (s *Server) revokeProjectTargetsOnNodes(ctx context.Context, targets []projectstore.WorkTarget) {
	if len(targets) == 0 || s.agentDock == nil || s.agentDockHub == nil {
		return
	}
	for _, target := range targets {
		node, err := s.agentDock.Get(ctx, target.Target.NodeID)
		if err != nil || !node.Enabled || !node.IsCurrent() || !s.agentDockHub.Online(node.ID) {
			continue
		}
		invokeCtx, cancel := context.WithTimeout(ctx, projectNodeApplyTimeout)
		result, invokeErr := s.agentDockHub.Invoke(invokeCtx, node.ID, protocol.OperationProjectTargetRevoke, protocol.ProjectTargetRevokeRequest{TargetID: target.Target.ID})
		cancel()
		if invokeErr == nil {
			invokeErr = validateProjectTargetRevokeResult(result, target.Target.ID)
		}
		if invokeErr != nil && s.logger != nil {
			s.logger.Warn("撤销 Project Target 的 Node binding 失败；Nexus 已保持 revoked", "target_id", target.Target.ID, "node_id", node.ID, "error", invokeErr)
		}
	}
}

func validateProjectTargetRevokeResult(result map[string]any, targetID string) error {
	if result == nil {
		return errors.New("AgentDock Target revoke acknowledgment is empty")
	}
	if got, _ := result["target_id"].(string); got != targetID {
		return errors.New("AgentDock Target revoke acknowledgment target_id mismatch")
	}
	if revoked, _ := result["revoked"].(bool); !revoked {
		return errors.New("AgentDock Target revoke acknowledgment did not confirm revoked=true")
	}
	return nil
}

func validateProjectDeploymentApplyResult(result map[string]any, expected projectstore.Deployment, expectedPermissions protocol.DeploymentPermissions) error {
	if result == nil {
		return errors.New("AgentDock apply acknowledgment is empty")
	}
	raw, ok := result["deployment"]
	if !ok {
		return errors.New("AgentDock apply acknowledgment omitted deployment")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encode AgentDock apply acknowledgment: %w", err)
	}
	var applied protocol.Deployment
	if err := json.Unmarshal(encoded, &applied); err != nil {
		return fmt.Errorf("decode AgentDock apply acknowledgment: %w", err)
	}
	wantStatus := protocol.DeploymentApplyApplied
	if !expected.Enabled {
		wantStatus = protocol.DeploymentApplyDisabled
	}
	if applied.ID != expected.ID || applied.ProjectID != expected.ProjectID || applied.NodeID != expected.NodeID ||
		applied.WorkingFolder != expected.WorkingFolder || applied.Role != expected.Role || applied.Purpose != expected.Purpose ||
		applied.Permissions != expectedPermissions || applied.DesiredRevision != expected.DesiredRevision || applied.AppliedRevision != expected.DesiredRevision ||
		applied.Enabled != expected.Enabled || applied.ApplyStatus != wantStatus {
		return fmt.Errorf("AgentDock apply acknowledgment does not match desired Deployment %s revision %s", expected.ID, expected.DesiredRevision)
	}
	return nil
}

func validateProjectDeploymentRemoveResult(result map[string]any, expectedDeploymentID string) error {
	if result == nil {
		return errors.New("AgentDock remove acknowledgment is empty")
	}
	deploymentID, _ := result["deployment_id"].(string)
	removed, _ := result["removed"].(bool)
	if deploymentID != expectedDeploymentID || !removed {
		return fmt.Errorf("AgentDock remove acknowledgment does not confirm Deployment %s removal", expectedDeploymentID)
	}
	return nil
}

func (s *Server) handleAgentDockHello(node agentdock.Node, hello agentdock.Hello) {
	s.registerNodeTools(node, hello)
	if s.projects == nil || !node.IsCurrent() {
		return
	}
	go s.reconcileProjectNode(node.ID)
}

func (s *Server) reconcileProjectNode(nodeID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	removals, err := s.projects.ListPendingRemovalsForNode(ctx, nodeID)
	if err == nil {
		for _, removal := range removals {
			deployment := projectstore.Deployment{ID: removal.DeploymentID, ProjectID: removal.ProjectID, NodeID: removal.NodeID}
			s.tryRemoveProjectDeployment(ctx, deployment)
		}
	} else if s.logger != nil {
		s.logger.Warn("读取待清理 Project Deployments 失败", "node_id", nodeID, "error", err)
	}
	deployments, err := s.projects.ListDeploymentsForNode(ctx, nodeID)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("读取 Node Project Deployments 失败", "node_id", nodeID, "error", err)
		}
		return
	}
	for _, deployment := range deployments {
		if deployment.AppliedRevision == deployment.DesiredRevision && (deployment.ApplyStatus == "applied" || deployment.ApplyStatus == "disabled") {
			continue
		}
		s.tryApplyProjectDeployment(ctx, deployment)
	}
}

func writeProjectError(w http.ResponseWriter, err error) {
	var validation projectstore.ValidationError
	var conflict *projectstore.RevisionConflictError
	switch {
	case errors.Is(err, projectstore.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "PROJECT_NOT_FOUND", err.Error())
	case errors.Is(err, projectstore.ErrDeploymentNotFound):
		writeError(w, http.StatusNotFound, "DEPLOYMENT_NOT_FOUND", err.Error())
	case errors.Is(err, projectstore.ErrNodeNotFound):
		writeError(w, http.StatusNotFound, "AGENTDOCK_NODE_NOT_FOUND", err.Error())
	case errors.Is(err, projectstore.ErrDuplicateNode):
		writeError(w, http.StatusConflict, "DEPLOYMENT_NODE_EXISTS", err.Error())
	case errors.As(err, &conflict):
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"ok": false, "error": map[string]any{"code": "REVISION_CONFLICT", "message": "资源已被其他编辑器更新，请重新读取后再保存", "details": map[string]any{"resource": conflict.Resource}}})
	case errors.As(err, &validation):
		writeError(w, http.StatusBadRequest, "INVALID_PROJECT", validation.Error())
	default:
		writeError(w, http.StatusInternalServerError, "PROJECT_OPERATION_FAILED", "无法完成 Project 操作")
	}
}

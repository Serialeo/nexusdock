package httpx

import (
	"errors"
	"net/http"
	"strings"

	protocol "github.com/Serialeo/agentdock-protocol"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

type nodeSessionUpdateRequest struct {
	Enabled     bool                           `json:"enabled"`
	Permissions protocol.DeploymentPermissions `json:"permissions"`
}

func (s *Server) agentDockNodeSessionGet(w http.ResponseWriter, r *http.Request) {
	if s.agentDock == nil || s.projects == nil {
		writeError(w, http.StatusServiceUnavailable, "NODE_SESSION_STORE_UNAVAILABLE", "节点临时会话存储不可用")
		return
	}
	nodeID := strings.TrimSpace(r.PathValue("nodeID"))
	if _, err := s.agentDock.Get(r.Context(), nodeID); err != nil {
		writeAgentDockNodeError(w, err)
		return
	}
	item, err := s.projects.GetNodeSession(r.Context(), nodeID)
	if errors.Is(err, projectstore.ErrNodeSessionNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": nodeSessionConfigurationView(projectstore.Deployment{}, false)})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "NODE_SESSION_READ_FAILED", "无法读取节点临时会话配置")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": nodeSessionConfigurationView(item.Deployment, true)})
}

func (s *Server) agentDockNodeSessionUpdate(w http.ResponseWriter, r *http.Request) {
	if s.agentDock == nil || s.projects == nil {
		writeError(w, http.StatusServiceUnavailable, "NODE_SESSION_STORE_UNAVAILABLE", "节点临时会话存储不可用")
		return
	}
	var request nodeSessionUpdateRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Permissions.FullAccess = false
	nodeID := strings.TrimSpace(r.PathValue("nodeID"))
	item, _, err := s.projects.PutNodeSessionConfiguration(r.Context(), nodeID, request.Enabled, request.Permissions)
	if err != nil {
		var validation projectstore.ValidationError
		if errors.As(err, &validation) {
			writeError(w, http.StatusBadRequest, "INVALID_NODE_SESSION", validation.Error())
			return
		}
		writeProjectError(w, err)
		return
	}
	// Always reconcile stale Targets: the desired-state transaction may have
	// committed during an earlier request whose context was cancelled before
	// revocation or apply completed.
	revoked, revokeErr := s.projects.RevokeStaleTargetsForDeployment(r.Context(), item.Deployment.ID, item.Deployment.DesiredRevision, "Node session configuration changed")
	if revokeErr != nil {
		var conflict *projectstore.RevisionConflictError
		if errors.As(revokeErr, &conflict) {
			latest, latestErr := s.projects.GetNodeSession(r.Context(), nodeID)
			if latestErr != nil {
				writeProjectError(w, latestErr)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": nodeSessionConfigurationView(latest.Deployment, true)})
			return
		}
		writeError(w, http.StatusInternalServerError, "NODE_SESSION_TARGET_REVOKE_FAILED", revokeErr.Error())
		return
	}
	s.revokeProjectTargetsOnNodes(r.Context(), revoked)
	wantStatus := string(protocol.DeploymentApplyApplied)
	if !item.Deployment.Enabled {
		wantStatus = string(protocol.DeploymentApplyDisabled)
	}
	if item.Deployment.AppliedRevision != item.Deployment.DesiredRevision || item.Deployment.ApplyStatus != wantStatus {
		item.Deployment = s.tryApplyProjectDeployment(r.Context(), item.Deployment)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": nodeSessionConfigurationView(item.Deployment, true)})
}

func nodeSessionConfigurationView(deployment projectstore.Deployment, configured bool) map[string]any {
	permissions := deployment.Permissions
	permissions.FullAccess = false
	if !configured {
		permissions.Files = protocol.FileCapabilityNone
	}
	view := map[string]any{
		"configured":  configured,
		"enabled":     configured && deployment.Enabled,
		"permissions": permissions,
	}
	if configured {
		view["deployment_id"] = deployment.ID
		view["desired_revision"] = deployment.DesiredRevision
		view["applied_revision"] = deployment.AppliedRevision
		view["apply_status"] = deployment.ApplyStatus
		view["last_error"] = deployment.LastError
	}
	return view
}

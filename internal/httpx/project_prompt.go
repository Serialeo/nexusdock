package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/agentdock"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

type projectPromptWriteRequest struct {
	Scope          string `json:"scope"`
	Content        string `json:"content"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	Create         bool   `json:"create"`
}

func (s *Server) projectPromptPreview(w http.ResponseWriter, r *http.Request) {
	deployment, ok := s.projectPromptReadyDeployment(w, r)
	if !ok {
		return
	}
	cwdRel := strings.TrimSpace(r.URL.Query().Get("cwd_rel"))
	if cwdRel == "" {
		cwdRel = "."
	}
	invokeCtx, cancel := context.WithTimeout(r.Context(), projectNodeApplyTimeout)
	defer cancel()
	result, err := s.agentDockHub.Invoke(invokeCtx, deployment.NodeID, protocol.OperationProjectPromptLoad, protocol.ProjectPromptLoadRequest{
		DeploymentID: deployment.ID, DeploymentRevision: deployment.AppliedRevision, CWDRel: cwdRel,
	})
	if err != nil {
		writeProjectPromptError(w, err)
		return
	}
	decoded, err := decodeProjectPromptLoadResult(result, deployment.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "PROJECT_PROMPT_READ_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "project_id": deployment.ProjectID, "deployment": deployment,
		"working_folder": deployment.WorkingFolder, "result": decoded,
	})
}

func (s *Server) projectPromptWrite(w http.ResponseWriter, r *http.Request) {
	deployment, ok := s.projectPromptReadyDeployment(w, r)
	if !ok {
		return
	}
	var request projectPromptWriteRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Scope = strings.TrimSpace(request.Scope)
	if request.Scope == "" {
		request.Scope = "."
	}
	invokeCtx, cancel := context.WithTimeout(r.Context(), projectNodeApplyTimeout)
	defer cancel()
	result, err := s.agentDockHub.Invoke(invokeCtx, deployment.NodeID, protocol.OperationProjectPromptWrite, protocol.ProjectPromptWriteRequest{
		DeploymentID: deployment.ID, DeploymentRevision: deployment.AppliedRevision,
		Scope: request.Scope, Content: request.Content, ExpectedSHA256: strings.TrimSpace(request.ExpectedSHA256), Create: request.Create,
	})
	if err != nil {
		writeProjectPromptError(w, err)
		return
	}
	decoded, err := decodeProjectPromptWriteResult(result, deployment.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "PROJECT_PROMPT_READ_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "project_id": deployment.ProjectID, "deployment": deployment,
		"working_folder": deployment.WorkingFolder, "result": decoded,
	})
}

func (s *Server) projectPromptReadyDeployment(w http.ResponseWriter, r *http.Request) (projectstore.Deployment, bool) {
	if !s.requireProjectStore(w) {
		return projectstore.Deployment{}, false
	}
	projectID := strings.TrimSpace(r.PathValue("projectID"))
	deploymentID := strings.TrimSpace(r.PathValue("deploymentID"))
	if _, err := s.projects.GetProject(r.Context(), projectID); err != nil {
		writeProjectError(w, err)
		return projectstore.Deployment{}, false
	}
	deployment, err := s.projects.GetDeployment(r.Context(), projectID, deploymentID)
	if err != nil {
		writeProjectError(w, err)
		return projectstore.Deployment{}, false
	}
	if !deployment.Enabled || deployment.ApplyStatus != string(protocol.DeploymentApplyApplied) || deployment.AppliedRevision == "" || deployment.AppliedRevision != deployment.DesiredRevision {
		writeError(w, http.StatusConflict, protocol.ErrorDeploymentNotReady, "Deployment 当前没有可用于 Project Prompt 的 applied revision")
		return projectstore.Deployment{}, false
	}
	if s.agentDock == nil || s.agentDockHub == nil {
		writeError(w, http.StatusServiceUnavailable, protocol.ErrorDeploymentNotReady, "AgentDock 连接服务不可用")
		return projectstore.Deployment{}, false
	}
	node, err := s.agentDock.Get(r.Context(), deployment.NodeID)
	if err != nil {
		writeProjectError(w, err)
		return projectstore.Deployment{}, false
	}
	if !node.Enabled || !nodeUsesCurrentBridgeProtocol(node) || !s.agentDockHub.Online(node.ID) {
		writeError(w, http.StatusServiceUnavailable, protocol.ErrorDeploymentNotReady, "目标 AgentDock 未在线并完成 Bridge v4 握手")
		return projectstore.Deployment{}, false
	}
	return deployment, true
}

func decodeProjectPromptLoadResult(result map[string]any, deploymentID string) (protocol.ProjectPromptLoadResult, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return protocol.ProjectPromptLoadResult{}, err
	}
	var decoded protocol.ProjectPromptLoadResult
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return protocol.ProjectPromptLoadResult{}, err
	}
	if decoded.DeploymentID != deploymentID || strings.TrimSpace(decoded.CWDRel) == "" || !decoded.Prompt.Complete || strings.TrimSpace(decoded.Prompt.PromptRevision) == "" || decoded.Prompt.Sources == nil {
		return protocol.ProjectPromptLoadResult{}, errors.New("AgentDock returned an incomplete Project Prompt result")
	}
	return decoded, nil
}

func decodeProjectPromptWriteResult(result map[string]any, deploymentID string) (protocol.ProjectPromptWriteResult, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return protocol.ProjectPromptWriteResult{}, err
	}
	var decoded protocol.ProjectPromptWriteResult
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return protocol.ProjectPromptWriteResult{}, err
	}
	if decoded.DeploymentID != deploymentID || strings.TrimSpace(decoded.Scope) == "" || decoded.Source.Scope != decoded.Scope || strings.TrimSpace(decoded.Source.Path) == "" || strings.TrimSpace(decoded.Source.SHA256) == "" || !decoded.Prompt.Complete || strings.TrimSpace(decoded.Prompt.PromptRevision) == "" || decoded.Prompt.Sources == nil {
		return protocol.ProjectPromptWriteResult{}, errors.New("AgentDock returned an incomplete Project Prompt write result")
	}
	return decoded, nil
}

func writeProjectPromptError(w http.ResponseWriter, err error) {
	if errors.Is(err, agentdock.ErrNodeOffline) || errors.Is(err, agentdock.ErrNodeDisconnected) {
		writeError(w, http.StatusServiceUnavailable, protocol.ErrorDeploymentNotReady, err.Error())
		return
	}
	var remote *protocol.RemoteError
	if !errors.As(err, &remote) || remote == nil {
		writeError(w, http.StatusBadGateway, protocol.ErrorProjectPromptReadFailed, err.Error())
		return
	}
	status := http.StatusBadGateway
	switch remote.Code {
	case protocol.ErrorExecutionContextInvalid:
		status = http.StatusBadRequest
	case protocol.ErrorPromptScopeEscape, protocol.ErrorSessionTargetDenied, protocol.ErrorCapabilityDenied:
		status = http.StatusForbidden
	case protocol.ErrorRevisionConflict, protocol.ErrorContextRefreshRequired:
		status = http.StatusPreconditionFailed
	case protocol.ErrorDeploymentNotReady:
		status = http.StatusServiceUnavailable
	case protocol.ErrorProjectPromptTooLarge:
		status = http.StatusRequestEntityTooLarge
	case protocol.ErrorProjectPromptReadFailed:
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]any{"ok": false, "error": map[string]any{"code": remote.Code, "message": remote.Message, "details": remote.Details}})
}

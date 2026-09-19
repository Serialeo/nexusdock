package httpx

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/agentdock"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

type projectPromptWriteRequest struct {
	Scope           string `json:"scope"`
	Content         string `json:"content"`
	ExpectedContent string `json:"expected_content,omitempty"`
	Create          bool   `json:"create"`
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
		"working_folder": deployment.WorkingFolder, "result": projectPromptLoadAdminView(decoded),
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
		Scope: request.Scope, Content: request.Content, ExpectedSHA256: expectedProjectPromptSHA(request.ExpectedContent, request.Create), Create: request.Create,
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
		"working_folder": deployment.WorkingFolder, "result": projectPromptWriteAdminView(decoded),
	})
}

func expectedProjectPromptSHA(expectedContent string, create bool) string {
	if create {
		return ""
	}
	sum := sha256.Sum256([]byte(expectedContent))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func projectPromptSourceAdminView(source protocol.PromptSource) map[string]any {
	return map[string]any{"path": source.Path, "scope": source.Scope, "bytes": source.Bytes, "content": source.Content}
}

func projectPromptAdminView(prompt protocol.ProjectPrompt) map[string]any {
	sources := make([]map[string]any, 0, len(prompt.Sources))
	for _, source := range prompt.Sources {
		sources = append(sources, projectPromptSourceAdminView(source))
	}
	return map[string]any{"complete": prompt.Complete, "bytes": prompt.Bytes, "sources": sources}
}

func projectPromptLoadAdminView(result protocol.ProjectPromptLoadResult) map[string]any {
	return map[string]any{"deployment_id": result.DeploymentID, "cwd_rel": result.CWDRel, "prompt": projectPromptAdminView(result.Prompt)}
}

func projectPromptWriteAdminView(result protocol.ProjectPromptWriteResult) map[string]any {
	return map[string]any{
		"deployment_id": result.DeploymentID, "scope": result.Scope, "source": projectPromptSourceAdminView(result.Source),
		"prompt": projectPromptAdminView(result.Prompt), "created": result.Created,
	}
}

func (s *Server) projectPromptReadyDeployment(w http.ResponseWriter, r *http.Request) (projectstore.Deployment, bool) {
	if !s.requireProjectStore(w) {
		return projectstore.Deployment{}, false
	}
	projectID := strings.TrimSpace(r.PathValue("projectID"))
	deploymentID := strings.TrimSpace(r.PathValue("deploymentID"))
	if _, err := s.projects.GetUserProject(r.Context(), projectID); err != nil {
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

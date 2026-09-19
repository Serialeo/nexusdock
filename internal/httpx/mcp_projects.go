package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/core"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

type projectOpenTargetSelection struct {
	DeploymentID string `json:"deployment_id"`
	CWDRel       string `json:"cwd_rel,omitempty"`
}

type projectOpenRequest struct {
	ProjectID       string                       `json:"project_id"`
	ClientRequestID string                       `json:"client_request_id"`
	Targets         []projectOpenTargetSelection `json:"targets,omitempty"`
}

type nodeOpenRequest struct {
	NodeID          string `json:"node_id"`
	ClientRequestID string `json:"client_request_id"`
	CWDRel          string `json:"cwd_rel,omitempty"`
}

type projectContextRequest struct {
	WorkSessionID string `json:"work_session_id"`
	TargetID      string `json:"target_id"`
	CWDRel        string `json:"cwd_rel,omitempty"`
}

func (s *Server) callProjectList(ctx context.Context) (map[string]any, error) {
	if s.projects == nil {
		return projectToolError("PROJECT_STORE_UNAVAILABLE", "Project store is unavailable", nil)
	}
	projects, err := s.projects.ListProjects(ctx)
	if err != nil {
		return projectToolError("PROJECT_LIST_FAILED", "failed to list Projects", map[string]any{"reason": err.Error()})
	}
	items := make([]map[string]any, 0, len(projects))
	for _, project := range projects {
		if !project.Enabled {
			continue
		}
		deployments, listErr := s.currentProjectDeployments(ctx, project.ID)
		if listErr != nil {
			return projectToolError("PROJECT_LIST_FAILED", "failed to list Project Deployments", map[string]any{"project_id": project.ID, "reason": listErr.Error()})
		}
		available := 0
		for _, deployment := range deployments {
			if status, _ := s.projectDeploymentAvailability(ctx, deployment); status == "candidate" {
				available++
			}
		}
		items = append(items, map[string]any{
			"id": project.ID, "name": project.Name, "revision": project.Revision, "enabled": project.Enabled,
			"deployment_count": len(deployments), "available_deployment_count": available,
		})
	}
	return map[string]any{"projects": items, "count": len(items)}, nil
}

func (s *Server) callProjectOpen(ctx context.Context, args map[string]any) (map[string]any, error) {
	binding, ok := mcpClientBindingFromContext(ctx)
	if !ok {
		return projectToolError("MCP_CLIENT_BINDING_REQUIRED", "authenticated MCP client binding is required", nil)
	}
	if s.projects == nil || s.agentDock == nil || s.agentDockHub == nil {
		return projectToolError("PROJECT_STORE_UNAVAILABLE", "Project execution services are unavailable", nil)
	}
	var request projectOpenRequest
	if err := decodeProjectToolArgs(args, &request); err != nil {
		return projectToolError("INVALID_PROJECT", "invalid project_open request", map[string]any{"reason": err.Error()})
	}
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ClientRequestID = strings.TrimSpace(request.ClientRequestID)
	if request.ProjectID == "" || request.ClientRequestID == "" {
		return projectToolError("INVALID_PROJECT", "project_id and client_request_id are required", nil)
	}
	_, targetsProvided := args["targets"]
	if targetsProvided && len(request.Targets) == 0 {
		return projectToolError("INVALID_PROJECT", "targets must contain at least one Deployment when explicitly provided", nil)
	}
	project, err := s.projects.GetUserProject(ctx, request.ProjectID)
	if err != nil {
		return projectToolStoreError(err)
	}
	if !project.Enabled {
		return projectToolError(protocol.ErrorProjectNotFound, "Project is disabled and cannot be entered", map[string]any{"project_id": project.ID})
	}
	deployments, err := s.currentProjectDeployments(ctx, project.ID)
	if err != nil {
		return projectToolError("PROJECT_OPERATION_FAILED", "failed to list Project Deployments", map[string]any{"reason": err.Error()})
	}
	selections, err := normalizeProjectOpenSelections(request.Targets, targetsProvided, deployments)
	if err != nil {
		return projectToolError("INVALID_PROJECT", err.Error(), nil)
	}
	requestHash, err := hashProjectOpenRequest(project.ID, targetsProvided, selections)
	if err != nil {
		return nil, err
	}
	session, created, err := s.projects.BeginWorkSession(ctx, binding.OwnerKey, project.ID, request.ClientRequestID, requestHash, project.Revision)
	if err != nil {
		var conflict *projectstore.WorkSessionRequestConflictError
		if errors.As(err, &conflict) {
			return projectToolError(protocol.ErrorRevisionConflict, conflict.Error(), map[string]any{"work_session_id": conflict.WorkSessionID, "client_request_id": conflict.ClientRequestID})
		}
		return projectToolError("PROJECT_OPERATION_FAILED", "failed to create WorkSession", map[string]any{"reason": err.Error()})
	}
	if created {
		for _, selection := range selections {
			deployment := findProjectDeployment(deployments, selection.DeploymentID)
			target, prepareErr := s.prepareProjectTarget(ctx, session, project, deployment, selection.CWDRel)
			if prepareErr != nil {
				return projectToolError("PROJECT_OPERATION_FAILED", "failed to allocate WorkSession Target", map[string]any{"deployment_id": selection.DeploymentID, "reason": prepareErr.Error()})
			}
			if _, saveErr := s.projects.PutWorkTarget(ctx, binding.OwnerKey, target); saveErr != nil {
				return projectToolError("PROJECT_OPERATION_FAILED", "failed to persist WorkSession Target", map[string]any{"deployment_id": selection.DeploymentID, "reason": saveErr.Error()})
			}
		}
		targets, listErr := s.projects.ListWorkTargets(ctx, binding.OwnerKey, session.ID)
		if listErr != nil {
			return projectToolError("PROJECT_OPERATION_FAILED", "failed to load prepared WorkSession Targets", map[string]any{"reason": listErr.Error()})
		}
		status := workSessionStatusForTargets(targets)
		contextRevision := projectSessionContextRevision(project, targets)
		session, err = s.projects.UpdateWorkSessionContext(ctx, binding.OwnerKey, session.ID, project.Revision, status, contextRevision)
		if err != nil {
			return projectToolError("PROJECT_OPERATION_FAILED", "failed to finalize WorkSession", map[string]any{"reason": err.Error()})
		}
	}
	return s.projectOpenResult(ctx, binding.OwnerKey, session.ID)
}

func (s *Server) callNodeOpen(ctx context.Context, args map[string]any) (map[string]any, error) {
	binding, ok := mcpClientBindingFromContext(ctx)
	if !ok {
		return projectToolError("MCP_CLIENT_BINDING_REQUIRED", "authenticated MCP client binding is required", nil)
	}
	if s.projects == nil || s.agentDock == nil || s.agentDockHub == nil {
		return projectToolError("NODE_SESSION_STORE_UNAVAILABLE", "Node session execution services are unavailable", nil)
	}
	var request nodeOpenRequest
	if err := decodeProjectToolArgs(args, &request); err != nil {
		return projectToolError("INVALID_NODE_SESSION", "invalid node_open request", map[string]any{"reason": err.Error()})
	}
	request.NodeID = strings.TrimSpace(request.NodeID)
	request.ClientRequestID = strings.TrimSpace(request.ClientRequestID)
	request.CWDRel = strings.TrimSpace(request.CWDRel)
	if request.CWDRel == "" {
		request.CWDRel = "."
	}
	if request.NodeID == "" || request.ClientRequestID == "" {
		return projectToolError("INVALID_NODE_SESSION", "node_id and client_request_id are required", nil)
	}
	node, err := s.currentAgentDockNode(ctx, request.NodeID)
	if err != nil || !node.Enabled {
		return projectToolError("AGENTDOCK_NODE_NOT_FOUND", "AgentDock Node is unavailable", nil)
	}
	configured, err := s.projects.GetNodeSession(ctx, request.NodeID)
	if errors.Is(err, projectstore.ErrNodeSessionNotFound) || (err == nil && !configured.Deployment.Enabled) {
		return projectToolError("NODE_SESSION_DISABLED", "Node temporary sessions are disabled; enable them in NexusDock Settings > System and Nodes", map[string]any{"node_id": request.NodeID})
	}
	if err != nil {
		return projectToolError("NODE_SESSION_OPERATION_FAILED", "failed to read Node session configuration", map[string]any{"reason": err.Error()})
	}
	requestHash, err := hashNodeOpenRequest(request.NodeID, request.CWDRel)
	if err != nil {
		return nil, err
	}
	// Duplicate idempotent requests share one cancellable allocation lane. The
	// reservation itself is persisted before Bridge work, allowing an
	// interrupted request to resume with the same Target ID without blocking
	// unrelated owners or requests.
	unlock, lockErr := s.acquireOperation(ctx, operationLockKey{scope: "node_open", primary: binding.OwnerKey, secondary: request.ClientRequestID})
	if lockErr != nil {
		return projectToolError("NODE_SESSION_OPERATION_FAILED", "Node WorkSession allocation was cancelled", map[string]any{"reason": lockErr.Error()})
	}
	defer unlock()
	session, _, err := s.projects.BeginWorkSession(ctx, binding.OwnerKey, configured.Project.ID, request.ClientRequestID, requestHash, configured.Project.Revision)
	if err != nil {
		var conflict *projectstore.WorkSessionRequestConflictError
		if errors.As(err, &conflict) {
			return projectToolError(protocol.ErrorRevisionConflict, conflict.Error(), map[string]any{"work_session_id": conflict.WorkSessionID, "client_request_id": conflict.ClientRequestID})
		}
		return projectToolError("NODE_SESSION_OPERATION_FAILED", "failed to create Node WorkSession", map[string]any{"reason": err.Error()})
	}
	targets, err := s.projects.ListWorkTargets(ctx, binding.OwnerKey, session.ID)
	if err != nil {
		return projectToolError("NODE_SESSION_OPERATION_FAILED", "failed to load Node WorkSession Target", map[string]any{"reason": err.Error()})
	}
	if len(targets) > 1 {
		return projectToolError("NODE_SESSION_OPERATION_FAILED", "Node WorkSession contains more than one Target", nil)
	}
	if len(targets) == 0 {
		targetID, idErr := core.NewID("target")
		if idErr != nil {
			return projectToolError("NODE_SESSION_OPERATION_FAILED", "failed to allocate Node WorkSession Target", map[string]any{"reason": idErr.Error()})
		}
		reserved := s.preparingProjectTarget(ctx, targetID, session, configured.Project, configured.Deployment, request.CWDRel)
		stored, saveErr := s.projects.PutWorkTarget(ctx, binding.OwnerKey, reserved)
		if saveErr != nil {
			return projectToolError("NODE_SESSION_OPERATION_FAILED", "failed to reserve Node WorkSession Target", map[string]any{"reason": saveErr.Error()})
		}
		targets = []projectstore.WorkTarget{stored}
	}
	if targets[0].Target.Status == protocol.TargetPreparing {
		target, prepareErr := s.prepareProjectTargetWithID(ctx, targets[0].Target.ID, session, configured.Project, configured.Deployment, request.CWDRel)
		if prepareErr != nil {
			return projectToolError("NODE_SESSION_OPERATION_FAILED", "failed to allocate Node WorkSession Target", map[string]any{"reason": prepareErr.Error()})
		}
		if _, saveErr := s.projects.PutWorkTarget(ctx, binding.OwnerKey, target); saveErr != nil {
			return projectToolError("NODE_SESSION_OPERATION_FAILED", "failed to persist Node WorkSession Target", map[string]any{"reason": saveErr.Error()})
		}
		targets = []projectstore.WorkTarget{target}
	}
	session, err = s.projects.UpdateWorkSessionContext(ctx, binding.OwnerKey, session.ID, configured.Project.Revision, workSessionStatusForTargets(targets), projectSessionContextRevision(configured.Project, targets))
	if err != nil {
		return projectToolError("NODE_SESSION_OPERATION_FAILED", "failed to finalize Node WorkSession", map[string]any{"reason": err.Error()})
	}
	return s.nodeOpenResult(ctx, binding.OwnerKey, session.ID)
}

func (s *Server) nodeOpenResult(ctx context.Context, ownerKey, workSessionID string) (map[string]any, error) {
	session, err := s.projects.GetWorkSession(ctx, ownerKey, workSessionID)
	if err != nil {
		return nil, err
	}
	project, err := s.projects.GetProject(ctx, session.ProjectID)
	if err != nil || project.Kind != projectstore.ProjectKindNode {
		return projectToolError(protocol.ErrorSessionTargetDenied, "WorkSession is not a Node temporary session", nil)
	}
	targets, err := s.projects.ListWorkTargets(ctx, ownerKey, session.ID)
	if err != nil {
		return nil, err
	}
	if len(targets) != 1 {
		return projectToolError("NODE_SESSION_OPERATION_FAILED", "Node WorkSession does not contain exactly one Target", nil)
	}
	if err := validateProjectPromptContextBudget(targets); err != nil {
		return projectToolError(protocol.ErrorProjectPromptTooLarge, "Node WorkSession Prompt exceeds the context budget", map[string]any{"reason": err.Error()})
	}
	node, err := s.currentAgentDockNode(ctx, project.NodeID)
	if err != nil {
		return projectToolError("AGENTDOCK_NODE_NOT_FOUND", "AgentDock Node is unavailable", nil)
	}
	delivery, err := s.projectContextDeliveryView(ctx, ownerKey, session.ID, "", session.ContextRevision)
	if err != nil {
		return projectToolError("NODE_SESSION_OPERATION_FAILED", "failed to read Node Context delivery state", map[string]any{"reason": err.Error()})
	}
	result := map[string]any{
		"work_session_id":  session.ID,
		"status":           string(session.Status),
		"context_revision": session.ContextRevision,
		"delivery":         delivery,
		"node":             map[string]any{"node_id": node.ID, "name": node.Name, "online": s.agentDockHub.Online(node.ID)},
		"targets":          []protocol.WorkTarget{targets[0].Target},
	}
	if err := validateProjectContextDeliveryEnvelopeForTool("node_open", result); err != nil {
		return projectToolError(protocol.ErrorProjectPromptTooLarge, "Node Context delivery exceeds the model-visible result budget", map[string]any{"reason": err.Error(), "max_bytes": protocol.MaxProjectContextDeliveryBytes})
	}
	return result, nil
}

func (s *Server) callProjectContext(ctx context.Context, args map[string]any) (map[string]any, error) {
	binding, ok := mcpClientBindingFromContext(ctx)
	if !ok {
		return projectToolError("MCP_CLIENT_BINDING_REQUIRED", "authenticated MCP client binding is required", nil)
	}
	var request projectContextRequest
	if err := decodeProjectToolArgs(args, &request); err != nil {
		return projectToolError("INVALID_PROJECT", "invalid project_context request", map[string]any{"reason": err.Error()})
	}
	workSessionID := strings.TrimSpace(request.WorkSessionID)
	targetID := strings.TrimSpace(request.TargetID)
	if workSessionID == "" || targetID == "" {
		return projectToolError("INVALID_PROJECT", "work_session_id and target_id are required", nil)
	}
	session, err := s.projects.GetWorkSession(ctx, binding.OwnerKey, workSessionID)
	if err != nil {
		return projectToolSessionError(err)
	}
	if session.Status == protocol.WorkSessionCancelled || session.Status == protocol.WorkSessionCompleted {
		return projectToolError(protocol.ErrorSessionTargetDenied, "WorkSession is no longer active", map[string]any{"work_session_id": workSessionID, "status": session.Status})
	}
	stored, err := s.projects.GetWorkTarget(ctx, binding.OwnerKey, workSessionID, targetID)
	if err != nil {
		return projectToolSessionError(err)
	}
	if _, err := s.currentAgentDockNode(ctx, stored.Target.NodeID); err != nil {
		return projectToolError(protocol.ErrorSessionTargetDenied, "Target is unavailable", nil)
	}
	if stored.Target.Status == protocol.TargetRevoked {
		return projectToolError(protocol.ErrorSessionTargetDenied, "Project Target has been revoked", map[string]any{"target_id": targetID})
	}
	project, err := s.projects.GetProject(ctx, session.ProjectID)
	if err != nil {
		return projectToolStoreError(err)
	}
	if !project.Enabled {
		return projectToolError(protocol.ErrorProjectNotFound, "Project is disabled and cannot refresh execution context", map[string]any{"project_id": project.ID})
	}
	deployment, err := s.projects.GetDeployment(ctx, project.ID, stored.Target.DeploymentID)
	if err != nil {
		return projectToolStoreError(err)
	}
	if !deployment.Enabled || deployment.ApplyStatus != string(protocol.DeploymentApplyApplied) || deployment.DesiredRevision != deployment.AppliedRevision || deployment.AppliedRevision != stored.Target.DeploymentRevision {
		return projectToolError(protocol.ErrorRevisionConflict, "Target Deployment configuration changed; refresh the Project Context", map[string]any{"target_id": targetID, "deployment_id": deployment.ID})
	}
	if !s.agentDockHub.Online(deployment.NodeID) {
		return projectToolError(protocol.ErrorDeploymentNotReady, "Target AgentDock is offline", map[string]any{"target_id": targetID, "node_id": deployment.NodeID})
	}
	node, err := s.currentAgentDockNode(ctx, deployment.NodeID)
	if err != nil {
		return projectToolError(protocol.ErrorSessionTargetDenied, "Target is unavailable", nil)
	}
	effectivePermissions := effectiveProjectPermissions(deployment.Permissions, node.FullAccess)
	cwdRel := strings.TrimSpace(request.CWDRel)
	if cwdRel == "" {
		cwdRel = stored.Target.CWDRel
	}
	promptResult, err := s.loadProjectPrompt(ctx, deployment, cwdRel)
	if err != nil {
		return projectToolError(protocol.ErrorProjectPromptReadFailed, "failed to refresh Project Prompt", map[string]any{"target_id": targetID, "reason": err.Error()})
	}
	promptScopes := mergePromptScope(stored.PromptScopes, protocol.PromptScopeRevision{Scope: promptResult.CWDRel, PromptRevision: promptResult.Prompt.PromptRevision})
	contextRevision := projectTargetContextRevision(project, deployment, effectivePermissions, promptResult.CWDRel, promptResult.Prompt, promptScopes, promptResult.SourceProvenance)
	rebind := protocol.ProjectTargetRebindRequest{WorkSessionID: workSessionID, TargetID: targetID, CWDRel: promptResult.CWDRel, ContextRevision: contextRevision, PromptScopes: promptScopes, SourceProvenance: promptResult.SourceProvenance}
	rebindResult, err := s.agentDockHub.Invoke(ctx, deployment.NodeID, protocol.OperationProjectTargetRebind, rebind)
	if err != nil {
		return projectToolError(protocol.ErrorSessionTargetDenied, "AgentDock rejected Project Target context refresh", map[string]any{"target_id": targetID, "reason": err.Error()})
	}
	if err := validateProjectTargetRebindAcknowledgment(rebindResult, rebind); err != nil {
		return projectToolError(protocol.ErrorSessionTargetDenied, "AgentDock returned an invalid Project Target context acknowledgment", map[string]any{"target_id": targetID, "reason": err.Error()})
	}
	stored.Target.CWDRel = promptResult.CWDRel
	stored.Target.ContextRevision = contextRevision
	stored.Target.Status = protocol.TargetReady
	stored.Target.Permissions = effectivePermissions
	stored.Target.Prompt = promptResult.Prompt
	stored.Target.SourceProvenance = promptResult.SourceProvenance
	stored.PromptScopes = promptScopes
	stored.LastError = ""
	stored, err = s.projects.PutWorkTarget(ctx, binding.OwnerKey, stored)
	if err != nil {
		return projectToolError("PROJECT_OPERATION_FAILED", "failed to persist refreshed Target context", map[string]any{"reason": err.Error()})
	}
	targets, err := s.projects.ListWorkTargets(ctx, binding.OwnerKey, workSessionID)
	if err != nil {
		return nil, err
	}
	status := workSessionStatusForTargets(targets)
	if _, err := s.projects.UpdateWorkSessionContext(ctx, binding.OwnerKey, workSessionID, project.Revision, status, projectSessionContextRevision(project, targets)); err != nil {
		return nil, err
	}
	delivery, err := s.projectContextDeliveryView(ctx, binding.OwnerKey, workSessionID, stored.Target.ID, stored.Target.ContextRevision)
	if err != nil {
		return projectToolError("PROJECT_OPERATION_FAILED", "failed to read Project Context delivery state", map[string]any{"reason": err.Error()})
	}
	result := map[string]any{
		"work_session_id": workSessionID,
		"deployment":      projectDeploymentView(deployment, effectivePermissions, true, "ready", ""),
		"target":          stored.Target,
		"delivery":        delivery,
	}
	if project.Kind == projectstore.ProjectKindNode {
		node, nodeErr := s.currentAgentDockNode(ctx, project.NodeID)
		if nodeErr != nil {
			return projectToolError("AGENTDOCK_NODE_NOT_FOUND", "AgentDock Node is unavailable", nil)
		}
		result["node"] = map[string]any{"node_id": node.ID, "name": node.Name, "online": s.agentDockHub.Online(node.ID)}
	} else {
		result["project"] = projectProtocolView(project)
	}
	if err := validateProjectContextDeliveryEnvelope(result); err != nil {
		return projectToolError(protocol.ErrorProjectPromptTooLarge, "Project Context delivery exceeds the model-visible result budget", map[string]any{"reason": err.Error(), "max_bytes": protocol.MaxProjectContextDeliveryBytes})
	}
	return result, nil
}

func (s *Server) prepareProjectTarget(ctx context.Context, session projectstore.WorkSession, project projectstore.Project, deployment projectstore.Deployment, cwdRel string) (projectstore.WorkTarget, error) {
	targetID, err := core.NewID("target")
	if err != nil {
		return projectstore.WorkTarget{}, err
	}
	return s.prepareProjectTargetWithID(ctx, targetID, session, project, deployment, cwdRel)
}

func (s *Server) preparingProjectTarget(ctx context.Context, targetID string, session projectstore.WorkSession, project projectstore.Project, deployment projectstore.Deployment, cwdRel string) projectstore.WorkTarget {
	if strings.TrimSpace(cwdRel) == "" {
		cwdRel = "."
	}
	effectivePermissions := deployment.Permissions
	if node, nodeErr := s.agentDock.Get(ctx, deployment.NodeID); nodeErr == nil {
		effectivePermissions = effectiveProjectPermissions(deployment.Permissions, node.FullAccess)
	}
	return projectstore.WorkTarget{Target: protocol.WorkTarget{
		ID: targetID, WorkSessionID: session.ID, ProjectID: project.ID, DeploymentID: deployment.ID, NodeID: deployment.NodeID,
		CWDRel: cwdRel, DeploymentRevision: deployment.DesiredRevision, Status: protocol.TargetPreparing, Permissions: effectivePermissions,
		Prompt: emptyProjectPrompt(),
	}}
}

func (s *Server) prepareProjectTargetWithID(ctx context.Context, targetID string, session projectstore.WorkSession, project projectstore.Project, deployment projectstore.Deployment, cwdRel string) (projectstore.WorkTarget, error) {
	item := s.preparingProjectTarget(ctx, targetID, session, project, deployment, cwdRel)
	availability, reason := s.projectDeploymentAvailability(ctx, deployment)
	if availability != "candidate" {
		item.Target.Status = protocol.TargetUnavailable
		item.LastError = reason
		return item, nil
	}
	promptResult, err := s.loadProjectPrompt(ctx, deployment, cwdRel)
	if err != nil {
		item.Target.Status = protocol.TargetContextError
		item.LastError = err.Error()
		return item, nil
	}
	item.Target.CWDRel = promptResult.CWDRel
	item.Target.DeploymentRevision = deployment.AppliedRevision
	item.Target.Prompt = promptResult.Prompt
	item.Target.SourceProvenance = promptResult.SourceProvenance
	item.PromptScopes = []protocol.PromptScopeRevision{{Scope: promptResult.CWDRel, PromptRevision: promptResult.Prompt.PromptRevision}}
	item.Target.ContextRevision = projectTargetContextRevision(project, deployment, item.Target.Permissions, promptResult.CWDRel, promptResult.Prompt, item.PromptScopes, item.Target.SourceProvenance)
	bind := protocol.ProjectTargetBindRequest{
		WorkSessionID: session.ID, TargetID: item.Target.ID, ProjectID: project.ID, DeploymentID: deployment.ID,
		CWDRel: item.Target.CWDRel, DeploymentRevision: deployment.AppliedRevision, ContextRevision: item.Target.ContextRevision, PromptScopes: item.PromptScopes,
		SourceProvenance: item.Target.SourceProvenance,
	}
	result, err := s.agentDockHub.Invoke(ctx, deployment.NodeID, protocol.OperationProjectTargetBind, bind)
	if err != nil {
		item.Target.Status = protocol.TargetUnavailable
		item.LastError = err.Error()
		return item, nil
	}
	if err := validateProjectTargetBindAcknowledgment(result, bind); err != nil {
		item.Target.Status = protocol.TargetUnavailable
		item.LastError = err.Error()
		return item, nil
	}
	item.Target.Status = protocol.TargetReady
	return item, nil
}

func (s *Server) loadProjectPrompt(ctx context.Context, deployment projectstore.Deployment, cwdRel string) (protocol.ProjectPromptLoadResult, error) {
	result, err := s.agentDockHub.Invoke(ctx, deployment.NodeID, protocol.OperationProjectPromptLoad, protocol.ProjectPromptLoadRequest{
		DeploymentID: deployment.ID, DeploymentRevision: deployment.AppliedRevision, CWDRel: cwdRel,
	})
	if err != nil {
		return protocol.ProjectPromptLoadResult{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return protocol.ProjectPromptLoadResult{}, err
	}
	var decoded protocol.ProjectPromptLoadResult
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return protocol.ProjectPromptLoadResult{}, err
	}
	if decoded.DeploymentID != deployment.ID || strings.TrimSpace(decoded.CWDRel) == "" || !decoded.Prompt.Complete || strings.TrimSpace(decoded.Prompt.PromptRevision) == "" || decoded.Prompt.Sources == nil {
		return protocol.ProjectPromptLoadResult{}, errors.New("AgentDock returned an incomplete Project Prompt acknowledgment")
	}
	if err := decoded.SourceProvenance.Validate(); err != nil {
		return protocol.ProjectPromptLoadResult{}, fmt.Errorf("AgentDock returned invalid source provenance: %w", err)
	}
	if decoded.SourceProvenance.Kind == protocol.SourceProvenanceUnknown {
		return protocol.ProjectPromptLoadResult{}, errors.New("AgentDock returned uninspected source provenance for a prepared Project Prompt")
	}
	return decoded, nil
}

func (s *Server) projectOpenResult(ctx context.Context, ownerKey, workSessionID string) (map[string]any, error) {
	session, err := s.projects.GetWorkSession(ctx, ownerKey, workSessionID)
	if err != nil {
		return nil, err
	}
	project, err := s.projects.GetProject(ctx, session.ProjectID)
	if err != nil {
		return nil, err
	}
	deployments, err := s.currentProjectDeployments(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	targets, err := s.currentWorkTargets(ctx, ownerKey, session.ID)
	if err != nil {
		return nil, err
	}
	targetByDeployment := make(map[string]projectstore.WorkTarget, len(targets))
	publicTargets := make([]protocol.WorkTarget, 0, len(targets))
	for _, target := range targets {
		targetByDeployment[target.Target.DeploymentID] = target
		publicTargets = append(publicTargets, target.Target)
	}
	if err := validateProjectPromptContextBudget(targets); err != nil {
		return projectToolError(protocol.ErrorProjectPromptTooLarge, "WorkSession Project Prompt source bodies exceed the context budget", map[string]any{"reason": err.Error(), "max_bytes": protocol.MaxProjectPromptContextBytes})
	}
	views := make([]map[string]any, 0, len(deployments))
	for _, deployment := range deployments {
		status, reason := s.projectDeploymentAvailability(ctx, deployment)
		permissions := deployment.Permissions
		if target, ok := targetByDeployment[deployment.ID]; ok {
			permissions = target.Target.Permissions
			switch target.Target.Status {
			case protocol.TargetReady, protocol.TargetIdle, protocol.TargetRunning:
				status, reason = "ready", ""
			case protocol.TargetContextError:
				status, reason = "context_error", target.LastError
			case protocol.TargetUnavailable, protocol.TargetRevoked:
				status, reason = string(target.Target.Status), target.LastError
			}
		} else if node, nodeErr := s.agentDock.Get(ctx, deployment.NodeID); nodeErr == nil {
			permissions = effectiveProjectPermissions(deployment.Permissions, node.FullAccess)
		}
		views = append(views, projectDeploymentView(deployment, permissions, s.agentDockHub.Online(deployment.NodeID), status, reason))
	}
	delivery, err := s.projectContextDeliveryView(ctx, ownerKey, session.ID, "", session.ContextRevision)
	if err != nil {
		return projectToolError("PROJECT_OPERATION_FAILED", "failed to read Project Context delivery state", map[string]any{"reason": err.Error()})
	}
	status := session.Status
	if status == protocol.WorkSessionReady || status == protocol.WorkSessionPartial || status == protocol.WorkSessionFailed {
		status = workSessionStatusForTargets(targets)
	}
	result := map[string]any{
		"work_session_id":  session.ID,
		"status":           string(status),
		"context_revision": session.ContextRevision,
		"delivery":         delivery,
		"project":          projectProtocolView(project),
		"deployments":      views,
		"targets":          publicTargets,
	}
	if err := validateProjectContextDeliveryEnvelope(result); err != nil {
		return projectToolError(protocol.ErrorProjectPromptTooLarge, "Project Context delivery exceeds the model-visible result budget", map[string]any{"reason": err.Error(), "max_bytes": protocol.MaxProjectContextDeliveryBytes})
	}
	return result, nil
}

func (s *Server) projectDeploymentAvailability(ctx context.Context, deployment projectstore.Deployment) (string, string) {
	if !deployment.Enabled {
		return "disabled", "Deployment is disabled"
	}
	if deployment.ApplyStatus != string(protocol.DeploymentApplyApplied) || deployment.AppliedRevision == "" || deployment.AppliedRevision != deployment.DesiredRevision {
		return "deployment_not_ready", "Deployment desired revision is not applied"
	}
	if s.agentDockHub == nil || !s.agentDockHub.Online(deployment.NodeID) {
		return "offline", "AgentDock node is offline"
	}
	node, err := s.currentAgentDockNode(ctx, deployment.NodeID)
	if err != nil {
		return "node_unavailable", "AgentDock node is unavailable"
	}
	if !deploymentHasUsableCapability(effectiveProjectPermissions(deployment.Permissions, node.FullAccess), node.Capabilities) {
		return "capability_denied", "Deployment has no usable allowed capability on this node"
	}
	return "candidate", ""
}

func deploymentHasUsableCapability(permissions protocol.DeploymentPermissions, capabilities []string) bool {
	if permissions.FullAccess {
		for _, capability := range []string{"read_file", "exec_command", "browser_session", "mcp_manage", "acp_session"} {
			if containsString(capabilities, capability) {
				return true
			}
		}
		return false
	}
	if permissions.Files != protocol.FileCapabilityNone && containsString(capabilities, "read_file") {
		return true
	}
	if permissions.Shell && containsString(capabilities, "exec_command") {
		return true
	}
	if permissions.Browser && containsString(capabilities, "browser_session") {
		return true
	}
	if permissions.DynamicMCP && containsString(capabilities, "mcp_manage") {
		return true
	}
	if permissions.ACP && containsString(capabilities, "acp_session") {
		return true
	}
	return false
}

func normalizeProjectOpenSelections(input []projectOpenTargetSelection, provided bool, deployments []projectstore.Deployment) ([]projectOpenTargetSelection, error) {
	if !provided {
		out := make([]projectOpenTargetSelection, 0, len(deployments))
		for _, deployment := range deployments {
			if deployment.Enabled {
				out = append(out, projectOpenTargetSelection{DeploymentID: deployment.ID, CWDRel: "."})
			}
		}
		return out, nil
	}
	seen := make(map[string]struct{}, len(input))
	out := make([]projectOpenTargetSelection, 0, len(input))
	for _, item := range input {
		item.DeploymentID = strings.TrimSpace(item.DeploymentID)
		item.CWDRel = strings.TrimSpace(item.CWDRel)
		if item.CWDRel == "" {
			item.CWDRel = "."
		}
		if item.DeploymentID == "" || findProjectDeployment(deployments, item.DeploymentID).ID == "" {
			return nil, fmt.Errorf("Deployment %q does not belong to the Project", item.DeploymentID)
		}
		if _, exists := seen[item.DeploymentID]; exists {
			return nil, fmt.Errorf("Deployment %q is selected more than once", item.DeploymentID)
		}
		seen[item.DeploymentID] = struct{}{}
		out = append(out, item)
	}
	return out, nil
}

func findProjectDeployment(items []projectstore.Deployment, id string) projectstore.Deployment {
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	return projectstore.Deployment{}
}

func hashProjectOpenRequest(projectID string, targetsProvided bool, targets []projectOpenTargetSelection) (string, error) {
	encoded, err := json.Marshal(struct {
		ProjectID       string                       `json:"project_id"`
		TargetsProvided bool                         `json:"targets_provided"`
		Targets         []projectOpenTargetSelection `json:"targets"`
	}{ProjectID: projectID, TargetsProvided: targetsProvided, Targets: targets})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func hashNodeOpenRequest(nodeID, cwdRel string) (string, error) {
	encoded, err := json.Marshal(struct {
		NodeID string `json:"node_id"`
		CWDRel string `json:"cwd_rel"`
	}{NodeID: nodeID, CWDRel: cwdRel})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func projectTargetContextRevision(project projectstore.Project, deployment projectstore.Deployment, permissions protocol.DeploymentPermissions, cwdRel string, prompt protocol.ProjectPrompt, scopes []protocol.PromptScopeRevision, sourceProvenance protocol.SourceProvenance) string {
	material := struct {
		CombinerVersion    string                         `json:"combiner_version"`
		ProjectID          string                         `json:"project_id"`
		DeploymentID       string                         `json:"deployment_id"`
		DeploymentRevision string                         `json:"deployment_revision"`
		NodeID             string                         `json:"node_id"`
		CWDRel             string                         `json:"cwd_rel"`
		Permissions        protocol.DeploymentPermissions `json:"permissions"`
		PromptRevision     string                         `json:"prompt_revision"`
		PromptScopes       []protocol.PromptScopeRevision `json:"prompt_scopes"`
		SourceProvenance   protocol.SourceProvenance      `json:"source_provenance"`
	}{
		CombinerVersion: "nexus-project-context-v3", ProjectID: project.ID,
		DeploymentID: deployment.ID, DeploymentRevision: deployment.AppliedRevision,
		NodeID: deployment.NodeID, CWDRel: cwdRel, Permissions: permissions, PromptRevision: prompt.PromptRevision, PromptScopes: scopes,
		SourceProvenance: sourceProvenance,
	}
	encoded, _ := json.Marshal(material)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func effectiveProjectPermissions(configured protocol.DeploymentPermissions, nodeFullAccess bool) protocol.DeploymentPermissions {
	configured.FullAccess = nodeFullAccess
	return configured
}

func projectSessionContextRevision(project projectstore.Project, targets []projectstore.WorkTarget) string {
	type targetRevision struct {
		DeploymentID    string `json:"deployment_id"`
		ContextRevision string `json:"context_revision"`
		Status          string `json:"status"`
	}
	items := make([]targetRevision, 0, len(targets))
	for _, target := range targets {
		items = append(items, targetRevision{DeploymentID: target.Target.DeploymentID, ContextRevision: target.Target.ContextRevision, Status: string(target.Target.Status)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DeploymentID < items[j].DeploymentID })
	encoded, _ := json.Marshal(struct {
		Version   string           `json:"version"`
		ProjectID string           `json:"project_id"`
		Targets   []targetRevision `json:"targets"`
	}{Version: "nexus-work-session-context-v2", ProjectID: project.ID, Targets: items})
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *Server) projectContextDeliveryView(ctx context.Context, ownerKey, workSessionID, targetID, contextRevision string) (map[string]any, error) {
	status := protocol.ProjectContextReturned
	stored, err := s.projects.GetContextDelivery(ctx, ownerKey, workSessionID, targetID)
	if err == nil {
		if stored.ContextRevision == contextRevision && stored.Status == protocol.ProjectContextHostConsumed {
			status = protocol.ProjectContextHostConsumed
		}
		return map[string]any{"status": status}, nil
	}
	if errors.Is(err, projectstore.ErrContextDeliveryNotFound) {
		return map[string]any{"status": status}, nil
	}
	return nil, err
}

func validateProjectPromptContextBudget(targets []projectstore.WorkTarget) error {
	total := 0
	for _, target := range targets {
		prompt := target.Target.Prompt
		switch target.Target.Status {
		case protocol.TargetReady, protocol.TargetIdle, protocol.TargetRunning:
			if !prompt.Complete || prompt.Sources == nil || strings.TrimSpace(prompt.PromptRevision) == "" {
				return fmt.Errorf("Target %s has incomplete Project Prompt", target.Target.ID)
			}
		}
		if !prompt.Complete {
			continue
		}
		targetBytes := 0
		for _, source := range prompt.Sources {
			if source.Bytes != len([]byte(source.Content)) {
				return fmt.Errorf("Target %s Prompt source %s byte count is inconsistent", target.Target.ID, source.Path)
			}
			targetBytes += source.Bytes
		}
		if targetBytes != prompt.Bytes {
			return fmt.Errorf("Target %s Prompt byte total is inconsistent", target.Target.ID)
		}
		total += targetBytes
		if total > protocol.MaxProjectPromptContextBytes {
			return fmt.Errorf("Project Prompt source bodies total %d bytes", total)
		}
	}
	return nil
}

func validateProjectContextDeliveryEnvelope(result map[string]any) error {
	return validateProjectContextDeliveryEnvelopeForTool("project_context", result)
}

func validateProjectContextDeliveryEnvelopeForTool(name string, result map[string]any) error {
	response, err := gatewayToolResult(name, result, nil)
	if err != nil {
		return fmt.Errorf("build Project Context delivery: %w", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode Project Context delivery: %w", err)
	}
	if len(encoded) > protocol.MaxProjectContextDeliveryBytes {
		return fmt.Errorf("Project Context MCP envelope is %d bytes", len(encoded))
	}
	return nil
}

func workSessionStatusForTargets(targets []projectstore.WorkTarget) protocol.WorkSessionStatus {
	if len(targets) == 0 {
		return protocol.WorkSessionFailed
	}
	ready := 0
	for _, target := range targets {
		if target.Target.Status == protocol.TargetReady || target.Target.Status == protocol.TargetIdle || target.Target.Status == protocol.TargetRunning {
			ready++
		}
	}
	if ready == len(targets) {
		return protocol.WorkSessionReady
	}
	if ready > 0 {
		return protocol.WorkSessionPartial
	}
	return protocol.WorkSessionFailed
}

func mergePromptScope(existing []protocol.PromptScopeRevision, next protocol.PromptScopeRevision) []protocol.PromptScopeRevision {
	out := make([]protocol.PromptScopeRevision, 0, len(existing)+1)
	replaced := false
	for _, item := range existing {
		if item.Scope == next.Scope {
			if !replaced {
				out = append(out, next)
				replaced = true
			}
			continue
		}
		out = append(out, item)
	}
	if !replaced {
		out = append(out, next)
	}
	return out
}

func projectProtocolView(project projectstore.Project) protocol.Project {
	return protocol.Project{ID: project.ID, Name: project.Name, OrchestrationPolicy: project.OrchestrationPolicy, Revision: project.Revision, Enabled: project.Enabled}
}

func projectDeploymentView(deployment projectstore.Deployment, permissions protocol.DeploymentPermissions, online bool, availabilityStatus, lastError string) map[string]any {
	if strings.TrimSpace(lastError) == "" {
		lastError = deployment.LastError
	}
	return map[string]any{
		"id": deployment.ID, "project_id": deployment.ProjectID, "node_id": deployment.NodeID, "working_folder": deployment.WorkingFolder,
		"role": deployment.Role, "purpose": deployment.Purpose, "permissions": permissions,
		"desired_revision": deployment.DesiredRevision, "applied_revision": deployment.AppliedRevision, "enabled": deployment.Enabled,
		"apply_status": deployment.ApplyStatus, "online": online, "availability_status": availabilityStatus, "last_error": lastError,
	}
}

func emptyProjectPrompt() protocol.ProjectPrompt {
	return protocol.ProjectPrompt{PromptRevision: "", Complete: false, Bytes: 0, Sources: []protocol.PromptSource{}}
}

func validateProjectTargetBindAcknowledgment(result map[string]any, expected protocol.ProjectTargetBindRequest) error {
	target, ok := result["target"].(map[string]any)
	if !ok {
		encoded, err := json.Marshal(result["target"])
		if err != nil {
			return errors.New("AgentDock Target bind acknowledgment is missing")
		}
		if err := json.Unmarshal(encoded, &target); err != nil {
			return errors.New("AgentDock Target bind acknowledgment is malformed")
		}
	}
	for key, value := range map[string]string{
		"work_session_id": expected.WorkSessionID, "target_id": expected.TargetID, "project_id": expected.ProjectID,
		"deployment_id": expected.DeploymentID, "cwd_rel": expected.CWDRel, "deployment_revision": expected.DeploymentRevision, "context_revision": expected.ContextRevision,
	} {
		if got, _ := target[key].(string); got != value {
			return fmt.Errorf("AgentDock Target bind acknowledgment mismatched %s", key)
		}
	}
	if err := validateProjectTargetSourceAcknowledgment(target, expected.SourceProvenance); err != nil {
		return fmt.Errorf("AgentDock Target bind acknowledgment %w", err)
	}
	return nil
}

func validateProjectTargetRebindAcknowledgment(result map[string]any, expected protocol.ProjectTargetRebindRequest) error {
	target, ok := result["target"].(map[string]any)
	if !ok {
		encoded, err := json.Marshal(result["target"])
		if err != nil {
			return errors.New("AgentDock Target rebind acknowledgment is missing")
		}
		if err := json.Unmarshal(encoded, &target); err != nil {
			return errors.New("AgentDock Target rebind acknowledgment is malformed")
		}
	}
	for key, value := range map[string]string{
		"work_session_id": expected.WorkSessionID, "target_id": expected.TargetID, "cwd_rel": expected.CWDRel, "context_revision": expected.ContextRevision,
	} {
		if got, _ := target[key].(string); got != value {
			return fmt.Errorf("AgentDock Target rebind acknowledgment mismatched %s", key)
		}
	}
	if err := validateProjectTargetSourceAcknowledgment(target, expected.SourceProvenance); err != nil {
		return fmt.Errorf("AgentDock Target rebind acknowledgment %w", err)
	}
	return nil
}

func validateProjectTargetSourceAcknowledgment(target map[string]any, expected protocol.SourceProvenance) error {
	encoded, err := json.Marshal(target["source_provenance"])
	if err != nil {
		return errors.New("source_provenance is malformed")
	}
	var got protocol.SourceProvenance
	if err := json.Unmarshal(encoded, &got); err != nil {
		return errors.New("source_provenance is malformed")
	}
	if err := got.Validate(); err != nil {
		return fmt.Errorf("contains invalid source_provenance: %w", err)
	}
	if got != expected {
		return errors.New("mismatched source_provenance")
	}
	return nil
}

func projectToolError(code, message string, details map[string]any) (map[string]any, error) {
	result := map[string]any{"code": code}
	for key, value := range details {
		result[key] = value
	}
	return result, errors.New(message)
}

func projectToolStoreError(err error) (map[string]any, error) {
	switch {
	case errors.Is(err, projectstore.ErrProjectNotFound):
		return projectToolError(protocol.ErrorProjectNotFound, err.Error(), nil)
	case errors.Is(err, projectstore.ErrDeploymentNotFound):
		return projectToolError(protocol.ErrorDeploymentNotReady, err.Error(), nil)
	default:
		return projectToolError("PROJECT_OPERATION_FAILED", err.Error(), nil)
	}
}

func projectToolSessionError(err error) (map[string]any, error) {
	switch {
	case errors.Is(err, projectstore.ErrWorkSessionNotFound), errors.Is(err, projectstore.ErrWorkTargetNotFound):
		return projectToolError(protocol.ErrorSessionTargetDenied, err.Error(), nil)
	default:
		return projectToolError("PROJECT_OPERATION_FAILED", err.Error(), nil)
	}
}

func decodeProjectToolArgs(args map[string]any, destination any) error {
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

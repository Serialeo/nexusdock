package httpx

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/gorilla/websocket"
	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/config"
	"github.com/uvwt/nexusdock/internal/core"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

const projectAPITestToken = "project-api-test-token"

func TestProjectAdminMutationsAreNotModelFacingMCPTools(t *testing.T) {
	forbidden := map[string]bool{
		"project_create": true, "project_update": true, "project_delete": true, "project_manage": true,
		"deployment_create": true, "deployment_update": true, "deployment_delete": true, "deployment_apply": true,
		"project_deployment_manage": true,
	}
	for _, tool := range nexusToolDefinitions() {
		if forbidden[tool.Name] {
			t.Fatalf("admin mutation tool %q leaked into model-facing MCP", tool.Name)
		}
	}
}

func TestProjectAdminAPIAuthStrictJSONCASAndOfflineDesiredState(t *testing.T) {
	server, handler, projects, nodes, db := newProjectsHTTPTestServer(t)
	_ = server
	node := pairProjectHTTPTestNode(t, nodes, "device_project_offline_12345678", "OfflineNode")

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/v1/projects", strings.NewReader(`{"name":"Alpha"}`)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	bad := projectAPIRequest(t, handler, http.MethodPost, "/v1/projects", `{"name":"Alpha","unexpected":true}`)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "INVALID_JSON") {
		t.Fatalf("strict JSON status=%d body=%s", bad.Code, bad.Body.String())
	}

	created := projectAPIRequest(t, handler, http.MethodPost, "/v1/projects", `{"name":"Alpha","orchestration_policy":"policy-v1"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", created.Code, created.Body.String())
	}
	project := decodeProjectHTTPResponse(t, created.Body.Bytes())
	if project.ID == "" || project.Revision != "rev-1" || !project.Enabled {
		t.Fatalf("created project = %#v", project)
	}

	updated := projectAPIRequest(t, handler, http.MethodPut, "/v1/projects/"+project.ID, `{"expected_revision":"rev-1","name":"Renamed","orchestration_policy":"policy-v2","enabled":true}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update project status=%d body=%s", updated.Code, updated.Body.String())
	}
	renamed := decodeProjectHTTPResponse(t, updated.Body.Bytes())
	if renamed.ID != project.ID || renamed.Name != "Renamed" || renamed.Revision != "rev-2" {
		t.Fatalf("renamed project = %#v", renamed)
	}
	stale := projectAPIRequest(t, handler, http.MethodPut, "/v1/projects/"+project.ID, `{"expected_revision":"rev-1","name":"Stale","orchestration_policy":"","enabled":true}`)
	if stale.Code != http.StatusPreconditionFailed || strings.Contains(stale.Body.String(), "current_revision") {
		t.Fatalf("stale project update status=%d body=%s", stale.Code, stale.Body.String())
	}

	windowsPath := `C:\Users\Alice\Source Repo`
	body, _ := json.Marshal(map[string]any{
		"node_id": node.ID, "working_folder": windowsPath, "role": "backend", "purpose": "build",
		"permissions": map[string]any{"files": "read_write", "shell": true, "browser": false, "dynamic_mcp": false, "acp": false},
	})
	createdDeployment := projectAPIRequest(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/deployments", string(body))
	if createdDeployment.Code != http.StatusCreated {
		t.Fatalf("create deployment status=%d body=%s", createdDeployment.Code, createdDeployment.Body.String())
	}
	deployment := decodeDeploymentHTTPResponse(t, createdDeployment.Body.Bytes())
	if deployment.NodeID != node.ID || deployment.WorkingFolder != windowsPath || deployment.ApplyStatus != "pending" || deployment.DesiredRevision != "rev-1" || deployment.AppliedRevision != "" {
		t.Fatalf("offline deployment = %#v", deployment)
	}
	if !strings.Contains(deployment.LastError, "离线") {
		t.Fatalf("offline pending reason = %q", deployment.LastError)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM project_deployments WHERE project_id = ?`, project.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("deployment persistence count=%d err=%v", count, err)
	}
	stored, err := projects.GetDeployment(t.Context(), project.ID, deployment.ID)
	if err != nil || stored.WorkingFolder != windowsPath {
		t.Fatalf("stored deployment = %#v err=%v", stored, err)
	}
	updateBody, _ := json.Marshal(map[string]any{
		"expected_revision": deployment.DesiredRevision, "working_folder": "", "role": "backend-v2", "purpose": "build-v2", "enabled": true,
		"permissions": map[string]any{"files": "read_write", "shell": true, "browser": false, "dynamic_mcp": false, "acp": false},
	})
	updatedDeploymentResponse := projectAPIRequest(t, handler, http.MethodPut, "/v1/projects/"+project.ID+"/deployments/"+deployment.ID, string(updateBody))
	if updatedDeploymentResponse.Code != http.StatusOK {
		t.Fatalf("update deployment status=%d body=%s", updatedDeploymentResponse.Code, updatedDeploymentResponse.Body.String())
	}
	updatedDeployment := decodeDeploymentHTTPResponse(t, updatedDeploymentResponse.Body.Bytes())
	if updatedDeployment.ID != deployment.ID || updatedDeployment.WorkingFolder != "" || updatedDeployment.DesiredRevision != "rev-2" || updatedDeployment.Role != "backend-v2" || updatedDeployment.ApplyStatus != "pending" {
		t.Fatalf("updated deployment = %#v", updatedDeployment)
	}
	staleDeploymentResponse := projectAPIRequest(t, handler, http.MethodPut, "/v1/projects/"+project.ID+"/deployments/"+deployment.ID, string(updateBody))
	if staleDeploymentResponse.Code != http.StatusPreconditionFailed || strings.Contains(staleDeploymentResponse.Body.String(), "current_revision") {
		t.Fatalf("stale deployment update status=%d body=%s", staleDeploymentResponse.Code, staleDeploymentResponse.Body.String())
	}

	for _, legacy := range []string{
		"/v1/settings/instructions",
		"/v1/runtime/nodes/" + node.ID + "/instructions",
		"/v1/runtime/nodes/" + node.ID + "/direct-instructions",
		"/v1/runtime/nodes/" + node.ID + "/file-access",
	} {
		response := projectAPIRequest(t, handler, http.MethodGet, legacy, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("legacy route %s status=%d body=%s", legacy, response.Code, response.Body.String())
		}
	}
}

func TestNodeSessionSettingsAreExplicitAndHiddenFromProjects(t *testing.T) {
	_, handler, projects, nodes, db := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_node_session_settings_12345678", "SettingsNode")

	initial := projectAPIRequest(t, handler, http.MethodGet, "/v1/runtime/nodes/"+node.ID+"/session", "")
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"configured":false`) || !strings.Contains(initial.Body.String(), `"files":"none"`) {
		t.Fatalf("initial node session status=%d body=%s", initial.Code, initial.Body.String())
	}
	var internalProjects int
	if err := db.QueryRow(`SELECT COUNT(*) FROM projects WHERE kind='node'`).Scan(&internalProjects); err != nil || internalProjects != 0 {
		t.Fatalf("GET created node session container: count=%d err=%v", internalProjects, err)
	}

	updated := projectAPIRequest(t, handler, http.MethodPut, "/v1/runtime/nodes/"+node.ID+"/session", `{"enabled":true,"permissions":{"files":"read_only","shell":true,"browser":false,"dynamic_mcp":false,"acp":false}}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"configured":true`) || !strings.Contains(updated.Body.String(), `"enabled":true`) {
		t.Fatalf("update node session status=%d body=%s", updated.Code, updated.Body.String())
	}
	configured, err := projects.GetNodeSession(t.Context(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if configured.Deployment.WorkingFolder != "" || configured.Deployment.Permissions.Files != protocol.FileCapabilityReadOnly || !configured.Deployment.Permissions.Shell {
		t.Fatalf("stored node session = %#v", configured)
	}
	list := projectAPIRequest(t, handler, http.MethodGet, "/v1/projects", "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), configured.Project.ID) {
		t.Fatalf("node session leaked in projects list: status=%d body=%s", list.Code, list.Body.String())
	}
	direct := projectAPIRequest(t, handler, http.MethodGet, "/v1/projects/"+configured.Project.ID, "")
	if direct.Code != http.StatusNotFound {
		t.Fatalf("node session reachable as Project: status=%d body=%s", direct.Code, direct.Body.String())
	}
	deleted := projectAPIRequest(t, handler, http.MethodDelete, "/v1/runtime/nodes/"+node.ID, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete node with internal session status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, err := projects.GetNodeSession(t.Context(), node.ID); !errors.Is(err, projectstore.ErrNodeSessionNotFound) {
		t.Fatalf("node deletion kept internal session: %v", err)
	}
}

func TestNodeSessionSettingsRetryCompletesRevokeAndApply(t *testing.T) {
	server, handler, projects, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_node_session_revoke_12345678", "NodeSessionRevoke")
	configured, _, err := projects.PutNodeSessionConfiguration(t.Context(), node.ID, true, protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	socket, closeNode := connectProjectFakeNode(t, server, node)
	defer closeNode()
	initialApply := readProjectInvoke(t, socket)
	if initialApply.Operation != protocol.OperationProjectDeploymentApply {
		t.Fatalf("initial node session operation = %q", initialApply.Operation)
	}
	var initialPayload protocol.Deployment
	if err := json.Unmarshal(initialApply.Arguments, &initialPayload); err != nil {
		t.Fatal(err)
	}
	writeProjectResult(t, socket, initialApply.RequestID, map[string]any{"deployment": initialPayload})
	configured.Deployment = waitForDeploymentState(t, projects, configured.Project.ID, configured.Deployment.ID, func(value projectstore.Deployment) bool {
		return value.ApplyStatus == string(protocol.DeploymentApplyApplied) && value.AppliedRevision == value.DesiredRevision
	})

	owner := "mcp:node-session-revoke"
	session, _, err := projects.BeginWorkSession(t.Context(), owner, configured.Project.ID, "node-session-revoke", "sha256:node-session-revoke", configured.Project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	target, err := projects.PutWorkTarget(t.Context(), owner, projectstore.WorkTarget{Target: protocol.WorkTarget{
		WorkSessionID: session.ID, ProjectID: configured.Project.ID, DeploymentID: configured.Deployment.ID, NodeID: node.ID, CWDRel: ".",
		DeploymentRevision: configured.Deployment.AppliedRevision, ContextRevision: "sha256:node-session-target", Status: protocol.TargetReady,
		Permissions: configured.Deployment.Permissions,
		Prompt:      protocol.ProjectPrompt{PromptRevision: "sha256:node-session-empty", Complete: true, Sources: []protocol.PromptSource{}},
	}, PromptScopes: []protocol.PromptScopeRevision{{Scope: ".", PromptRevision: "sha256:node-session-empty"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.UpdateWorkSessionContext(t.Context(), owner, session.ID, configured.Project.Revision, protocol.WorkSessionReady, "sha256:node-session-context"); err != nil {
		t.Fatal(err)
	}
	// Simulate a previous PUT whose desired-state transaction committed before
	// its request was cancelled. Retrying the same body must still revoke the
	// stale Target and converge the pending Deployment.
	interrupted, created, err := projects.PutNodeSessionConfiguration(t.Context(), node.ID, false, protocol.DeploymentPermissions{Files: protocol.FileCapabilityNone})
	if err != nil || created || interrupted.Deployment.ApplyStatus != string(protocol.DeploymentApplyPending) {
		t.Fatalf("interrupted node session update = %#v created=%v err=%v", interrupted, created, err)
	}
	stillReady, err := projects.GetWorkTarget(t.Context(), owner, session.ID, target.Target.ID)
	if err != nil || stillReady.Target.Status != protocol.TargetReady {
		t.Fatalf("interrupted update unexpectedly revoked Target: %#v err=%v", stillReady, err)
	}

	responseDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseDone <- projectAPIRequest(t, handler, http.MethodPut, "/v1/runtime/nodes/"+node.ID+"/session", `{"enabled":false,"permissions":{"files":"none","shell":false,"browser":false,"dynamic_mcp":false,"acp":false}}`)
	}()
	revoke := readProjectInvoke(t, socket)
	if revoke.Operation != protocol.OperationProjectTargetRevoke {
		t.Fatalf("first node session update operation = %q", revoke.Operation)
	}
	storedTarget, err := projects.GetWorkTarget(t.Context(), owner, session.ID, target.Target.ID)
	if err != nil || storedTarget.Target.Status != protocol.TargetRevoked {
		t.Fatalf("node session Target was not persisted revoked before Bridge ack: %#v err=%v", storedTarget, err)
	}
	writeProjectResult(t, socket, revoke.RequestID, map[string]any{"target_id": target.Target.ID, "revoked": true})

	apply := readProjectInvoke(t, socket)
	if apply.Operation != protocol.OperationProjectDeploymentApply {
		t.Fatalf("second node session update operation = %q", apply.Operation)
	}
	var disabledPayload protocol.Deployment
	if err := json.Unmarshal(apply.Arguments, &disabledPayload); err != nil {
		t.Fatal(err)
	}
	if disabledPayload.Enabled || disabledPayload.ApplyStatus != protocol.DeploymentApplyDisabled {
		t.Fatalf("disabled node session apply payload = %#v", disabledPayload)
	}
	writeProjectResult(t, socket, apply.RequestID, map[string]any{"deployment": disabledPayload})
	response := <-responseDone
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"enabled":false`) {
		t.Fatalf("disable node session status=%d body=%s", response.Code, response.Body.String())
	}
	denied, callErr := server.callNodeOpen(withMCPClientBinding(t.Context(), owner), map[string]any{"node_id": node.ID, "client_request_id": "disabled-node-session"})
	if callErr == nil || denied["code"] != "NODE_SESSION_DISABLED" {
		t.Fatalf("disabled node_open = %#v err=%v", denied, callErr)
	}
}

func TestNodeFullAccessIsIndependentFromOptionalProjectFolder(t *testing.T) {
	_, handler, projects, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_full_access_12345678", "FullAccessNode")
	project := createProjectThroughAPI(t, handler, "Full Access")
	deployment := createDeploymentThroughAPI(t, handler, project.ID, node.ID, "")
	if deployment.WorkingFolder != "" || deployment.Permissions.FullAccess {
		t.Fatalf("initial folderless deployment = %#v", deployment)
	}

	response := projectAPIRequest(t, handler, http.MethodPatch, "/v1/runtime/nodes/"+node.ID, `{"full_access":true}`)
	if response.Code != http.StatusOK {
		t.Fatalf("enable Full Access status=%d body=%s", response.Code, response.Body.String())
	}
	get := projectAPIRequest(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/deployments/"+deployment.ID, "")
	if get.Code != http.StatusOK {
		t.Fatalf("get Full Access deployment status=%d body=%s", get.Code, get.Body.String())
	}
	effective := decodeDeploymentHTTPResponse(t, get.Body.Bytes())
	if effective.WorkingFolder != "" || !effective.Permissions.FullAccess || effective.DesiredRevision != "rev-2" {
		t.Fatalf("effective Full Access deployment = %#v", effective)
	}
	stored, err := projects.GetDeployment(t.Context(), project.ID, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Permissions.FullAccess {
		t.Fatalf("Node Full Access leaked into Deployment desired permissions: %#v", stored.Permissions)
	}

	response = projectAPIRequest(t, handler, http.MethodPatch, "/v1/runtime/nodes/"+node.ID, `{"full_access":false}`)
	if response.Code != http.StatusOK {
		t.Fatalf("disable Full Access status=%d body=%s", response.Code, response.Body.String())
	}
	get = projectAPIRequest(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/deployments/"+deployment.ID, "")
	effective = decodeDeploymentHTTPResponse(t, get.Body.Bytes())
	if effective.Permissions.FullAccess || effective.DesiredRevision != "rev-3" {
		t.Fatalf("disabled Full Access deployment = %#v", effective)
	}
}

func TestTryApplyProjectDeploymentSkipsSupersededSnapshot(t *testing.T) {
	server, _, projects, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_stale_apply_12345678", "StaleApplyNode")
	project, err := projects.CreateProject(t.Context(), projectstore.CreateProjectInput{Name: "Stale Apply", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := projects.CreateDeployment(t.Context(), projectstore.CreateDeploymentInput{
		ProjectID: project.ID, NodeID: node.ID, Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadOnly}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := projects.UpdateDeployment(t.Context(), project.ID, stale.ID, projectstore.UpdateDeploymentInput{
		ExpectedRevision: stale.DesiredRevision, Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadOnly}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := server.tryApplyProjectDeployment(t.Context(), stale)
	if got.DesiredRevision != current.DesiredRevision || got.ApplyStatus != current.ApplyStatus {
		t.Fatalf("stale apply returned %#v, want current %#v", got, current)
	}
	stored, err := projects.GetDeployment(t.Context(), project.ID, stale.ID)
	if err != nil || stored.DesiredRevision != current.DesiredRevision || stored.ApplyStatus != string(protocol.DeploymentApplyPending) {
		t.Fatalf("stale apply changed current desired state: %#v err=%v", stored, err)
	}
}

func TestOfflineDeploymentReconcilesOnV4NodeReconnect(t *testing.T) {
	server, handler, projects, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_reconnect_12345678", "ReconnectNode")
	project := createProjectThroughAPI(t, handler, "Reconnect")
	root := t.TempDir()
	deployment := createDeploymentThroughAPI(t, handler, project.ID, node.ID, root)
	if deployment.ApplyStatus != "pending" {
		t.Fatalf("offline deployment = %#v", deployment)
	}

	socket, closeNode := connectProjectFakeNode(t, server, node)
	defer closeNode()
	invoke := readProjectInvoke(t, socket)
	if invoke.Operation != protocol.OperationProjectDeploymentApply {
		t.Fatalf("reconnect operation = %q", invoke.Operation)
	}
	var payload protocol.Deployment
	if err := json.Unmarshal(invoke.Arguments, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID != deployment.ID || payload.WorkingFolder != root || payload.AppliedRevision != deployment.DesiredRevision || payload.ApplyStatus != protocol.DeploymentApplyApplied {
		t.Fatalf("apply payload = %#v", payload)
	}
	writeProjectResult(t, socket, invoke.RequestID, map[string]any{"deployment": payload})

	applied := waitForDeploymentState(t, projects, project.ID, deployment.ID, func(value projectstore.Deployment) bool {
		return value.ApplyStatus == "applied" && value.AppliedRevision == value.DesiredRevision
	})
	if applied.LastError != "" {
		t.Fatalf("applied deployment retained error: %#v", applied)
	}
}

func TestDeploymentUpdateRevokesPreparedTargetBeforeApplyingNewRevision(t *testing.T) {
	server, handler, projects, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_target_revoke_12345678", "RevokeNode")
	project := createProjectThroughAPI(t, handler, "Target Revoke")
	root := t.TempDir()
	deployment := createDeploymentThroughAPI(t, handler, project.ID, node.ID, root)

	socket, closeNode := connectProjectFakeNode(t, server, node)
	defer closeNode()
	initialApply := readProjectInvoke(t, socket)
	if initialApply.Operation != protocol.OperationProjectDeploymentApply {
		t.Fatalf("initial operation = %q", initialApply.Operation)
	}
	var initialPayload protocol.Deployment
	if err := json.Unmarshal(initialApply.Arguments, &initialPayload); err != nil {
		t.Fatal(err)
	}
	writeProjectResult(t, socket, initialApply.RequestID, map[string]any{"deployment": initialPayload})
	deployment = waitForDeploymentState(t, projects, project.ID, deployment.ID, func(value projectstore.Deployment) bool {
		return value.ApplyStatus == "applied" && value.AppliedRevision == value.DesiredRevision
	})

	owner := "mcp:target-revoke-test"
	session, _, err := projects.BeginWorkSession(t.Context(), owner, project.ID, "request-target-revoke", "sha256:target-revoke", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	target, err := projects.PutWorkTarget(t.Context(), owner, projectstore.WorkTarget{Target: protocol.WorkTarget{
		WorkSessionID: session.ID, ProjectID: project.ID, DeploymentID: deployment.ID, NodeID: node.ID, CWDRel: ".",
		DeploymentRevision: deployment.AppliedRevision, ContextRevision: "sha256:target-context", Status: protocol.TargetReady,
		Permissions: deployment.Permissions,
		Prompt:      protocol.ProjectPrompt{PromptRevision: "sha256:target-prompt", Complete: true, Sources: []protocol.PromptSource{}},
	}, PromptScopes: []protocol.PromptScopeRevision{{Scope: ".", PromptRevision: "sha256:target-prompt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.UpdateWorkSessionContext(t.Context(), owner, session.ID, project.Revision, protocol.WorkSessionReady, "sha256:session-context"); err != nil {
		t.Fatal(err)
	}

	updateBody, _ := json.Marshal(map[string]any{
		"expected_revision": deployment.DesiredRevision,
		"working_folder":    root,
		"role":              "updated",
		"purpose":           "new revision",
		"enabled":           true,
		"permissions":       deployment.Permissions,
	})
	responseDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseDone <- projectAPIRequest(t, handler, http.MethodPut, "/v1/projects/"+project.ID+"/deployments/"+deployment.ID, string(updateBody))
	}()

	revoke := readProjectInvoke(t, socket)
	if revoke.Operation != protocol.OperationProjectTargetRevoke {
		t.Fatalf("first update operation = %q, want %q", revoke.Operation, protocol.OperationProjectTargetRevoke)
	}
	var revokeRequest protocol.ProjectTargetRevokeRequest
	if err := json.Unmarshal(revoke.Arguments, &revokeRequest); err != nil {
		t.Fatal(err)
	}
	if revokeRequest.TargetID != target.Target.ID {
		t.Fatalf("revoke request = %#v, want target %s", revokeRequest, target.Target.ID)
	}
	storedTarget, err := projects.GetWorkTarget(t.Context(), owner, session.ID, target.Target.ID)
	if err != nil || storedTarget.Target.Status != protocol.TargetRevoked {
		t.Fatalf("Target was not persisted revoked before Bridge ack: %#v err=%v", storedTarget, err)
	}
	storedSession, err := projects.GetWorkSession(t.Context(), owner, session.ID)
	if err != nil || storedSession.Status != protocol.WorkSessionFailed {
		t.Fatalf("WorkSession was not downgraded before Bridge ack: %#v err=%v", storedSession, err)
	}
	writeProjectResult(t, socket, revoke.RequestID, map[string]any{"target_id": target.Target.ID, "revoked": true})

	apply := readProjectInvoke(t, socket)
	if apply.Operation != protocol.OperationProjectDeploymentApply {
		t.Fatalf("second update operation = %q, want %q", apply.Operation, protocol.OperationProjectDeploymentApply)
	}
	var updatedPayload protocol.Deployment
	if err := json.Unmarshal(apply.Arguments, &updatedPayload); err != nil {
		t.Fatal(err)
	}
	if updatedPayload.DesiredRevision == deployment.DesiredRevision || updatedPayload.AppliedRevision != updatedPayload.DesiredRevision {
		t.Fatalf("updated apply payload = %#v", updatedPayload)
	}
	writeProjectResult(t, socket, apply.RequestID, map[string]any{"deployment": updatedPayload})

	response := <-responseDone
	if response.Code != http.StatusOK {
		t.Fatalf("deployment update status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOnlineApplyFailureKeepsDesiredStateAndReportsFailed(t *testing.T) {
	server, handler, _, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_failed_12345678", "FailedNode")
	socket, closeNode := connectProjectFakeNode(t, server, node)
	defer closeNode()
	project := createProjectThroughAPI(t, handler, "Failure")

	responseDone := make(chan *httptest.ResponseRecorder, 1)
	root := t.TempDir()
	go func() {
		responseDone <- deploymentCreateRecorder(handler, project.ID, node.ID, root)
	}()
	invoke := readProjectInvoke(t, socket)
	if invoke.Operation != protocol.OperationProjectDeploymentApply {
		t.Fatalf("operation = %q", invoke.Operation)
	}
	if err := socket.WriteJSON(protocol.Message{Type: protocol.MessageToolError, RequestID: invoke.RequestID, Error: &protocol.RemoteError{Code: protocol.ErrorDeploymentNotReady, Message: "working folder unavailable", Category: "runtime"}}); err != nil {
		t.Fatal(err)
	}
	response := <-responseDone
	if response.Code != http.StatusCreated {
		t.Fatalf("failed apply save status=%d body=%s", response.Code, response.Body.String())
	}
	deployment := decodeDeploymentHTTPResponse(t, response.Body.Bytes())
	if deployment.ApplyStatus != "failed" || deployment.DesiredRevision != "rev-1" || deployment.AppliedRevision != "" || !strings.Contains(deployment.LastError, "working folder unavailable") {
		t.Fatalf("failed desired state = %#v", deployment)
	}
}

func TestOnlineApplyRejectsMismatchedSuccessfulAcknowledgment(t *testing.T) {
	server, handler, _, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_bad_ack_12345678", "BadAckNode")
	socket, closeNode := connectProjectFakeNode(t, server, node)
	defer closeNode()
	project := createProjectThroughAPI(t, handler, "BadAck")

	responseDone := make(chan *httptest.ResponseRecorder, 1)
	root := t.TempDir()
	go func() {
		responseDone <- deploymentCreateRecorder(handler, project.ID, node.ID, root)
	}()
	invoke := readProjectInvoke(t, socket)
	if invoke.Operation != protocol.OperationProjectDeploymentApply {
		t.Fatalf("operation = %q", invoke.Operation)
	}
	var payload protocol.Deployment
	if err := json.Unmarshal(invoke.Arguments, &payload); err != nil {
		t.Fatal(err)
	}
	payload.AppliedRevision = "rev-999"
	writeProjectResult(t, socket, invoke.RequestID, map[string]any{"deployment": payload})

	response := <-responseDone
	if response.Code != http.StatusCreated {
		t.Fatalf("bad ack save status=%d body=%s", response.Code, response.Body.String())
	}
	deployment := decodeDeploymentHTTPResponse(t, response.Body.Bytes())
	if deployment.ApplyStatus != "failed" || deployment.AppliedRevision != "" || deployment.DesiredRevision != "rev-1" || !strings.Contains(deployment.LastError, "acknowledgment") {
		t.Fatalf("mismatched success acknowledgment was accepted: %#v", deployment)
	}
}

func TestKnownOldProtocolNodeFailsWithoutLegacyFallback(t *testing.T) {
	_, handler, _, nodes, db := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_old_12345678", "OldNode")
	if _, err := db.Exec(`UPDATE agentdock_devices SET protocol_version = '3' WHERE id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	project := createProjectThroughAPI(t, handler, "OldProtocol")
	response := deploymentCreateRecorder(handler, project.ID, node.ID, t.TempDir())
	if response.Code != http.StatusCreated {
		t.Fatalf("save on old node status=%d body=%s", response.Code, response.Body.String())
	}
	deployment := decodeDeploymentHTTPResponse(t, response.Body.Bytes())
	if deployment.ApplyStatus != "failed" || !strings.Contains(deployment.LastError, "Bridge v4") {
		t.Fatalf("old protocol state = %#v", deployment)
	}
}

func TestOfflineDeletePersistsRemovalUntilV4Reconnect(t *testing.T) {
	server, handler, projects, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_remove_12345678", "RemoveNode")
	project := createProjectThroughAPI(t, handler, "Remove")
	working := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(working, 0o700); err != nil {
		t.Fatal(err)
	}
	deployment := createDeploymentThroughAPI(t, handler, project.ID, node.ID, working)
	deleteBody := `{"expected_revision":"` + deployment.DesiredRevision + `"}`
	deleted := projectAPIRequest(t, handler, http.MethodDelete, "/v1/projects/"+project.ID+"/deployments/"+deployment.ID, deleteBody)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete deployment status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(working); err != nil {
		t.Fatalf("working folder removed by control-plane delete: %v", err)
	}
	removals, err := projects.ListPendingRemovalsForNode(t.Context(), node.ID)
	if err != nil || len(removals) != 1 {
		t.Fatalf("pending removals = %#v err=%v", removals, err)
	}

	socket, closeNode := connectProjectFakeNode(t, server, node)
	defer closeNode()
	invoke := readProjectInvoke(t, socket)
	if invoke.Operation != protocol.OperationProjectDeploymentRemove {
		t.Fatalf("reconnect removal operation = %q", invoke.Operation)
	}
	var request protocol.ProjectDeploymentRemoveRequest
	if err := json.Unmarshal(invoke.Arguments, &request); err != nil {
		t.Fatal(err)
	}
	if request.DeploymentID != deployment.ID {
		t.Fatalf("removal payload = %#v", request)
	}
	writeProjectResult(t, socket, invoke.RequestID, map[string]any{"deployment_id": deployment.ID, "removed": true})
	deadline := time.Now().Add(2 * time.Second)
	for {
		removals, err = projects.ListPendingRemovalsForNode(t.Context(), node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(removals) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("removal tombstone was not cleared: %#v", removals)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(working); err != nil {
		t.Fatalf("working folder removed after Node authorization cleanup: %v", err)
	}
}

func TestRemovalTombstoneSurvivesMismatchedSuccessfulAcknowledgment(t *testing.T) {
	server, handler, projects, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_remove_bad_ack_12345678", "RemoveBadAckNode")
	project := createProjectThroughAPI(t, handler, "RemoveBadAck")
	deployment := createDeploymentThroughAPI(t, handler, project.ID, node.ID, t.TempDir())
	deleted := projectAPIRequest(t, handler, http.MethodDelete, "/v1/projects/"+project.ID+"/deployments/"+deployment.ID, `{"expected_revision":"`+deployment.DesiredRevision+`"}`)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete deployment status=%d body=%s", deleted.Code, deleted.Body.String())
	}

	socket, closeNode := connectProjectFakeNode(t, server, node)
	defer closeNode()
	invoke := readProjectInvoke(t, socket)
	if invoke.Operation != protocol.OperationProjectDeploymentRemove {
		t.Fatalf("reconnect removal operation = %q", invoke.Operation)
	}
	writeProjectResult(t, socket, invoke.RequestID, map[string]any{"deployment_id": deployment.ID, "removed": false})

	deadline := time.Now().Add(2 * time.Second)
	for {
		removals, err := projects.ListPendingRemovalsForNode(t.Context(), node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(removals) == 1 && strings.Contains(removals[0].LastError, "acknowledgment") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bad removal acknowledgment did not leave a retry tombstone: %#v", removals)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newProjectsHTTPTestServer(t *testing.T) (*Server, http.Handler, *projectstore.Store, *agentdock.Store, *sql.DB) {
	t.Helper()
	db, err := core.OpenSQLite(t.Context(), ":memory:", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := core.EnsureSchema(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	nodes, err := agentdock.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := projectstore.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(config.Config{AuthToken: projectAPITestToken, RequireAuth: true}, nil, nil, slog.Default(), WithSystemDatabase(db), WithAgentDockNodes(nodes), WithProjects(projects))
	return server, server.Handler(), projects, nodes, db
}

func pairProjectHTTPTestNode(t *testing.T, store *agentdock.Store, deviceID, name string) agentdock.Node {
	t.Helper()
	pairing, err := store.CreatePairingCode(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.Pair(t.Context(), agentdock.PairInput{Code: pairing.Code, DeviceID: deviceID, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func projectAPIRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+projectAPITestToken)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func createProjectThroughAPI(t *testing.T, handler http.Handler, name string) projectstore.Project {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "orchestration_policy": ""})
	response := projectAPIRequest(t, handler, http.MethodPost, "/v1/projects", string(body))
	if response.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", response.Code, response.Body.String())
	}
	return decodeProjectHTTPResponse(t, response.Body.Bytes())
}

func deploymentCreateRecorder(handler http.Handler, projectID, nodeID, workingFolder string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{
		"node_id": nodeID, "working_folder": workingFolder, "role": "test", "purpose": "test",
		"permissions": map[string]any{"files": "read_write", "shell": true, "browser": false, "dynamic_mcp": false, "acp": false},
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/projects/"+projectID+"/deployments", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+projectAPITestToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func createDeploymentThroughAPI(t *testing.T, handler http.Handler, projectID, nodeID, workingFolder string) projectstore.Deployment {
	t.Helper()
	response := deploymentCreateRecorder(handler, projectID, nodeID, workingFolder)
	if response.Code != http.StatusCreated {
		t.Fatalf("create deployment status=%d body=%s", response.Code, response.Body.String())
	}
	return decodeDeploymentHTTPResponse(t, response.Body.Bytes())
}

func decodeProjectHTTPResponse(t *testing.T, data []byte) projectstore.Project {
	t.Helper()
	var response struct {
		Project projectstore.Project `json:"project"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	return response.Project
}

func decodeDeploymentHTTPResponse(t *testing.T, data []byte) projectstore.Deployment {
	t.Helper()
	var response struct {
		Deployment projectstore.Deployment `json:"deployment"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	return response.Deployment
}

func connectProjectFakeNode(t *testing.T, server *Server, node agentdock.Node) (*websocket.Conn, func()) {
	return connectProjectFakeNodeWithTools(t, server, node, nil)
}

func connectProjectFakeNodeWithTools(t *testing.T, server *Server, node agentdock.Node, tools []protocol.ToolDescriptor) (*websocket.Conn, func()) {
	t.Helper()
	acceptErr := make(chan error, 1)
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptErr <- server.agentDockHub.Accept(w, r, node.ID)
	}))
	socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(bridge.URL, "http"), nil)
	if err != nil {
		bridge.Close()
		t.Fatal(err)
	}
	capabilities := make([]string, 0, len(tools))
	for _, tool := range tools {
		capabilities = append(capabilities, tool.Name)
	}
	hello := protocol.Message{
		Type: protocol.MessageNodeHello, ProtocolVersion: protocol.ConnectionProtocolVersion,
		Hello: &protocol.Hello{
			DeviceID: node.DeviceID, Version: "test", ProtocolVersion: protocol.ConnectionProtocolVersion, OS: "linux", Arch: "amd64",
			Capabilities: capabilities, BridgeCapabilities: []string{}, ToolContractHash: "", Tools: tools, UIResources: []protocol.UIResourceCapability{},
		},
	}
	if err := socket.WriteJSON(hello); err != nil {
		socket.Close()
		bridge.Close()
		t.Fatal(err)
	}
	var ready protocol.Message
	if err := socket.ReadJSON(&ready); err != nil || ready.Type != protocol.MessageNodeReady {
		socket.Close()
		bridge.Close()
		t.Fatalf("ready=%#v err=%v", ready, err)
	}
	select {
	case err := <-acceptErr:
		if err != nil {
			socket.Close()
			bridge.Close()
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		socket.Close()
		bridge.Close()
		t.Fatal("Node Accept did not return after handshake")
	}
	return socket, func() {
		_ = socket.Close()
		bridge.Close()
	}
}

func readProjectInvoke(t *testing.T, socket *websocket.Conn) protocol.Message {
	t.Helper()
	_ = socket.SetReadDeadline(time.Now().Add(2 * time.Second))
	var invoke protocol.Message
	if err := socket.ReadJSON(&invoke); err != nil {
		t.Fatal(err)
	}
	if invoke.Type != protocol.MessageToolInvoke || invoke.RequestID == "" {
		t.Fatalf("invoke = %#v", invoke)
	}
	return invoke
}

func writeProjectResult(t *testing.T, socket *websocket.Conn, requestID string, result any) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.WriteJSON(protocol.Message{Type: protocol.MessageToolResult, RequestID: requestID, Result: encoded}); err != nil {
		t.Fatal(err)
	}
}

func waitForDeploymentState(t *testing.T, store *projectstore.Store, projectID, deploymentID string, ready func(projectstore.Deployment) bool) projectstore.Deployment {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		value, err := store.GetDeployment(t.Context(), projectID, deploymentID)
		if err != nil {
			t.Fatal(err)
		}
		if ready(value) {
			return value
		}
		if time.Now().After(deadline) {
			t.Fatalf("deployment did not reach expected state: %#v", value)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProjectHTTPTestHelpersUseExpectedErrorValues(t *testing.T) {
	if !errors.Is(agentdock.ErrNodeOffline, agentdock.ErrNodeOffline) {
		t.Fatal("unexpected errors.Is behavior")
	}
}

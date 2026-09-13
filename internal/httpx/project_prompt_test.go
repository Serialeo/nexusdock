package httpx

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	protocol "github.com/Serialeo/agentdock-protocol"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

func TestProjectPromptHTTPPreviewAndWriteUsePrivateBridgeOperations(t *testing.T) {
	server, handler, projects, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_prompt_http_12345678", "PromptHTTPNode")
	project := createProjectThroughAPI(t, handler, "Prompt HTTP")
	deployment := createDeploymentThroughAPI(t, handler, project.ID, node.ID, t.TempDir())

	socket, closeNode := connectProjectFakeNode(t, server, node)
	defer closeNode()
	apply := readProjectInvoke(t, socket)
	if apply.Operation != protocol.OperationProjectDeploymentApply {
		t.Fatalf("setup operation = %q", apply.Operation)
	}
	var applied protocol.Deployment
	if err := json.Unmarshal(apply.Arguments, &applied); err != nil {
		t.Fatal(err)
	}
	writeProjectResult(t, socket, apply.RequestID, map[string]any{"deployment": applied})
	deployment = waitForDeploymentState(t, projects, project.ID, deployment.ID, func(value projectstore.Deployment) bool {
		return value.ApplyStatus == string(protocol.DeploymentApplyApplied) && value.AppliedRevision == value.DesiredRevision
	})

	previewDone := make(chan *responseSnapshot, 1)
	go func() {
		recorder := projectAPIRequest(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/deployments/"+deployment.ID+"/prompt?cwd_rel=backend", "")
		previewDone <- snapshotResponse(recorder)
	}()
	load := readProjectInvoke(t, socket)
	if load.Operation != protocol.OperationProjectPromptLoad || load.ExecutionContext != nil {
		t.Fatalf("Prompt preview invoke = %#v", load)
	}
	var loadRequest protocol.ProjectPromptLoadRequest
	if err := json.Unmarshal(load.Arguments, &loadRequest); err != nil {
		t.Fatal(err)
	}
	if loadRequest.DeploymentID != deployment.ID || loadRequest.DeploymentRevision != deployment.AppliedRevision || loadRequest.CWDRel != "backend" {
		t.Fatalf("Prompt preview request = %#v", loadRequest)
	}
	prompt := protocol.ProjectPrompt{
		PromptRevision: "sha256:prompt-v1", Complete: true, Bytes: 6,
		Sources: []protocol.PromptSource{{Path: "backend/AGENTS.md", Scope: "backend", SHA256: "sha256:source-v1", Bytes: 6, Content: "rules\n"}},
	}
	writeProjectResult(t, socket, load.RequestID, protocol.ProjectPromptLoadResult{DeploymentID: deployment.ID, CWDRel: "backend", Prompt: prompt})
	preview := <-previewDone
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body, `"prompt_revision":"sha256:prompt-v1"`) || !strings.Contains(preview.Body, `"working_folder"`) {
		t.Fatalf("Prompt preview response status=%d body=%s", preview.Code, preview.Body)
	}

	writeBody, _ := json.Marshal(map[string]any{
		"scope": "backend", "content": "updated\n", "expected_sha256": "sha256:source-v1", "create": false,
	})
	writeDone := make(chan *responseSnapshot, 1)
	go func() {
		recorder := projectAPIRequest(t, handler, http.MethodPut, "/v1/projects/"+project.ID+"/deployments/"+deployment.ID+"/prompt", string(writeBody))
		writeDone <- snapshotResponse(recorder)
	}()
	writeInvoke := readProjectInvoke(t, socket)
	if writeInvoke.Operation != protocol.OperationProjectPromptWrite || writeInvoke.ExecutionContext != nil {
		t.Fatalf("Prompt write invoke = %#v", writeInvoke)
	}
	var writeRequest protocol.ProjectPromptWriteRequest
	if err := json.Unmarshal(writeInvoke.Arguments, &writeRequest); err != nil {
		t.Fatal(err)
	}
	if writeRequest.DeploymentID != deployment.ID || writeRequest.DeploymentRevision != deployment.AppliedRevision || writeRequest.Scope != "backend" || writeRequest.ExpectedSHA256 != "sha256:source-v1" || writeRequest.Create || writeRequest.Content != "updated\n" {
		t.Fatalf("Prompt write request = %#v", writeRequest)
	}
	updatedPrompt := protocol.ProjectPrompt{
		PromptRevision: "sha256:prompt-v2", Complete: true, Bytes: 8,
		Sources: []protocol.PromptSource{{Path: "backend/AGENTS.md", Scope: "backend", SHA256: "sha256:source-v2", Bytes: 8, Content: "updated\n"}},
	}
	writeProjectResult(t, socket, writeInvoke.RequestID, protocol.ProjectPromptWriteResult{
		DeploymentID: deployment.ID, Scope: "backend", Source: updatedPrompt.Sources[0], Prompt: updatedPrompt, Created: false,
	})
	written := <-writeDone
	if written.Code != http.StatusOK || !strings.Contains(written.Body, `"sha256:source-v2"`) || !strings.Contains(written.Body, `"created":false`) {
		t.Fatalf("Prompt write response status=%d body=%s", written.Code, written.Body)
	}
}

func TestProjectPromptHTTPRejectsNotAppliedAndUnknownWriteFieldsBeforeBridge(t *testing.T) {
	_, handler, _, nodes, _ := newProjectsHTTPTestServer(t)
	node := pairProjectHTTPTestNode(t, nodes, "device_project_prompt_pending_12345678", "PromptPendingNode")
	project := createProjectThroughAPI(t, handler, "Prompt Pending")
	deployment := createDeploymentThroughAPI(t, handler, project.ID, node.ID, t.TempDir())
	response := projectAPIRequest(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/deployments/"+deployment.ID+"/prompt?cwd_rel=.", "")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), protocol.ErrorDeploymentNotReady) {
		t.Fatalf("pending Prompt preview status=%d body=%s", response.Code, response.Body.String())
	}

	bad := projectAPIRequest(t, handler, http.MethodPut, "/v1/projects/"+project.ID+"/deployments/"+deployment.ID+"/prompt", `{"scope":".","content":"rules","create":true,"node_id":"override"}`)
	// Deployment readiness is intentionally checked before accepting a write body, so
	// the pending Deployment remains fail-closed even when the body is malformed.
	if bad.Code != http.StatusConflict || !strings.Contains(bad.Body.String(), protocol.ErrorDeploymentNotReady) {
		t.Fatalf("pending malformed Prompt write status=%d body=%s", bad.Code, bad.Body.String())
	}
}

type responseSnapshot struct {
	Code int
	Body string
}

func snapshotResponse(recorder interface{ Result() *http.Response }) *responseSnapshot {
	response := recorder.Result()
	defer response.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(response.Body).Decode(&body)
	encoded, _ := json.Marshal(body)
	return &responseSnapshot{Code: response.StatusCode, Body: string(encoded)}
}

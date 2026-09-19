package httpx

import (
	"net/http"
	"strings"
	"testing"

	protocol "github.com/Serialeo/agentdock-protocol"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

func TestProjectSessionHTTPIsProjectScopedAndDoesNotExposeOwnerBinding(t *testing.T) {
	_, handler, projects, _, _ := newProjectsHTTPTestServer(t)
	first := createProjectThroughAPI(t, handler, "Sessions First")
	second := createProjectThroughAPI(t, handler, "Sessions Second")

	session, _, err := projects.BeginWorkSession(t.Context(), "mcp:secret-owner-a", first.ID, "request-first", "sha256:secret-request", first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	target, err := projects.PutWorkTarget(t.Context(), "mcp:secret-owner-a", projectstore.WorkTarget{Target: protocol.WorkTarget{
		WorkSessionID: session.ID, ProjectID: first.ID, DeploymentID: "deployment-a", NodeID: "node-a", CWDRel: "backend",
		DeploymentRevision: "rev-1", ContextRevision: "sha256:ctx-a", Status: protocol.TargetReady,
		Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadOnly, Shell: true},
		Prompt: protocol.ProjectPrompt{PromptRevision: "sha256:prompt-a", Complete: true, Bytes: 5,
			Sources: []protocol.PromptSource{{Path: "AGENTS.md", Scope: ".", SHA256: "sha256:source-a", Bytes: 5, Content: "rules"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.UpdateWorkSessionContext(t.Context(), "mcp:secret-owner-a", session.ID, first.Revision, protocol.WorkSessionReady, "sha256:session-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.RecordContextReturned(t.Context(), "mcp:secret-owner-a", session.ID, "", "sha256:session-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.RecordContextReturned(t.Context(), "mcp:secret-owner-a", session.ID, target.Target.ID, target.Target.ContextRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.AcknowledgeContextConsumed(t.Context(), "mcp:secret-owner-a", protocol.ProjectContextAcknowledgment{WorkSessionID: session.ID, TargetID: target.Target.ID, ContextRevision: target.Target.ContextRevision}); err != nil {
		t.Fatal(err)
	}
	other, _, err := projects.BeginWorkSession(t.Context(), "mcp:secret-owner-b", second.ID, "request-second", "sha256:secret-request-b", second.Revision)
	if err != nil {
		t.Fatal(err)
	}

	listed := projectAPIRequest(t, handler, http.MethodGet, "/v1/projects/"+first.ID+"/sessions?limit=20", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("session list status=%d body=%s", listed.Code, listed.Body.String())
	}
	body := listed.Body.String()
	if !strings.Contains(body, session.ID) || !strings.Contains(body, `"status":"returned"`) || !strings.Contains(body, `"returned_at"`) || strings.Contains(body, other.ID) || strings.Contains(body, "mcp:secret-owner-a") || strings.Contains(body, "request-first") || strings.Contains(body, "sha256:") || strings.Contains(body, `"client_request_id"`) || strings.Contains(body, `"project_revision"`) || strings.Contains(body, `"context_revision"`) {
		t.Fatalf("Project session list leaked, crossed scope, or lost delivery evidence: %s", body)
	}

	detail := projectAPIRequest(t, handler, http.MethodGet, "/v1/projects/"+first.ID+"/sessions/"+session.ID, "")
	if detail.Code != http.StatusOK {
		t.Fatalf("session detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	detailBody := detail.Body.String()
	if !strings.Contains(detailBody, target.Target.ID) || !strings.Contains(detailBody, `"cwd_rel":"backend"`) || !strings.Contains(detailBody, `"path":"AGENTS.md"`) || !strings.Contains(detailBody, `"bytes":5`) || !strings.Contains(detailBody, `"status":"host_consumed"`) || !strings.Contains(detailBody, `"host_consumed_at"`) || strings.Contains(detailBody, "mcp:secret-owner-a") || strings.Contains(detailBody, "request-first") || strings.Contains(detailBody, "sha256:") || strings.Contains(detailBody, `"content":"rules"`) || strings.Contains(detailBody, `"prompt_revision"`) || strings.Contains(detailBody, `"prompt_scopes"`) || strings.Contains(detailBody, `"context_revision"`) || strings.Contains(detailBody, `"deployment_revision"`) || strings.Contains(detailBody, `"source_provenance"`) {
		t.Fatalf("Project session detail = %s", detailBody)
	}

	crossProject := projectAPIRequest(t, handler, http.MethodGet, "/v1/projects/"+first.ID+"/sessions/"+other.ID, "")
	if crossProject.Code != http.StatusNotFound || !strings.Contains(crossProject.Body.String(), "SESSION_NOT_FOUND") {
		t.Fatalf("cross-Project session detail status=%d body=%s", crossProject.Code, crossProject.Body.String())
	}

	badLimit := projectAPIRequest(t, handler, http.MethodGet, "/v1/projects/"+first.ID+"/sessions?limit=9999", "")
	if badLimit.Code != http.StatusBadRequest {
		t.Fatalf("invalid session limit status=%d body=%s", badLimit.Code, badLimit.Body.String())
	}
}

func TestProjectSessionHTTPDoesNotInventDeliveryEvidence(t *testing.T) {
	_, handler, projects, _, _ := newProjectsHTTPTestServer(t)
	project := createProjectThroughAPI(t, handler, "No Delivery Evidence")
	session, _, err := projects.BeginWorkSession(t.Context(), "mcp:no-delivery-owner", project.ID, "request-no-delivery", "sha256:no-delivery-request", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.UpdateWorkSessionContext(t.Context(), "mcp:no-delivery-owner", session.ID, project.Revision, protocol.WorkSessionReady, "sha256:no-delivery-context"); err != nil {
		t.Fatal(err)
	}

	listed := projectAPIRequest(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/sessions", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("session list status=%d body=%s", listed.Code, listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), `"delivery"`) || strings.Contains(listed.Body.String(), `"status":"returned"`) || strings.Contains(listed.Body.String(), `"status":"host_consumed"`) {
		t.Fatalf("session list invented delivery evidence: %s", listed.Body.String())
	}

	detail := projectAPIRequest(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/sessions/"+session.ID, "")
	if detail.Code != http.StatusOK {
		t.Fatalf("session detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	if strings.Contains(detail.Body.String(), `"delivery"`) || strings.Contains(detail.Body.String(), `"status":"returned"`) || strings.Contains(detail.Body.String(), `"status":"host_consumed"`) {
		t.Fatalf("session detail invented delivery evidence: %s", detail.Body.String())
	}
}

package project

import (
	"database/sql"
	"errors"
	"testing"

	protocol "github.com/Serialeo/agentdock-protocol"
)

func TestWorkSessionIdempotencyAndOwnerIsolation(t *testing.T) {
	store, _ := newProjectStoreForTest(t)
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Session Project", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := store.BeginWorkSession(t.Context(), "owner-a", project.ID, "request-1", "sha256:req-a", project.Revision)
	if err != nil || !created {
		t.Fatalf("first BeginWorkSession = %#v created=%v err=%v", first, created, err)
	}
	repeated, created, err := store.BeginWorkSession(t.Context(), "owner-a", project.ID, "request-1", "sha256:req-a", project.Revision)
	if err != nil || created || repeated.ID != first.ID {
		t.Fatalf("idempotent BeginWorkSession = %#v created=%v err=%v", repeated, created, err)
	}
	_, _, err = store.BeginWorkSession(t.Context(), "owner-a", project.ID, "request-1", "sha256:different", project.Revision)
	var conflict *WorkSessionRequestConflictError
	if !errors.As(err, &conflict) || conflict.WorkSessionID != first.ID {
		t.Fatalf("request-id conflict = %#v", err)
	}
	other, created, err := store.BeginWorkSession(t.Context(), "owner-b", project.ID, "request-1", "sha256:req-a", project.Revision)
	if err != nil || !created || other.ID == first.ID {
		t.Fatalf("second owner session = %#v created=%v err=%v", other, created, err)
	}
	if _, err := store.GetWorkSession(t.Context(), "owner-b", first.ID); !errors.Is(err, ErrWorkSessionNotFound) {
		t.Fatalf("cross-owner session read error = %v, want not found", err)
	}
}

func TestBeginWorkSessionRequiresExistingProject(t *testing.T) {
	store, db := newProjectStoreForTest(t)
	_, created, err := store.BeginWorkSession(t.Context(), "owner-a", "project_missing", "request-missing", "sha256:req", "rev-1")
	if !errors.Is(err, ErrWorkSessionNotFound) || created {
		t.Fatalf("BeginWorkSession for missing Project: created=%v err=%v", created, err)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM work_sessions WHERE project_id = ?`, "project_missing").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("missing Project left %d orphan WorkSessions", count)
	}
}

func TestWorkTargetRoundTripAndOwnerIsolation(t *testing.T) {
	store, _ := newProjectStoreForTest(t)
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Target Project", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := store.BeginWorkSession(t.Context(), "owner-a", project.ID, "request-target", "sha256:req", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.PutWorkTarget(t.Context(), "owner-a", WorkTarget{Target: protocol.WorkTarget{
		WorkSessionID: session.ID, ProjectID: project.ID, DeploymentID: "deployment-1", NodeID: "node-1", CWDRel: "backend",
		DeploymentRevision: "rev-3", ContextRevision: "sha256:ctx", Status: protocol.TargetReady,
		Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadWrite, Shell: true},
		Prompt:      protocol.ProjectPrompt{PromptRevision: "sha256:prompt", Complete: true, Bytes: 8, Sources: []protocol.PromptSource{{Path: "AGENTS.md", Scope: ".", SHA256: "sha256:source", Bytes: 8, Content: "rules\n"}}},
	}, PromptScopes: []protocol.PromptScopeRevision{{Scope: "backend", PromptRevision: "sha256:prompt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Target.ID == "" || stored.Target.Prompt.Sources[0].Content != "rules\n" || len(stored.PromptScopes) != 1 {
		t.Fatalf("stored Target = %#v", stored)
	}
	listed, err := store.ListWorkTargets(t.Context(), "owner-a", session.ID)
	if err != nil || len(listed) != 1 || listed[0].Target.ID != stored.Target.ID {
		t.Fatalf("ListWorkTargets = %#v err=%v", listed, err)
	}
	if _, err := store.GetWorkTarget(t.Context(), "owner-b", session.ID, stored.Target.ID); !errors.Is(err, ErrWorkTargetNotFound) {
		t.Fatalf("cross-owner target read error = %v, want not found", err)
	}
	updated, err := store.SetWorkSessionState(t.Context(), "owner-a", session.ID, protocol.WorkSessionReady, "sha256:session")
	if err != nil || updated.Status != protocol.WorkSessionReady || updated.ContextRevision != "sha256:session" {
		t.Fatalf("SetWorkSessionState = %#v err=%v", updated, err)
	}
}

func TestRevokeTargetsForDeploymentPersistsRevocationAndDowngradesSession(t *testing.T) {
	store, _ := newProjectStoreForTest(t)
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Revoke Deployment", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := store.BeginWorkSession(t.Context(), "owner-a", project.ID, "request-revoke-deployment", "sha256:req", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	first := putReadyTargetForSessionTest(t, store, "owner-a", session, project.ID, "deployment-a", "node-a")
	second := putReadyTargetForSessionTest(t, store, "owner-a", session, project.ID, "deployment-b", "node-b")
	if _, err := store.SetWorkSessionState(t.Context(), "owner-a", session.ID, protocol.WorkSessionReady, "sha256:session"); err != nil {
		t.Fatal(err)
	}

	revoked, err := store.RevokeTargetsForDeployment(t.Context(), "deployment-a", "Deployment revision changed")
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 1 || revoked[0].Target.ID != first.Target.ID || revoked[0].Target.Status != protocol.TargetRevoked {
		t.Fatalf("revoked Targets = %#v", revoked)
	}
	storedFirst, err := store.GetWorkTarget(t.Context(), "owner-a", session.ID, first.Target.ID)
	if err != nil || storedFirst.Target.Status != protocol.TargetRevoked || storedFirst.LastError != "Deployment revision changed" {
		t.Fatalf("stored revoked Target = %#v err=%v", storedFirst, err)
	}
	storedSecond, err := store.GetWorkTarget(t.Context(), "owner-a", session.ID, second.Target.ID)
	if err != nil || storedSecond.Target.Status != protocol.TargetReady {
		t.Fatalf("unrelated Target changed = %#v err=%v", storedSecond, err)
	}
	updatedSession, err := store.GetWorkSession(t.Context(), "owner-a", session.ID)
	if err != nil || updatedSession.Status != protocol.WorkSessionPartial {
		t.Fatalf("WorkSession after one Deployment revoke = %#v err=%v", updatedSession, err)
	}
	repeated, err := store.RevokeTargetsForDeployment(t.Context(), "deployment-a", "again")
	if err != nil || len(repeated) != 0 {
		t.Fatalf("idempotent revoke = %#v err=%v", repeated, err)
	}
}

func TestRevokeStaleTargetsForDeploymentPreservesCurrentRevision(t *testing.T) {
	store, db := newProjectStoreForTest(t)
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Stale Target Revoke", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	insertDeploymentRevisionForSessionTest(t, store, db, project.ID, "node-a", "deployment-a", 2)
	oldSession, _, err := store.BeginWorkSession(t.Context(), "owner-a", project.ID, "request-old-target", "sha256:old", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	currentSession, _, err := store.BeginWorkSession(t.Context(), "owner-a", project.ID, "request-current-target", "sha256:current", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	oldTarget := putReadyTargetForSessionTest(t, store, "owner-a", oldSession, project.ID, "deployment-a", "node-a")
	currentTarget := putReadyTargetForSessionTest(t, store, "owner-a", currentSession, project.ID, "deployment-a", "node-a")
	currentTarget.Target.DeploymentRevision = "rev-2"
	if _, err := store.PutWorkTarget(t.Context(), "owner-a", currentTarget); err != nil {
		t.Fatal(err)
	}

	revoked, err := store.RevokeStaleTargetsForDeployment(t.Context(), "deployment-a", "rev-2", "configuration changed")
	if err != nil || len(revoked) != 1 || revoked[0].Target.ID != oldTarget.Target.ID {
		t.Fatalf("stale Targets = %#v err=%v", revoked, err)
	}
	storedCurrent, err := store.GetWorkTarget(t.Context(), "owner-a", currentSession.ID, currentTarget.Target.ID)
	if err != nil || storedCurrent.Target.Status != protocol.TargetReady {
		t.Fatalf("current revision Target changed = %#v err=%v", storedCurrent, err)
	}
}

func TestRevokeStaleTargetsForDeploymentRejectsSupersededSnapshot(t *testing.T) {
	store, db := newProjectStoreForTest(t)
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Superseded Target Revoke", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	insertDeploymentRevisionForSessionTest(t, store, db, project.ID, "node-a", "deployment-a", 3)
	session, _, err := store.BeginWorkSession(t.Context(), "owner-a", project.ID, "request-newer-target", "sha256:newer", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	target := putReadyTargetForSessionTest(t, store, "owner-a", session, project.ID, "deployment-a", "node-a")
	target.Target.DeploymentRevision = "rev-3"
	if _, err := store.PutWorkTarget(t.Context(), "owner-a", target); err != nil {
		t.Fatal(err)
	}
	_, err = store.RevokeStaleTargetsForDeployment(t.Context(), "deployment-a", "rev-2", "stale request")
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) || conflict.Current != "rev-3" {
		t.Fatalf("superseded revoke error = %#v", err)
	}
	stored, err := store.GetWorkTarget(t.Context(), "owner-a", session.ID, target.Target.ID)
	if err != nil || stored.Target.Status != protocol.TargetReady {
		t.Fatalf("superseded request revoked current Target: %#v err=%v", stored, err)
	}
}

func insertDeploymentRevisionForSessionTest(t *testing.T, store *Store, db *sql.DB, projectID, nodeID, deploymentID string, revision int) {
	t.Helper()
	insertNodeForProjectTest(t, db, nodeID, "4")
	now := formatTime(store.now().UTC())
	if _, err := db.ExecContext(t.Context(), `INSERT INTO project_deployments(
		id, project_id, node_id, working_folder, role, purpose, files_permission,
		shell_enabled, browser_enabled, dynamic_mcp_enabled, acp_enabled,
		desired_revision, applied_revision, enabled, apply_status, last_error, created_at, updated_at
	) VALUES(?, ?, ?, '', '', '', 'read_only', 0, 0, 0, 0, ?, ?, 1, 'applied', '', ?, ?)`,
		deploymentID, projectID, nodeID, revision, revision, now, now); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeTargetsForProjectCancelsSessionsWithoutDeletingHistory(t *testing.T) {
	store, _ := newProjectStoreForTest(t)
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Revoke Project", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := store.BeginWorkSession(t.Context(), "owner-a", project.ID, "request-revoke-project", "sha256:req", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	target := putReadyTargetForSessionTest(t, store, "owner-a", session, project.ID, "deployment-a", "node-a")
	if _, err := store.SetWorkSessionState(t.Context(), "owner-a", session.ID, protocol.WorkSessionReady, "sha256:session"); err != nil {
		t.Fatal(err)
	}

	revoked, err := store.RevokeTargetsForProject(t.Context(), project.ID, "Project disabled")
	if err != nil || len(revoked) != 1 || revoked[0].Target.ID != target.Target.ID {
		t.Fatalf("Project revoke = %#v err=%v", revoked, err)
	}
	stored, err := store.GetWorkTarget(t.Context(), "owner-a", session.ID, target.Target.ID)
	if err != nil || stored.Target.Status != protocol.TargetRevoked {
		t.Fatalf("revoked historical Target = %#v err=%v", stored, err)
	}
	updatedSession, err := store.GetWorkSession(t.Context(), "owner-a", session.ID)
	if err != nil || updatedSession.Status != protocol.WorkSessionCancelled {
		t.Fatalf("cancelled WorkSession = %#v err=%v", updatedSession, err)
	}
	if _, err := store.GetWorkSession(t.Context(), "owner-b", session.ID); !errors.Is(err, ErrWorkSessionNotFound) {
		t.Fatalf("revocation weakened owner isolation: %v", err)
	}
}

func putReadyTargetForSessionTest(t *testing.T, store *Store, owner string, session WorkSession, projectID, deploymentID, nodeID string) WorkTarget {
	t.Helper()
	target, err := store.PutWorkTarget(t.Context(), owner, WorkTarget{Target: protocol.WorkTarget{
		WorkSessionID: session.ID, ProjectID: projectID, DeploymentID: deploymentID, NodeID: nodeID, CWDRel: ".",
		DeploymentRevision: "rev-1", ContextRevision: "sha256:ctx-" + deploymentID, Status: protocol.TargetReady,
		Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadOnly},
		Prompt:      protocol.ProjectPrompt{PromptRevision: "sha256:prompt-" + deploymentID, Complete: true, Sources: []protocol.PromptSource{}},
	}, PromptScopes: []protocol.PromptScopeRevision{{Scope: ".", PromptRevision: "sha256:prompt-" + deploymentID}}})
	if err != nil {
		t.Fatal(err)
	}
	return target
}

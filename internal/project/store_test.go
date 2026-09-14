package project

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/core"
)

func TestProjectAndDeploymentCASStateMachine(t *testing.T) {
	store, db := newProjectStoreForTest(t)
	insertNodeForProjectTestOS(t, db, "node-a", "4", "windows")

	created, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Alpha", OrchestrationPolicy: "policy-v1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Revision != "rev-1" {
		t.Fatalf("created project = %#v", created)
	}
	projectID := created.ID
	updated, err := store.UpdateProject(t.Context(), projectID, UpdateProjectInput{ExpectedRevision: "rev-1", Name: "Renamed", OrchestrationPolicy: "policy-v2", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != projectID || updated.Name != "Renamed" || updated.Revision != "rev-2" {
		t.Fatalf("updated project = %#v", updated)
	}
	_, err = store.UpdateProject(t.Context(), projectID, UpdateProjectInput{ExpectedRevision: "rev-1", Name: "stale", Enabled: true})
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) || conflict.Current != "rev-2" {
		t.Fatalf("stale project update error = %#v", err)
	}

	permissions := protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadWrite, Shell: true, Browser: true, DynamicMCP: true, ACP: true}
	deployment, err := store.CreateDeployment(t.Context(), CreateDeploymentInput{
		ProjectID: projectID, NodeID: "node-a", WorkingFolder: `D:\Work\My Repo`, Role: "backend", Purpose: "build", Permissions: permissions, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.ID == "" || deployment.ID == deployment.NodeID || deployment.WorkingFolder != `D:\Work\My Repo` || deployment.DesiredRevision != "rev-1" || deployment.AppliedRevision != "" || deployment.ApplyStatus != "pending" {
		t.Fatalf("created deployment = %#v", deployment)
	}
	if _, err := store.CreateDeployment(t.Context(), CreateDeploymentInput{ProjectID: projectID, NodeID: "node-a", WorkingFolder: `C:\other`, Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityNone}, Enabled: true}); !errors.Is(err, ErrDuplicateNode) {
		t.Fatalf("duplicate node relation error = %v", err)
	}

	deployment, err = store.RecordApplyResult(t.Context(), projectID, deployment.ID, "rev-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.AppliedRevision != "rev-1" || deployment.ApplyStatus != "applied" {
		t.Fatalf("applied deployment = %#v", deployment)
	}
	deployment, err = store.UpdateDeployment(t.Context(), projectID, deployment.ID, UpdateDeploymentInput{
		ExpectedRevision: "rev-1", WorkingFolder: deployment.WorkingFolder, Role: deployment.Role, Purpose: deployment.Purpose, Permissions: permissions, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.DesiredRevision != "rev-2" || deployment.AppliedRevision != "rev-1" || deployment.ApplyStatus != "pending" || deployment.Enabled {
		t.Fatalf("disabled desired state before Node ack = %#v", deployment)
	}
	deployment, err = store.RecordApplyResult(t.Context(), projectID, deployment.ID, "rev-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.AppliedRevision != "rev-2" || deployment.ApplyStatus != "disabled" {
		t.Fatalf("disabled applied state = %#v", deployment)
	}
	_, err = store.UpdateDeployment(t.Context(), projectID, deployment.ID, UpdateDeploymentInput{
		ExpectedRevision: "rev-1", WorkingFolder: deployment.WorkingFolder, Role: deployment.Role, Purpose: deployment.Purpose, Permissions: permissions, Enabled: true,
	})
	conflict = nil
	if !errors.As(err, &conflict) || conflict.Resource != "deployment" || conflict.Current != "rev-2" {
		t.Fatalf("stale deployment update error = %#v", err)
	}
}

func TestDeleteProjectUsesCASAndLeavesCurrentProjectUntouchedOnConflict(t *testing.T) {
	store, _ := newProjectStoreForTest(t)
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Alpha", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err = store.UpdateProject(t.Context(), project.ID, UpdateProjectInput{ExpectedRevision: project.Revision, Name: "Beta", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DeleteProject(t.Context(), project.ID, "rev-1")
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) || conflict.Resource != "project" || conflict.Current != "rev-2" {
		t.Fatalf("stale project delete error = %#v", err)
	}
	current, err := store.GetProject(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Name != "Beta" || current.Revision != "rev-2" {
		t.Fatalf("stale delete mutated Project: %#v", current)
	}
}

func TestLateApplyResultCannotOverwriteNewerDesiredState(t *testing.T) {
	store, db := newProjectStoreForTest(t)
	insertNodeForProjectTest(t, db, "node-a", "4")
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Alpha", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	permissions := protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadOnly}
	deployment, err := store.CreateDeployment(t.Context(), CreateDeploymentInput{ProjectID: project.ID, NodeID: "node-a", WorkingFolder: "/srv/project", Permissions: permissions, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = store.UpdateDeployment(t.Context(), project.ID, deployment.ID, UpdateDeploymentInput{ExpectedRevision: "rev-1", WorkingFolder: "/srv/project-v2", Permissions: permissions, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.DesiredRevision != "rev-2" || deployment.ApplyStatus != "pending" {
		t.Fatalf("new desired state = %#v", deployment)
	}
	late, err := store.RecordApplyResult(t.Context(), project.ID, deployment.ID, "rev-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if late.DesiredRevision != "rev-2" || late.AppliedRevision != "" || late.ApplyStatus != "pending" {
		t.Fatalf("late rev-1 result overwrote rev-2 desired state: %#v", late)
	}
}

func TestDeleteDeploymentStagesRemovalWithoutDeletingWorkingFolder(t *testing.T) {
	store, db := newProjectStoreForTest(t)
	insertNodeForProjectTest(t, db, "node-a", "4")
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Alpha", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(working, 0o700); err != nil {
		t.Fatal(err)
	}
	deployment, err := store.CreateDeployment(t.Context(), CreateDeploymentInput{
		ProjectID: project.ID, NodeID: "node-a", WorkingFolder: working, Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadOnly}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := store.DeleteDeployment(t.Context(), project.ID, deployment.ID, deployment.DesiredRevision)
	if err != nil {
		t.Fatal(err)
	}
	if removed.ID != deployment.ID {
		t.Fatalf("removed deployment = %#v", removed)
	}
	if _, err := os.Stat(working); err != nil {
		t.Fatalf("working folder was deleted: %v", err)
	}
	if _, err := store.GetDeployment(t.Context(), project.ID, deployment.ID); !errors.Is(err, ErrDeploymentNotFound) {
		t.Fatalf("deleted deployment still present: %v", err)
	}
	removals, err := store.ListPendingRemovalsForNode(t.Context(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(removals) != 1 || removals[0].DeploymentID != deployment.ID {
		t.Fatalf("removals = %#v", removals)
	}
	if err := store.RecordRemovalResult(t.Context(), deployment.ID, errors.New("offline")); err != nil {
		t.Fatal(err)
	}
	removals, err = store.ListPendingRemovalsForNode(t.Context(), "node-a")
	if err != nil || len(removals) != 1 || removals[0].LastError != "offline" {
		t.Fatalf("failed removal state = %#v err=%v", removals, err)
	}
	if err := store.RecordRemovalResult(t.Context(), deployment.ID, nil); err != nil {
		t.Fatal(err)
	}
	removals, err = store.ListPendingRemovalsForNode(t.Context(), "node-a")
	if err != nil || len(removals) != 0 {
		t.Fatalf("cleared removals = %#v err=%v", removals, err)
	}
}

func TestDeleteProjectStagesAllDeploymentRemovalsAndPreservesDirectories(t *testing.T) {
	store, db := newProjectStoreForTest(t)
	insertNodeForProjectTest(t, db, "node-a", "4")
	insertNodeForProjectTest(t, db, "node-b", "4")
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Alpha", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")}
	for i, node := range []string{"node-a", "node-b"} {
		if err := os.MkdirAll(paths[i], 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateDeployment(t.Context(), CreateDeploymentInput{ProjectID: project.ID, NodeID: node, WorkingFolder: paths[i], Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityNone}, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	deployments, err := store.DeleteProject(t.Context(), project.ID, project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 2 {
		t.Fatalf("deleted deployments = %#v", deployments)
	}
	if _, err := store.GetProject(t.Context(), project.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("project still exists: %v", err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("working folder deleted: %s: %v", path, err)
		}
	}
	for _, node := range []string{"node-a", "node-b"} {
		removals, err := store.ListPendingRemovalsForNode(t.Context(), node)
		if err != nil || len(removals) != 1 {
			t.Fatalf("%s removals = %#v err=%v", node, removals, err)
		}
	}
}

func TestDeploymentWorkingFolderUsesTargetOSNativeAbsolutePath(t *testing.T) {
	store, db := newProjectStoreForTest(t)
	insertNodeForProjectTestOS(t, db, "linux-node", "4", "linux")
	insertNodeForProjectTestOS(t, db, "darwin-node", "4", "darwin")
	insertNodeForProjectTestOS(t, db, "windows-node", "4", "windows")
	insertNodeForProjectTestOS(t, db, "unknown-node", "4", "")
	permissions := protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadOnly}

	for _, test := range []struct {
		name   string
		nodeID string
		path   string
		ok     bool
	}{
		{name: "linux folderless", nodeID: "linux-node", path: "", ok: true},
		{name: "linux absolute", nodeID: "linux-node", path: "/srv/Project A", ok: true},
		{name: "linux relative", nodeID: "linux-node", path: "srv/project"},
		{name: "linux windows drive", nodeID: "linux-node", path: `D:\Work\Repo`},
		{name: "darwin absolute", nodeID: "darwin-node", path: "/Users/alice/Repo", ok: true},
		{name: "windows drive backslash", nodeID: "windows-node", path: `D:\Work\My Repo`, ok: true},
		{name: "windows folderless", nodeID: "windows-node", path: "", ok: true},
		{name: "windows drive slash", nodeID: "windows-node", path: `C:/Dev/Repo`, ok: true},
		{name: "windows unc", nodeID: "windows-node", path: `\\server\share\Repo`, ok: true},
		{name: "windows root relative", nodeID: "windows-node", path: `\Repo`},
		{name: "windows posix", nodeID: "windows-node", path: "/srv/repo"},
		{name: "unknown posix", nodeID: "unknown-node", path: "/opt/repo", ok: true},
		{name: "unknown windows", nodeID: "unknown-node", path: `E:\Repo`, ok: true},
		{name: "unknown relative", nodeID: "unknown-node", path: "repo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Paths " + test.name, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.CreateDeployment(t.Context(), CreateDeploymentInput{
				ProjectID: project.ID, NodeID: test.nodeID, WorkingFolder: test.path, Permissions: permissions, Enabled: true,
			})
			if test.ok {
				if err != nil {
					t.Fatalf("CreateDeployment(%q): %v", test.path, err)
				}
				return
			}
			var validation ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("CreateDeployment(%q) error = %T %v, want ValidationError", test.path, err, err)
			}
		})
	}
}

func TestDeploymentUpdateRevalidatesWorkingFolderAgainstNodeOS(t *testing.T) {
	store, db := newProjectStoreForTest(t)
	insertNodeForProjectTestOS(t, db, "windows-node", "4", "windows")
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Windows", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	permissions := protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadOnly}
	deployment, err := store.CreateDeployment(t.Context(), CreateDeploymentInput{
		ProjectID: project.ID, NodeID: "windows-node", WorkingFolder: `D:\Repo`, Permissions: permissions, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateDeployment(t.Context(), project.ID, deployment.ID, UpdateDeploymentInput{
		ExpectedRevision: deployment.DesiredRevision, WorkingFolder: "/srv/not-windows", Permissions: permissions, Enabled: true,
	})
	var validation ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("invalid update error = %T %v, want ValidationError", err, err)
	}
	current, err := store.GetDeployment(t.Context(), project.ID, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.WorkingFolder != `D:\Repo` || current.DesiredRevision != "rev-1" {
		t.Fatalf("invalid update mutated desired state: %#v", current)
	}
}

func newProjectStoreForTest(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := core.OpenSQLite(t.Context(), ":memory:", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := core.EnsureSchema(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

func insertNodeForProjectTest(t *testing.T, db *sql.DB, id, protocolVersion string) {
	t.Helper()
	insertNodeForProjectTestOS(t, db, id, protocolVersion, "linux")
}

func insertNodeForProjectTestOS(t *testing.T, db *sql.DB, id, protocolVersion, nodeOS string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `INSERT INTO agentdock_devices(
		id, device_id, name, enabled, version, protocol_version, os, arch, capabilities_json, tool_contract_hash, created_at, updated_at
	) VALUES(?, ?, ?, 1, 'test', ?, ?, 'amd64', '[]', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, "device_"+id+"_12345678", id, protocolVersion, nodeOS)
	if err != nil {
		t.Fatal(err)
	}
}

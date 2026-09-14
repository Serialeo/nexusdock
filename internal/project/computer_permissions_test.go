package project

import (
	protocol "github.com/Serialeo/agentdock-protocol"
	"testing"
)

func TestComputerDeploymentPermissionPersistence(t *testing.T) {
	store, db := newProjectStoreForTest(t)
	insertNodeForProjectTestOS(t, db, "computer-node", "4", "windows")
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Computer", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := store.CreateDeployment(t.Context(), CreateDeploymentInput{ProjectID: project.ID, NodeID: "computer-node", Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityNone, Computer: protocol.ComputerPermissionNone}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Permissions.Computer != protocol.ComputerPermissionNone {
		t.Fatal("explicit none permission was not preserved")
	}
	for _, level := range []protocol.ComputerPermission{protocol.ComputerPermissionObserve, protocol.ComputerPermissionControl, protocol.ComputerPermissionNone} {
		deployment, err = store.UpdateDeployment(t.Context(), project.ID, deployment.ID, UpdateDeploymentInput{ExpectedRevision: deployment.DesiredRevision, Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityNone, Computer: level}, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		reloaded, err := store.GetDeployment(t.Context(), project.ID, deployment.ID)
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.Permissions.Computer != level {
			t.Fatalf("lost permission %s: %v", level, reloaded)
		}
	}
	if _, err := store.UpdateDeployment(t.Context(), project.ID, deployment.ID, UpdateDeploymentInput{ExpectedRevision: deployment.DesiredRevision, Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityNone, Computer: "invalid"}, Enabled: true}); err == nil {
		t.Fatal("invalid computer permission persisted")
	}
}

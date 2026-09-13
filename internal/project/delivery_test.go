package project

import (
	"errors"
	"testing"

	protocol "github.com/Serialeo/agentdock-protocol"
)

func TestContextDeliveryReturnedHostConsumedAndNewRevision(t *testing.T) {
	store, _ := newProjectStoreForTest(t)
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Delivery", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := store.BeginWorkSession(t.Context(), "owner-a", project.ID, "request-delivery", "sha256:req", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.PutWorkTarget(t.Context(), "owner-a", WorkTarget{Target: protocol.WorkTarget{
		WorkSessionID: session.ID, ProjectID: project.ID, DeploymentID: "deployment-a", NodeID: "node-a", CWDRel: ".",
		DeploymentRevision: "rev-1", ContextRevision: "sha256:target-v1", Status: protocol.TargetReady,
		Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadOnly},
		Prompt:      protocol.ProjectPrompt{PromptRevision: "sha256:prompt", Complete: true, Sources: []protocol.PromptSource{}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	returned, err := store.RecordContextReturned(t.Context(), "owner-a", session.ID, "", "sha256:session-v1")
	if err != nil || returned.Status != protocol.ProjectContextReturned || returned.ContextRevision != "sha256:session-v1" || returned.HostConsumedAt != nil {
		t.Fatalf("session returned delivery = %#v err=%v", returned, err)
	}
	consumed, err := store.AcknowledgeContextConsumed(t.Context(), "owner-a", protocol.ProjectContextAcknowledgment{WorkSessionID: session.ID, ContextRevision: "sha256:session-v1"})
	if err != nil || consumed.Status != protocol.ProjectContextHostConsumed || consumed.HostConsumedAt == nil {
		t.Fatalf("session consumed delivery = %#v err=%v", consumed, err)
	}
	originalConsumedAt := consumed.HostConsumedAt
	repeated, err := store.RecordContextReturned(t.Context(), "owner-a", session.ID, "", "sha256:session-v1")
	if err != nil || repeated.Status != protocol.ProjectContextHostConsumed || repeated.HostConsumedAt == nil || !repeated.HostConsumedAt.Equal(*originalConsumedAt) {
		t.Fatalf("same revision returned downgraded consumed delivery = %#v err=%v", repeated, err)
	}
	newRevision, err := store.RecordContextReturned(t.Context(), "owner-a", session.ID, "", "sha256:session-v2")
	if err != nil || newRevision.Status != protocol.ProjectContextReturned || newRevision.HostConsumedAt != nil || newRevision.ContextRevision != "sha256:session-v2" {
		t.Fatalf("new revision delivery = %#v err=%v", newRevision, err)
	}

	targetDelivery, err := store.RecordContextReturned(t.Context(), "owner-a", session.ID, target.Target.ID, target.Target.ContextRevision)
	if err != nil || targetDelivery.TargetID != target.Target.ID {
		t.Fatalf("target returned delivery = %#v err=%v", targetDelivery, err)
	}
	if _, err := store.GetContextDelivery(t.Context(), "owner-b", session.ID, target.Target.ID); !errors.Is(err, ErrWorkSessionNotFound) {
		t.Fatalf("cross-owner delivery read = %v", err)
	}
}

func TestContextDeliveryAckRequiresPreviouslyReturnedExactRevision(t *testing.T) {
	store, _ := newProjectStoreForTest(t)
	project, err := store.CreateProject(t.Context(), CreateProjectInput{Name: "Delivery Ack", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := store.BeginWorkSession(t.Context(), "owner-a", project.ID, "request-ack", "sha256:req", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AcknowledgeContextConsumed(t.Context(), "owner-a", protocol.ProjectContextAcknowledgment{WorkSessionID: session.ID, ContextRevision: "sha256:not-returned"})
	if !errors.Is(err, ErrContextDeliveryNotFound) {
		t.Fatalf("ack before returned = %v", err)
	}
	if _, err := store.RecordContextReturned(t.Context(), "owner-a", session.ID, "", "sha256:returned"); err != nil {
		t.Fatal(err)
	}
	_, err = store.AcknowledgeContextConsumed(t.Context(), "owner-a", protocol.ProjectContextAcknowledgment{WorkSessionID: session.ID, ContextRevision: "sha256:stale"})
	var conflict *ContextDeliveryRevisionConflictError
	if !errors.As(err, &conflict) || conflict.ContextRevision != "sha256:returned" {
		t.Fatalf("stale ack = %#v", err)
	}
}

package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/core"
)

type continuationFixture struct {
	s               *Store
	db              *sql.DB
	owner, ws, node string
	target          protocol.WorkTarget
	now             time.Time
}

func newContinuationFixture(t *testing.T) *continuationFixture {
	t.Helper()
	db, err := core.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "continuation.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = core.EnsureSchema(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	f := &continuationFixture{s: s, db: db, owner: "owner-a", node: "node-continuation", now: now}
	s.now = func() time.Time { return f.now }
	insertNodeForProjectTest(t, db, f.node, "4")
	p, err := s.CreateProject(t.Context(), CreateProjectInput{Name: "Continuation", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateDeployment(t.Context(), CreateDeploymentInput{ProjectID: p.ID, NodeID: f.node, Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityReadWrite, Shell: true}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.RecordApplyResult(t.Context(), p.ID, d.ID, d.DesiredRevision, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws, _, err := s.BeginWorkSession(t.Context(), f.owner, p.ID, "continuation-request", "sha256:request", p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	f.ws = ws.ID
	_, err = s.SetWorkSessionState(t.Context(), f.owner, f.ws, protocol.WorkSessionReady, "context-session")
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.PutWorkTarget(t.Context(), f.owner, WorkTarget{Target: protocol.WorkTarget{WorkSessionID: f.ws, ProjectID: p.ID, DeploymentID: d.ID, NodeID: f.node, DeploymentRevision: d.AppliedRevision, ContextRevision: "context-1", CWDRel: ".", Status: protocol.TargetReady, Permissions: d.Permissions, Prompt: protocol.ProjectPrompt{Complete: true, Sources: []protocol.PromptSource{}}}})
	if err != nil {
		t.Fatal(err)
	}
	f.target = target.Target
	return f
}
func (f *continuationFixture) control(t *testing.T, in protocol.WorkContinuationInput) protocol.WorkContinuationResult {
	t.Helper()
	in.WorkSessionID = f.ws
	r, err := f.s.ControlContinuation(t.Context(), f.owner, in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *continuationFixture) outcome(id string) protocol.CommandOutcome {
	return protocol.CommandOutcome{EventID: "event-" + id, CommandSessionID: id, ExecutionContext: protocol.ExecutionContext{WorkSessionID: f.ws, TargetID: f.target.ID, ProjectID: f.target.ProjectID, DeploymentID: f.target.DeploymentID, DeploymentRevision: f.target.DeploymentRevision, ContextRevision: f.target.ContextRevision}, State: protocol.CommandOutcomeCompleted, Output: "result " + id, StartedAt: formatTime(f.now), FinishedAt: formatTime(f.now), UpdatedAt: formatTime(f.now), PendingReport: true}
}
func (f *continuationFixture) record(t *testing.T, ids ...string) {
	t.Helper()
	items := make([]protocol.CommandOutcome, 0, len(ids))
	for _, id := range ids {
		items = append(items, f.outcome(id))
	}
	acks, err := f.s.RecordCommandOutcomes(t.Context(), f.node, items)
	if err != nil || len(acks) != len(ids) {
		t.Fatalf("record: %v %v", acks, err)
	}
}
func (f *continuationFixture) await(t *testing.T, ids ...string) protocol.WorkContinuationResult {
	t.Helper()
	sources := []protocol.ContinuationSource{}
	for _, id := range ids {
		sources = append(sources, protocol.ContinuationSource{TargetID: f.target.ID, CommandSessionID: id})
	}
	return f.control(t, protocol.WorkContinuationInput{Action: "await", Sources: sources})
}
func (f *continuationFixture) bind(t *testing.T) protocol.ContinuationControllerInput {
	t.Helper()
	r, err := f.s.PresentContinuation(t.Context(), f.owner, f.ws, false)
	if err != nil {
		t.Fatal(err)
	}
	in := protocol.ContinuationControllerInput{WorkSessionID: f.ws, EndpointID: r.EndpointID, ControllerGeneration: r.ControllerGeneration, BindingSecret: r.BindingSecret, BindingID: "view-a", UserEnabled: true}
	f.app(t, "bind", in)
	return in
}
func (f *continuationFixture) app(t *testing.T, action string, in protocol.ContinuationControllerInput) protocol.WorkContinuationResult {
	t.Helper()
	r, err := f.s.ControllerContinuation(t.Context(), f.owner, action, in)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	if r.WorkSessionID != in.WorkSessionID || r.EndpointID != in.EndpointID || r.ControllerGeneration != in.ControllerGeneration {
		t.Fatalf("incomplete controller identity: %#v", r)
	}
	return r
}
func (f *continuationFixture) prepare(t *testing.T, in protocol.ContinuationControllerInput) (protocol.ContinuationControllerInput, protocol.ResumeEnvelope) {
	t.Helper()
	r := f.app(t, "acquire", in)
	in.WakeID = r.WakeID
	in.AttemptID = r.AttemptID
	r = f.app(t, "prepare", in)
	var envelope protocol.ResumeEnvelope
	start := strings.Index(r.AutomaticMessage, "{")
	if start < 0 {
		t.Fatal("missing automatic message")
	}
	if err := json.Unmarshal([]byte(r.AutomaticMessage[start:]), &envelope); err != nil {
		t.Fatal(err)
	}
	return in, envelope
}
func continuationCode(t *testing.T, err error, code string) {
	t.Helper()
	var coded *ContinuationError
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error %v, want %s", err, code)
	}
}

func TestContinuationFinishedBeforeAwaitAndReceiptCommit(t *testing.T) {
	f := newContinuationFixture(t)
	f.record(t, "one")
	f.record(t, "one")
	var count int
	if err := f.db.QueryRow("SELECT count(*) FROM command_source_receipts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipt dedupe count=%d err=%v", count, err)
	}
	r := f.control(t, protocol.WorkContinuationInput{Action: "enable", Confirmed: true})
	if r.State.UpdatedAt == "" {
		t.Fatal("missing state timestamp")
	}
	r = f.await(t, "one")
	if r.Wake == nil || len(r.Wake.Sources) != 1 || r.Wake.State != protocol.WorkWakePending || r.Wake.EndpointID != "" {
		t.Fatalf("finished before await: %#v", r)
	}
	in := f.bind(t)
	r = f.app(t, "state", in)
	if r.Wake.EndpointID != in.EndpointID || r.BindingSecret != "" || r.AutomaticMessage != "" {
		t.Fatalf("presentation rebinding/leak: %#v", r)
	}
}
func TestContinuationDispatchFenceConsumeBeforeFinishAndSettlement(t *testing.T) {
	f := newContinuationFixture(t)
	f.control(t, protocol.WorkContinuationInput{Action: "enable", Confirmed: true})
	in := f.bind(t)
	f.await(t, "one", "two", "three")
	f.record(t, "one")
	claimed := f.app(t, "acquire", in)
	f.record(t, "two")
	in.WakeID = claimed.WakeID
	in.AttemptID = claimed.AttemptID
	prepared := f.app(t, "prepare", in)
	var envelope protocol.ResumeEnvelope
	if err := json.Unmarshal([]byte(prepared.AutomaticMessage[strings.Index(prepared.AutomaticMessage, "{"):]), &envelope); err != nil {
		t.Fatal(err)
	}
	f.record(t, "three")
	if len(prepared.Wake.Sources) != 2 {
		t.Fatal("claim did not merge results")
	}
	retry, err := f.s.ControllerContinuation(t.Context(), f.owner, "prepare", in)
	continuationCode(t, err, "CONTINUATION_DISPATCH_FENCED")
	if retry.AutomaticMessage != "" {
		t.Fatal("prepare replay returned message")
	}
	var raw string
	if err = f.db.QueryRow("SELECT document_json FROM work_continuations WHERE work_session_id=?", f.ws).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, envelope.ConsumeToken) || strings.Contains(raw, in.BindingSecret) {
		t.Fatal("plaintext credentials persisted")
	}
	consumed, err := f.s.ConsumeWorkWake(t.Context(), f.owner, envelope)
	if err != nil || len(consumed.Outcomes) != 2 {
		t.Fatalf("consume: %#v %v", consumed, err)
	}
	_, err = f.s.ConsumeWorkWake(t.Context(), f.owner, envelope)
	continuationCode(t, err, "CONTINUATION_CONSUME_DENIED")
	in.DeliveryStatus = "dispatch_accepted"
	late := f.app(t, "finish", in)
	if late.Wake.State != protocol.WorkWakeConsumed {
		t.Fatalf("late finish regressed %s", late.Wake.State)
	}
	f.control(t, protocol.WorkContinuationInput{Action: "settle", WakeID: in.WakeID, Checkpoint: "first results processed"})
	next := f.app(t, "acquire", protocol.ContinuationControllerInput{WorkSessionID: in.WorkSessionID, EndpointID: in.EndpointID, ControllerGeneration: in.ControllerGeneration, BindingID: in.BindingID, BindingSecret: in.BindingSecret})
	if next.Wake == nil || len(next.Wake.Sources) != 1 || next.Wake.Sources[0].CommandSessionID != "three" {
		t.Fatalf("prepared source freeze: %#v", next)
	}
	f.record(t, "one")
	if err = f.db.QueryRow("SELECT outcome_json FROM command_source_receipts WHERE event_id='event-one'").Scan(&raw); err != nil || raw != "{}" {
		t.Fatalf("settled receipt should be compact tombstone: %q %v", raw, err)
	}
}
func TestContinuationSingleViewAndPrepareRaceAcrossStores(t *testing.T) {
	f := newContinuationFixture(t)
	f.control(t, protocol.WorkContinuationInput{Action: "enable", Confirmed: true})
	in := f.bind(t)
	f.await(t, "one")
	f.record(t, "one")
	other := in
	other.BindingID = "view-b"
	_, err := f.s.ControllerContinuation(t.Context(), f.owner, "bind", other)
	continuationCode(t, err, "CONTINUATION_VIEW_BUSY")
	claimed := f.app(t, "acquire", in)
	in.WakeID = claimed.WakeID
	in.AttemptID = claimed.AttemptID
	second, err := NewStore(f.db)
	if err != nil {
		t.Fatal(err)
	}
	second.now = f.s.now
	var wg sync.WaitGroup
	responses := make(chan protocol.WorkContinuationResult, 2)
	errs := make(chan error, 2)
	for _, s := range []*Store{f.s, second} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			r, e := s.ControllerContinuation(context.Background(), f.owner, "prepare", in)
			responses <- r
			errs <- e
		}(s)
	}
	wg.Wait()
	close(responses)
	close(errs)
	messages, success := 0, 0
	for r := range responses {
		if r.AutomaticMessage != "" {
			messages++
		}
	}
	for err := range errs {
		if err == nil {
			success++
		} else {
			continuationCode(t, err, "CONTINUATION_DISPATCH_FENCED")
		}
	}
	if messages != 1 || success != 1 {
		t.Fatalf("fence race messages=%d success=%d", messages, success)
	}
}
func TestContinuationTimeoutSingleUseAndManualRecovery(t *testing.T) {
	f := newContinuationFixture(t)
	f.control(t, protocol.WorkContinuationInput{Action: "enable", Confirmed: true, MaxFailures: 1})
	in := f.bind(t)
	f.await(t, "one")
	f.record(t, "one")
	in, envelope := f.prepare(t, in)
	f.now = f.now.Add(46 * time.Second)
	r := f.app(t, "state", in)
	if r.Wake.State != protocol.WorkWakeDeliveryUnknown || r.State.Enabled {
		t.Fatalf("timeout/circuit: %#v", r)
	}
	_, err := f.s.ConsumeWorkWake(t.Context(), f.owner, envelope)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(6 * time.Minute)
	r = f.control(t, protocol.WorkContinuationInput{Action: "status"})
	if r.Wake.State != protocol.WorkWakeNeedsAttention {
		t.Fatalf("unsettled work not retained: %#v", r)
	}
	f.control(t, protocol.WorkContinuationInput{Action: "settle", WakeID: in.WakeID, Checkpoint: "processed durable output"})
	f.control(t, protocol.WorkContinuationInput{Action: "recover", Confirmed: true})
	newPresentation, err := f.s.PresentContinuation(t.Context(), f.owner, f.ws, true)
	if err != nil || newPresentation.ControllerGeneration != in.ControllerGeneration+1 {
		t.Fatalf("recover presentation: %#v %v", newPresentation, err)
	}
	_, err = f.s.ControllerContinuation(t.Context(), f.owner, "bind", in)
	continuationCode(t, err, "CONTINUATION_BINDING_DENIED")
}
func TestContinuationRejectedDeliveryRequiresExplicitResolution(t *testing.T) {
	f := newContinuationFixture(t)
	f.control(t, protocol.WorkContinuationInput{Action: "enable", Confirmed: true, MaxRounds: 1})
	in := f.bind(t)
	f.await(t, "one")
	f.record(t, "one")
	in, envelope := f.prepare(t, in)
	in.DeliveryStatus = "delivery_rejected"
	r := f.app(t, "finish", in)
	if r.Wake.State != protocol.WorkWakeDeliveryRejected {
		t.Fatal("rejection conflated with unknown")
	}
	_, err := f.s.ConsumeWorkWake(t.Context(), f.owner, envelope)
	continuationCode(t, err, "CONTINUATION_CONSUME_DENIED")
	_, err = f.s.ControlContinuation(t.Context(), f.owner, protocol.WorkContinuationInput{WorkSessionID: f.ws, Action: "recover", Confirmed: true})
	continuationCode(t, err, "CONTINUATION_NEEDS_ATTENTION")
	r = f.control(t, protocol.WorkContinuationInput{Action: "recover", Confirmed: true, WakeID: in.WakeID, Checkpoint: "verified result manually; no retry"})
	if !r.State.Enabled || r.State.RoundsUsed != 0 || r.Wake != nil {
		t.Fatalf("explicit manual recovery: %#v", r)
	}
	_, err = f.s.ControllerContinuation(t.Context(), f.owner, "acquire", in)
	continuationCode(t, err, "CONTINUATION_DISPATCH_FENCED")
	in.WakeID = ""
	in.AttemptID = ""
	r = f.app(t, "acquire", in)
	if r.AutomaticMessage != "" || r.Wake != nil {
		t.Fatal("manual recovery requeued prepared work")
	}
}
func TestContinuationOwnerTargetAndGenerationBoundaries(t *testing.T) {
	f := newContinuationFixture(t)
	_, err := f.s.ControlContinuation(t.Context(), f.owner, protocol.WorkContinuationInput{WorkSessionID: f.ws, Action: "enable"})
	continuationCode(t, err, "CONTINUATION_CONFIRMATION_REQUIRED")
	f.control(t, protocol.WorkContinuationInput{Action: "enable", Confirmed: true})
	in := f.bind(t)
	_, err = f.s.ControllerContinuation(t.Context(), "owner-b", "state", in)
	continuationCode(t, err, "CONTINUATION_DENIED")
	wrong := in
	wrong.ControllerGeneration++
	_, err = f.s.ControllerContinuation(t.Context(), f.owner, "state", wrong)
	continuationCode(t, err, "CONTINUATION_BINDING_DENIED")
	f.await(t, "one")
	f.record(t, "one")
	claimed := f.app(t, "acquire", in)
	in.WakeID = claimed.WakeID
	in.AttemptID = claimed.AttemptID
	if _, err = f.db.Exec("UPDATE agentdock_devices SET full_access=1 WHERE id=?", f.node); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ControllerContinuation(t.Context(), f.owner, "prepare", in)
	continuationCode(t, err, "CONTINUATION_TARGET_DENIED")
	if _, err = f.db.Exec("UPDATE agentdock_devices SET full_access=0 WHERE id=?", f.node); err != nil {
		t.Fatal(err)
	}
	in, envelope := f.prepare(t, in)
	if _, err = f.db.Exec("UPDATE work_targets SET status='revoked' WHERE id=?", f.target.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ConsumeWorkWake(t.Context(), f.owner, envelope)
	continuationCode(t, err, "CONTINUATION_TARGET_DENIED")
}
func TestContinuationReceiptOrphanDoesNotPoisonBatchAndForgeryRollsBack(t *testing.T) {
	f := newContinuationFixture(t)
	orphan := f.outcome("deleted")
	orphan.ExecutionContext.WorkSessionID = "deleted-ws"
	orphan.ExecutionContext.TargetID = "deleted-target"
	acks, err := f.s.RecordCommandOutcomes(t.Context(), f.node, []protocol.CommandOutcome{orphan, f.outcome("good")})
	if err != nil || len(acks) != 2 {
		t.Fatalf("orphan poisoned batch: %v %v", acks, err)
	}
	var eligible int
	if err = f.db.QueryRow("SELECT eligible FROM command_source_receipts WHERE event_id=?", orphan.EventID).Scan(&eligible); err != nil || eligible != 0 {
		t.Fatalf("orphan not quarantined: %d %v", eligible, err)
	}
	forged := f.outcome("forged")
	forged.ExecutionContext.ProjectID = "other-project"
	acks, err = f.s.RecordCommandOutcomes(t.Context(), f.node, []protocol.CommandOutcome{f.outcome("rollback"), forged})
	continuationCode(t, err, "CONTINUATION_SOURCE_DENIED")
	if len(acks) != 0 {
		t.Fatal("uncommitted receipts were ACKed")
	}
	var count int
	if err = f.db.QueryRow("SELECT count(*) FROM command_source_receipts WHERE event_id='event-rollback'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed batch partial commit %d %v", count, err)
	}
}
func TestContinuationClaimExpiryAndExactWakeAcquire(t *testing.T) {
	f := newContinuationFixture(t)
	f.control(t, protocol.WorkContinuationInput{Action: "enable", Confirmed: true})
	in := f.bind(t)
	f.await(t, "one")
	f.record(t, "one")
	wrong := in
	wrong.WakeID = "missing"
	_, err := f.s.ControllerContinuation(t.Context(), f.owner, "acquire", wrong)
	continuationCode(t, err, "CONTINUATION_WAKE_NOT_FOUND")
	first := f.app(t, "acquire", in)
	f.now = f.now.Add(31 * time.Second)
	next := f.app(t, "acquire", in)
	if first.AttemptID == next.AttemptID || next.Wake.State != protocol.WorkWakeClaimed {
		t.Fatalf("expired claim not safely reclaimed: %#v", next)
	}
	in.WakeID = first.WakeID
	in.AttemptID = first.AttemptID
	_, err = f.s.ControllerContinuation(t.Context(), f.owner, "prepare", in)
	continuationCode(t, err, "CONTINUATION_ATTEMPT_DENIED")
}
func TestContinuationSettledHistoryRetention(t *testing.T) {
	f := newContinuationFixture(t)
	f.control(t, protocol.WorkContinuationInput{Action: "enable", Confirmed: true, MaxRounds: 100})
	in := f.bind(t)
	for i := 0; i < 35; i++ {
		id := fmt.Sprintf("history-%d", i)
		f.await(t, id)
		f.record(t, id)
		var envelope protocol.ResumeEnvelope
		in, envelope = f.prepare(t, in)
		if _, err := f.s.ConsumeWorkWake(t.Context(), f.owner, envelope); err != nil {
			t.Fatal(err)
		}
		f.control(t, protocol.WorkContinuationInput{Action: "settle", WakeID: in.WakeID, Checkpoint: "done"})
		in.WakeID = ""
		in.AttemptID = ""
	}
	var raw []byte
	if err := f.db.QueryRow("SELECT document_json FROM work_continuations WHERE work_session_id=?", f.ws).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var d continuationDocument
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Wakes) != 32 || len(d.Attempts) != 32 {
		t.Fatalf("unbounded settled history: %d/%d", len(d.Wakes), len(d.Attempts))
	}
	f.record(t, "history-0")
	r := f.await(t, "history-0")
	if r.Wake != nil {
		t.Fatal("tombstone replay generated old work")
	}
}

func TestContinuationRestartPreservesDispatchFence(t *testing.T) {
	f := newContinuationFixture(t)
	f.control(t, protocol.WorkContinuationInput{Action: "enable", Confirmed: true})
	in := f.bind(t)
	f.await(t, "restart")
	f.record(t, "restart")
	in, envelope := f.prepare(t, in)
	var seq int
	var name, path string
	if err := f.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := core.OpenSQLite(t.Context(), path, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = core.EnsureSchema(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	f.db = db
	f.s, err = NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	f.s.now = func() time.Time { return f.now }
	_, err = f.s.ControllerContinuation(t.Context(), f.owner, "prepare", in)
	continuationCode(t, err, "CONTINUATION_DISPATCH_FENCED")
	r, err := f.s.ConsumeWorkWake(t.Context(), f.owner, envelope)
	if err != nil || len(r.Outcomes) != 1 {
		t.Fatalf("restart lost prepared result: %#v %v", r, err)
	}
}
func TestContinuationUnawaitedRetentionPreservesPendingAndSignalsExpiry(t *testing.T) {
	f := newContinuationFixture(t)
	f.control(t, protocol.WorkContinuationInput{Action: "enable", Confirmed: true})
	f.await(t, "pending")
	f.record(t, "pending", "unawaited")
	f.now = f.now.Add(31 * 24 * time.Hour)
	f.record(t, "new")
	var pending, expired int
	if err := f.db.QueryRow("SELECT eligible FROM command_source_receipts WHERE command_session_id='pending'").Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT eligible FROM command_source_receipts WHERE command_session_id='unawaited'").Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || expired != 2 {
		t.Fatalf("retention pending=%d unawaited=%d", pending, expired)
	}
	_, err := f.s.ControlContinuation(t.Context(), f.owner, protocol.WorkContinuationInput{WorkSessionID: f.ws, Action: "await", Sources: []protocol.ContinuationSource{{TargetID: f.target.ID, CommandSessionID: "unawaited"}}})
	continuationCode(t, err, "CONTINUATION_SOURCE_EXPIRED")
	r := f.control(t, protocol.WorkContinuationInput{Action: "status"})
	if r.Wake == nil || r.Wake.Sources[0].Output == "" {
		t.Fatal("retention removed unresolved result")
	}
}
func TestContinuationSourcePartitionExactSelectionAndSingleClaim(t *testing.T) {
	f := newContinuationFixture(t)
	f.control(t, protocol.WorkContinuationInput{Action: "enable", Confirmed: true})
	in := f.bind(t)
	for batch := 0; batch < 2; batch++ {
		ids := make([]string, 32)
		for i := range ids {
			ids[i] = fmt.Sprintf("partition-%d-%d", batch, i)
		}
		f.await(t, ids...)
		f.record(t, ids...)
	}
	var raw []byte
	if err := f.db.QueryRow("SELECT document_json FROM work_continuations WHERE work_session_id=?", f.ws).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var d continuationDocument
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Wakes) != 2 || len(d.Wakes[0].Sources) != 32 || len(d.Wakes[1].Sources) != 32 {
		t.Fatalf("unbounded Wake merge: %#v", d.Wakes)
	}
	second := in
	second.WakeID = d.Wakes[1].WakeID
	claimed := f.app(t, "acquire", second)
	if claimed.WakeID != second.WakeID || claimed.AttemptID == "" {
		t.Fatal("acquire returned another Wake identity")
	}
	second.AttemptID = claimed.AttemptID
	first := in
	first.WakeID = d.Wakes[0].WakeID
	_, err := f.s.ControllerContinuation(t.Context(), f.owner, "acquire", first)
	continuationCode(t, err, "CONTINUATION_VIEW_BUSY")
	prepared := f.app(t, "prepare", second)
	if prepared.WakeID != second.WakeID || prepared.AttemptID != second.AttemptID {
		t.Fatal("prepare mixed message with another Wake")
	}
	state := f.app(t, "state", in)
	if state.WakeID != second.WakeID {
		t.Fatal("state hid active fence behind pending Wake")
	}
	second.DeliveryStatus = "dispatch_accepted"
	finished := f.app(t, "finish", second)
	if finished.WakeID != second.WakeID {
		t.Fatal("finish returned another Wake")
	}
}

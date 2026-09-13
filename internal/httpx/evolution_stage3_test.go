package httpx

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/config"
	"github.com/uvwt/nexusdock/internal/stage3"
)

func TestStage3SchedulerRunsImmediatelyAndWakeKeepsOriginalDeadline(t *testing.T) {
	base := time.Date(2026, 8, 16, 6, 0, 0, 0, time.UTC)
	var nowNanos atomic.Int64
	nowNanos.Store(base.UnixNano())
	now := func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() }
	cfg := config.Config{
		EvolutionEnabled: true, ModelEndpoint: "http://model.invalid", ModelName: "test", EvolutionInterval: 2 * time.Hour,
	}
	server := &Server{cfg: cfg, aiCfg: cfg, aiCfgSet: true, stage3Wake: make(chan struct{}, 1)}
	runs := make(chan config.Config, 8)
	delays := make(chan time.Duration, 8)
	ticks := make(chan time.Time, 8)
	newTimer := func(wait time.Duration) evolutionStage3Timer {
		delays <- wait
		return evolutionStage3Timer{c: ticks, stop: func() {}}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go server.evolutionStage3Loop(ctx, now, newTimer, func(_ context.Context, got config.Config) { runs <- got })

	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("Stage 3 did not run immediately on startup")
	}
	if wait := <-delays; wait != 2*time.Hour {
		t.Fatalf("initial wait = %s", wait)
	}
	nowNanos.Store(base.Add(30 * time.Minute).UnixNano())
	server.stage3Wake <- struct{}{}
	if wait := <-delays; wait != 90*time.Minute {
		t.Fatalf("wait after wake = %s", wait)
	}
	select {
	case <-runs:
		t.Fatal("config wake triggered an unintended immediate run")
	default:
	}
	ticks <- now()
	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("scheduled Stage 3 run did not execute")
	}
}

func TestStage3SchedulerRunsImmediatelyWhenReenabled(t *testing.T) {
	cfg := config.Config{
		EvolutionEnabled: false, ModelEndpoint: "http://model.invalid", ModelName: "test", EvolutionInterval: 2 * time.Hour,
	}
	server := &Server{cfg: cfg, aiCfg: cfg, aiCfgSet: true, stage3Wake: make(chan struct{}, 1)}
	runs := make(chan struct{}, 2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go server.evolutionStage3Loop(ctx, time.Now, func(time.Duration) evolutionStage3Timer {
		return evolutionStage3Timer{c: make(chan time.Time), stop: func() {}}
	}, func(context.Context, config.Config) { runs <- struct{}{} })

	server.mu.Lock()
	server.aiCfg.EvolutionEnabled = true
	server.mu.Unlock()
	server.stage3Wake <- struct{}{}
	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("Stage 3 did not run immediately after being re-enabled")
	}
}

func TestStage3TargetNodeNeverFallsBackToFirstNode(t *testing.T) {
	nodes := []agentdock.Node{{ID: "node_a", Enabled: true}, {ID: "node_b", Enabled: true}}
	if got, reason := stage3TargetNode(nodes, stage3.Candidate{}, "", nil); got != "" || reason != "no_evidence_source_or_review_node" {
		t.Fatalf("empty candidate routed to %q reason=%q", got, reason)
	}
	if got, reason := stage3TargetNode(nodes, stage3.Candidate{Scope: "device", Device: "node_missing"}, "node_a", nil); got != "" || reason != "candidate_device_not_enabled" {
		t.Fatalf("unknown device fell back: node=%q reason=%q", got, reason)
	}
	if got, reason := stage3TargetNode(nodes, stage3.Candidate{Scope: "device"}, "node_a", nil); got != "" || reason != "device_scope_missing_node_id" {
		t.Fatalf("device scope without id fell back: node=%q reason=%q", got, reason)
	}
	if got, reason := stage3TargetNode(nodes, stage3.Candidate{Scope: "device", Device: "node_b"}, "", []string{"node_a"}); got != "" || reason != "candidate_device_evidence_mismatch" {
		t.Fatalf("cross-node device evidence was routed: node=%q reason=%q", got, reason)
	}
	if got, reason := stage3TargetNode(nodes, stage3.Candidate{Scope: "device", Device: "node_b"}, "", []string{"node_a", "node_b"}); got != "" || reason != "candidate_device_evidence_mismatch" {
		t.Fatalf("mixed device evidence was routed: node=%q reason=%q", got, reason)
	}
	if got, reason := stage3TargetNode(nodes, stage3.Candidate{Scope: "device", Device: "node_b"}, "", []string{"node_b"}); got != "node_b" || reason != "candidate_device" {
		t.Fatalf("matching device evidence was not routed: node=%q reason=%q", got, reason)
	}
	if got, reason := stage3TargetNode(nodes, stage3.Candidate{}, "node_b", []string{"node_a", "node_b"}); got != "node_b" || reason != "configured_review_node" {
		t.Fatalf("configured review node = %q reason=%q", got, reason)
	}
	if got, reason := stage3TargetNode(nodes, stage3.Candidate{}, "node_b", []string{"node_a"}); got != "node_a" || reason != "unique_evidence_source" {
		t.Fatalf("unique evidence source = %q reason=%q", got, reason)
	}
}

func TestStage3EvidenceOwnershipIsNodeScopedAndAmbiguityIsNotHardEvidence(t *testing.T) {
	tasks := []stage3.TaskFact{
		{NodeID: "node_a", TaskID: "same", ReviewRevision: "rev_same", VerifiedFacts: []string{"A"}},
		{NodeID: "node_b", TaskID: "same", ReviewRevision: "rev_same", VerifiedFacts: []string{"B"}},
		{NodeID: "node_a", TaskID: "unique", ReviewRevision: "rev_unique", VerifiedFacts: []string{"A2"}},
	}
	sources := stage3EvidenceSources(tasks)
	ambiguous := "task:same:review:rev_same:verified:0"
	unique := "task:unique:review:rev_unique:verified:0"
	if len(sources[ambiguous]) != 2 || len(sources[unique]) != 1 || !sources[unique]["node_a"] {
		t.Fatalf("sources = %#v", sources)
	}
	valid := filterStage3Evidence([]string{ambiguous, unique, "invented"}, sources)
	if !reflect.DeepEqual(valid, []string{ambiguous, unique}) {
		t.Fatalf("valid refs = %#v", valid)
	}
	if got := filterStage3EvidenceForNode(valid, sources, "node_a"); !reflect.DeepEqual(got, []string{unique}) {
		t.Fatalf("node_a hard evidence = %#v", got)
	}
	if got := stage3CandidateSourceNodes(valid, sources); !reflect.DeepEqual(got, []string{"node_a", "node_b"}) {
		t.Fatalf("candidate source nodes = %#v", got)
	}
}

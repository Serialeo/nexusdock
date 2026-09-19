package httpx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/config"
	"github.com/uvwt/nexusdock/internal/recall"
	"github.com/uvwt/nexusdock/internal/stage3"
)

const (
	stage3TaskListPerNode   = 32
	stage3TaskDetailPerNode = 8
	stage3LifecycleLimit    = 50
	stage3WorkflowLimit     = 30
)

// StartEvolutionStage3 starts one long-lived scheduler. Runtime settings changes wake the
// scheduler so model endpoint, model name, key and interval take effect without restarting Nexus.
func (s *Server) StartEvolutionStage3(ctx context.Context) {
	go s.evolutionStage3Loop(ctx, time.Now, newEvolutionStage3Timer, s.runEvolutionStage3Configured)
}

// evolutionStage3Loop keeps scheduling semantics deterministic and testable:
// a runnable configuration gets one immediate attempt, then every interval is anchored to
// the previous attempt. A wake only reloads configuration; it cannot keep postponing the timer.
func (s *Server) evolutionStage3Loop(
	ctx context.Context,
	now func() time.Time,
	newTimer func(time.Duration) evolutionStage3Timer,
	run func(context.Context, config.Config),
) {
	var lastAttempt time.Time
	wasRunnable := false
	for {
		cfg := s.currentConfig()
		runnable := cfg.EvolutionEnabled && strings.TrimSpace(cfg.ModelEndpoint) != "" && strings.TrimSpace(cfg.ModelName) != ""
		if !runnable {
			// Re-enabling Stage 3 is a new runnable period, so it should run immediately once.
			lastAttempt = time.Time{}
			wasRunnable = false
			select {
			case <-ctx.Done():
				return
			case <-s.stage3Wake:
				continue
			}
		}

		interval := cfg.EvolutionInterval
		if interval < time.Hour {
			interval = time.Hour
		}
		if !wasRunnable || lastAttempt.IsZero() {
			wasRunnable = true
			run(ctx, cfg)
			lastAttempt = now()
			continue
		}

		wait := lastAttempt.Add(interval).Sub(now())
		if wait <= 0 {
			run(ctx, cfg)
			lastAttempt = now()
			continue
		}
		timer := newTimer(wait)
		select {
		case <-ctx.Done():
			timer.stop()
			return
		case <-s.stage3Wake:
			timer.stop()
			continue
		case <-timer.c:
			run(ctx, cfg)
			lastAttempt = now()
		}
	}
}

type evolutionStage3Timer struct {
	c    <-chan time.Time
	stop func()
}

func newEvolutionStage3Timer(wait time.Duration) evolutionStage3Timer {
	timer := time.NewTimer(wait)
	return evolutionStage3Timer{
		c: timer.C,
		stop: func() {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		},
	}
}

func (s *Server) runEvolutionStage3Configured(ctx context.Context, cfg config.Config) {
	client, err := stage3.NewClient(stage3.Config{
		Endpoint:     cfg.ModelEndpoint,
		Model:        cfg.ModelName,
		APIKey:       cfg.ModelAPIKey,
		Timeout:      cfg.ModelTimeout,
		SystemPrompt: cfg.ModelSystemPrompt,
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("Stage 3 evolution skipped invalid model configuration", "error", err)
		}
		return
	}
	if err := s.runEvolutionStage3(ctx, client, cfg.Stage3ReviewNodeID); err != nil && s.logger != nil {
		s.logger.Warn("Stage 3 evolution run failed", "error", err)
	}
}

func (s *Server) runEvolutionStage3(ctx context.Context, client *stage3.Client, reviewNodeID string) error {
	if client == nil {
		return fmt.Errorf("Stage 3 model client is nil")
	}
	snapshot, nodes, err := s.stage3Snapshot(ctx)
	if err != nil {
		return err
	}
	if len(nodes) == 0 || (len(snapshot.Tasks) == 0 && len(snapshot.Lifecycle) == 0 && len(snapshot.Workflows) == 0) {
		return nil
	}
	output, err := client.Generate(ctx, snapshot)
	if err != nil {
		return err
	}
	evidenceSources := stage3EvidenceSources(snapshot.Tasks)
	for _, candidate := range output.Candidates {
		validRefs := filterStage3Evidence(candidate.EvidenceRefs, evidenceSources)
		sourceNodes := stage3CandidateSourceNodes(validRefs, evidenceSources)
		nodeID, routeReason := stage3TargetNode(nodes, candidate, reviewNodeID, sourceNodes)
		if nodeID == "" {
			s.recordStage3ProposalAudit(candidate, "", sourceNodes, validRefs, "skipped", routeReason)
			if s.logger != nil {
				s.logger.Debug("Stage 3 proposal skipped without an unambiguous review node", "candidate_type", candidate.Type, "reason", routeReason)
			}
			continue
		}
		hardEvidenceRefs := filterStage3EvidenceForNode(validRefs, evidenceSources, nodeID)
		payload := map[string]any{
			"intent": "propose",
			"candidate": map[string]any{
				"type": candidate.Type, "statement": candidate.Statement, "scope": candidate.Scope,
				"project": candidate.Project, "device": candidate.Device, "canonical_key": candidate.CanonicalKey,
				"tags": candidate.Tags,
			},
			// Only references uniquely owned by the target node cross the AgentDock evidence boundary.
			// Other valid references remain non-authoritative provenance in Nexus audit records.
			"evidence_refs": hardEvidenceRefs,
			"rationale":     candidate.Rationale,
		}
		if _, err := s.runtimePost(ctx, nodeID, "/internal/runtime/evolve", payload); err != nil {
			s.recordStage3ProposalAudit(candidate, nodeID, sourceNodes, validRefs, "rejected", stage3RuntimeErrorCode(err))
			if s.logger != nil {
				s.logger.Warn("Stage 3 proposal rejected by AgentDock", "node_id", nodeID, "candidate_type", candidate.Type, "error", err)
			}
			continue
		}
		s.recordStage3ProposalAudit(candidate, nodeID, sourceNodes, validRefs, "proposed", "")
	}
	return nil
}

func (s *Server) stage3Snapshot(ctx context.Context) (stage3.Snapshot, []agentdock.Node, error) {
	if s.agentDock == nil {
		return stage3.Snapshot{}, nil, fmt.Errorf("AgentDock node store unavailable")
	}
	nodes, err := s.agentDock.List(ctx)
	if err != nil {
		return stage3.Snapshot{}, nil, err
	}
	enabled := make([]agentdock.Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Enabled && node.IsCurrent() {
			enabled = append(enabled, node)
		}
	}
	snapshot := stage3.Snapshot{}

	records, err := s.store.QueryLifecycle(recall.LifecycleQuery{Limit: stage3LifecycleLimit})
	if err != nil {
		return stage3.Snapshot{}, nil, fmt.Errorf("query lifecycle for Stage 3: %w", err)
	}
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.Scope), "local_only") || stage3SensitiveTags(record.Tags) {
			continue
		}
		snapshot.Lifecycle = append(snapshot.Lifecycle, stage3.LifecycleFact{
			EvolutionID: record.EvolutionID, Type: record.Type, Statement: record.Statement, Scope: record.Scope,
			Project: record.Project, Device: record.Device, Status: record.Status, SupportCount: record.SupportCount,
			ContradictCount: record.ContradictCount, Tags: append([]string(nil), record.Tags...),
		})
	}

	for _, node := range enabled {
		tasks, taskErr := s.collectOpsTasksFromRuntime(ctx, node.ID, stage3TaskListPerNode)
		if taskErr != nil {
			if s.logger != nil {
				s.logger.Debug("Stage 3 skipped unavailable AgentDock node", "node_id", node.ID, "error", taskErr)
			}
			continue
		}
		sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].UpdatedAt > tasks[j].UpdatedAt })
		count := 0
		for _, summary := range tasks {
			if count >= stage3TaskDetailPerNode {
				break
			}
			if summary.ReviewStatus != "pass" && summary.ReviewStatus != "failed" {
				continue
			}
			detail, detailErr := s.runtimeTaskDetailFromRuntime(ctx, node.ID, summary.ID)
			if detailErr != nil {
				continue
			}
			reviewRevision := opsString(detail.FinalReview["review_revision"])
			if reviewRevision == "" {
				continue
			}
			snapshot.Tasks = append(snapshot.Tasks, stage3.TaskFact{
				NodeID: node.ID, TaskID: summary.ID, Title: summary.Title, Goal: summary.Goal, Summary: summary.Summary,
				Status: summary.Status, ReviewStatus: summary.ReviewStatus, ReviewRevision: reviewRevision,
				VerifiedFacts: opsStringArray(detail.FinalReview["verified_facts"]), OpenRisks: opsStringArray(detail.FinalReview["open_risks"]),
				MissingChecks: opsStringArray(detail.FinalReview["missing_checks"]), UpdatedAt: summary.UpdatedAt,
			})
			count++
		}
	}

	workflows, err := s.listWorkflowTemplates(workflowTemplateActive)
	if err == nil {
		workflows = latestWorkflowTemplateVersions(workflows)
		if len(workflows) > stage3WorkflowLimit {
			workflows = workflows[:stage3WorkflowLimit]
		}
		for _, workflow := range workflows {
			snapshot.Workflows = append(snapshot.Workflows, stage3.WorkflowFact{
				ID: workflow.ID, Version: workflow.Version, Title: workflow.Title, Description: workflow.Description, Type: workflow.Match.Type,
			})
		}
	}
	return stage3.RedactSnapshot(snapshot), enabled, nil
}

func stage3SensitiveTags(tags []string) bool {
	for _, tag := range tags {
		switch strings.ToLower(strings.TrimSpace(tag)) {
		case "sensitive", "local_only", "private", "secret":
			return true
		}
	}
	return false
}

func stage3EvidenceSources(tasks []stage3.TaskFact) map[string]map[string]bool {
	sources := map[string]map[string]bool{}
	for _, task := range tasks {
		prefix := "task:" + task.TaskID + ":review:" + task.ReviewRevision
		for kind, count := range map[string]int{"verified": len(task.VerifiedFacts), "risk": len(task.OpenRisks), "missing": len(task.MissingChecks)} {
			for i := 0; i < count; i++ {
				ref := fmt.Sprintf("%s:%s:%d", prefix, kind, i)
				if sources[ref] == nil {
					sources[ref] = map[string]bool{}
				}
				sources[ref][task.NodeID] = true
			}
		}
	}
	return sources
}

func filterStage3Evidence(refs []string, sources map[string]map[string]bool) []string {
	out := make([]string, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if len(sources[ref]) == 0 || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

func stage3CandidateSourceNodes(refs []string, sources map[string]map[string]bool) []string {
	set := map[string]bool{}
	for _, ref := range refs {
		for nodeID := range sources[ref] {
			set[nodeID] = true
		}
	}
	out := make([]string, 0, len(set))
	for nodeID := range set {
		out = append(out, nodeID)
	}
	sort.Strings(out)
	return out
}

func filterStage3EvidenceForNode(refs []string, sources map[string]map[string]bool, nodeID string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		owners := sources[ref]
		if len(owners) == 1 && owners[nodeID] {
			out = append(out, ref)
		}
	}
	return out
}

func stage3TargetNode(nodes []agentdock.Node, candidate stage3.Candidate, reviewNodeID string, sourceNodes []string) (string, string) {
	candidate.Device = strings.TrimSpace(candidate.Device)
	candidate.Scope = strings.ToLower(strings.TrimSpace(candidate.Scope))
	if candidate.Device != "" || candidate.Scope == "device" {
		if candidate.Device == "" {
			return "", "device_scope_missing_node_id"
		}
		for _, node := range nodes {
			if node.ID == candidate.Device && node.Enabled {
				if len(sourceNodes) > 0 && (len(sourceNodes) != 1 || sourceNodes[0] != candidate.Device) {
					return "", "candidate_device_evidence_mismatch"
				}
				return node.ID, "candidate_device"
			}
		}
		return "", "candidate_device_not_enabled"
	}
	if len(sourceNodes) == 1 {
		for _, node := range nodes {
			if node.ID == sourceNodes[0] && node.Enabled {
				return node.ID, "unique_evidence_source"
			}
		}
		return "", "unique_evidence_source_not_enabled"
	}
	reviewNodeID = strings.TrimSpace(reviewNodeID)
	if reviewNodeID == "" {
		if len(sourceNodes) > 1 {
			return "", "multiple_evidence_sources_without_review_node"
		}
		return "", "no_evidence_source_or_review_node"
	}
	for _, node := range nodes {
		if node.ID == reviewNodeID && node.Enabled {
			return node.ID, "configured_review_node"
		}
	}
	return "", "configured_review_node_not_enabled"
}

func (s *Server) recordStage3ProposalAudit(candidate stage3.Candidate, targetNodeID string, sourceNodes, evidenceRefs []string, result, errorCode string) {
	if s == nil || s.db == nil {
		return
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	sourceJSON, _ := json.Marshal(sourceNodes)
	evidenceJSON, _ := json.Marshal(evidenceRefs)
	digest := sha256.Sum256([]byte(strings.Join([]string{createdAt, targetNodeID, candidate.Type, candidate.Scope, candidate.CanonicalKey, candidate.Statement}, "\x00")))
	id := "s3a_" + hex.EncodeToString(digest[:16])
	if _, err := s.db.Exec(`INSERT INTO stage3_proposal_audit(
		id, created_at, target_node_id, candidate_type, candidate_scope, candidate_device, canonical_key,
		source_nodes_json, evidence_refs_json, rationale, result, error_code
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, createdAt, targetNodeID, candidate.Type, candidate.Scope, candidate.Device, candidate.CanonicalKey,
		string(sourceJSON), string(evidenceJSON), stage3.RedactText(candidate.Rationale), result, errorCode); err != nil && s.logger != nil {
		s.logger.Warn("record Stage 3 proposal audit failed", "error", err)
	}
}

func stage3RuntimeErrorCode(err error) string {
	var runtimeErr agentDockRuntimeError
	if errors.As(err, &runtimeErr) {
		if runtimeErr.UpstreamCode != "" {
			return runtimeErr.UpstreamCode
		}
		if runtimeErr.Code != "" {
			return runtimeErr.Code
		}
	}
	return "runtime_error"
}

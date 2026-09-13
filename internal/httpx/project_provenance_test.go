package httpx

import protocol "github.com/Serialeo/agentdock-protocol"

func projectTestSourceProvenance() protocol.SourceProvenance {
	return protocol.SourceProvenance{Kind: protocol.SourceProvenanceNone}
}

func projectTestTargetAcknowledgment(bind protocol.ProjectTargetBindRequest) map[string]any {
	return map[string]any{"target": map[string]any{
		"work_session_id":     bind.WorkSessionID,
		"target_id":           bind.TargetID,
		"project_id":          bind.ProjectID,
		"deployment_id":       bind.DeploymentID,
		"cwd_rel":             bind.CWDRel,
		"deployment_revision": bind.DeploymentRevision,
		"context_revision":    bind.ContextRevision,
		"source_provenance":   bind.SourceProvenance,
	}}
}

func projectTestTargetRebindAcknowledgment(bind protocol.ProjectTargetRebindRequest) map[string]any {
	return map[string]any{"target": map[string]any{
		"work_session_id":   bind.WorkSessionID,
		"target_id":         bind.TargetID,
		"cwd_rel":           bind.CWDRel,
		"context_revision":  bind.ContextRevision,
		"source_provenance": bind.SourceProvenance,
	}}
}

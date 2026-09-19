package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	protocol "github.com/Serialeo/agentdock-protocol"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

func isProjectContextTool(name string) bool {
	return name == "project_open" || name == "project_context"
}

func projectContextAckFromRequest(request *mcpsdk.CallToolRequest) (*protocol.ProjectContextAcknowledgment, error) {
	if request == nil || request.Params == nil || request.Params.Meta == nil {
		return nil, nil
	}
	raw, ok := request.Params.Meta[protocol.ProjectContextAckMetaKey]
	if !ok {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode Project Context acknowledgment metadata: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var ack protocol.ProjectContextAcknowledgment
	if err := decoder.Decode(&ack); err != nil {
		return nil, fmt.Errorf("decode Project Context acknowledgment metadata: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("Project Context acknowledgment metadata must contain exactly one JSON value")
		}
		return nil, fmt.Errorf("decode trailing Project Context acknowledgment metadata: %w", err)
	}
	ack.WorkSessionID = strings.TrimSpace(ack.WorkSessionID)
	ack.TargetID = strings.TrimSpace(ack.TargetID)
	ack.ContextRevision = strings.TrimSpace(ack.ContextRevision)
	if ack.WorkSessionID == "" || ack.ContextRevision == "" {
		return nil, errors.New("Project Context acknowledgment requires work_session_id and context_revision")
	}
	return &ack, nil
}

func (s *Server) consumeProjectContextAck(ctx context.Context, request *mcpsdk.CallToolRequest) (map[string]any, error) {
	ack, err := projectContextAckFromRequest(request)
	if err != nil {
		return projectToolError(protocol.ErrorExecutionContextInvalid, "invalid Project Context Host acknowledgment metadata", map[string]any{"reason": err.Error()})
	}
	if ack == nil {
		return nil, nil
	}
	binding, ok := mcpClientBindingFromContext(ctx)
	if !ok {
		return projectToolError("MCP_CLIENT_BINDING_REQUIRED", "authenticated MCP client binding is required for Project Context acknowledgment", nil)
	}
	if s.projects == nil {
		return projectToolError("PROJECT_STORE_UNAVAILABLE", "Project store is unavailable", nil)
	}
	_, err = s.projects.AcknowledgeContextConsumed(ctx, binding.OwnerKey, *ack)
	if err == nil {
		return nil, nil
	}
	var conflict *projectstore.ContextDeliveryRevisionConflictError
	switch {
	case errors.As(err, &conflict):
		return projectToolError(protocol.ErrorRevisionConflict, "Project Context acknowledgment is stale; refresh the Project Context", nil)
	case errors.Is(err, projectstore.ErrContextDeliveryNotFound):
		return projectToolError(protocol.ErrorContextRefreshRequired, "Project Context revision was not previously returned to this Host", nil)
	case errors.Is(err, projectstore.ErrWorkSessionNotFound), errors.Is(err, projectstore.ErrWorkTargetNotFound):
		return projectToolError(protocol.ErrorSessionTargetDenied, "Project Context acknowledgment does not belong to this MCP client", nil)
	default:
		return projectToolError("PROJECT_OPERATION_FAILED", "failed to persist Project Context Host acknowledgment", map[string]any{"reason": err.Error()})
	}
}

func (s *Server) recordProjectContextReturned(ctx context.Context, name string, response *mcpsdk.CallToolResult) error {
	if !isProjectContextTool(name) || response == nil {
		return nil
	}
	binding, ok := mcpClientBindingFromContext(ctx)
	if !ok {
		return errors.New("authenticated MCP client binding is required to record Project Context delivery")
	}
	raw, ok := response.Meta[protocol.ProjectContextDeliveryMetaKey]
	if !ok {
		return errors.New("Project Context result is missing delivery metadata")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encode Project Context delivery metadata: %w", err)
	}
	var identity protocol.ProjectContextAcknowledgment
	if err := json.Unmarshal(encoded, &identity); err != nil {
		return fmt.Errorf("decode Project Context delivery metadata: %w", err)
	}
	identity.WorkSessionID = strings.TrimSpace(identity.WorkSessionID)
	identity.TargetID = strings.TrimSpace(identity.TargetID)
	identity.ContextRevision = strings.TrimSpace(identity.ContextRevision)
	if identity.WorkSessionID == "" || identity.ContextRevision == "" || (name == "project_context" && identity.TargetID == "") {
		return errors.New("Project Context result is missing delivery identity")
	}
	delivery, err := s.projects.RecordContextReturned(ctx, binding.OwnerKey, identity.WorkSessionID, identity.TargetID, identity.ContextRevision)
	if err != nil {
		return err
	}
	response.Meta[protocol.ProjectContextDeliveryMetaKey] = map[string]any{
		"work_session_id":  identity.WorkSessionID,
		"target_id":        identity.TargetID,
		"context_revision": identity.ContextRevision,
		"status":           delivery.Status,
	}
	if identity.TargetID == "" {
		delete(response.Meta[protocol.ProjectContextDeliveryMetaKey].(map[string]any), "target_id")
	}
	return nil
}

package httpx

import (
	"context"
	"errors"
	"strings"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/Serialeo/agentdock-protocol/mcpcontract"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

const continuationPrivateMeta = protocol.WorkContinuationMetaKey

func isContinuationTool(name string) bool {
	switch name {
	case "work_continuation", "present_work_continuation", "consume_work_wake",
		"work_continuation_bind", "work_continuation_state", "work_continuation_heartbeat", "work_continuation_pause",
		"work_wake_acquire", "work_wake_prepare", "work_wake_finish":
		return true
	}
	return false
}

func continuationAppOnly(name string) bool {
	return isContinuationTool(name) && name != "work_continuation" && name != "present_work_continuation" && name != "consume_work_wake"
}

// 私有绑定材料只放入 tool result _meta；模型看到的 text/structuredContent 均不包含它。
func (s *Server) continuationToolResult(ctx context.Context, name string, args map[string]any) (*mcpsdk.CallToolResult, error) {
	result, err := s.callWorkContinuation(ctx, name, args)
	var secret any
	if result != nil {
		secret = result["binding_secret"]
		delete(result, "binding_secret")
	}
	response, responseErr := s.gatewayToolResult(name, result, err)
	if responseErr == nil && response != nil && err == nil && name == "present_work_continuation" {
		response.Meta = centralToolUIResourceMeta(protocol.WorkContinuationUIResourceURI)
		binding := map[string]any{"binding_secret": secret}
		for _, key := range []string{"work_session_id", "endpoint_id", "controller_generation"} {
			binding[key] = result[key]
		}
		response.Meta[continuationPrivateMeta] = binding
	}
	return response, responseErr
}

func (s *Server) callWorkContinuation(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	binding, ok := mcpClientBindingFromContext(ctx)
	if !ok {
		return projectToolError("MCP_CLIENT_BINDING_REQUIRED", "authenticated MCP client binding is required", nil)
	}
	if s.projects == nil {
		return projectToolError("PROJECT_STORE_UNAVAILABLE", "Project store is unavailable", nil)
	}
	if !s.mcpAppsEnabled() && (continuationAppOnly(name) || name == "present_work_continuation") {
		return projectToolError("MCP_APPS_ENABLED_REQUIRED", "MCP Apps are disabled", nil)
	}
	var result protocol.WorkContinuationResult
	var err error
	switch name {
	case "work_continuation":
		var input protocol.WorkContinuationInput
		if err := decodeProjectToolArgs(args, &input); err != nil {
			return continuationError(err)
		}
		if !s.mcpAppsEnabled() && input.Action != "status" && input.Action != "pause" && input.Action != "settle" {
			return projectToolError("MCP_APPS_ENABLED_REQUIRED", "enable MCP Apps before requesting automatic continuation", nil)
		}
		if input.Action == "await" {
			for _, source := range input.Sources {
				target, targetErr := s.projects.GetWorkTarget(ctx, binding.OwnerKey, input.WorkSessionID, source.TargetID)
				if targetErr != nil {
					return continuationError(targetErr)
				}
				if s.agentDock == nil {
					return continuationError(errors.New("node directory is unavailable"))
				}
				capabilities, capErr := s.agentDock.BridgeCapabilities(ctx, target.Target.NodeID)
				if capErr != nil {
					return continuationError(capErr)
				}
				if !containsString(capabilities, protocol.CommandOutcomesCapability) {
					return continuationError(errors.New("target AgentDock does not support durable command outcomes; upgrade the node before awaiting commands"))
				}
			}
		}
		result, err = s.projects.ControlContinuation(ctx, binding.OwnerKey, input)
	case "present_work_continuation":
		var input protocol.PresentWorkContinuationInput
		if err := decodeProjectToolArgs(args, &input); err != nil {
			return continuationError(err)
		}
		result, err = s.projects.PresentContinuation(ctx, binding.OwnerKey, input.WorkSessionID, input.Recover)
	case "consume_work_wake":
		var input protocol.ResumeEnvelope
		if err := decodeProjectToolArgs(args, &input); err != nil {
			return continuationError(err)
		}
		result, err = s.projects.ConsumeWorkWake(ctx, binding.OwnerKey, input)
	default:
		if !continuationAppOnly(name) {
			return continuationError(errors.New("unknown continuation tool"))
		}
		var input protocol.ContinuationControllerInput
		if err := decodeProjectToolArgs(args, &input); err != nil {
			return continuationError(err)
		}
		action := strings.TrimPrefix(strings.TrimPrefix(name, "work_continuation_"), "work_wake_")
		result, err = s.projects.ControllerContinuation(ctx, binding.OwnerKey, action, input)
	}
	if err != nil {
		return continuationError(err)
	}
	return asMap(result)
}

func continuationError(err error) (map[string]any, error) {
	code := "WORK_CONTINUATION_DENIED"
	var detail *projectstore.ContinuationError
	if errors.As(err, &detail) {
		code = detail.Code
	}
	return projectToolError(code, "continuation operation could not be completed", map[string]any{"reason": err.Error()})
}

func continuationToolNames() []string { return mcpcontract.ContinuationToolNames() }

func continuationToolDefinitions(apps bool) []*mcpsdk.Tool {
	descriptions := map[string]string{
		"work_continuation":           "Manage explicit WorkSession continuation. Enable only after the user requests automatic continuation, with confirmed=true and bounded max_rounds/max_failures. Await only command_session_id sources intentionally handed off at the end of a model turn. Consume is not completion: settle the exact wake after checking outcomes and saving a checkpoint. Pause stops future messages; it does not cancel commands. Status supports inspection and recovery.",
		"present_work_continuation":   "Present the sole continuation controller for this exact WorkSession in the current conversation. Never infer a recent session. This does not enable automation. Use recover=true only after explicit user-directed inspection of the previous controller. Prepared messages are never resent automatically.",
		"consume_work_wake":           "Consume the exact single-use envelope from a continuation message before taking further action. Obtain authoritative command outcomes and bound targets from this result; never infer or rerun a command from a notification. Save a checkpoint and settle the exact wake once handled.",
		"work_continuation_bind":      "Bind this controller view to the explicitly presented endpoint. Requires presentation proof and current authenticated WorkSession owner. user_enabled records explicit permission for this view to dispatch; it does not enable the WorkSession.",
		"work_continuation_state":     "Read current durable continuation state for an authenticated controller binding.",
		"work_continuation_heartbeat": "Renew this controller binding lease. Does not send a message or indicate model idleness.",
		"work_continuation_pause":     "Pause further automatic continuation messages for this WorkSession. Running commands continue.",
		"work_wake_acquire":           "Claim a pending wake for the current foreground controller. Claims expire before preparation.",
		"work_wake_prepare":           "Durably fence one dispatch and return its complete automatic_message exactly once. Never repeat prepare after a timeout, and never construct or alter the message.",
		"work_wake_finish":            "Record dispatch_accepted, delivery_rejected, or delivery_unknown. A resolved Host request with isError is rejected. A timeout is unknown. Accepted is not proof of a new model turn; late reports cannot undo consume.",
	}
	var definitions []*mcpsdk.Tool
	for _, name := range continuationToolNames() {
		if !apps && (continuationAppOnly(name) || name == "present_work_continuation") {
			continue
		}
		presentation := centralToolPresentation{name: name, title: strings.ReplaceAll(name, "_", " "), description: descriptions[name]}
		if name == "present_work_continuation" {
			presentation.uiURI = protocol.WorkContinuationUIResourceURI
		}
		definition := canonicalCentralToolWithApps(presentation, apps)
		meta, _ := mcpcontract.ToolMeta(name)
		definition.Meta = mcpsdk.Meta(meta)
		definitions = append(definitions, definition)
	}
	return definitions
}

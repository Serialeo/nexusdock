package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"regexp"
	"strings"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/Serialeo/agentdock-protocol/mcpcontract"
	googlejsonschema "github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/mcpresult"
	"github.com/uvwt/nexusdock/internal/privatenotes"
	projectstore "github.com/uvwt/nexusdock/internal/project"
	"github.com/uvwt/nexusdock/internal/recall"
)

func (s *Server) initializeMCPGateway() {
	// Bridge v4 Project Prompt is the only repository-instruction channel used for
	// Project execution. Legacy Nexus Global/Node Instructions are intentionally
	// not injected into the MCP server prompt.
	s.mcpServer = s.newMCPServer()
	s.registerCentralTools()
	if s.agentDockHub != nil {
		s.agentDockHub.SetHelloHandler(s.handleAgentDockHello)
	}
	if s.agentDock != nil {
		ctx := context.Background()
		if err := s.resetPersistedNodeToolCatalog(ctx); err != nil && s.logger != nil {
			s.logger.Warn("清理上一协议 generation 的 AgentDock 工具发布缓存失败", "error", err)
		}
		if nodes, err := s.agentDock.List(ctx); err == nil {
			for _, node := range nodes {
				if !nodeUsesCurrentBridgeProtocol(node) {
					continue
				}
				descriptors, descriptorErr := s.agentDock.ToolDescriptors(ctx, node.ID)
				if descriptorErr == nil {
					s.registerNodeTools(node, agentdock.Hello{Tools: descriptors})
				}
			}
		}
		// 启动时也核对一次已发布目录，清理旧版本遗留但 fleet 已不再提供的 stale tool。
		s.reconcileNodeToolContracts(s.publishedNodeToolNames())
	}
	// Nexus 自有的 Context / Recall / Workflow Apps 不依赖任何 AgentDock 节点，启动时始终注册。
	s.syncMCPAppResources()
	s.mcpHandler = mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return s.currentMCPServer() },
		&mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20, PropagateRequestCancellation: true},
	)
}

func (s *Server) newMCPServer() *mcpsdk.Server {
	return mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "nexusdock", Version: "1"},
		&mcpsdk.ServerOptions{Capabilities: &mcpsdk.ServerCapabilities{}},
	)
}

func (s *Server) registerCentralTools() {
	s.registerCentralToolsOn(s.currentMCPServer())
}

func (s *Server) registerCentralToolsOn(server *mcpsdk.Server) {
	if s == nil || server == nil {
		return
	}
	// 开关变化必须显式移除 app-only 工具，AddTool 不能退休上一份目录。
	if !s.mcpAppsEnabled() {
		for _, name := range continuationToolNames() {
			if continuationAppOnly(name) || name == "present_work_continuation" {
				server.RemoveTools(name)
			}
		}
	}
	for _, definition := range nexusToolDefinitionsWithApps(s.mcpAppsEnabled()) {
		definition := definition
		server.AddTool(definition, func(ctx context.Context, request *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			arguments, err := toolArguments(request)
			if err != nil {
				s.logInvalidToolArguments(definition.Name, request, err)
				return nil, err
			}
			if isContinuationTool(definition.Name) {
				return s.continuationToolResult(ctx, definition.Name, arguments)
			}
			if isProjectContextTool(definition.Name) {
				ackResult, ackErr := s.consumeProjectContextAck(ctx, request)
				if ackErr != nil {
					response, responseErr := s.gatewayToolResult(definition.Name, ackResult, ackErr)
					if responseErr == nil && response != nil {
						if response.Meta == nil {
							response.Meta = mcpsdk.Meta{}
						}
						for key, value := range centralToolResultMetaWithApps(definition.Name, arguments, s.mcpAppsEnabled()) {
							response.Meta[key] = value
						}
					}
					return response, responseErr
				}
			}
			result, err := s.callNexusTool(ctx, definition.Name, arguments)
			response, responseErr := s.gatewayToolResult(definition.Name, result, err)
			if responseErr == nil && response != nil && !response.IsError && isProjectContextTool(definition.Name) {
				if deliveryErr := s.recordProjectContextReturned(ctx, definition.Name, response); deliveryErr != nil {
					failureResult, failureErr := projectToolError("PROJECT_OPERATION_FAILED", "failed to persist Project Context returned delivery", map[string]any{"reason": deliveryErr.Error()})
					response, responseErr = s.gatewayToolResult(definition.Name, failureResult, failureErr)
				}
			}
			if responseErr == nil && response != nil {
				if response.Meta == nil {
					response.Meta = mcpsdk.Meta{}
				}
				for key, value := range centralToolResultMetaWithApps(definition.Name, arguments, s.mcpAppsEnabled()) {
					response.Meta[key] = value
				}
			}
			return response, responseErr
		})
	}
}

func (s *Server) currentMCPServer() *mcpsdk.Server {
	if s == nil {
		return nil
	}
	s.mcpServerMu.RLock()
	defer s.mcpServerMu.RUnlock()
	return s.mcpServer
}

func (s *Server) mcpAppsEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.MCPAppsEnabled
}

func (s *Server) setMCPAppsEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	changed := s.cfg.MCPAppsEnabled != enabled
	s.cfg.MCPAppsEnabled = enabled
	s.mu.Unlock()
	server := s.currentMCPServer()
	if !changed || server == nil {
		return
	}

	// 关闭 Apps 时下架 app-only 工具；普通工具仅移除展示绑定，不改变持久化 descriptor。
	s.registerCentralTools()
	s.mcpToolsMu.RLock()
	published := make([]publishedNodeTool, 0, len(s.mcpTools))
	for _, tool := range s.mcpTools {
		published = append(published, tool)
	}
	s.mcpToolsMu.RUnlock()
	for _, tool := range published {
		s.registerNodeMCPTool(server, tool.Descriptor, enabled)
	}
	s.syncMCPAppResources()
}

func unavailableAgentDockToolName(name string) bool {
	name = strings.TrimSpace(name)
	return name == "file_publish"
}

func (s *Server) registerNodeTools(node agentdock.Node, hello agentdock.Hello) {
	defer s.syncMCPAppResources()
	helloToolNames := make(map[string]struct{}, len(hello.Tools))
	for _, descriptor := range hello.Tools {
		if mcpcontract.IsCanonicalTool(descriptor.Name) || unavailableAgentDockToolName(descriptor.Name) || strings.TrimSpace(descriptor.Name) == "" {
			continue
		}
		helloToolNames[descriptor.Name] = struct{}{}
		if s.agentDock != nil {
			// 持久化 Hello 是完整快照；先检查全部 provider，避免短暂公开冲突的可见性。
			if err := s.reconcileFleetNodeTool(descriptor.Name); err != nil && s.logger != nil {
				s.logger.Warn("检查 AgentDock 工具契约兼容性失败", "tool", descriptor.Name, "error", err)
			}
			continue
		}
		contractHash, err := toolContractHash(descriptor)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("计算 AgentDock 工具契约失败", "node_id", node.ID, "tool", descriptor.Name, "error", err)
			}
			continue
		}

		name := descriptor.Name
		candidate := publishedNodeTool{
			Descriptor: descriptor, ContractHash: contractHash,
			AcceptedSemanticHashes: []string{contractHash},
		}
		s.mcpToolsMu.Lock()
		published, exists := s.mcpTools[name]
		if !exists {
			// 首次出现的契约先持久化再公开，确保 Nexus 重启后仍沿用同一个 schema。
			if err := s.persistPublishedNodeTool(context.Background(), candidate); err != nil {
				s.mcpToolsMu.Unlock()
				if s.logger != nil {
					s.logger.Warn("保存 AgentDock 公开工具契约失败", "node_id", node.ID, "tool", name, "error", err)
				}
				continue
			}
			s.mcpTools[name] = candidate
			if server := s.currentMCPServer(); server != nil {
				s.registerNodeMCPTool(server, descriptor, s.mcpAppsEnabled())
			}
		}
		s.mcpToolsMu.Unlock()
		if exists && (published.ContractHash != contractHash ||
			!containsToolContractHash(published.AcceptedSemanticHashes, contractHash) ||
			!jsonValuesEqual(published.Descriptor.Meta, descriptor.Meta) ||
			!jsonValuesEqual(published.Descriptor.Annotations, descriptor.Annotations)) {
			// schema 不同不等于不兼容；由 Fleet 合并器决定能否安全形成同一代公开契约。
			if err := s.reconcileFleetNodeTool(name); err != nil && s.logger != nil {
				s.logger.Warn("检查 AgentDock 工具契约兼容性失败", "tool", name, "error", err)
			}
		}
	}

	// Hello 是当前节点完整能力快照。已公开但本次不再上报的工具也要重新核对，
	// 这样最后一个 provider 真正移除能力时才会退休工具，而不是永久留下 stale schema。
	missingPublished := make([]string, 0)
	for _, name := range s.publishedNodeToolNames() {
		if _, present := helloToolNames[name]; !present {
			missingPublished = append(missingPublished, name)
		}
	}
	s.reconcileNodeToolContracts(missingPublished)
}

func nodeMCPTool(descriptor agentdock.ToolDescriptor) *mcpsdk.Tool {
	return nodeMCPToolWithApps(descriptor, true)
}

func nodeMCPToolWithApps(descriptor agentdock.ToolDescriptor, mcpAppsEnabled bool) *mcpsdk.Tool {
	visibility, err := normalizedToolVisibility(descriptor.Meta)
	if err != nil || (!mcpAppsEnabled && !containsString(visibility, "model")) {
		return nil
	}
	tool := &mcpsdk.Tool{
		Name: descriptor.Name, Title: descriptor.Title, Description: mcpresult.Description(descriptor.Name, descriptor.Description),
		InputSchema: nodeInputSchema(descriptor.InputSchema), OutputSchema: nodeOutputSchema(descriptor.Name, descriptor.OutputSchema),
	}
	if len(descriptor.Annotations) > 0 {
		encoded, _ := json.Marshal(descriptor.Annotations)
		var annotations mcpsdk.ToolAnnotations
		if json.Unmarshal(encoded, &annotations) == nil {
			tool.Annotations = &annotations
		}
	}
	if len(descriptor.Meta) > 0 {
		meta := make(mcpsdk.Meta, len(descriptor.Meta))
		for key, value := range descriptor.Meta {
			if key == "ui" && !mcpAppsEnabled {
				if len(visibility) != 2 {
					meta[key] = map[string]any{"visibility": visibility}
				}
				continue
			}
			meta[key] = value
		}
		if len(meta) > 0 {
			tool.Meta = meta
		}
	}
	return tool
}

func (s *Server) nodeToolHandler(name string) mcpsdk.ToolHandler {
	return func(ctx context.Context, request *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		arguments, err := toolArguments(request)
		if err != nil {
			s.logInvalidToolArguments(name, request, err)
			return nil, err
		}
		return s.callNodeTool(ctx, name, arguments)
	}
}

func (s *Server) logInvalidToolArguments(name string, request *mcpsdk.CallToolRequest, err error) {
	if s == nil || s.logger == nil {
		return
	}
	argumentBytes := 0
	if request != nil && request.Params != nil {
		argumentBytes = len(request.Params.Arguments)
	}
	s.logger.Warn(
		"MCP tool params invalid",
		"tool", name,
		"argument_bytes", argumentBytes,
		"error_offset", toolArgumentJSONErrorOffset(err),
		"error", err,
	)
}

func (s *Server) callNodeTool(ctx context.Context, name string, arguments map[string]any) (*mcpsdk.CallToolResult, error) {
	if published, exists := s.publishedNodeTool(name); exists {
		visibility, err := normalizedToolVisibility(published.Descriptor.Meta)
		if err != nil || (!s.mcpAppsEnabled() && !containsString(visibility, "model")) {
			return s.gatewayToolResult(name, map[string]any{"code": "TOOL_NOT_AVAILABLE"}, errors.New("tool visibility is unavailable with current MCP Apps settings"))
		}
	}
	if unavailableAgentDockToolName(name) {
		return s.gatewayToolResult(name, map[string]any{"code": "UNKNOWN_TOOL"}, errors.New("tool is unavailable in this release"))
	}
	binding, ok := mcpClientBindingFromContext(ctx)
	if !ok {
		return s.gatewayToolResult(name, map[string]any{"code": "MCP_CLIENT_BINDING_REQUIRED"}, errors.New("authenticated MCP client binding is required"))
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	for _, forbidden := range []string{"node_id", "working_folder", "permissions"} {
		if _, exists := arguments[forbidden]; exists {
			return s.gatewayToolResult(name, map[string]any{"code": protocol.ErrorSessionTargetDenied, "field": forbidden}, fmt.Errorf("routing override %s is not allowed", forbidden))
		}
	}
	workSessionID := strings.TrimSpace(stringArgument(arguments, "work_session_id"))
	targetID := strings.TrimSpace(stringArgument(arguments, "target_id"))
	if workSessionID == "" || targetID == "" {
		return s.gatewayToolResult(name, map[string]any{"code": protocol.ErrorExecutionContextRequired}, errors.New("work_session_id and target_id are required"))
	}
	if s.projects == nil {
		return s.gatewayToolResult(name, map[string]any{"code": "PROJECT_STORE_UNAVAILABLE"}, errors.New("Project store is unavailable"))
	}
	session, err := s.projects.GetWorkSession(ctx, binding.OwnerKey, workSessionID)
	if err != nil {
		return s.gatewayToolResult(name, map[string]any{"code": protocol.ErrorSessionTargetDenied}, err)
	}
	target, err := s.projects.GetWorkTarget(ctx, binding.OwnerKey, workSessionID, targetID)
	if err != nil || target.Target.ProjectID != session.ProjectID {
		if err == nil {
			err = projectstore.ErrWorkTargetNotFound
		}
		return s.gatewayToolResult(name, map[string]any{"code": protocol.ErrorSessionTargetDenied}, err)
	}
	historicalControl := allowsHistoricalTargetControl(name, arguments)
	if !historicalControl {
		if target.Target.Status != protocol.TargetReady && target.Target.Status != protocol.TargetIdle && target.Target.Status != protocol.TargetRunning {
			return s.gatewayToolResult(name, map[string]any{"code": protocol.ErrorSessionTargetDenied, "target_id": targetID, "status": target.Target.Status}, errors.New("Project Target is not ready for execution"))
		}
		project, projectErr := s.projects.GetProject(ctx, session.ProjectID)
		if projectErr != nil || !project.Enabled {
			if projectErr == nil {
				projectErr = projectstore.ErrProjectNotFound
			}
			return s.gatewayToolResult(name, map[string]any{"code": protocol.ErrorContextRefreshRequired, "target_id": targetID}, projectErr)
		}
		deployment, deploymentErr := s.projects.GetDeployment(ctx, project.ID, target.Target.DeploymentID)
		if deploymentErr != nil || !deployment.Enabled || deployment.ApplyStatus != string(protocol.DeploymentApplyApplied) || deployment.DesiredRevision != deployment.AppliedRevision || deployment.AppliedRevision != target.Target.DeploymentRevision {
			if deploymentErr == nil {
				deploymentErr = errors.New("Project Deployment revision is no longer current")
			}
			return s.gatewayToolResult(name, map[string]any{"code": protocol.ErrorRevisionConflict, "target_id": targetID}, deploymentErr)
		}
	}

	nodeID := target.Target.NodeID
	node, err := s.agentDock.Get(ctx, nodeID)
	if err != nil {
		return s.gatewayToolResult(name, nil, err)
	}
	if !historicalControl && target.Target.Permissions.FullAccess != node.FullAccess {
		return s.gatewayToolResult(name, map[string]any{
			"code": protocol.ErrorContextRefreshRequired, "target_id": targetID, "node_id": nodeID,
		}, errors.New("Node Full Access changed; reopen or refresh the Project Target before executing"))
	}
	if !nodeUsesCurrentBridgeProtocol(node) {
		details := map[string]any{"code": "BRIDGE_PROTOCOL_MISMATCH", "node_id": nodeID, "required_protocol": agentdock.ConnectionProtocolVersion}
		return s.gatewayToolResult(name, details, errors.New("target AgentDock has not completed the current Bridge v4 handshake"))
	}
	if !containsString(node.Capabilities, name) {
		return s.gatewayToolResult(name, nil, fmt.Errorf("AgentDock node %s does not provide tool %s", nodeID, name))
	}

	mismatch, err := s.nodeToolContractMismatch(ctx, node, name)
	if err != nil {
		return s.gatewayToolResult(name, nil, err)
	}
	if mismatch != nil {
		details, encodeErr := asMap(mismatch)
		if encodeErr != nil {
			return nil, encodeErr
		}
		return s.gatewayToolResult(name, details, errors.New(mismatch.Message))
	}

	targetArguments, err := s.validateTargetNodeToolArguments(ctx, nodeID, name, arguments)
	if err != nil {
		if errors.Is(err, errTargetToolArgumentsInvalid) {
			details := map[string]any{"code": "TARGET_TOOL_ARGUMENT_INVALID", "target_id": targetID, "tool": name}
			return s.gatewayToolResult(name, details, errTargetToolArgumentsInvalid)
		}
		return s.gatewayToolResult(name, nil, err)
	}
	executionContext := &protocol.ExecutionContext{
		WorkSessionID:      target.Target.WorkSessionID,
		TargetID:           target.Target.ID,
		ProjectID:          target.Target.ProjectID,
		DeploymentID:       target.Target.DeploymentID,
		DeploymentRevision: target.Target.DeploymentRevision,
		ContextRevision:    target.Target.ContextRevision,
	}
	result, err := s.agentDockHub.InvokeWithExecutionContext(ctx, nodeID, protocol.OperationToolCall, executionContext, protocol.ToolCallRequest{Tool: name, Arguments: targetArguments})
	if err == nil {
		bridgeCapabilities, capabilityErr := s.agentDock.BridgeCapabilities(ctx, nodeID)
		if capabilityErr != nil {
			if s.logger != nil {
				s.logger.Warn("读取 AgentDock Bridge 能力失败，保留原始工具结果", "node_id", nodeID, "error", capabilityErr)
			}
		} else if containsString(bridgeCapabilities, protocol.ArtifactReadCapability) {
			if decorateErr := s.decorateArtifactToolResult(nodeID, result); decorateErr != nil && s.logger != nil {
				s.logger.Warn("生成 Nexus Artifact 下载地址失败，保留原始工具结果", "node_id", nodeID, "error", decorateErr)
			}
		}
	}
	return s.gatewayToolResult(name, result, err)
}

func allowsHistoricalTargetControl(name string, arguments map[string]any) bool {
	action := strings.ToLower(strings.TrimSpace(stringArgument(arguments, "action")))
	switch name {
	case "session_observe":
		return action == "" || action == "list" || action == "status"
	case "session_act":
		return action == "kill" || action == "kill_all"
	case "acp_session":
		return action == "list" || action == "inspect" || action == "close" || action == "delete"
	case "acp_prompt":
		return action == "events" || action == "cancel"
	case "acp_interaction":
		return action == "list" || action == "inspect" || action == "cancel"
	default:
		return false
	}
}

var errTargetToolArgumentsInvalid = errors.New("tool arguments do not match the target AgentDock input schema")

func (s *Server) validateTargetNodeToolArguments(ctx context.Context, nodeID, name string, arguments map[string]any) (map[string]any, error) {
	descriptors, err := s.agentDock.ToolDescriptors(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read target AgentDock tool contract: %w", err)
	}
	descriptor, ok := findToolDescriptor(descriptors, name)
	if !ok {
		return nil, fmt.Errorf("target AgentDock tool contract is missing: %s", name)
	}
	encodedSchema, err := json.Marshal(descriptor.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("encode target AgentDock input schema: %w", err)
	}
	var schema googlejsonschema.Schema
	if err := json.Unmarshal(encodedSchema, &schema); err != nil {
		return nil, fmt.Errorf("decode target AgentDock input schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve target AgentDock input schema: %w", err)
	}
	targetArguments := make(map[string]any, len(arguments))
	for key, value := range arguments {
		if key != "work_session_id" && key != "target_id" && key != "node_id" && key != "working_folder" && key != "permissions" {
			targetArguments[key] = value
		}
	}
	encodedArguments, err := json.Marshal(targetArguments)
	if err != nil {
		return nil, fmt.Errorf("encode target AgentDock tool arguments: %w", err)
	}
	var normalized any
	if err := json.Unmarshal(encodedArguments, &normalized); err != nil {
		return nil, fmt.Errorf("decode target AgentDock tool arguments: %w", err)
	}
	if err := resolved.Validate(normalized); err != nil {
		return nil, errTargetToolArgumentsInvalid
	}
	return targetArguments, nil
}

func nodeUsesCurrentBridgeProtocol(node agentdock.Node) bool {
	return strings.TrimSpace(node.ProtocolVersion) == agentdock.ConnectionProtocolVersion
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func truncateRunes(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum]) + "…"
}

func nodeInputSchema(schema map[string]any) map[string]any {
	encoded, _ := json.Marshal(schema)
	cloned := map[string]any{"type": "object"}
	_ = json.Unmarshal(encoded, &cloned)
	properties, _ := cloned["properties"].(map[string]any)
	if properties == nil {
		properties = make(map[string]any)
		cloned["properties"] = properties
	}
	delete(properties, "node_id")
	delete(properties, "working_folder")
	delete(properties, "permissions")
	properties["work_session_id"] = map[string]any{"type": "string", "description": "Bound WorkSession id returned by project_open or node_open."}
	properties["target_id"] = map[string]any{"type": "string", "description": "Bound Target id returned by project_open, node_open, or project_context."}
	required, _ := cloned["required"].([]any)
	filtered := make([]any, 0, len(required)+2)
	for _, value := range required {
		if value != "node_id" && value != "working_folder" && value != "permissions" && value != "work_session_id" && value != "target_id" {
			filtered = append(filtered, value)
		}
	}
	cloned["required"] = append(filtered, "work_session_id", "target_id")
	return cloned
}

func nodeOutputSchema(name string, schema map[string]any) map[string]any {
	return mcpresult.Schema(name, schema)
}

func toolArguments(request *mcpsdk.CallToolRequest) (map[string]any, error) {
	arguments := map[string]any{}
	if request == nil || request.Params == nil || len(request.Params.Arguments) == 0 || string(request.Params.Arguments) == "null" {
		return arguments, nil
	}
	if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
		offset := toolArgumentJSONErrorOffset(err)
		if offset > 0 {
			return nil, fmt.Errorf("tool arguments must be a JSON object (%d bytes, offset %d): %w", len(request.Params.Arguments), offset, err)
		}
		return nil, fmt.Errorf("tool arguments must be a JSON object (%d bytes): %w", len(request.Params.Arguments), err)
	}
	return arguments, nil
}

func toolArgumentJSONErrorOffset(err error) int64 {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return syntaxErr.Offset
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return typeErr.Offset
	}
	return 0
}

func gatewayToolResult(name string, result map[string]any, err error) (*mcpsdk.CallToolResult, error) {
	if err != nil {
		copied := make(map[string]any, len(result)+2)
		for key, value := range result {
			copied[key] = value
		}
		copied["tool"] = name
		copied["error"] = err.Error()
		return mcpresult.Build(name, copied, true)
	}
	return mcpresult.Build(name, result, false)
}

func (s *Server) gatewayToolResult(name string, result map[string]any, err error) (*mcpsdk.CallToolResult, error) {
	response, responseErr := gatewayToolResult(name, result, err)
	if responseErr != nil || response == nil || s.mcpAppsEnabled() || response.Meta == nil {
		return response, responseErr
	}
	delete(response.Meta, "ui")
	if len(response.Meta) == 0 {
		response.Meta = nil
	}
	return response, nil
}

func (s *Server) decorateRecallSearchResults(results []recall.SearchResult) ([]map[string]any, error) {
	decorated := make([]map[string]any, 0, len(results))
	if len(results) == 0 {
		return decorated, nil
	}
	baseURL, err := url.Parse(strings.TrimSpace(s.cfg.PublicURL))
	if err != nil || baseURL == nil || !baseURL.IsAbs() || baseURL.Host == "" {
		return nil, errors.New("NEXUS_PUBLIC_URL is required to generate recall_search citation URLs")
	}
	for _, result := range results {
		item, err := asMap(result)
		if err != nil {
			return nil, err
		}
		path := strings.TrimSpace(result.Path)
		if path == "" {
			continue
		}
		item["id"] = path
		if strings.TrimSpace(result.Title) == "" {
			name := pathpkg.Base(path)
			item["title"] = strings.TrimSuffix(name, pathpkg.Ext(name))
		}
		sourceURL := *baseURL
		query := sourceURL.Query()
		query.Set("path", path)
		sourceURL.RawQuery = query.Encode()
		sourceURL.Fragment = "recall/library"
		item["url"] = sourceURL.String()
		decorated = append(decorated, item)
	}
	return decorated, nil
}

func (s *Server) callNexusTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	if isContinuationTool(name) {
		return s.callWorkContinuation(ctx, name, args)
	}
	switch name {
	case "agentdock_context":
		return s.callFleetAgentDockContext(ctx)
	case mcpcontract.ToolProjectList:
		if len(args) != 0 {
			return projectToolError("INVALID_PROJECT", "project_list does not accept arguments", nil)
		}
		return s.callProjectList(ctx)
	case mcpcontract.ToolProjectOpen:
		return s.callProjectOpen(ctx, args)
	case mcpcontract.ToolNodeOpen:
		return s.callNodeOpen(ctx, args)
	case mcpcontract.ToolProjectContext:
		return s.callProjectContext(ctx, args)
	case "workflow_template_manage":
		return s.callWorkflowTemplateManage(ctx, args)
	case "recall_search":
		query := stringArgument(args, "query")
		if query == "" {
			return nil, errors.New("query is required")
		}
		kind := strings.ToLower(stringArgumentDefault(args, "kind", "all"))
		options := recall.SearchOptions{Query: query, MaxResults: intArgument(args, "max_results", 20)}
		switch kind {
		case "card":
			options.Prefix = "recall/managed/cards"
		case "markdown":
			options.ExcludePrefix = "recall/managed/cards"
		case "all":
		default:
			return nil, fmt.Errorf("unsupported recall_search kind: %s", kind)
		}
		results, err := s.searchRecall(ctx, options)
		if err != nil {
			return nil, err
		}
		decorated, err := s.decorateRecallSearchResults(results)
		if err != nil {
			return nil, err
		}
		return asMap(map[string]any{"query": query, "recall_kind": kind, "results": decorated, "count": len(decorated), "recall_store": "NexusDock Recall", "recall_endpoint": s.cfg.PublicURL})
	case "recall_read":
		path := stringArgument(args, "path")
		if strings.HasPrefix(path, "private-notes/") {
			return nil, errors.New("private notes must be read through private_note_manage")
		}
		memory, err := s.store.Read(path)
		if err != nil {
			return nil, err
		}
		item, err := asMap(memory)
		if err != nil {
			return nil, err
		}
		content, _ := item["content"].(string)
		delete(item, "content")
		if boolArgument(args, "include_raw") {
			item["raw_content"] = content
		}
		return asMap(map[string]any{"recall": item, "recall_store": "NexusDock Recall", "recall_endpoint": s.cfg.PublicURL})
	case "recall_write":
		return s.callRecallWrite(ctx, args)
	case "recall_maintain":
		return s.callRecallMaintain(ctx, args)
	case "private_note_manage":
		return s.callPrivateNote(ctx, args)
	default:
		return nil, fmt.Errorf("unknown NexusDock tool: %s", name)
	}
}

func centralToolResultMeta(name string, args map[string]any) mcpsdk.Meta {
	return centralToolResultMetaWithApps(name, args, true)
}

func centralToolResultMetaWithApps(name string, args map[string]any, mcpAppsEnabled bool) mcpsdk.Meta {
	if mcpAppsEnabled && name == "workflow_template_manage" && strings.EqualFold(stringArgument(args, "action"), "match") {
		return centralToolUIResourceMeta(protocol.WorkflowUIResourceURI)
	}
	return nil
}

func (s *Server) callRecallWrite(ctx context.Context, args map[string]any) (map[string]any, error) {
	target, action := strings.ToLower(stringArgument(args, "target")), strings.ToLower(stringArgument(args, "action"))
	result, err := s.callRecallWriteOperation(ctx, args, target, action)
	if result != nil {
		delete(result, "ok")
		result["recall_target"] = target
		result["recall_action"] = action
		result["recall_endpoint"] = s.cfg.PublicURL
	}
	return result, err
}

func (s *Server) callRecallWriteOperation(ctx context.Context, args map[string]any, target, action string) (map[string]any, error) {
	dryRun := boolArgument(args, "dry_run")
	confirmed := boolArgument(args, "confirmed")
	if target == "card" {
		previewOnly := dryRun || !confirmed
		var request recall.CardRequest
		if err := decodeMap(args, &request); err != nil {
			return nil, err
		}
		if action == "plan" || (action == "create" && previewOnly) {
			result, err := s.store.CaptureCard(request)
			mapped, mapErr := asMap(result, err)
			if mapErr != nil {
				return nil, mapErr
			}
			mapped["dry_run"] = true
			return mapped, nil
		}
		if action != "create" {
			return nil, errors.New("card only supports plan and create")
		}
		result, err := s.store.WriteCard(request)
		if err == nil {
			s.versions.MarkChanged(ctx)
		}
		return asMap(result, err)
	}
	if target != "markdown" {
		return nil, errors.New("target must be card or markdown")
	}
	path := stringArgument(args, "path")
	if action == "delete" {
		if dryRun {
			current, err := s.store.Read(path)
			if err != nil {
				return nil, err
			}
			return asMap(map[string]any{"path": path, "dry_run": true, "would_delete": true, "size_bytes": current.SizeBytes})
		}
		if !confirmed {
			return nil, recall.ErrConfirmationNeeded
		}
		err := s.store.Delete(path, true)
		if err == nil {
			s.versions.MarkChanged(ctx)
		}
		return asMap(map[string]any{"path": path, "deleted": err == nil}, err)
	}
	if action == "update_fact" {
		return s.updateRecallFacts(ctx, path, args)
	}
	var request recall.WriteRequest
	if err := decodeMap(args, &request); err != nil {
		return nil, err
	}
	var beforeEdit string
	hasBeforeEdit := false
	switch action {
	case "plan", "create":
		request.Overwrite = false
	case "replace":
		request.Overwrite = true
	case "append", "patch":
		current, err := s.store.Read(path)
		if err != nil {
			return nil, err
		}
		appendText := stringArgument(args, "append")
		if action == "append" && strings.TrimSpace(appendText) == "" {
			appendText = stringArgument(args, "content")
		}
		if action == "append" && strings.TrimSpace(appendText) == "" {
			return nil, errors.New("append or content is required")
		}
		old, replacement := "", ""
		section, sectionContent := "", ""
		if action == "patch" {
			old, replacement = stringArgument(args, "old"), stringArgument(args, "new")
			section, sectionContent = stringArgument(args, "section"), stringArgument(args, "section_content")
			if strings.TrimSpace(section) != "" && sectionContent == "" {
				sectionContent = stringArgument(args, "content")
			}
		}
		content, _, err := recall.ApplyMarkdownPatch(current.Content, old, replacement, section, sectionContent, appendText)
		if err != nil {
			return nil, err
		}
		beforeEdit, hasBeforeEdit = current.Content, true
		request.Content, request.Overwrite = content, true
	case "diff":
		current, err := s.store.Read(path)
		if err != nil {
			return nil, err
		}
		proposed := stringArgument(args, "content")
		changeCount := 0
		if proposed == "" {
			proposed, changeCount, err = recall.ApplyMarkdownPatch(
				current.Content,
				stringArgument(args, "old"), stringArgument(args, "new"),
				stringArgument(args, "section"), stringArgument(args, "section_content"), stringArgument(args, "append"),
			)
			if err != nil {
				return nil, err
			}
		}
		maxBytes := intArgument(args, "max_bytes", 60000)
		if maxBytes <= 0 {
			maxBytes = 60000
		}
		diff := recall.UnifiedDiff(path, current.Content, proposed, maxBytes)
		return asMap(map[string]any{
			"path": path, "changed": current.Content != proposed, "diff": diff,
			"truncated": len(diff) >= maxBytes, "change_count": changeCount,
		})
	default:
		return nil, fmt.Errorf("unsupported markdown action: %s", action)
	}
	previewOnly := action == "plan" || dryRun
	if action == "replace" || action == "append" || action == "patch" {
		previewOnly = previewOnly || !confirmed
	}
	if previewOnly {
		preview, err := s.store.PreviewWrite(request)
		if err != nil {
			return nil, err
		}
		result := map[string]any{
			"dry_run": true, "confirmed": confirmed, "path": preview.Path,
			"proposed_content": preview.ProposedContent, "overwrite": preview.Overwrite,
		}
		if hasBeforeEdit {
			maxBytes := intArgument(args, "max_bytes", 60000)
			if maxBytes <= 0 {
				maxBytes = 60000
			}
			diff := recall.UnifiedDiff(path, beforeEdit, preview.ProposedContent, maxBytes)
			result["changed"] = beforeEdit != preview.ProposedContent
			result["diff"] = diff
			result["truncated"] = len(diff) >= maxBytes
		}
		return asMap(result)
	}
	result, err := s.store.Write(request)
	if err == nil {
		s.versions.MarkChanged(ctx)
	}
	return asMap(map[string]any{"recall": result, "recall_store": "NexusDock Recall"}, err)
}

func (s *Server) callRecallMaintain(ctx context.Context, args map[string]any) (map[string]any, error) {
	action := strings.ToLower(stringArgumentDefault(args, "action", "list"))
	result, err := s.callRecallMaintainOperation(ctx, args, action)
	if result != nil {
		delete(result, "ok")
		result["recall_action"] = action
		result["recall_endpoint"] = s.cfg.PublicURL
	}
	return result, err
}

func (s *Server) callRecallMaintainOperation(ctx context.Context, args map[string]any, action string) (map[string]any, error) {
	switch action {
	case "list":
		entries, err := s.store.List(stringArgument(args, "prefix"), intArgument(args, "max_entries", 200))
		return asMap(map[string]any{"entries": entries, "count": len(entries)}, err)
	case "lint":
		return s.lintRecall(args)
	case "embedding_status":
		if s.currentEmbedding() == nil {
			return asMap(map[string]any{"enabled": false})
		}
		return asMap(s.currentEmbedding().Status(ctx))
	case "reindex", "reindex_cards":
		if s.currentEmbedding() == nil {
			return nil, errors.New("embedding service is not configured")
		}
		prefix := stringArgument(args, "prefix")
		if action == "reindex_cards" && prefix == "" {
			prefix = "recall/managed/cards"
		}
		result, err := s.currentEmbedding().Reindex(ctx, recall.EmbeddingReindexRequest{Prefix: prefix})
		return asMap(result, err)
	default:
		return nil, fmt.Errorf("unsupported recall maintenance action: %s", action)
	}
}

func (s *Server) updateRecallFacts(ctx context.Context, path string, args map[string]any) (map[string]any, error) {
	if path == "" {
		return nil, errors.New("path is required")
	}
	facts := make(map[string]string)
	if key := strings.TrimSpace(stringArgument(args, "key")); key != "" {
		value, exists := args["value"]
		if !exists || value == nil {
			return nil, errors.New("value is required when key is provided")
		}
		facts[key] = fmt.Sprint(value)
	}
	if values, ok := args["facts"].(map[string]any); ok {
		for key, value := range values {
			if key = strings.TrimSpace(key); key != "" {
				facts[key] = fmt.Sprint(value)
			}
		}
	}
	current, err := s.store.Read(path)
	if err != nil {
		return nil, err
	}
	updated, updates, err := recall.UpdateMarkdownFacts(
		current.Content, stringArgument(args, "section"), facts, boolArgument(args, "append_if_missing"),
	)
	if err != nil {
		return nil, err
	}
	maxBytes := intArgument(args, "max_bytes", 60000)
	if maxBytes <= 0 {
		maxBytes = 60000
	}
	diff := recall.UnifiedDiff(path, current.Content, updated, maxBytes)
	changed := updated != current.Content
	preview := map[string]any{
		"path": path, "changed": changed, "confirmed": boolArgument(args, "confirmed"),
		"updates": updates, "diff": diff, "truncated": len(diff) >= maxBytes,
	}
	if boolArgument(args, "dry_run") || !boolArgument(args, "confirmed") || !changed {
		preview["dry_run"] = true
		return asMap(preview)
	}
	result, err := s.store.Write(recall.WriteRequest{Path: path, Content: updated, Confirmed: true, Overwrite: true})
	if err == nil {
		s.versions.MarkChanged(ctx)
	}
	return asMap(map[string]any{
		"path": path, "changed": true, "confirmed": true, "written": err == nil,
		"updates": updates, "diff": diff, "truncated": len(diff) >= maxBytes, "recall": result,
	}, err)
}

func (s *Server) lintRecall(args map[string]any) (map[string]any, error) {
	terms := stringSliceArgument(args, "terms")
	if len(terms) == 0 {
		terms = []string{"Connector", "connector", "CONNECTOR", "connectors", "connector_"}
	}
	entries, err := s.store.List(stringArgument(args, "prefix"), intArgument(args, "max_entries", 200))
	if err != nil {
		return nil, err
	}
	maximum := intArgument(args, "max_findings", 200)
	findings := make([]map[string]any, 0)
	filesScanned := 0
	for _, entry := range entries {
		if len(findings) >= maximum || !strings.HasSuffix(strings.ToLower(entry.Path), ".md") {
			continue
		}
		memory, readErr := s.store.Read(entry.Path)
		if readErr != nil {
			continue
		}
		filesScanned++
		for lineIndex, line := range strings.Split(memory.Content, "\n") {
			for _, term := range terms {
				matched := strings.Contains(line, term)
				if boolArgument(args, "regex") {
					expression, compileErr := regexp.Compile(term)
					if compileErr != nil {
						return nil, fmt.Errorf("invalid lint regular expression %q: %w", term, compileErr)
					}
					matched = expression.MatchString(line)
				}
				if matched {
					findings = append(findings, map[string]any{"path": entry.Path, "line": lineIndex + 1, "term": term, "text": line})
				}
				if len(findings) >= maximum {
					break
				}
			}
		}
	}
	return asMap(map[string]any{"terms": terms, "regex": boolArgument(args, "regex"), "files_scanned": filesScanned, "finding_count": len(findings), "findings": findings, "truncated": len(findings) >= maximum})
}

func (s *Server) callPrivateNote(ctx context.Context, args map[string]any) (map[string]any, error) {
	if s.privateNotes == nil {
		return nil, errors.New("private notes are not configured")
	}
	action := strings.ToLower(stringArgument(args, "action"))
	result, err := s.callPrivateNoteOperation(ctx, args, action)
	if result != nil {
		delete(result, "ok")
		result["action"] = action
		result["private_note_store"] = "NexusDock Private Notes"
		result["recall_endpoint"] = s.cfg.PublicURL
	}
	return result, err
}

func (s *Server) callPrivateNoteOperation(ctx context.Context, args map[string]any, action string) (map[string]any, error) {
	switch action {
	case "search":
		query := stringArgument(args, "query")
		results, err := s.privateNotes.Search(ctx, query, intArgument(args, "max_results", 8))
		return asMap(map[string]any{
			"action": action, "query": query, "root": s.privateNotes.Root(),
			"results": results, "count": len(results), "metadata_only": true,
		}, err)
	case "read":
		result, err := s.privateNotes.Read(stringArgument(args, "path"), intArgument(args, "max_bytes", 256000))
		return asMap(result, err)
	case "write":
		var request privatenotes.WriteRequest
		if err := decodeMap(args, &request); err != nil {
			return nil, err
		}
		result, err := s.privateNotes.Write(request)
		return asMap(result, err)
	case "delete":
		result, err := s.privateNotes.Delete(stringArgument(args, "path"), boolArgument(args, "confirmed"))
		return asMap(result, err)
	case "status":
		result, err := s.privateNotes.Status(ctx, stringArgumentDefault(args, "status_action", "check"))
		return asMap(result, err)
	case "maintain":
		result, err := s.privateNotes.Maintain(ctx, stringArgumentDefault(args, "maintenance_action", "sync-encrypted"))
		return asMap(result, err)
	default:
		return nil, fmt.Errorf("unsupported private note action: %s", action)
	}
}

func asMap(value any, optionalErr ...error) (map[string]any, error) {
	if len(optionalErr) > 0 && optionalErr[0] != nil {
		return nil, optionalErr[0]
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeMap(value map[string]any, destination any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, destination)
}

func stringArgument(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func stringArgumentDefault(args map[string]any, key, fallback string) string {
	if value := stringArgument(args, key); value != "" {
		return value
	}
	return fallback
}

func intArgument(args map[string]any, key string, fallback int) int {
	switch value := args[key].(type) {
	case float64:
		if value > 0 {
			return int(value)
		}
	case int:
		if value > 0 {
			return value
		}
	}
	return fallback
}

func boolArgument(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func stringSliceArgument(args map[string]any, key string) []string {
	var values []any
	switch typed := args[key].(type) {
	case []any:
		values = typed
	case []string:
		values = make([]any, len(typed))
		for index := range typed {
			values[index] = typed[index]
		}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
			result = append(result, text)
		}
	}
	return result
}

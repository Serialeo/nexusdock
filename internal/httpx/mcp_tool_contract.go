package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/Serialeo/agentdock-protocol/mcpcontract"
	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/mcpresult"
)

var errIncompatibleToolContract = errors.New("AgentDock 工具契约不可安全合并")

var errUnsafeToolVisibility = errors.New("AgentDock 工具 visibility 无效或不一致")

type publishedNodeTool struct {
	Descriptor             agentdock.ToolDescriptor
	ContractHash           string
	AcceptedSemanticHashes []string
}

type comparableToolContract struct {
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Meta         map[string]any `json:"_meta,omitempty"`
}

func isNexusCentralTool(name string) bool {
	return mcpcontract.IsCanonicalTool(name)
}

func toolContractHash(descriptor agentdock.ToolDescriptor) (string, error) {
	contract, err := comparableContract(descriptor)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(contract)
	if err != nil {
		return "", fmt.Errorf("编码工具 %s 契约: %w", descriptor.Name, err)
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", sum), nil
}

func comparableContract(descriptor agentdock.ToolDescriptor) (comparableToolContract, error) {
	inputSchema, err := semanticSchemaMap(descriptor.InputSchema)
	if err != nil {
		return comparableToolContract{}, fmt.Errorf("规范化工具 %s 输入契约: %w", descriptor.Name, err)
	}
	outputSchema, err := semanticSchemaMap(descriptor.OutputSchema)
	if err != nil {
		return comparableToolContract{}, fmt.Errorf("规范化工具 %s 输出契约: %w", descriptor.Name, err)
	}
	meta, err := executionToolMeta(descriptor.Meta)
	if err != nil {
		return comparableToolContract{}, fmt.Errorf("规范化工具 %s 可见性: %w", descriptor.Name, err)
	}
	return comparableToolContract{InputSchema: inputSchema, OutputSchema: outputSchema, Meta: meta}, nil
}

func executionToolMeta(meta map[string]any) (map[string]any, error) {
	visibility, err := normalizedToolVisibility(meta)
	if err != nil {
		return nil, err
	}
	execution := make(map[string]any, len(meta)+1)
	for key, value := range meta {
		if key != "ui" {
			execution[key] = value
		}
	}
	// resourceUri 仅决定展示；visibility 决定可调用面，必须纳入执行契约。
	execution["ui"] = map[string]any{"visibility": visibility}
	return execution, nil
}

// semanticSchemaMap 只去掉不影响校验结果的展示字段，并规范 JSON Schema 中本身无顺序语义的集合。
// 其他关键字保持原样，避免把真实的版本或实现差异误判成兼容。
func semanticSchemaMap(schema map[string]any) (map[string]any, error) {
	if schema == nil {
		return nil, nil
	}
	normalized, err := normalizeJSONValue(schema)
	if err != nil {
		return nil, err
	}
	value, ok := normalized.(map[string]any)
	if !ok {
		return nil, errors.New("schema 不是 JSON object")
	}
	return semanticSchemaObject(value), nil
}

func normalizeJSONValue(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func semanticSchemaObject(schema map[string]any) map[string]any {
	result := make(map[string]any, len(schema))
	for key, value := range schema {
		if key == "description" || key == "title" {
			continue
		}
		normalized := semanticSchemaKeywordValue(key, value)
		if key == "required" {
			if items, ok := normalized.([]any); ok && len(items) == 0 {
				continue
			}
		}
		result[key] = normalized
	}
	return result
}

func semanticSchemaKeywordValue(key string, value any) any {
	switch key {
	case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
		mapping, ok := value.(map[string]any)
		if !ok {
			return value
		}
		result := make(map[string]any, len(mapping))
		for name, child := range mapping {
			if schema, ok := child.(map[string]any); ok {
				result[name] = semanticSchemaObject(schema)
			} else {
				result[name] = child
			}
		}
		return result
	case "items", "contains", "not", "if", "then", "else", "propertyNames", "additionalProperties", "unevaluatedProperties":
		if schema, ok := value.(map[string]any); ok {
			return semanticSchemaObject(schema)
		}
		return value
	case "allOf", "anyOf", "oneOf", "prefixItems":
		items, ok := value.([]any)
		if !ok {
			return value
		}
		result := make([]any, len(items))
		for index, child := range items {
			if schema, ok := child.(map[string]any); ok {
				result[index] = semanticSchemaObject(schema)
			} else {
				result[index] = child
			}
		}
		return result
	case "required", "enum":
		items, ok := value.([]any)
		if !ok {
			return value
		}
		result := append([]any(nil), items...)
		sort.Slice(result, func(i, j int) bool {
			left, _ := json.Marshal(result[i])
			right, _ := json.Marshal(result[j])
			return bytes.Compare(left, right) < 0
		})
		return result
	default:
		return value
	}
}

func (s *Server) publishedNodeTool(name string) (publishedNodeTool, bool) {
	s.mcpToolsMu.RLock()
	defer s.mcpToolsMu.RUnlock()
	tool, ok := s.mcpTools[name]
	return tool, ok
}

func (s *Server) publishedNodeToolNames() []string {
	s.mcpToolsMu.RLock()
	defer s.mcpToolsMu.RUnlock()
	names := make([]string, 0, len(s.mcpTools))
	for name := range s.mcpTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *Server) resetPersistedNodeToolCatalog(ctx context.Context) error {
	s.mcpNodeToolsMu.Lock()
	s.mcpNodeTools = make(map[string]map[string]agentdock.ToolDescriptor)
	s.mcpNodeToolsMu.Unlock()
	s.mcpToolsMu.Lock()
	staleNames := make([]string, 0, len(s.mcpTools))
	for name := range s.mcpTools {
		staleNames = append(staleNames, name)
	}
	s.mcpTools = make(map[string]publishedNodeTool)
	s.mcpToolsMu.Unlock()
	if server := s.currentMCPServer(); server != nil {
		server.RemoveTools(staleNames...)
	}

	contracts, err := s.agentDock.ListPublishedToolContracts(ctx)
	if err != nil {
		return err
	}
	for _, contract := range contracts {
		name := strings.TrimSpace(contract.ToolName)
		if name == "" {
			continue
		}
		if err := s.agentDock.DeletePublishedToolContract(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// replaceLiveNodeToolSnapshot records one connected node's complete Hello tool
// snapshot. Persisted Hello descriptors are deliberately not used here: they
// describe the last connection, not the providers available in this process.
func (s *Server) replaceLiveNodeToolSnapshot(nodeID string, descriptors []agentdock.ToolDescriptor) []string {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil
	}
	next := make(map[string]agentdock.ToolDescriptor)
	for _, descriptor := range descriptors {
		name := strings.TrimSpace(descriptor.Name)
		if name == "" || mcpcontract.IsCanonicalTool(name) || unavailableAgentDockToolName(name) {
			continue
		}
		next[name] = descriptor
	}
	s.mcpNodeToolsMu.Lock()
	if s.mcpNodeTools == nil {
		s.mcpNodeTools = make(map[string]map[string]agentdock.ToolDescriptor)
	}
	previous := s.mcpNodeTools[nodeID]
	s.mcpNodeTools[nodeID] = next
	s.mcpNodeToolsMu.Unlock()
	names := make([]string, 0, len(previous)+len(next))
	for name := range previous {
		names = append(names, name)
	}
	for name := range next {
		names = append(names, name)
	}
	return names
}

func (s *Server) removeLiveNodeToolSnapshot(nodeID string) []string {
	s.mcpNodeToolsMu.Lock()
	previous := s.mcpNodeTools[strings.TrimSpace(nodeID)]
	delete(s.mcpNodeTools, strings.TrimSpace(nodeID))
	s.mcpNodeToolsMu.Unlock()
	names := make([]string, 0, len(previous))
	for name := range previous {
		names = append(names, name)
	}
	return names
}

func (s *Server) liveNodeToolDescriptors(name string) []agentdock.ToolDescriptor {
	s.mcpNodeToolsMu.RLock()
	defer s.mcpNodeToolsMu.RUnlock()
	descriptors := make([]agentdock.ToolDescriptor, 0)
	nodeIDs := make([]string, 0, len(s.mcpNodeTools))
	for nodeID := range s.mcpNodeTools {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	for _, nodeID := range nodeIDs {
		if descriptor, ok := s.mcpNodeTools[nodeID][name]; ok {
			descriptors = append(descriptors, descriptor)
		}
	}
	return descriptors
}

func (s *Server) handleAgentDockDisconnect(nodeID string) {
	s.reconcileNodeToolContracts(s.removeLiveNodeToolSnapshot(nodeID))
}

func (s *Server) persistPublishedNodeTool(ctx context.Context, published publishedNodeTool) error {
	if s.agentDock == nil {
		return nil
	}
	return s.agentDock.SavePublishedToolContract(ctx, agentdock.PublishedToolContract{
		ToolName: published.Descriptor.Name, Descriptor: published.Descriptor,
		AcceptedSemanticHashes: published.AcceptedSemanticHashes,
	})
}

// 只合并已准入当前 release 的平台差异。冲突时撤下工具，绝不选旧 schema 或保留上一份目录。
func (s *Server) reconcileFleetNodeTool(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || isNexusCentralTool(name) {
		return nil
	}
	s.mcpReconcileMu.Lock()
	defer s.mcpReconcileMu.Unlock()
	ctx := context.Background()
	descriptors := s.liveNodeToolDescriptors(name)
	if unavailableAgentDockToolName(name) || len(descriptors) == 0 {
		return s.retireNodeTool(ctx, name)
	}
	descriptor, hashes, err := mergeFleetToolDescriptors(descriptors)
	if err != nil {
		return errors.Join(err, s.retireNodeTool(ctx, name))
	}
	hash, err := toolContractHash(descriptor)
	if err != nil {
		return errors.Join(err, s.retireNodeTool(ctx, name))
	}
	candidate := publishedNodeTool{Descriptor: descriptor, ContractHash: hash, AcceptedSemanticHashes: hashes}
	s.mcpToolsMu.Lock()
	if s.mcpTools == nil {
		s.mcpTools = make(map[string]publishedNodeTool)
	}
	published, exists := s.mcpTools[name]
	changed := !exists || !reflect.DeepEqual(published.Descriptor, candidate.Descriptor)
	s.mcpTools[name] = candidate
	if server := s.currentMCPServer(); server != nil && changed {
		s.registerNodeMCPTool(server, candidate.Descriptor, s.mcpAppsEnabled())
	}
	s.mcpToolsMu.Unlock()
	return s.persistPublishedNodeTool(ctx, candidate)
}

func (s *Server) retireNodeTool(ctx context.Context, name string) error {
	s.mcpToolsMu.Lock()
	delete(s.mcpTools, name)
	s.mcpToolsMu.Unlock()
	if server := s.currentMCPServer(); server != nil {
		server.RemoveTools(name)
	}
	if s.agentDock != nil {
		return s.agentDock.DeletePublishedToolContract(ctx, name)
	}
	return nil
}

func mergeFleetToolDescriptors(descriptors []agentdock.ToolDescriptor) (agentdock.ToolDescriptor, []string, error) {
	if len(descriptors) == 0 {
		return agentdock.ToolDescriptor{}, nil, fmt.Errorf("%w: 没有 provider", errIncompatibleToolContract)
	}
	merged, err := cloneToolDescriptor(descriptors[0])
	if err != nil {
		return agentdock.ToolDescriptor{}, nil, err
	}
	acceptedHashes := make([]string, 0, len(descriptors))
	var expectedVisibility []string
	for _, descriptor := range descriptors {
		if descriptor.Name != merged.Name {
			return agentdock.ToolDescriptor{}, nil, fmt.Errorf("%w: tool name %q != %q", errIncompatibleToolContract, descriptor.Name, merged.Name)
		}
		hash, err := toolContractHash(descriptor)
		if err != nil {
			return agentdock.ToolDescriptor{}, nil, err
		}
		visibility, _ := normalizedToolVisibility(descriptor.Meta)
		if expectedVisibility == nil {
			expectedVisibility = visibility
		} else if !reflect.DeepEqual(expectedVisibility, visibility) {
			return agentdock.ToolDescriptor{}, nil, fmt.Errorf("%w: %w: %s", errIncompatibleToolContract, errUnsafeToolVisibility, merged.Name)
		}
		acceptedHashes = append(acceptedHashes, hash)
	}
	// 当前 release 的不同平台可有可选参数差异；输入校验仍由目标节点的完整契约执行。
	merged.OutputSchema = mcpresult.Schema(merged.Name, merged.OutputSchema)
	mergedExecution, _ := executionToolMeta(merged.Meta) // 上面的 hash 已校验每个 descriptor。
	for _, descriptor := range descriptors[1:] {
		nodeExecution, _ := executionToolMeta(descriptor.Meta)
		if !jsonValuesEqual(mergedExecution, nodeExecution) {
			return agentdock.ToolDescriptor{}, nil, fmt.Errorf("%w: %s execution meta", errIncompatibleToolContract, merged.Name)
		}
		merged.InputSchema, err = mergeSchemaMaps("inputSchema", merged.InputSchema, descriptor.InputSchema)
		if err != nil {
			return agentdock.ToolDescriptor{}, nil, err
		}
		merged.OutputSchema, err = mergeSchemaMaps("outputSchema", merged.OutputSchema, mcpresult.Schema(descriptor.Name, descriptor.OutputSchema))
		if err != nil {
			return agentdock.ToolDescriptor{}, nil, err
		}
	}
	// 展示绑定只有所有 provider 一致时才保留；受限 visibility 即使展示被移除也必须保留。
	// resource.read provider 由 Hello.ui_resources 独立决定，安全提示仍按 MCP 默认语义保守合并。
	merged.Meta = mergeFleetToolMeta(descriptors)
	visibility, _ := normalizedToolVisibility(descriptors[0].Meta)
	if len(visibility) != 2 {
		if merged.Meta == nil {
			merged.Meta = make(map[string]any)
		}
		ui, _ := merged.Meta["ui"].(map[string]any)
		copyUI := make(map[string]any, len(ui)+1)
		for key, value := range ui {
			copyUI[key] = value
		}
		copyUI["visibility"] = visibility
		merged.Meta["ui"] = copyUI
	}
	merged.Annotations = mergeFleetToolAnnotations(descriptors)
	return merged, normalizeToolContractHashes(acceptedHashes), nil
}

func mergeFleetToolMeta(descriptors []agentdock.ToolDescriptor) map[string]any {
	if len(descriptors) == 0 || len(descriptors[0].Meta) == 0 {
		return nil
	}
	common := make(map[string]any, len(descriptors[0].Meta))
	for key, value := range descriptors[0].Meta {
		common[key] = value
	}
	for _, descriptor := range descriptors[1:] {
		for key, value := range common {
			other, ok := descriptor.Meta[key]
			if !ok || !jsonValuesEqual(value, other) {
				delete(common, key)
			}
		}
	}
	if len(common) == 0 {
		return nil
	}
	return common
}

func mergeFleetToolAnnotations(descriptors []agentdock.ToolDescriptor) map[string]any {
	hasAnnotations := false
	for _, descriptor := range descriptors {
		if len(descriptor.Annotations) > 0 {
			hasAnnotations = true
			break
		}
	}
	if !hasAnnotations {
		return nil
	}

	// MCP defaults: readOnly=false, destructive=true, idempotent=false, openWorld=true.
	// 只在所有 provider 都给出更安全的保证时收紧提示；任一 provider 可能产生副作用或访问开放世界时保持保守值。
	readOnly := true
	idempotent := true
	destructive := false
	openWorld := false
	for _, descriptor := range descriptors {
		annotations := descriptor.Annotations
		readOnly = readOnly && annotationBool(annotations, "readOnlyHint", false)
		idempotent = idempotent && annotationBool(annotations, "idempotentHint", false)
		destructive = destructive || annotationBool(annotations, "destructiveHint", true)
		openWorld = openWorld || annotationBool(annotations, "openWorldHint", true)
	}

	merged := mergeFleetToolAnnotationCommonValues(descriptors)
	if merged == nil {
		merged = make(map[string]any, 4)
	}
	merged["readOnlyHint"] = readOnly
	merged["destructiveHint"] = destructive
	merged["idempotentHint"] = idempotent
	merged["openWorldHint"] = openWorld
	return merged
}

func mergeFleetToolAnnotationCommonValues(descriptors []agentdock.ToolDescriptor) map[string]any {
	if len(descriptors) == 0 || len(descriptors[0].Annotations) == 0 {
		return nil
	}
	common := make(map[string]any, len(descriptors[0].Annotations))
	for key, value := range descriptors[0].Annotations {
		common[key] = value
	}
	for _, descriptor := range descriptors[1:] {
		for key, value := range common {
			other, ok := descriptor.Annotations[key]
			if !ok || !jsonValuesEqual(value, other) {
				delete(common, key)
			}
		}
	}
	if len(common) == 0 {
		return nil
	}
	return common
}

func annotationBool(annotations map[string]any, key string, defaultValue bool) bool {
	value, ok := annotations[key]
	if !ok || value == nil {
		return defaultValue
	}
	parsed, ok := value.(bool)
	if !ok {
		return defaultValue
	}
	return parsed
}

func cloneToolDescriptor(descriptor agentdock.ToolDescriptor) (agentdock.ToolDescriptor, error) {
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return agentdock.ToolDescriptor{}, fmt.Errorf("复制工具 %s 契约: %w", descriptor.Name, err)
	}
	var cloned agentdock.ToolDescriptor
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return agentdock.ToolDescriptor{}, fmt.Errorf("复制工具 %s 契约: %w", descriptor.Name, err)
	}
	return cloned, nil
}

func mergeSchemaMaps(path string, left, right map[string]any) (map[string]any, error) {
	if left == nil || right == nil {
		if left == nil && right == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %s schema presence differs", errIncompatibleToolContract, path)
	}
	leftValue, err := normalizeJSONValue(left)
	if err != nil {
		return nil, err
	}
	rightValue, err := normalizeJSONValue(right)
	if err != nil {
		return nil, err
	}
	return mergeSchemaObjects(path, leftValue.(map[string]any), rightValue.(map[string]any))
}

func mergeSchemaObjects(path string, left, right map[string]any) (map[string]any, error) {
	leftRequired, err := schemaRequiredSet(left)
	if err != nil {
		return nil, fmt.Errorf("%w: %s.required: %v", errIncompatibleToolContract, path, err)
	}
	rightRequired, err := schemaRequiredSet(right)
	if err != nil {
		return nil, fmt.Errorf("%w: %s.required: %v", errIncompatibleToolContract, path, err)
	}
	if !reflect.DeepEqual(leftRequired, rightRequired) {
		return nil, fmt.Errorf("%w: %s.required differs", errIncompatibleToolContract, path)
	}

	result := make(map[string]any, len(left)+len(right))
	keys := unionMapKeys(left, right)
	for _, key := range keys {
		leftValue, leftOK := left[key]
		rightValue, rightOK := right[key]
		switch key {
		case "title", "description":
			if leftOK {
				result[key] = leftValue
			} else if rightOK {
				result[key] = rightValue
			}
		case "required":
			if len(leftRequired) > 0 {
				required := make([]string, 0, len(leftRequired))
				for item := range leftRequired {
					required = append(required, item)
				}
				sort.Strings(required)
				result[key] = required
			} else if leftOK {
				result[key] = leftValue
			} else if rightOK {
				result[key] = rightValue
			}
		case "properties":
			properties, err := mergeSchemaProperties(path+".properties", leftValue, leftOK, rightValue, rightOK, leftRequired, rightRequired)
			if err != nil {
				return nil, err
			}
			if properties != nil {
				result[key] = properties
			}
		default:
			if !leftOK || !rightOK || !semanticSchemaValuesEqual(leftValue, rightValue, key) {
				return nil, fmt.Errorf("%w: %s.%s differs", errIncompatibleToolContract, path, key)
			}
			result[key] = leftValue
		}
	}
	return result, nil
}

func mergeSchemaProperties(path string, leftValue any, leftOK bool, rightValue any, rightOK bool, leftRequired, rightRequired map[string]struct{}) (map[string]any, error) {
	left, err := schemaProperties(leftValue, leftOK)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", errIncompatibleToolContract, path, err)
	}
	right, err := schemaProperties(rightValue, rightOK)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", errIncompatibleToolContract, path, err)
	}
	if !leftOK && !rightOK {
		return nil, nil
	}

	result := make(map[string]any, len(left)+len(right))
	for _, name := range unionMapKeys(left, right) {
		leftSchema, inLeft := left[name]
		rightSchema, inRight := right[name]
		switch {
		case inLeft && inRight:
			leftMap, leftIsMap := leftSchema.(map[string]any)
			rightMap, rightIsMap := rightSchema.(map[string]any)
			if leftIsMap && rightIsMap {
				merged, err := mergeSchemaObjects(path+"."+name, leftMap, rightMap)
				if err != nil {
					return nil, err
				}
				result[name] = merged
				continue
			}
			if !semanticSchemaValuesEqual(leftSchema, rightSchema, name) {
				return nil, fmt.Errorf("%w: %s.%s differs", errIncompatibleToolContract, path, name)
			}
			result[name] = leftSchema
		case inLeft:
			if _, required := leftRequired[name]; required {
				return nil, fmt.Errorf("%w: %s.%s only exists on one provider but is required", errIncompatibleToolContract, path, name)
			}
			result[name] = leftSchema
		case inRight:
			if _, required := rightRequired[name]; required {
				return nil, fmt.Errorf("%w: %s.%s only exists on one provider but is required", errIncompatibleToolContract, path, name)
			}
			result[name] = rightSchema
		}
	}
	return result, nil
}

func schemaProperties(value any, exists bool) (map[string]any, error) {
	if !exists {
		return map[string]any{}, nil
	}
	properties, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("properties 不是 JSON object")
	}
	return properties, nil
}

func schemaRequiredSet(schema map[string]any) (map[string]struct{}, error) {
	set := make(map[string]struct{})
	value, ok := schema["required"]
	if !ok {
		return set, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("required 不是数组")
	}
	for _, item := range items {
		name, ok := item.(string)
		if !ok {
			return nil, errors.New("required 包含非字符串值")
		}
		set[name] = struct{}{}
	}
	return set, nil
}

func semanticSchemaValuesEqual(left, right any, parentKey string) bool {
	leftNormalized := semanticSchemaKeywordValue(parentKey, left)
	rightNormalized := semanticSchemaKeywordValue(parentKey, right)
	return reflect.DeepEqual(leftNormalized, rightNormalized)
}

func jsonValuesEqual(left, right any) bool {
	leftNormalized, leftErr := normalizeJSONValue(left)
	rightNormalized, rightErr := normalizeJSONValue(right)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(leftNormalized, rightNormalized)
}

func unionMapKeys(left, right map[string]any) []string {
	keys := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		keys[key] = struct{}{}
	}
	for key := range right {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	return ordered
}

func normalizeToolContractHashes(hashes []string) []string {
	unique := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		hash = strings.TrimSpace(hash)
		if hash != "" {
			unique[hash] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(unique))
	for hash := range unique {
		normalized = append(normalized, hash)
	}
	sort.Strings(normalized)
	return normalized
}

func containsToolContractHash(hashes []string, target string) bool {
	index := sort.SearchStrings(hashes, target)
	return index < len(hashes) && hashes[index] == target
}

func (s *Server) reconcileNodeToolContracts(names []string) {
	defer s.syncMCPAppResources()
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		// 节点启停或删除后的 fleet 重算也会收到完整 descriptor 名单；中央工具始终由
		// Nexus 唯一持有，不能在这条旁路中被重新发布为要求 node_id 的节点工具。
		if name == "" || mcpcontract.IsCanonicalTool(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if err := s.reconcileFleetNodeTool(name); err != nil && s.logger != nil {
			s.logger.Warn("检查 AgentDock 工具契约收敛失败", "tool", name, "error", err)
		}
	}
}

func toolDescriptorNames(descriptors []agentdock.ToolDescriptor) []string {
	names := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if name := strings.TrimSpace(descriptor.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func findToolDescriptor(descriptors []agentdock.ToolDescriptor, name string) (agentdock.ToolDescriptor, bool) {
	for _, descriptor := range descriptors {
		if descriptor.Name == name {
			return descriptor, true
		}
	}
	return agentdock.ToolDescriptor{}, false
}

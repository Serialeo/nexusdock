package httpx

import (
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/nexusdock/internal/agentdock"
)

// visibility 是工具公开面而非客户端身份；Host 负责区分 model/app，调用仍需业务授权。
func normalizedToolVisibility(meta map[string]any) ([]string, error) {
	value, present := meta["ui"]
	if !present {
		return []string{"model", "app"}, nil
	}
	normalized, err := normalizeJSONValue(value)
	if err != nil {
		return nil, fmt.Errorf("%w: ui is not JSON: %v", errUnsafeToolVisibility, err)
	}
	ui, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: ui must be an object", errUnsafeToolVisibility)
	}
	value, present = ui["visibility"]
	if !present {
		return []string{"model", "app"}, nil
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("%w: visibility must be a nonempty array", errUnsafeToolVisibility)
	}
	seen := make(map[string]bool, 2)
	for _, item := range items {
		name, ok := item.(string)
		if !ok || (name != "model" && name != "app") {
			return nil, fmt.Errorf("%w: visibility must contain only model or app", errUnsafeToolVisibility)
		}
		seen[name] = true
	}
	result := make([]string, 0, len(seen))
	for _, name := range []string{"model", "app"} {
		if seen[name] {
			result = append(result, name)
		}
	}
	return result, nil
}

func (s *Server) registerNodeMCPTool(server *mcpsdk.Server, descriptor agentdock.ToolDescriptor, appsEnabled bool) {
	if tool := nodeMCPToolWithApps(descriptor, appsEnabled); tool != nil {
		server.AddTool(tool, s.nodeToolHandler(descriptor.Name))
	} else {
		server.RemoveTools(descriptor.Name)
	}
}

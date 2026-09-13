package httpx

import (
	protocol "github.com/Serialeo/agentdock-protocol"
	mcpcontract "github.com/Serialeo/agentdock-protocol/mcpcontract"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type centralToolPresentation struct {
	name        string
	title       string
	description string
	uiURI       string
}

func nexusToolDefinitions() []*mcpsdk.Tool {
	return nexusToolDefinitionsWithApps(true)
}

func nexusToolDefinitionsWithApps(mcpAppsEnabled bool) []*mcpsdk.Tool {
	presentations := []centralToolPresentation{
		{name: mcpcontract.ToolAgentDockContext, title: "AgentDock fleet context", description: "Return one combined context for all enabled AgentDock nodes, including node-local capabilities and Nexus-owned shared Workflow and Recall context.", uiURI: protocol.ContextUIResourceURI},
		{name: mcpcontract.ToolProjectList, title: "List Projects", description: "List enabled Nexus Projects and lightweight Deployment availability without executing on any AgentDock node."},
		{name: mcpcontract.ToolProjectOpen, title: "Open Project", description: "Create or idempotently resolve a Project WorkSession and prepare authorized Deployment Targets. Preparation never implies automatic execution."},
		{name: mcpcontract.ToolProjectContext, title: "Refresh Project context", description: "Refresh one bound WorkSession Target cwd and complete applicable Project Prompt before further execution."},
		{name: mcpcontract.ToolRecallSearch, title: "Search NexusDock Recall", description: "Search Markdown documents and cards with lexical retrieval and optional semantic enhancement when embeddings are available."},
		{name: mcpcontract.ToolRecallRead, title: "Read NexusDock Recall entry", description: "Read one central Recall entry by path."},
		{name: mcpcontract.ToolRecallWrite, title: "Write NexusDock Recall entry", description: "Plan, create, replace, append, patch, update facts, diff, or delete central Recall content. Target and action are explicit request fields.", uiURI: protocol.RecallUIResourceURI},
		{name: mcpcontract.ToolRecallMaintain, title: "Maintain NexusDock Recall", description: "Inspect sync/index state or rebuild the central Recall index."},
		{name: mcpcontract.ToolPrivateNoteManage, title: "Manage private notes", description: "Manage encrypted private-note storage and metadata."},
		{name: mcpcontract.ToolWorkflowTemplateManage, title: "Manage workflow templates", description: "List, get, get multiple, publish, retire, or match NexusDock workflow templates. get_many returns a composition set with composition_required=true."},
	}
	tools := make([]*mcpsdk.Tool, 0, len(presentations))
	for _, presentation := range presentations {
		tools = append(tools, canonicalCentralToolWithApps(presentation, mcpAppsEnabled))
	}
	return append(tools, continuationToolDefinitions(mcpAppsEnabled)...)
}

func canonicalCentralTool(presentation centralToolPresentation) *mcpsdk.Tool {
	return canonicalCentralToolWithApps(presentation, true)
}

func canonicalCentralToolWithApps(presentation centralToolPresentation, mcpAppsEnabled bool) *mcpsdk.Tool {
	input, ok := mcpcontract.InputSchema(presentation.name)
	if !ok {
		panic("missing canonical MCP input contract: " + presentation.name)
	}
	var output map[string]any
	if presentation.name == mcpcontract.ToolAgentDockContext {
		output = mcpcontract.FleetAgentDockContextOutputSchema()
	} else {
		var outputOK bool
		output, outputOK = mcpcontract.OutputSchema(presentation.name)
		if !outputOK {
			panic("missing canonical MCP output contract: " + presentation.name)
		}
	}
	annotations, ok := mcpcontract.AnnotationContract(presentation.name)
	if !ok {
		panic("missing canonical MCP annotations: " + presentation.name)
	}
	tool := &mcpsdk.Tool{
		Name:         presentation.name,
		Title:        presentation.title,
		Description:  presentation.description,
		InputSchema:  input,
		OutputSchema: output,
		Annotations:  canonicalCentralAnnotations(annotations),
	}
	if mcpAppsEnabled && presentation.uiURI != "" {
		tool.Meta = centralToolUIResourceMeta(presentation.uiURI)
	}
	return tool
}

func canonicalCentralAnnotations(value mcpcontract.Annotations) *mcpsdk.ToolAnnotations {
	annotations := &mcpsdk.ToolAnnotations{
		ReadOnlyHint:    value.ReadOnlyHint,
		DestructiveHint: value.DestructiveHint,
		OpenWorldHint:   value.OpenWorldHint,
	}
	if value.IdempotentHint != nil {
		annotations.IdempotentHint = *value.IdempotentHint
	}
	return annotations
}

func centralToolUIResourceMeta(uri string) mcpsdk.Meta {
	return mcpsdk.Meta{"ui": map[string]any{"resourceUri": uri}}
}

package httpx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/Serialeo/agentdock-protocol/mcpcontract"
	"github.com/uvwt/nexusdock/internal/agentdock"
	"github.com/uvwt/nexusdock/internal/recall"
)

const (
	agentDockContextToolName   = "agentdock_context"
	agentDockNodeInvokeTimeout = 8 * time.Second
	maxFleetContextConcurrency = 8
)

type fleetContextJob struct {
	Index int
	Node  agentdock.Node
}

type agentDockContext struct {
	Skills            []agentDockContextSkill       `json:"skills"`
	CommonSkills      *agentDockContextCommonSkills `json:"common_skills,omitempty"`
	DynamicMCP        []agentDockContextItem        `json:"dynamic_mcp"`
	ACP               *agentDockContextACP          `json:"acp,omitempty"`
	WorkflowTemplates []agentDockContextItem        `json:"workflow_templates"`
	Recall            *agentDockContextRecall       `json:"recall,omitempty"`
	Warnings          []agentDockContextWarning     `json:"warnings,omitempty"`
}

type agentDockContextSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	File        string `json:"file"`
	Bundled     bool   `json:"bundled,omitempty"`
}

type agentDockContextCommonSkills struct {
	Root      string                        `json:"root"`
	Total     int                           `json:"total"`
	Effective int                           `json:"effective"`
	Shadowed  int                           `json:"shadowed"`
	Truncated bool                          `json:"truncated"`
	Items     []agentDockContextCommonSkill `json:"items"`
}

type agentDockContextCommonSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	File        string `json:"file"`
}

type agentDockContextItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type agentDockContextACP struct {
	Enabled     bool   `json:"enabled"`
	Agent       string `json:"agent"`
	Description string `json:"description"`
}

type agentDockContextRecall struct {
	Enabled bool                   `json:"enabled"`
	Items   []agentDockContextItem `json:"items"`
}

type agentDockContextWarning struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

type fleetAgentDockContext struct {
	Nodes  []fleetAgentDockContextNode `json:"nodes"`
	Shared fleetAgentDockSharedContext `json:"shared"`
}

type fleetAgentDockContextNode struct {
	NodeID           string                     `json:"node_id"`
	Name             string                     `json:"name"`
	Online           bool                       `json:"online"`
	Version          string                     `json:"version,omitempty"`
	OS               string                     `json:"os,omitempty"`
	Arch             string                     `json:"arch,omitempty"`
	Capabilities     []string                   `json:"capabilities"`
	CapabilityStatus string                     `json:"capability_status"`
	Context          *fleetAgentDockNodeContext `json:"context,omitempty"`
	Error            string                     `json:"error,omitempty"`
}

type fleetAgentDockNodeContext struct {
	Skills       []agentDockContextSkill       `json:"skills"`
	CommonSkills *agentDockContextCommonSkills `json:"common_skills,omitempty"`
	DynamicMCP   []agentDockContextItem        `json:"dynamic_mcp"`
	ACP          *agentDockContextACP          `json:"acp,omitempty"`
	Warnings     []agentDockContextWarning     `json:"warnings,omitempty"`
}

type fleetAgentDockSharedContext struct {
	WorkflowTemplates []agentDockContextItem    `json:"workflow_templates"`
	Recall            *agentDockContextRecall   `json:"recall,omitempty"`
	Warnings          []agentDockContextWarning `json:"warnings,omitempty"`
}

func (s *Server) callFleetAgentDockContext(ctx context.Context) (map[string]any, error) {
	return s.callFleetAgentDockContextWithTimeout(ctx, agentDockNodeInvokeTimeout)
}

func (s *Server) callFleetAgentDockContextWithTimeout(ctx context.Context, leafTimeout time.Duration) (map[string]any, error) {
	if s.agentDock == nil || s.agentDockHub == nil {
		return nil, errors.New("AgentDock 节点运行时不可用")
	}
	nodes, err := s.agentDock.List(ctx)
	if err != nil {
		return nil, err
	}
	enabled := make([]agentdock.Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Enabled && nodeUsesCurrentBridgeProtocol(node) {
			enabled = append(enabled, node)
		}
	}

	fleet := fleetAgentDockContext{
		Nodes:  make([]fleetAgentDockContextNode, len(enabled)),
		Shared: s.buildFleetAgentDockSharedContext(ctx),
	}
	jobs := make([]fleetContextJob, 0, len(enabled))
	for index, node := range enabled {
		fleet.Nodes[index] = fleetAgentDockContextNode{
			NodeID: node.ID, Name: node.Name, Version: node.Version, OS: node.OS, Arch: node.Arch,
			Online: s.agentDockHub.Online(node.ID), Capabilities: deviceNodeCapabilities(node.Capabilities),
			CapabilityStatus: "unavailable",
		}
		if !containsString(node.Capabilities, agentDockContextToolName) {
			fleet.Nodes[index].CapabilityStatus = "unsupported"
			fleet.Nodes[index].Error = "AgentDock context capability unavailable"
			continue
		}
		if !fleet.Nodes[index].Online {
			fleet.Nodes[index].CapabilityStatus = "offline"
			fleet.Nodes[index].Error = agentdock.ErrNodeOffline.Error()
			continue
		}
		jobs = append(jobs, fleetContextJob{Index: index, Node: node})
	}
	runBoundedFleetContextJobs(jobs, func(job fleetContextJob) {
		leafCtx, cancel := context.WithTimeout(ctx, leafTimeout)
		defer cancel()
		remote, invokeErr := s.agentDockHub.Invoke(leafCtx, job.Node.ID, protocol.OperationContextLocal, map[string]any{})
		if invokeErr != nil {
			if errors.Is(invokeErr, context.DeadlineExceeded) {
				fleet.Nodes[job.Index].CapabilityStatus = "timeout"
				fleet.Nodes[job.Index].Error = "AgentDock context timed out"
			} else {
				fleet.Nodes[job.Index].CapabilityStatus = "unavailable"
				fleet.Nodes[job.Index].Error = "AgentDock context unavailable"
			}
			if s.logger != nil {
				s.logger.Debug("读取 AgentDock context 失败", "node_id", job.Node.ID, "error", invokeErr)
			}
			return
		}
		providerContext, decodeErr := decodeAgentDockContextResult(remote)
		if decodeErr != nil {
			fleet.Nodes[job.Index].CapabilityStatus = "malformed"
			fleet.Nodes[job.Index].Error = "AgentDock context malformed"
			if s.logger != nil {
				s.logger.Debug("解析 AgentDock context 失败", "node_id", job.Node.ID, "error", decodeErr)
			}
			return
		}
		fleet.Nodes[job.Index].CapabilityStatus = "ready"
		fleet.Nodes[job.Index].Context = localAgentDockContext(providerContext)
	})
	return asMap(fleet)
}

func runBoundedFleetContextJobs(jobs []fleetContextJob, run func(fleetContextJob)) {
	if len(jobs) == 0 {
		return
	}
	workers := maxFleetContextConcurrency
	if len(jobs) < workers {
		workers = len(jobs)
	}
	queue := make(chan fleetContextJob, len(jobs))
	for _, job := range jobs {
		queue <- job
	}
	close(queue)
	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wait.Done()
			for job := range queue {
				run(job)
			}
		}()
	}
	wait.Wait()
}

func decodeAgentDockContextResult(result map[string]any) (agentDockContext, error) {
	if result == nil {
		return agentDockContext{}, errors.New("agentdock_context 返回空结果")
	}
	if isError, _ := result["isError"].(bool); isError {
		return agentDockContext{}, errors.New(agentDockContextResultError(result))
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		return agentDockContext{}, errors.New("agentdock_context 缺少 structuredContent")
	}
	if err := validateCurrentCommonSkillsContract(structured); err != nil {
		return agentDockContext{}, err
	}
	var decoded agentDockContext
	if err := decodeMap(structured, &decoded); err != nil {
		return agentDockContext{}, fmt.Errorf("解析 agentdock_context: %w", err)
	}
	if decoded.Skills == nil || decoded.CommonSkills == nil || decoded.DynamicMCP == nil || decoded.WorkflowTemplates == nil {
		return agentDockContext{}, errors.New("agentdock_context structuredContent 不符合当前结构化契约")
	}
	if decoded.CommonSkills != nil && decoded.CommonSkills.Items == nil {
		return agentDockContext{}, errors.New("agentdock_context common_skills 不符合当前结构化契约")
	}
	return decoded, nil
}

func validateCurrentCommonSkillsContract(structured map[string]any) error {
	common, ok := structured["common_skills"].(map[string]any)
	if !ok {
		return errors.New("agentdock_context common_skills 缺少当前 Bridge v4 契约")
	}
	for _, key := range []string{"root", "total", "effective", "shadowed", "truncated", "items"} {
		if _, exists := common[key]; !exists {
			return fmt.Errorf("agentdock_context common_skills 缺少当前 Bridge v4 字段 %s", key)
		}
	}
	return nil
}

func agentDockContextResultError(result map[string]any) string {
	if structured, ok := result["structuredContent"].(map[string]any); ok {
		for _, key := range []string{"message", "error"} {
			if value, _ := structured[key].(string); strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	return "agentdock_context 调用失败"
}

func localAgentDockContext(context agentDockContext) *fleetAgentDockNodeContext {
	warnings := make([]agentDockContextWarning, 0, 2)
	for _, warning := range context.Warnings {
		switch warning.Source {
		case "skills":
			warnings = append(warnings, agentDockContextWarning{Source: "skills", Message: "Skill index unavailable."})
		case "common_skills":
			warnings = append(warnings, agentDockContextWarning{Source: "common_skills", Message: "Common Skill index unavailable."})
		}
	}
	var acp *agentDockContextACP
	if context.ACP != nil {
		acp = &agentDockContextACP{Enabled: context.ACP.Enabled, Agent: context.ACP.Agent, Description: "Coding Agent channel (Agent Client Protocol)."}
	}
	return &fleetAgentDockNodeContext{
		Skills: context.Skills, CommonSkills: context.CommonSkills, DynamicMCP: context.DynamicMCP, ACP: acp,
		Warnings: warnings,
	}
}

func (s *Server) buildFleetAgentDockSharedContext(ctx context.Context) fleetAgentDockSharedContext {
	shared := fleetAgentDockSharedContext{
		WorkflowTemplates: []agentDockContextItem{},
		Recall:            &agentDockContextRecall{Enabled: true, Items: []agentDockContextItem{}},
	}

	templates, err := s.listWorkflowTemplates(workflowTemplateActive)
	if err != nil {
		shared.Warnings = append(shared.Warnings, agentDockContextWarning{Source: "workflow_templates", Message: "工作流模板索引暂不可用。"})
	} else {
		for _, template := range latestWorkflowTemplateVersions(templates) {
			shared.WorkflowTemplates = append(shared.WorkflowTemplates, agentDockContextItem{Name: template.ID, Description: firstNonEmptyString(template.Title, template.ID)})
		}
	}

	if s.store == nil {
		shared.Recall.Enabled = false
		shared.Warnings = append(shared.Warnings, agentDockContextWarning{Source: "recall", Message: "NexusDock Recall 存储暂不可用。"})
		return shared
	}
	index, err := s.store.BuildContextIndex(recall.ContextIndexRequest{Project: "agentdock", MaxBytes: recall.ContextIndexDefaultMaxBytes})
	if err != nil {
		shared.Warnings = append(shared.Warnings, agentDockContextWarning{Source: "recall", Message: "记忆精简索引暂不可用。"})
		return shared
	}
	seen := make(map[string]struct{}, len(index.Items))
	for _, item := range index.Items {
		name := strings.TrimSpace(item.Path)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		shared.Recall.Items = append(shared.Recall.Items, agentDockContextItem{Name: name, Description: contextIndexDescription(item)})
	}
	if index.Truncated {
		shared.Warnings = append(shared.Warnings, agentDockContextWarning{
			Source:  "recall",
			Message: "记忆启动索引受预算或可读性限制，只包含部分候选。",
		})
	}
	return shared
}

func contextIndexDescription(item recall.ContextIndexItem) string {
	if summary := strings.TrimSpace(item.Summary); summary != "" {
		if title := strings.TrimSpace(item.Title); title != "" {
			return truncateRunes(title+" — "+summary, 360)
		}
		return truncateRunes(summary, 360)
	}
	parts := []string{}
	if title := strings.TrimSpace(item.Title); title != "" {
		parts = append(parts, title)
	}
	if kind := strings.TrimSpace(item.Kind); kind != "" {
		parts = append(parts, kind)
	}
	if item.CardType != "" {
		parts = append(parts, item.CardType)
	}
	labels := append(append(append([]string{}, item.Keywords...), item.Aliases...), item.Tags...)
	if len(labels) > 0 {
		parts = append(parts, strings.Join(labels, ", "))
	}
	return truncateRunes(strings.Join(parts, " · "), 360)
}

func deviceNodeCapabilities(capabilities []string) []string {
	out := make([]string, 0, len(capabilities))
	seen := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			continue
		}
		if mcpcontract.IsCanonicalTool(capability) {
			continue
		}
		if _, exists := seen[capability]; exists {
			continue
		}
		seen[capability] = struct{}{}
		out = append(out, capability)
	}
	return out
}

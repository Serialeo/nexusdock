package httpx

import (
	"testing"

	"github.com/uvwt/nexusdock/internal/config"
)

func TestCentralWorkflowTemplateManageUsesNexusRegistry(t *testing.T) {
	server := &Server{cfg: config.Config{NexusDataDir: t.TempDir()}}
	for _, id := range []string{"development.demo", "development.review"} {
		if _, err := server.publishWorkflowTemplateValue(testWorkflowTemplate(id, "1.0.0")); err != nil {
			t.Fatal(err)
		}
	}

	listed, err := server.callWorkflowTemplateManage(t.Context(), map[string]any{"action": "list", "template_status": "active"})
	if err != nil || listed["count"] != 2 {
		t.Fatalf("list=%#v err=%v", listed, err)
	}
	assertCentralToolResultMatchesOutputSchema(t, "workflow_template_manage", listed)

	loaded, err := server.callWorkflowTemplateManage(t.Context(), map[string]any{"action": "get", "template_id": "development.demo"})
	if err != nil {
		t.Fatal(err)
	}
	assertCentralToolResultMatchesOutputSchema(t, "workflow_template_manage", loaded)
	template, ok := loaded["template"].(workflowTemplate)
	if !ok || template.ID != "development.demo" || template.Status != workflowTemplateActive || template.Hash != "" {
		t.Fatalf("get=%#v", loaded)
	}

	many, err := server.callWorkflowTemplateManage(t.Context(), map[string]any{
		"action": "get_many", "template_ids": []string{"development.demo", "development.review"},
	})
	if err != nil || many["count"] != 2 || many["composition_required"] != true {
		t.Fatalf("get_many=%#v err=%v", many, err)
	}
	if _, ok := many["next_required_action"]; ok {
		t.Fatalf("get_many leaked next_required_action prose: %#v", many)
	}
	for _, template := range many["templates"].([]workflowTemplate) {
		if template.Hash != "" {
			t.Fatalf("get_many exposed registry hash: %#v", many)
		}
	}
	assertCentralToolResultMatchesOutputSchema(t, "workflow_template_manage", many)

	matched, err := server.callWorkflowTemplateManage(t.Context(), map[string]any{
		"action": "match", "goal": "demo development", "device": "DockMini", "type": "development",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCentralToolResultMatchesOutputSchema(t, "workflow_template_manage", matched)
	if matched["action"] != "match" || matched["count"].(int) == 0 {
		t.Fatalf("match=%#v", matched)
	}

	vectorIndex, err := server.callWorkflowTemplateManage(t.Context(), map[string]any{"action": "vector_index"})
	if err != nil {
		t.Fatal(err)
	}
	assertCentralToolResultMatchesOutputSchema(t, "workflow_template_manage", vectorIndex)
	if vectorIndex["action"] != "vector_index" || vectorIndex["vector_index_available"] != false {
		t.Fatalf("vector_index=%#v", vectorIndex)
	}
	if _, legacy := vectorIndex["available"]; legacy {
		t.Fatalf("vector_index leaked REST-only available field: %#v", vectorIndex)
	}
	if _, exposed := vectorIndex["content"]; exposed {
		t.Fatalf("vector_index exposed raw index content: %#v", vectorIndex)
	}
}

func TestCentralWorkflowTemplateManageSchemaHasNoNodeID(t *testing.T) {
	for _, tool := range nexusToolDefinitions() {
		if tool.Name != "workflow_template_manage" {
			continue
		}
		input := tool.InputSchema.(map[string]any)
		properties := input["properties"].(map[string]any)
		if _, exists := properties["node_id"]; exists {
			t.Fatalf("central workflow schema exposes node_id: %#v", input)
		}
		for _, name := range []string{"action", "template", "template_id", "template_ids", "template_version", "template_status", "goal", "device", "type"} {
			if _, exists := properties[name]; !exists {
				t.Fatalf("central workflow schema missing %s: %#v", name, properties)
			}
		}
		return
	}
	t.Fatal("central workflow_template_manage tool is missing")
}

func TestCentralWorkflowListDefaultsToCurrentVersionPerTemplate(t *testing.T) {
	server := &Server{cfg: config.Config{NexusDataDir: t.TempDir()}}
	for _, version := range []string{"1.0.0", "2.0.0"} {
		if _, err := server.publishWorkflowTemplateValue(testWorkflowTemplate("development.current", version)); err != nil {
			t.Fatal(err)
		}
	}

	listed, err := server.callWorkflowTemplateManage(t.Context(), map[string]any{"action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	if listed["count"] != 1 {
		t.Fatalf("default list=%#v", listed)
	}
	templates, ok := listed["templates"].([]workflowTemplateSummary)
	if !ok || len(templates) != 1 || templates[0].ID != "development.current" || templates[0].Version != "2.0.0" {
		t.Fatalf("default current templates=%#v", listed["templates"])
	}

	retired, err := server.callWorkflowTemplateManage(t.Context(), map[string]any{"action": "list", "template_status": "retired"})
	if err != nil || retired["count"] != 1 {
		t.Fatalf("retired history=%#v err=%v", retired, err)
	}
}

func TestCentralWorkflowPublishRetireOutputContract(t *testing.T) {
	server := &Server{cfg: config.Config{NexusDataDir: t.TempDir()}}
	template, err := asMap(testWorkflowTemplate("development.contract", "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}

	published, err := server.callWorkflowTemplateManage(t.Context(), map[string]any{"action": "publish", "template": template})
	if err != nil {
		t.Fatal(err)
	}
	assertCentralToolResultMatchesOutputSchema(t, "workflow_template_manage", published)

	retired, err := server.callWorkflowTemplateManage(t.Context(), map[string]any{
		"action": "retire", "template_id": "development.contract", "template_version": "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCentralToolResultMatchesOutputSchema(t, "workflow_template_manage", retired)
}

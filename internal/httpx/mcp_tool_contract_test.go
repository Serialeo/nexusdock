package httpx

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/agentdock"
)

func TestToolContractHashIgnoresSchemaPresentationOnly(t *testing.T) {
	left := agentdock.ToolDescriptor{
		Name: "exec_command",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description": map[string]any{"type": "string", "description": "actual parameter"},
				"mode":        map[string]any{"type": "string", "title": "Mode", "description": "macOS text", "enum": []any{"local", "remote"}},
			},
			"required": []any{"mode", "description"},
		},
	}
	right, err := cloneToolDescriptor(left)
	if err != nil {
		t.Fatal(err)
	}
	right.InputSchema["properties"].(map[string]any)["mode"].(map[string]any)["title"] = "Execution mode"
	right.InputSchema["properties"].(map[string]any)["mode"].(map[string]any)["description"] = "Windows text"
	right.InputSchema["required"] = []any{"description", "mode"}
	right.InputSchema["properties"].(map[string]any)["mode"].(map[string]any)["enum"] = []any{"remote", "local"}

	leftHash, err := toolContractHash(left)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := toolContractHash(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatalf("presentation/order-only changes altered semantic hash: %s != %s", leftHash, rightHash)
	}

	withoutRealDescriptionParameter, err := cloneToolDescriptor(left)
	if err != nil {
		t.Fatal(err)
	}
	delete(withoutRealDescriptionParameter.InputSchema["properties"].(map[string]any), "description")
	removedHash, err := toolContractHash(withoutRealDescriptionParameter)
	if err != nil {
		t.Fatal(err)
	}
	if removedHash == leftHash {
		t.Fatal("a real parameter named description was incorrectly treated as presentation metadata")
	}
}

func TestToolContractHashIgnoresToolPresentationMetadata(t *testing.T) {
	base := platformContractDescriptor("file_edit", map[string]any{
		"path": map[string]any{"type": "string"},
	}, []any{"path"})
	presented, err := cloneToolDescriptor(base)
	if err != nil {
		t.Fatal(err)
	}
	presented.Meta = map[string]any{"ui": map[string]any{"resourceUri": protocol.FileChangeUIResourceURI}}
	presented.Annotations = map[string]any{
		"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": false,
	}

	baseHash, err := toolContractHash(base)
	if err != nil {
		t.Fatal(err)
	}
	presentedHash, err := toolContractHash(presented)
	if err != nil {
		t.Fatal(err)
	}
	if baseHash != presentedHash {
		t.Fatalf("presentation metadata altered execution hash: %s != %s", baseHash, presentedHash)
	}

	withExecutionMeta, err := cloneToolDescriptor(presented)
	if err != nil {
		t.Fatal(err)
	}
	withExecutionMeta.Meta["file_arg_rewrite_paths"] = []any{"path"}
	executionMetaHash, err := toolContractHash(withExecutionMeta)
	if err != nil {
		t.Fatal(err)
	}
	if executionMetaHash == presentedHash {
		t.Fatal("non-UI _meta was incorrectly ignored by the execution hash")
	}
}

func TestMergeFleetToolDescriptorsMergesPresentationConservatively(t *testing.T) {
	old := platformContractDescriptor("file_edit", map[string]any{
		"path": map[string]any{"type": "string"},
	}, []any{"path"})
	old.Meta = map[string]any{
		"shared": "same",
		"ui":     map[string]any{"resourceUri": protocol.FileChangeUIResourceURI},
	}

	newDescriptor, err := cloneToolDescriptor(old)
	if err != nil {
		t.Fatal(err)
	}
	delete(newDescriptor.Meta, "ui")
	newDescriptor.Annotations = map[string]any{
		"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false,
	}

	merged, accepted, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{old, newDescriptor})
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 1 {
		t.Fatalf("same execution schema should have one accepted hash: %#v", accepted)
	}
	if merged.Meta["shared"] != "same" || merged.Meta["ui"] != nil {
		t.Fatalf("merged meta = %#v", merged.Meta)
	}
	wantAnnotations := map[string]any{
		"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true,
	}
	if !reflect.DeepEqual(merged.Annotations, wantAnnotations) {
		t.Fatalf("merged annotations = %#v, want %#v", merged.Annotations, wantAnnotations)
	}

	updatedOld, err := cloneToolDescriptor(old)
	if err != nil {
		t.Fatal(err)
	}
	updatedOld.Annotations = map[string]any{
		"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false,
	}
	updatedNew, err := cloneToolDescriptor(updatedOld)
	if err != nil {
		t.Fatal(err)
	}
	converged, _, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{updatedOld, updatedNew})
	if err != nil {
		t.Fatal(err)
	}
	ui, ok := converged.Meta["ui"].(map[string]any)
	if !ok || ui["resourceUri"] != protocol.FileChangeUIResourceURI {
		t.Fatalf("converged ui meta = %#v", converged.Meta["ui"])
	}
	if converged.Annotations["destructiveHint"] != false || converged.Annotations["idempotentHint"] != true || converged.Annotations["openWorldHint"] != false {
		t.Fatalf("converged annotations = %#v", converged.Annotations)
	}

	firstPresentation, err := cloneToolDescriptor(updatedOld)
	if err != nil {
		t.Fatal(err)
	}
	secondPresentation, err := cloneToolDescriptor(updatedOld)
	if err != nil {
		t.Fatal(err)
	}
	secondPresentation.Meta["ui"] = map[string]any{"resourceUri": protocol.TaskProgressUIResourceURI}

	mixedPresentation, _, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{firstPresentation, secondPresentation})
	if err != nil {
		t.Fatal(err)
	}
	if mixedPresentation.Meta["ui"] != nil {
		t.Fatalf("mixed presentation bindings must not publish shared UI: %#v", mixedPresentation.Meta)
	}

}

func TestMergeFleetToolDescriptorsSupportsPlatformOptionalProperties(t *testing.T) {
	tests := []struct {
		name string
		mac  agentdock.ToolDescriptor
		win  agentdock.ToolDescriptor
		want []string
	}{
		{
			name: "exec_command",
			mac: platformContractDescriptor("exec_command", map[string]any{
				"command": map[string]any{"type": "string"},
				"workdir": map[string]any{"type": "string", "description": "host path"},
			}, []any{"command"}),
			win: platformContractDescriptor("exec_command", map[string]any{
				"command":       map[string]any{"type": "string"},
				"workdir":       map[string]any{"type": "string", "description": "Windows path"},
				"windows_shell": map[string]any{"type": "string", "enum": []any{"powershell", "cmd"}},
			}, []any{"command"}),
			want: []string{"windows_shell"},
		},
		{
			name: "list_files",
			mac: platformContractDescriptor("list_files", map[string]any{
				"path": map[string]any{"type": "string", "description": "host path"},
			}, []any{"path"}),
			win: platformContractDescriptor("list_files", map[string]any{
				"path":       map[string]any{"type": "string", "description": "Windows path"},
				"drive_hint": map[string]any{"type": "string"},
			}, []any{"path"}),
			want: []string{"drive_hint"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mac, err := cloneToolDescriptor(tt.mac)
			if err != nil {
				t.Fatal(err)
			}
			win, err := cloneToolDescriptor(tt.win)
			if err != nil {
				t.Fatal(err)
			}
			mac.OutputSchema = map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"result": map[string]any{"type": "string"}},
				"additionalProperties": false,
			}
			win.OutputSchema = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result": map[string]any{"type": "string"},
				},
				"additionalProperties": false,
			}
			for _, property := range tt.want {
				win.OutputSchema["properties"].(map[string]any)[property] = map[string]any{"type": "string"}
			}

			merged, accepted, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{mac, win})
			if err != nil {
				t.Fatal(err)
			}
			properties := merged.InputSchema["properties"].(map[string]any)
			for _, property := range tt.want {
				if _, ok := properties[property]; !ok {
					t.Fatalf("merged schema missing optional platform property %s: %#v", property, properties)
				}
			}
			outputProperties := merged.OutputSchema["properties"].(map[string]any)
			for _, property := range tt.want {
				if _, ok := outputProperties[property]; !ok {
					t.Fatalf("merged output schema missing optional platform property %s: %#v", property, outputProperties)
				}
			}
			if required := merged.InputSchema["required"]; !reflect.DeepEqual(required, []string{tt.mac.InputSchema["required"].([]any)[0].(string)}) {
				t.Fatalf("required = %#v", required)
			}
			if len(accepted) != 2 {
				t.Fatalf("accepted hashes = %#v", accepted)
			}
		})
	}
}

func TestMergeFleetToolDescriptorsRejectsValidationDrift(t *testing.T) {
	base := platformContractDescriptor("exec_command", map[string]any{
		"command": map[string]any{"type": "string"},
		"mode":    map[string]any{"type": "string", "enum": []any{"local", "remote"}},
	}, []any{"command"})

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "shared type", mutate: func(properties map[string]any) { properties["command"].(map[string]any)["type"] = "array" }},
		{name: "shared enum", mutate: func(properties map[string]any) {
			properties["mode"].(map[string]any)["enum"] = []any{"host", "windows"}
		}},
		{name: "additional properties", mutate: func(properties map[string]any) {}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other, err := cloneToolDescriptor(base)
			if err != nil {
				t.Fatal(err)
			}
			if tt.name == "additional properties" {
				other.InputSchema["additionalProperties"] = true
			} else {
				tt.mutate(other.InputSchema["properties"].(map[string]any))
			}
			_, _, err = mergeFleetToolDescriptors([]agentdock.ToolDescriptor{base, other})
			if !errors.Is(err, errIncompatibleToolContract) {
				t.Fatalf("error = %v, want incompatible contract", err)
			}
		})
	}

	requiredDrift, err := cloneToolDescriptor(base)
	if err != nil {
		t.Fatal(err)
	}
	requiredDrift.InputSchema["required"] = []any{"command", "mode"}
	if _, _, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{base, requiredDrift}); !errors.Is(err, errIncompatibleToolContract) {
		t.Fatalf("required drift error = %v", err)
	}

	providerOnlyRequired, err := cloneToolDescriptor(base)
	if err != nil {
		t.Fatal(err)
	}
	providerOnlyRequired.InputSchema["properties"].(map[string]any)["runtime"] = map[string]any{"type": "string"}
	providerOnlyRequired.InputSchema["required"] = []any{"command", "runtime"}
	if _, _, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{base, providerOnlyRequired}); !errors.Is(err, errIncompatibleToolContract) {
		t.Fatalf("provider-only required property error = %v", err)
	}
}

func platformContractDescriptor(name string, properties map[string]any, required []any) agentdock.ToolDescriptor {
	return agentdock.ToolDescriptor{
		Name: name,
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             required,
			"additionalProperties": false,
		},
	}
}

func TestCurrentToolPresentationReachesMCPClient(t *testing.T) {
	descriptor := platformContractDescriptor("exec_command", map[string]any{
		"yield_time_ms": map[string]any{"type": "integer", "description": "Foreground wait threshold for execution_mode=auto. Defaults to 5000 milliseconds."},
	}, nil)
	descriptor.Title = "Run command"
	descriptor.Description = "Run commands in the selected execution mode."
	merged, _, err := mergeFleetToolDescriptors([]agentdock.ToolDescriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	tool := nodeMCPTool(merged)
	if tool.Title != descriptor.Title || !strings.Contains(tool.Description, descriptor.Description) {
		t.Fatalf("current tool guidance was removed: %#v", tool)
	}
	property := tool.InputSchema.(map[string]any)["properties"].(map[string]any)["yield_time_ms"].(map[string]any)
	if property["description"] != descriptor.InputSchema["properties"].(map[string]any)["yield_time_ms"].(map[string]any)["description"] {
		t.Fatalf("current parameter guidance was removed: %#v", property)
	}
}

func TestInconsistentCurrentContractsRetireToolInsteadOfSelectingOne(t *testing.T) {
	server := &Server{mcpTools: make(map[string]publishedNodeTool)}
	first := agentdock.Node{ID: "node_first", Enabled: true, Version: agentdock.RequiredVersion, ProtocolVersion: agentdock.ConnectionProtocolVersion}
	second := first
	second.ID = "node_second"
	one := platformContractDescriptor("exec_command", map[string]any{"timeout": map[string]any{"type": "integer"}}, nil)
	two := platformContractDescriptor("exec_command", map[string]any{"timeout": map[string]any{"type": "string"}}, nil)
	server.registerNodeTools(first, agentdock.Hello{Tools: []agentdock.ToolDescriptor{one}})
	server.registerNodeTools(second, agentdock.Hello{Tools: []agentdock.ToolDescriptor{two}})
	if _, ok := server.publishedNodeTool("exec_command"); ok {
		t.Fatal("inconsistent current contracts arbitrarily selected a schema")
	}
	server.registerNodeTools(first, agentdock.Hello{Tools: []agentdock.ToolDescriptor{one}})
	if _, ok := server.publishedNodeTool("exec_command"); ok {
		t.Fatal("repeat Hello restored an inconsistent schema")
	}
}

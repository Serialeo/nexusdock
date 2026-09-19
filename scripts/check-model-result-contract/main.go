// Verify generated public MCP examples against the actual Go result boundary.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/uvwt/nexusdock/internal/mcpresult"
	"os"
)

func run() error {
	var policy struct {
		CanonicalField string `json:"canonical_field"`
		MaxRunes       int    `json:"text_summary_max_runes"`
		MetaKey        string `json:"project_delivery_meta_key"`
		Examples       []struct {
			Tool   string         `json:"tool"`
			Input  map[string]any `json:"input"`
			Output map[string]any `json:"output"`
		} `json:"examples"`
	}
	decoder := json.NewDecoder(os.Stdin)
	decoder.UseNumber()
	if err := decoder.Decode(&policy); err != nil {
		return err
	}
	if policy.CanonicalField != "structuredContent" || policy.MaxRunes != mcpresult.MaxSummaryRunes || policy.MetaKey != mcpresult.ProjectDeliveryMetaKey {
		return fmt.Errorf("MCP contract constants differ from generator")
	}
	if len(policy.Examples) == 0 {
		return fmt.Errorf("MCP contract has no executable examples")
	}
	for _, example := range policy.Examples {
		result, err := mcpresult.Build(example.Tool, example.Input, false)
		if err != nil {
			return err
		}
		actual, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return err
		}
		expected, err := json.Marshal(example.Output)
		if err != nil {
			return err
		}
		if string(actual) != string(expected) {
			return fmt.Errorf("%s example drift: got %s; want %s", example.Tool, actual, expected)
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

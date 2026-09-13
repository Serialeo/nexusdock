package httpx

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestRuntimeMCPRequestPreservesExplicitEmptyEnvironmentValue(t *testing.T) {
	empty := ""
	payload, err := json.Marshal(runtimeMCPRequest{Action: "env_set", Name: "demo", Key: "EMPTY", Value: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) == "" || !json.Valid(payload) {
		t.Fatalf("invalid payload: %s", payload)
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatal(err)
	}
	if value, exists := raw["value"]; !exists || value != "" {
		t.Fatalf("explicit empty value was dropped: %#v", raw)
	}

	request := httptest.NewRequest("GET", "/v1/runtime/nodes/node/skills/common/demo/environment", nil)
	request.SetPathValue("source", "common")
	request.SetPathValue("skillID", "demo")
	response := httptest.NewRecorder()
	if _, _, ok := runtimeManagedSkillIdentity(response, request); ok || response.Code != 400 {
		t.Fatalf("common Skill unexpectedly became writable: status=%d body=%s", response.Code, response.Body.String())
	}
}

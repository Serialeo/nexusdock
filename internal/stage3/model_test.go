package stage3

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGenerateRedactsSnapshotAndRejectsLifecycleAuthority(t *testing.T) {
	var requestBody string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		requestBody = string(data)
		if r.Header.Get("Authorization") != "Bearer model-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"candidates":[{"type":"runbook","statement":"wait for readiness","scope":"project","project":"agentdock"}]}`}}}})
	}))
	defer model.Close()

	client, err := NewClient(Config{Endpoint: model.URL, Model: "test-model", APIKey: "model-secret", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(t.Context(), Snapshot{Tasks: []TaskFact{{TaskID: "tsk_1", Title: "deploy", Goal: "token=super-secret-value", VerifiedFacts: []string{"Authorization: Bearer abcdefghijklmnop"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"super-secret-value", "abcdefghijklmnop"} {
		if strings.Contains(requestBody, secret) {
			t.Fatalf("model request leaked %q: %s", secret, requestBody)
		}
	}
}

func TestGenerateUsesEffectiveSystemPrompt(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "bundled default", want: BundledDefaultPrompt()},
		{name: "custom override", prompt: "Custom system prompt.\nUse exactly this text.  ", want: "Custom system prompt.\nUse exactly this text.  "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotPrompt string
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if len(body.Messages) > 0 && body.Messages[0].Role == "system" {
					gotPrompt = body.Messages[0].Content
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"candidates":[]}`}}}})
			}))
			defer model.Close()

			client, err := NewClient(Config{Endpoint: model.URL, Model: "test", SystemPrompt: test.prompt})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Generate(t.Context(), Snapshot{Tasks: []TaskFact{{TaskID: "tsk_prompt", Title: "x", Goal: "y"}}}); err != nil {
				t.Fatal(err)
			}
			if gotPrompt != test.want {
				t.Fatalf("system prompt = %q, want %q", gotPrompt, test.want)
			}
		})
	}
}

func TestGenerateRejectsModelLifecycleFields(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"candidates":[{"type":"runbook","statement":"x","status":"verified"}]}`}}}})
	}))
	defer model.Close()
	client, err := NewClient(Config{Endpoint: model.URL, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Generate(t.Context(), Snapshot{Tasks: []TaskFact{{TaskID: "tsk_1", Title: "x", Goal: "y"}}}); err == nil {
		t.Fatal("expected unknown lifecycle field to be rejected")
	}
}

func TestProbeUsesAuthenticatedMinimalChatCompletion(t *testing.T) {
	var requestPath string
	var requestBody map[string]any
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer model-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "OK"}}}})
	}))
	defer model.Close()

	client, err := NewClient(Config{Endpoint: model.URL + "/v1", Model: "test-model", APIKey: "model-secret", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Probe(t.Context()); err != nil {
		t.Fatal(err)
	}
	if requestPath != "/v1/chat/completions" {
		t.Fatalf("request path = %q", requestPath)
	}
	if requestBody["model"] != "test-model" {
		t.Fatalf("model = %#v", requestBody["model"])
	}
	if _, ok := requestBody["response_format"]; ok {
		t.Fatal("probe should not request Stage 3 structured output")
	}
}

func TestNormalizeEndpointAcceptsBaseAndFullURLs(t *testing.T) {
	tests := map[string]string{
		"https://api.example.com":                     "https://api.example.com/v1/chat/completions",
		"https://api.example.com/v1":                  "https://api.example.com/v1/chat/completions",
		"https://api.example.com/v1/chat/completions": "https://api.example.com/v1/chat/completions",
	}
	for input, want := range tests {
		got, err := normalizeEndpoint(input)
		if err != nil {
			t.Fatalf("normalizeEndpoint(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizeEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
}

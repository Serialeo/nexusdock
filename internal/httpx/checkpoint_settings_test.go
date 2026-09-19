package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uvwt/nexusdock/internal/auth"
	"github.com/uvwt/nexusdock/internal/core"
	"github.com/uvwt/nexusdock/internal/settings"
)

func TestCheckpointPromptAPIAdminEditsAndPairedDeviceReads(t *testing.T) {
	server, handler, _, nodes, db := newProjectsHTTPTestServer(t)
	store, err := settings.NewCheckpointStore(db)
	if err != nil {
		t.Fatal(err)
	}
	server.checkpointSettings = store
	server.auth = auth.NewService(db)
	node := pairProjectHTTPTestNode(t, nodes, "device_checkpoint_12345678", "CheckpointNode")
	issued, err := server.auth.IssueToken(t.Context(), core.Actor{Type: core.ActorDevice, ID: node.ID}, "device_token", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/v1/settings/checkpoint", strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	readView := func(w *httptest.ResponseRecorder) settings.CheckpointPromptView {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var body checkpointPromptResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !body.OK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response=%#v headers=%#v", body, w.Header())
		}
		return body.Settings
	}
	if w := request(http.MethodGet, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated read=%d", w.Code)
	}
	initial := readView(request(http.MethodGet, issued.Token, ""))
	if initial.Source != "bundled_default" || initial.Prompt == "" {
		t.Fatalf("initial=%#v", initial)
	}
	custom := "阶段完成后保存：完成事项、证据、阻塞、下一步。\n保留空格  "
	encoded, _ := json.Marshal(settings.CheckpointPromptUpdate{ExpectedRevision: initial.Revision, Action: "replace", Prompt: &custom})
	if w := request(http.MethodPut, issued.Token, string(encoded)); w.Code != http.StatusUnauthorized {
		t.Fatalf("device edited prompt: %d %s", w.Code, w.Body.String())
	}
	saved := readView(request(http.MethodPut, projectAPITestToken, string(encoded)))
	deviceView := readView(request(http.MethodGet, issued.Token, ""))
	if saved.Prompt != custom || saved.Source != "custom" || deviceView != saved {
		t.Fatalf("saved=%#v device=%#v", saved, deviceView)
	}
	if w := request(http.MethodPut, projectAPITestToken, string(encoded)); w.Code != http.StatusConflict {
		t.Fatalf("stale write=%d %s", w.Code, w.Body.String())
	}
	resetBody, _ := json.Marshal(settings.CheckpointPromptUpdate{ExpectedRevision: saved.Revision, Action: "reset"})
	reset := readView(request(http.MethodPut, projectAPITestToken, string(resetBody)))
	if reset.Prompt != initial.Prompt || reset.Source != "bundled_default" || reset.Revision == initial.Revision {
		t.Fatalf("reset=%#v", reset)
	}
}

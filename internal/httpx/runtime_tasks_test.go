package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	protocol "github.com/Serialeo/agentdock-protocol"
	"github.com/uvwt/nexusdock/internal/agentdock"
)

func TestRuntimeTasksForwardsFiltersBeforePagination(t *testing.T) {
	server := newNodeTestServer(t)
	pairing, err := server.agentDock.CreatePairingCode(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	node, err := server.agentDock.Pair(t.Context(), agentdock.PairInput{Code: pairing.Code, DeviceID: "runtime_task_filters", Name: "Tasks"})
	if err != nil {
		t.Fatal(err)
	}
	socket, closeNode := connectProjectFakeNode(t, server, node)
	defer closeNode()
	query := url.Values{
		"status": {"blocked"}, "q": {"历史任务"}, "time_field": {"created_at"},
		"from": {"2026-08-01T00:00:00+08:00"}, "to": {"2026-09-01T00:00:00Z"},
		"offset": {"210"}, "limit": {"1"},
	}
	for _, legacy := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodGet, "/v1/runtime/nodes/"+node.ID+"/tasks?"+query.Encode(), nil)
		request.SetPathValue("nodeID", node.ID)
		response := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			server.runtimeTasks(response, request)
			close(done)
		}()
		invoke := readProjectInvoke(t, socket)
		var forwarded struct {
			Method string     `json:"method"`
			Path   string     `json:"path"`
			Query  url.Values `json:"query"`
		}
		if err := json.Unmarshal(invoke.Arguments, &forwarded); err != nil {
			t.Fatal(err)
		}
		if invoke.Operation != protocol.OperationRuntimeRequest || forwarded.Method != http.MethodGet || forwarded.Path != "/internal/runtime/tasks" || !reflect.DeepEqual(forwarded.Query, query) {
			t.Fatalf("forwarded request = %#v, operation = %s", forwarded, invoke.Operation)
		}
		payload := `{"tasks":[{"id":"older-match","title":"历史任务","status":"blocked","current_step":{"id":"review","title":"检查","status":"pending"},"completed_step_count":2}],"count":1,"total":212,"offset":210,"limit":1,"has_more":true,"counts":{"all":250,"active":30,"blocked":212,"completed":8}}`
		if legacy {
			payload = `{"tasks":[],"count":0}`
		}
		if err := socket.WriteJSON(protocol.Message{Type: protocol.MessageToolResult, RequestID: invoke.RequestID, Result: json.RawMessage(payload)}); err != nil {
			t.Fatal(err)
		}
		<-done
		if legacy {
			if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "AGENTDOCK_RUNTIME_BAD_RESPONSE") {
				t.Fatalf("legacy node: status=%d body=%s", response.Code, response.Body.String())
			}
			continue
		}
		var result runtimeTaskListResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || !result.OK || result.NodeID != node.ID || result.Source != "agentdock-runtime-api" || result.Count != 1 || result.Total != 212 || result.Offset != 210 || result.Limit != 1 || !result.HasMore || result.Counts.All != 250 || result.Counts.Active != 30 || result.Counts.Blocked != 212 || result.Counts.Completed != 8 {
			t.Fatalf("response: status=%d body=%s", response.Code, response.Body.String())
		}
		if len(result.Items) != 1 || result.Items[0].FileName != "older-match" || result.Items[0].CurrentStep == nil || result.Items[0].CurrentStep.ID != "review" || result.Items[0].CompletedStepCount != 2 {
			t.Fatalf("items = %#v", result.Items)
		}
	}
}

func TestRuntimeTaskQueryRejectsInvalidInputBeforeContactingNode(t *testing.T) {
	for _, query := range []string{
		"status=finished", "time_field=deleted_at", "from=yesterday", "to=2026-09-01",
		"from=2026-09-02T00:00:00Z&to=2026-09-01T00:00:00Z",
		"from=2026-09-01T00:00:00Z&to=2026-09-01T00:00:00Z",
		"from=0001-01-01T00:00:00Z&to=0001-01-01T00:00:00Z",
		"offset=-1", "offset=1.5", "offset=999999999999999999999999999999",
		"limit=0", "limit=201", "limit=oops", "q=one&q=two", "from=&from=", "unknown=1",
		"limit=", "offset=", "from=", "to=",
		"limit=%20", "offset=+", "from=%20%20", "to=+",
	} {
		t.Run(query, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/runtime/nodes/node/tasks?"+query, nil)
			response := httptest.NewRecorder()
			(&Server{}).runtimeTasks(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_QUERY") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	query, err := runtimeTaskListQuery(url.Values{})
	if err != nil || query.Get("offset") != "0" || query.Get("limit") != "200" || query.Get("time_field") != "updated_at" {
		t.Fatalf("default query=%#v err=%v", query, err)
	}
	for _, input := range []url.Values{{"from": {"2026-09-01T00:00:00.123456789Z"}}, {"to": {"2026-09-01T00:00:00-07:00"}}, {"status": {"all"}, "offset": {"20000"}}, {"status": {""}, "q": {""}, "time_field": {""}}} {
		if _, err := runtimeTaskListQuery(input); err != nil {
			t.Fatalf("valid query=%#v err=%v", input, err)
		}
	}
}

func TestRuntimeTaskResponseRequiresAccurateTypedMetadata(t *testing.T) {
	base := `{"tasks":[{"id":"task-1"},{"id":"task-2"}],"count":2,"total":5,"offset":0,"limit":2,"has_more":true,"counts":{"all":9,"active":5,"blocked":1,"completed":3}}`
	query, err := runtimeTaskListQuery(url.Values{"status": {"active"}, "limit": {"2"}})
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(map[string]any){
		"fractional count":      func(body map[string]any) { body["count"] = 1.5 },
		"string count":          func(body map[string]any) { body["count"] = "2" },
		"negative count":        func(body map[string]any) { body["count"] = -1 },
		"string has_more":       func(body map[string]any) { body["has_more"] = "true" },
		"wrong has_more":        func(body map[string]any) { body["has_more"] = false },
		"wrong total":           func(body map[string]any) { body["total"] = 2 },
		"wrong offset":          func(body map[string]any) { body["offset"] = 1 },
		"wrong limit":           func(body map[string]any) { body["limit"] = 200 },
		"wrong task count":      func(body map[string]any) { body["tasks"] = []any{} },
		"missing task id":       func(body map[string]any) { body["tasks"].([]any)[0] = map[string]any{} },
		"duplicate task id":     func(body map[string]any) { body["tasks"].([]any)[1] = body["tasks"].([]any)[0] },
		"invalid task field":    func(body map[string]any) { body["tasks"].([]any)[0].(map[string]any)["step_count"] = "invalid" },
		"count sum mismatch":    func(body map[string]any) { body["counts"].(map[string]any)["all"] = 10 },
		"negative status count": func(body map[string]any) { body["counts"].(map[string]any)["blocked"] = -1 },
		"string status count":   func(body map[string]any) { body["counts"].(map[string]any)["all"] = "9" },
	}
	for _, field := range []string{"tasks", "count", "total", "offset", "limit", "has_more", "counts"} {
		mutations["missing "+field] = func(body map[string]any) { delete(body, field) }
		mutations["null "+field] = func(body map[string]any) { body[field] = nil }
	}
	for _, field := range []string{"all", "active", "blocked", "completed"} {
		mutations["missing status "+field] = func(body map[string]any) { delete(body["counts"].(map[string]any), field) }
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal([]byte(base), &body); err != nil {
				t.Fatal(err)
			}
			if _, err := runtimeTaskListFromResponse(body, query); err != nil {
				t.Fatalf("valid response rejected: %v", err)
			}
			mutate(body)
			if _, err := runtimeTaskListFromResponse(body, query); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	query.Set("offset", "99")
	var empty map[string]any
	if err := json.Unmarshal([]byte(`{"tasks":[],"count":0,"total":5,"offset":99,"limit":2,"has_more":false,"counts":{"all":9,"active":5,"blocked":1,"completed":3}}`), &empty); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeTaskListFromResponse(empty, query); err != nil {
		t.Fatalf("valid page beyond the last task rejected: %v", err)
	}
}

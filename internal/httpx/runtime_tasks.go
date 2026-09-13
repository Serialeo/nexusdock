package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type runtimeTaskCounts struct {
	All       int `json:"all"`
	Active    int `json:"active"`
	Blocked   int `json:"blocked"`
	Completed int `json:"completed"`
}

type runtimeTaskListResponse struct {
	OK      bool              `json:"ok"`
	NodeID  string            `json:"node_id"`
	Items   []opsTaskSummary  `json:"items"`
	Count   int               `json:"count"`
	Total   int               `json:"total"`
	Offset  int               `json:"offset"`
	Limit   int               `json:"limit"`
	HasMore bool              `json:"has_more"`
	Counts  runtimeTaskCounts `json:"counts"`
	Source  string            `json:"source"`
}

func (s *Server) runtimeTasks(w http.ResponseWriter, r *http.Request) {
	// 显式列出公开参数供契约检查器验证；完整查询仍交给校验器拒绝重复和未知键。
	_ = r.URL.Query().Get("status")
	_ = r.URL.Query().Get("q")
	_ = r.URL.Query().Get("time_field")
	_ = r.URL.Query().Get("from")
	_ = r.URL.Query().Get("to")
	_ = r.URL.Query().Get("offset")
	_ = r.URL.Query().Get("limit")
	query, err := runtimeTaskListQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", err.Error())
		return
	}
	nodeID := r.PathValue("nodeID")
	// 搜索、时间和状态必须由节点在分页前执行，否则历史任务会从结果中消失。
	body, err := s.runtimeGet(r.Context(), nodeID, "/internal/runtime/tasks", query)
	if err != nil {
		writeJSON(w, runtimeErrorHTTPStatus(err), runtimeUnavailablePayload(err))
		return
	}
	payload, err := runtimeTaskListFromResponse(body, query)
	if err != nil {
		writeError(w, http.StatusBadGateway, "AGENTDOCK_RUNTIME_BAD_RESPONSE", "AgentDock 任务列表响应无效："+err.Error())
		return
	}
	payload.OK, payload.NodeID, payload.Source = true, nodeID, "agentdock-runtime-api"
	writeJSON(w, http.StatusOK, payload)
}

func runtimeTaskListQuery(input url.Values) (url.Values, error) {
	output := url.Values{}
	for key, values := range input {
		switch key {
		case "status", "q", "time_field", "from", "to", "offset", "limit":
		default:
			return nil, fmt.Errorf("unsupported query parameter %q", key)
		}
		if len(values) != 1 {
			return nil, fmt.Errorf("query parameter %q must appear exactly once", key)
		}
		output.Set(key, strings.TrimSpace(values[0]))
	}
	switch output.Get("status") {
	case "", "all", "active", "completed", "blocked":
	default:
		return nil, fmt.Errorf("status must be all, active, completed or blocked")
	}
	if output.Get("time_field") == "" {
		output.Set("time_field", "updated_at")
	}
	switch output.Get("time_field") {
	case "created_at", "updated_at":
	default:
		return nil, fmt.Errorf("time_field must be created_at or updated_at")
	}
	var from, to time.Time
	for _, key := range []string{"from", "to"} {
		if values, supplied := output[key]; supplied {
			parsed, err := time.Parse(time.RFC3339Nano, values[0])
			if err != nil {
				return nil, fmt.Errorf("%s must be an RFC3339 timestamp", key)
			}
			if key == "from" {
				from = parsed
			} else {
				to = parsed
			}
		}
	}
	if output.Get("from") != "" && output.Get("to") != "" && !from.Before(to) {
		return nil, fmt.Errorf("from must be earlier than to")
	}
	for _, key := range []string{"offset", "limit"} {
		value := 0
		if key == "limit" {
			value = runtimeTaskListLimit
		}
		// 上游区分未提供和显式空值；空值不能被改写为更宽的默认分页范围。
		if values, supplied := output[key]; supplied {
			parsed, err := strconv.Atoi(values[0])
			if err != nil || parsed < 0 || (key == "limit" && (parsed < 1 || parsed > runtimeTaskListLimit)) {
				return nil, fmt.Errorf("%s must be an integer within its allowed range", key)
			}
			value = parsed
		}
		output.Set(key, strconv.Itoa(value))
	}
	return output, nil
}

func runtimeTaskListFromResponse(body map[string]any, query url.Values) (runtimeTaskListResponse, error) {
	var result runtimeTaskListResponse
	// 指针区分缺字段和合法的 0/false；旧节点没有完整分页契约时必须明确报错。
	var wire struct {
		Tasks   *[]opsTaskSummary `json:"tasks"`
		Count   *int              `json:"count"`
		Total   *int              `json:"total"`
		Offset  *int              `json:"offset"`
		Limit   *int              `json:"limit"`
		HasMore *bool             `json:"has_more"`
		Counts  *struct {
			All       *int `json:"all"`
			Active    *int `json:"active"`
			Blocked   *int `json:"blocked"`
			Completed *int `json:"completed"`
		} `json:"counts"`
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return result, fmt.Errorf("encode task response: %w", err)
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return result, fmt.Errorf("decode task response: %w", err)
	}
	if wire.Tasks == nil || wire.Count == nil || wire.Total == nil || wire.Offset == nil || wire.Limit == nil || wire.HasMore == nil || wire.Counts == nil ||
		wire.Counts.All == nil || wire.Counts.Active == nil || wire.Counts.Blocked == nil || wire.Counts.Completed == nil {
		return result, fmt.Errorf("required tasks, pagination or status counts are missing; update the AgentDock node")
	}
	result.Items, result.Count, result.Total = *wire.Tasks, *wire.Count, *wire.Total
	result.Offset, result.Limit, result.HasMore = *wire.Offset, *wire.Limit, *wire.HasMore
	result.Counts = runtimeTaskCounts{All: *wire.Counts.All, Active: *wire.Counts.Active, Blocked: *wire.Counts.Blocked, Completed: *wire.Counts.Completed}
	counts := result.Counts
	if counts.All < 0 || counts.Active < 0 || counts.Blocked < 0 || counts.Completed < 0 ||
		counts.Active > counts.All || counts.Blocked > counts.All-counts.Active || counts.Completed != counts.All-counts.Active-counts.Blocked {
		return result, fmt.Errorf("status counts must be nonnegative integers adding up to counts.all")
	}
	wantTotal := counts.All
	switch query.Get("status") {
	case "active":
		wantTotal = counts.Active
	case "blocked":
		wantTotal = counts.Blocked
	case "completed":
		wantTotal = counts.Completed
	}
	if result.Total != wantTotal {
		return result, fmt.Errorf("total does not match the selected status count")
	}
	if strconv.Itoa(result.Offset) != query.Get("offset") || strconv.Itoa(result.Limit) != query.Get("limit") {
		return result, fmt.Errorf("response offset or limit does not match the request")
	}
	wantCount := max(0, result.Total-result.Offset)
	if wantCount > result.Limit {
		wantCount = result.Limit
	}
	if result.Count != len(result.Items) || result.Count != wantCount || result.HasMore != (result.Offset < result.Total && result.Count < result.Total-result.Offset) {
		return result, fmt.Errorf("count, tasks or has_more is inconsistent with pagination")
	}
	seen := make(map[string]bool, len(result.Items))
	for index := range result.Items {
		item := &result.Items[index]
		if strings.TrimSpace(item.ID) == "" || seen[item.ID] {
			return result, fmt.Errorf("tasks[%d] has a missing or duplicate id", index)
		}
		seen[item.ID] = true
		item.FileName = item.ID
	}
	return result, nil
}

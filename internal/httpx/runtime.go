package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	runtimeTaskListLimit = 200
	recentTaskWindow     = 24 * time.Hour
)

type opsTaskStep struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type opsTaskSummary struct {
	ID                 string       `json:"id"`
	Title              string       `json:"title"`
	Goal               string       `json:"goal"`
	Status             string       `json:"status"`
	Phase              string       `json:"phase"`
	ReviewStatus       string       `json:"review_status"`
	Summary            string       `json:"summary,omitempty"`
	Blocker            string       `json:"blocker,omitempty"`
	CurrentStep        *opsTaskStep `json:"current_step,omitempty"`
	CompletedStepCount int          `json:"completed_step_count"`
	UpdatedAt          string       `json:"updated_at"`
	CreatedAt          string       `json:"created_at"`
	TemplateID         string       `json:"template_id,omitempty"`
	TemplateVersion    string       `json:"template_version,omitempty"`
	ConditionCount     int          `json:"condition_count"`
	StepCount          int          `json:"step_count"`
	AttemptCount       int          `json:"attempt_count"`
	EventCount         int          `json:"event_count"`
	FileName           string       `json:"file_name"`
}

type opsTaskDetail struct {
	opsTaskSummary
	Path        string         `json:"path"`
	Content     string         `json:"content"`
	JSON        map[string]any `json:"json"`
	Conditions  []any          `json:"conditions,omitempty"`
	Steps       []any          `json:"steps,omitempty"`
	Attempts    []any          `json:"attempts,omitempty"`
	Events      []any          `json:"events,omitempty"`
	FinalReview map[string]any `json:"final_review,omitempty"`
}

type opsSkillSummary struct {
	ID               string            `json:"id"`
	Title            string            `json:"title"`
	Source           string            `json:"source"`
	Path             string            `json:"path"`
	Description      string            `json:"description,omitempty"`
	UpdatedAt        string            `json:"updated_at"`
	FileCount        int               `json:"file_count"`
	Status           string            `json:"status"`
	ActiveVersion    string            `json:"active_version,omitempty"`
	Versions         []string          `json:"versions,omitempty"`
	Channels         map[string]string `json:"channels,omitempty"`
	RuntimeStatePath string            `json:"runtime_state_path,omitempty"`
	DocRoot          string            `json:"doc_root,omitempty"`
}

type opsSkillFile struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
	UpdatedAt string `json:"updated_at"`
}

type opsSkillDetail struct {
	opsSkillSummary
	Root         string         `json:"root"`
	SkillDoc     string         `json:"skill_doc,omitempty"`
	Files        []opsSkillFile `json:"files"`
	RuntimeState map[string]any `json:"runtime_state,omitempty"`
}

type opsSkillFileContent struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
	UpdatedAt string `json:"updated_at"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

func (s *Server) registerRuntimeRoutes(mux *http.ServeMux, protected func(http.HandlerFunc) http.HandlerFunc) {
	s.registerAgentDockNodeRoutes(mux, protected)
	mux.HandleFunc("GET /v1/runtime/nodes/{nodeID}/overview", protected(s.runtimeOverview))
	mux.HandleFunc("GET /v1/runtime/nodes/{nodeID}/files", protected(s.runtimeFiles))
	mux.HandleFunc("GET /v1/runtime/nodes/{nodeID}/tasks", protected(s.runtimeTasks))
	mux.HandleFunc("GET /v1/runtime/nodes/{nodeID}/tasks/{fileName}", protected(s.runtimeTaskDetail))
	mux.HandleFunc("DELETE /v1/runtime/nodes/{nodeID}/tasks/{fileName}", protected(s.runtimeDeleteTask))
	mux.HandleFunc("GET /v1/runtime/nodes/{nodeID}/skills", protected(s.runtimeSkills))
	mux.HandleFunc("GET /v1/runtime/nodes/{nodeID}/skills/{source}/{skillID}/files/{filePath...}", protected(s.runtimeSkillFile))
	mux.HandleFunc("GET /v1/runtime/nodes/{nodeID}/skills/{source}/{skillID}/environment", protected(s.runtimeSkillEnvironment))
	mux.HandleFunc("POST /v1/runtime/nodes/{nodeID}/skills/{source}/{skillID}/manage", protected(s.runtimeSkillManage))
	mux.HandleFunc("GET /v1/runtime/nodes/{nodeID}/skills/{source}/{skillID}", protected(s.runtimeSkillDetail))
	s.registerRuntimeMCPRoutes(mux, protected)
}

func (s *Server) runtimeOverview(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("nodeID")
	tasks, taskErr := s.collectOpsTasksFromRuntime(r.Context(), nodeID, runtimeTaskListLimit)
	skills, skillErr := s.collectOpsSkillsFromRuntime(r.Context(), nodeID)
	mcp, mcpErr := s.runtimeGet(r.Context(), nodeID, "/internal/runtime/mcp", nil)
	counts := map[string]int{"active": 0, "completed": 0, "blocked": 0, "active_recent_24h": 0}
	recentCutoff := time.Now().UTC().Add(-recentTaskWindow)
	for _, task := range tasks {
		counts[task.Status]++
		if task.Status == "active" && taskUpdatedSince(task.UpdatedAt, recentCutoff) {
			counts["active_recent_24h"]++
		}
	}
	payload := map[string]any{
		"ok":         taskErr == nil && skillErr == nil && mcpErr == nil,
		"tasks":      counts,
		"skills":     map[string]any{"count": len(skills), "items": firstSkills(skills, 6)},
		"mcp":        map[string]any{"count": len(opsArray(mcp["servers"]))},
		"paths":      s.opsPaths(),
		"node_id":    nodeID,
		"source":     "agentdock-runtime-api",
		"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if taskErr != nil || skillErr != nil || mcpErr != nil {
		err := firstOpsError(taskErr, skillErr, mcpErr)
		writeJSON(w, runtimeErrorHTTPStatus(err), runtimeUnavailablePayload(err))
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func taskUpdatedSince(value string, cutoff time.Time) bool {
	updatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return err == nil && !updatedAt.Before(cutoff)
}

func (s *Server) runtimeTaskDetail(w http.ResponseWriter, r *http.Request) {
	id, err := cleanOpsTaskID(r.PathValue("fileName"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TASK_ID", err.Error())
		return
	}
	nodeID := r.PathValue("nodeID")
	detail, err := s.runtimeTaskDetailFromRuntime(r.Context(), nodeID, id)
	if err != nil {
		writeJSON(w, runtimeErrorHTTPStatus(err), runtimeUnavailablePayload(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node_id": nodeID, "task": detail, "source": "agentdock-runtime-api"})
}

func (s *Server) runtimeDeleteTask(w http.ResponseWriter, r *http.Request) {
	id, err := cleanOpsTaskID(r.PathValue("fileName"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TASK_ID", err.Error())
		return
	}
	nodeID := r.PathValue("nodeID")
	payload, err := s.runtimeDelete(r.Context(), nodeID, "/internal/runtime/tasks/"+urlPath(id))
	if err != nil {
		writeJSON(w, runtimeErrorHTTPStatus(err), runtimeUnavailablePayload(err))
		return
	}
	payload["source"] = "agentdock-runtime-api"
	payload["node_id"] = nodeID
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) runtimeSkills(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("nodeID")
	items, err := s.collectOpsSkillsFromRuntime(r.Context(), nodeID)
	if err != nil {
		writeJSON(w, runtimeErrorHTTPStatus(err), runtimeUnavailablePayload(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node_id": nodeID, "items": items, "count": len(items), "source": "agentdock-runtime-api"})
}

func (s *Server) runtimeSkillDetail(w http.ResponseWriter, r *http.Request) {
	skillID, err := cleanOpsName(r.PathValue("skillID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SKILL_ID", err.Error())
		return
	}
	nodeID := r.PathValue("nodeID")
	detail, err := s.runtimeSkillDetailFromRuntime(r.Context(), nodeID, skillID)
	if err != nil {
		writeJSON(w, runtimeErrorHTTPStatus(err), runtimeUnavailablePayload(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node_id": nodeID, "skill": detail, "source": "agentdock-runtime-api"})
}

func (s *Server) runtimeSkillFile(w http.ResponseWriter, r *http.Request) {
	skillID, err := cleanOpsName(r.PathValue("skillID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SKILL_ID", err.Error())
		return
	}
	relativePath, err := cleanRuntimeSkillFilePath(r.PathValue("filePath"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SKILL_FILE", err.Error())
		return
	}

	nodeID := r.PathValue("nodeID")
	body, err := s.runtimeGet(r.Context(), nodeID, "/internal/runtime/skills/"+urlPath(skillID)+"/files/"+urlPathSegments(relativePath), nil)
	if err != nil {
		writeJSON(w, runtimeErrorHTTPStatus(err), runtimeUnavailablePayload(err))
		return
	}
	file := opsMap(body["file"])
	content := opsSkillFileContent{
		Path:      opsString(file["path"]),
		Kind:      opsString(file["kind"]),
		SizeBytes: int64(opsInt(file["size_bytes"])),
		UpdatedAt: opsString(file["updated_at"]),
		Content:   opsString(file["content"]),
		Truncated: opsBool(file["truncated"]),
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node_id": nodeID, "file": content, "source": "agentdock-runtime-api"})
}

func (s *Server) collectOpsTasksFromRuntime(ctx context.Context, nodeID string, limit int) ([]opsTaskSummary, error) {
	body, err := s.runtimeGet(ctx, nodeID, "/internal/runtime/tasks", runtimeQueryLimitStatus(limit, ""))
	if err != nil {
		return nil, err
	}
	items := make([]opsTaskSummary, 0, len(opsArray(body["tasks"])))
	for _, raw := range opsArray(body["tasks"]) {
		m := opsMap(raw)
		id := firstNonEmptyString(opsString(m["id"]), opsString(m["task_id"]))
		if id == "" {
			continue
		}
		summary := opsTaskSummary{
			ID: id, Title: opsString(m["title"]), Goal: opsString(m["goal"]),
			Status: firstNonEmptyString(opsString(m["status"]), "unknown"), Phase: opsString(m["phase"]), ReviewStatus: firstNonEmptyString(opsString(m["review_status"]), "not_started"),
			Summary: opsString(m["summary"]), Blocker: opsString(m["blocker"]), CurrentStep: opsTaskStepFromValue(m["current_step"]),
			CompletedStepCount: opsInt(m["completed_step_count"]), StepCount: opsInt(m["step_count"]),
			UpdatedAt: opsString(m["updated_at"]), CreatedAt: opsString(m["created_at"]),
			TemplateID: opsString(m["template_id"]), TemplateVersion: opsString(m["template_version"]),
			ConditionCount: opsInt(m["condition_count"]), AttemptCount: opsInt(m["attempt_count"]), EventCount: opsInt(m["event_count"]), FileName: id,
		}
		items = append(items, summary)
	}
	return items, nil
}

func (s *Server) runtimeTaskDetailFromRuntime(ctx context.Context, nodeID, id string) (opsTaskDetail, error) {
	body, err := s.runtimeGet(ctx, nodeID, "/internal/runtime/tasks/"+urlPath(id), nil)
	if err != nil {
		return opsTaskDetail{}, err
	}
	task := opsMap(body["task"])
	summary := opsTaskSummaryFromMap(task)
	if summary.ID == "" {
		summary.ID = id
		summary.FileName = id
	}
	return opsTaskDetail{opsTaskSummary: summary, Path: "agentdock-runtime-api", Conditions: opsArray(task["conditions"]), Steps: opsArray(task["steps"]), Attempts: opsArray(task["attempts"]), Events: opsArray(task["events"]), FinalReview: opsMap(task["final_review"])}, nil
}

func opsTaskSummaryFromMap(task map[string]any) opsTaskSummary {
	finalReview := opsMap(task["final_review"])
	review := firstNonEmptyString(opsString(task["review_status"]), opsString(finalReview["status"]), "not_started")
	template := opsMap(task["template"])
	steps := opsArray(task["steps"])
	completedSteps, currentStep := opsTaskProgress(steps)
	return opsTaskSummary{
		ID: opsString(task["id"]), Title: opsString(task["title"]), Goal: opsString(task["goal"]),
		Status: firstNonEmptyString(opsString(task["status"]), "unknown"), Phase: opsString(task["phase"]), ReviewStatus: review,
		Summary: opsString(task["summary"]), Blocker: opsString(task["blocker"]), CurrentStep: currentStep, CompletedStepCount: completedSteps,
		UpdatedAt: opsString(task["updated_at"]), CreatedAt: opsString(task["created_at"]),
		TemplateID: opsString(template["id"]), TemplateVersion: opsString(template["version"]),
		ConditionCount: len(opsArray(task["conditions"])), StepCount: len(steps), AttemptCount: len(opsArray(task["attempts"])), EventCount: len(opsArray(task["events"])), FileName: opsString(task["id"]),
	}
}

func opsTaskStepFromValue(value any) *opsTaskStep {
	step := opsMap(value)
	if len(step) == 0 {
		return nil
	}
	result := &opsTaskStep{ID: opsString(step["id"]), Title: opsString(step["title"]), Status: opsString(step["status"])}
	if result.ID == "" && result.Title == "" {
		return nil
	}
	return result
}

func opsTaskProgress(steps []any) (int, *opsTaskStep) {
	completed := 0
	var current, pending *opsTaskStep
	for _, raw := range steps {
		step := opsTaskStepFromValue(raw)
		if step == nil {
			continue
		}
		switch step.Status {
		case "completed":
			completed++
		case "in_progress":
			if current == nil {
				current = step
			}
		case "pending":
			if pending == nil {
				pending = step
			}
		}
	}
	if current != nil {
		return completed, current
	}
	return completed, pending
}

func (s *Server) collectOpsSkillsFromRuntime(ctx context.Context, nodeID string) ([]opsSkillSummary, error) {
	body, err := s.runtimeGet(ctx, nodeID, "/internal/runtime/skills", nil)
	if err != nil {
		return nil, err
	}
	items := make([]opsSkillSummary, 0, len(opsArray(body["skills"])))
	for _, raw := range opsArray(body["skills"]) {
		m := opsMap(raw)
		id := firstNonEmptyString(opsString(m["skill"]), opsString(m["id"]), opsString(m["name"]))
		if id == "" {
			continue
		}
		selection := opsMap(m["selection"])
		channels := opsStringMap(firstNonNil(m["channels"], selection["channels"]))
		active := firstNonEmptyString(opsString(m["active_version"]), opsString(selection["active_version"]))
		versions := opsStringArray(m["versions"])
		summary := opsSkillSummary{
			ID: id, Title: firstNonEmptyString(opsString(m["name"]), id), Source: "agentdock-api", Path: "agentdock-api/" + id,
			Description: opsString(m["description"]), UpdatedAt: firstNonEmptyString(opsString(m["updated_at"]), opsString(selection["updated_at"])),
			FileCount: opsInt(m["file_count"]), Status: "installed", ActiveVersion: active, Versions: versions, Channels: channels,
		}
		items = append(items, summary)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (s *Server) runtimeSkillDetailFromRuntime(ctx context.Context, nodeID, skillID string) (opsSkillDetail, error) {
	body, err := s.runtimeGet(ctx, nodeID, "/internal/runtime/skills/"+urlPath(skillID), nil)
	if err != nil {
		return opsSkillDetail{}, err
	}
	document := opsMap(body["document"])
	selection := opsMap(body["selection"])
	versions := opsStringArray(body["versions"])
	channels := opsStringMap(selection["channels"])
	active := firstNonEmptyString(opsString(body["version"]), opsString(selection["active_version"]), opsString(document["version"]))
	files := make([]opsSkillFile, 0, len(opsArray(body["files"])))
	for _, raw := range opsArray(body["files"]) {
		item := opsMap(raw)
		if filePath := opsString(item["path"]); filePath != "" {
			files = append(files, opsSkillFile{
				Path: filePath, Kind: opsString(item["kind"]), SizeBytes: int64(opsInt(item["size_bytes"])), UpdatedAt: opsString(item["updated_at"]),
			})
		}
	}
	summary := opsSkillSummary{
		ID: skillID, Title: firstNonEmptyString(opsString(document["name"]), skillID), Source: "agentdock-api", Path: "agentdock-api/" + skillID,
		Description: opsString(document["description"]), UpdatedAt: opsString(selection["updated_at"]), FileCount: len(files), Status: "installed",
		ActiveVersion: active, Versions: versions, Channels: channels,
	}
	return opsSkillDetail{
		opsSkillSummary: summary, Root: "agentdock-runtime-api", SkillDoc: skillDocumentText(document), Files: files, RuntimeState: body,
	}, nil
}

func skillDocumentText(document map[string]any) string {
	name := strings.TrimSpace(opsString(document["name"]))
	description := strings.TrimSpace(opsString(document["description"]))
	version := strings.TrimSpace(opsString(document["version"]))
	body := strings.TrimSpace(opsString(document["body"]))
	if name == "" || description == "" || version == "" || body == "" {
		return ""
	}
	return fmt.Sprintf("---\nname: %s\ndescription: %s\nversion: %s\n---\n\n%s\n", strconv.Quote(name), strconv.Quote(description), strconv.Quote(version), body)
}

func cleanOpsTaskID(value string) (string, error) {
	value = strings.TrimSuffix(strings.TrimSpace(value), ".json")
	return cleanOpsName(value)
}

func urlPath(value string) string {
	return url.PathEscape(strings.Trim(value, "/"))
}

func urlPathSegments(value string) string {
	parts := strings.Split(value, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func cleanRuntimeSkillFilePath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("invalid Skill file path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", fmt.Errorf("invalid Skill file path")
		}
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid Skill file path")
	}
	return clean, nil
}

func firstOpsError(values ...error) error {
	for _, err := range values {
		if err != nil {
			return err
		}
	}
	return nil
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func (s *Server) opsPaths() map[string]string {
	return map[string]string{"agentdock": "agentdock-runtime-api"}
}

func cleanOpsName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value != filepath.Base(value) || strings.Contains(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, "..") {
		return "", fmt.Errorf("invalid name")
	}
	return value, nil
}

func firstSkills(items []opsSkillSummary, n int) []opsSkillSummary {
	if len(items) <= n {
		return items
	}
	return items[:n]
}

func opsBool(v any) bool {
	value, _ := v.(bool)
	return value
}

func opsInt(v any) int {
	switch typed := v.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

func opsStringArray(v any) []string {
	items := []string{}
	switch typed := v.(type) {
	case []string:
		return append(items, typed...)
	case []any:
		for _, item := range typed {
			if s := opsString(item); s != "" {
				items = append(items, s)
			}
		}
	}
	return items
}

func opsStringMap(v any) map[string]string {
	out := map[string]string{}
	switch typed := v.(type) {
	case map[string]string:
		for key, value := range typed {
			out[key] = value
		}
	case map[string]any:
		for key, value := range typed {
			if s := opsString(value); s != "" {
				out[key] = s
			}
		}
	}
	return out
}

func opsString(v any) string { s, _ := v.(string); return s }
func opsMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func opsArray(v any) []any { a, _ := v.([]any); return a }
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
func modTime(info fs.FileInfo) string {
	if info == nil {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339Nano)
}

func fileSize(info fs.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}

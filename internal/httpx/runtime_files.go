package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	runtimeFileBrowseMaxLimit  = 500
	runtimeFileBrowseMaxOffset = 10000
)

func (s *Server) runtimeFiles(w http.ResponseWriter, r *http.Request) {
	// Keep each public query parameter explicit in the HTTP handler so the OpenAPI
	// contract checker can verify the route and implementation stay aligned. The
	// full Values object still goes through validation so repeated keys are rejected.
	_ = r.URL.Query().Get("path")
	_ = r.URL.Query().Get("offset")
	_ = r.URL.Query().Get("limit")
	_ = r.URL.Query().Get("include_hidden")
	query, err := runtimeFileBrowseQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_FILE_BROWSE_QUERY", err.Error())
		return
	}
	nodeID := r.PathValue("nodeID")
	payload, err := s.runtimeGet(r.Context(), nodeID, "/internal/runtime/files", query)
	if err != nil {
		writeJSON(w, runtimeErrorHTTPStatus(err), runtimeUnavailablePayload(err))
		return
	}
	payload["ok"] = true
	payload["node_id"] = nodeID
	payload["source"] = "agentdock-runtime-api"
	writeJSON(w, http.StatusOK, payload)
}

func runtimeFileBrowseQuery(input url.Values) (url.Values, error) {
	output := url.Values{}
	for key, values := range input {
		switch key {
		case "path", "offset", "limit", "include_hidden":
		default:
			return nil, fmt.Errorf("unsupported query parameter %q", key)
		}
		if len(values) != 1 {
			return nil, fmt.Errorf("query parameter %q must appear exactly once", key)
		}
		output.Set(key, values[0])
	}
	if raw := strings.TrimSpace(output.Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > runtimeFileBrowseMaxOffset {
			return nil, fmt.Errorf("offset must be between 0 and %d", runtimeFileBrowseMaxOffset)
		}
		output.Set("offset", strconv.Itoa(value))
	}
	if raw := strings.TrimSpace(output.Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > runtimeFileBrowseMaxLimit {
			return nil, fmt.Errorf("limit must be between 1 and %d", runtimeFileBrowseMaxLimit)
		}
		output.Set("limit", strconv.Itoa(value))
	}
	if raw := strings.TrimSpace(output.Get("include_hidden")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, errors.New("include_hidden must be a boolean")
		}
		output.Set("include_hidden", strconv.FormatBool(value))
	}
	return output, nil
}

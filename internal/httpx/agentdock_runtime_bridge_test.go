package httpx

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/uvwt/nexusdock/internal/agentdock"
)

func TestRuntimeBridgeErrorPreservesRemoteErrorDetails(t *testing.T) {
	remote := &agentdock.RemoteError{
		Code: "INVALID_LIMIT", Message: "limit must be an integer between 0 and 200",
		Category: "validation", Retryable: true,
		Details: map[string]any{"minimum": float64(0), "maximum": float64(200)},
	}

	converted := runtimeBridgeError(remote)
	if converted.Code != "AGENTDOCK_RUNTIME_REQUEST_FAILED" || converted.UpstreamCode != remote.Code {
		t.Fatalf("converted codes = %#v", converted)
	}
	if converted.Status != http.StatusBadRequest || converted.Category != remote.Category || converted.Retryable != remote.Retryable {
		t.Fatalf("converted semantics = %#v", converted)
	}
	if !reflect.DeepEqual(converted.Details, remote.Details) {
		t.Fatalf("details = %#v, want %#v", converted.Details, remote.Details)
	}

	payload := runtimeUnavailablePayload(converted)
	detail := payload["error"].(map[string]any)
	if detail["upstream_code"] != remote.Code || detail["category"] != remote.Category || detail["retryable"] != true {
		t.Fatalf("payload detail = %#v", detail)
	}
	if !reflect.DeepEqual(detail["details"], remote.Details) {
		t.Fatalf("payload details = %#v", detail["details"])
	}
	if runtimeErrorHTTPStatus(converted) != http.StatusBadRequest {
		t.Fatalf("status = %d", runtimeErrorHTTPStatus(converted))
	}
}

func TestRuntimeBridgeErrorMapsRemoteCategories(t *testing.T) {
	for _, test := range []struct {
		category string
		want     int
	}{
		{category: "validation", want: http.StatusBadRequest},
		{category: "not_found", want: http.StatusNotFound},
		{category: "conflict", want: http.StatusConflict},
		{category: "runtime", want: http.StatusInternalServerError},
	} {
		t.Run(test.category, func(t *testing.T) {
			converted := runtimeBridgeError(&agentdock.RemoteError{Code: "UPSTREAM", Message: "failed", Category: test.category})
			if got := runtimeErrorHTTPStatus(converted); got != test.want {
				t.Fatalf("status = %d, want %d", got, test.want)
			}
		})
	}
}

func TestRuntimeBridgeErrorKeepsTransportFailuresUnavailable(t *testing.T) {
	converted := runtimeBridgeError(agentdock.ErrNodeOffline)
	if converted.Code != "AGENTDOCK_RUNTIME_UNREACHABLE" || converted.UpstreamCode != "" {
		t.Fatalf("converted = %#v", converted)
	}
	if got := runtimeErrorHTTPStatus(converted); got != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

func TestAgentDockRuntimeRequestContextSelectsActionBoundary(t *testing.T) {
	for _, test := range []struct {
		name                   string
		path                   string
		preserveParentDeadline bool
		want                   time.Duration
	}{
		{name: "ordinary runtime request", path: "/internal/runtime/mcp", want: 8 * time.Second},
		{name: "builtins request", path: "/internal/runtime/builtins", want: 30 * time.Second},
		{name: "MCP refresh keeps caller deadline", path: "/internal/runtime/mcp", preserveParentDeadline: true, want: 2 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent, parentCancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer parentCancel()
			started := time.Now()
			requestCtx, cancel := agentDockRuntimeRequestContext(parent, test.path, test.preserveParentDeadline)
			defer cancel()
			deadline, ok := requestCtx.Deadline()
			if !ok {
				t.Fatal("runtime request context omitted deadline")
			}
			got := deadline.Sub(started)
			if got < test.want-time.Second || got > test.want+time.Second {
				t.Fatalf("runtime request deadline = %s, want about %s", got, test.want)
			}
		})
	}

	t.Run("MCP refresh has a bounded fallback", func(t *testing.T) {
		started := time.Now()
		requestCtx, cancel := agentDockRuntimeRequestContext(t.Context(), "/internal/runtime/mcp", true)
		defer cancel()
		deadline, ok := requestCtx.Deadline()
		if !ok {
			t.Fatal("runtime request context omitted deadline")
		}
		got := deadline.Sub(started)
		if got < agentDockMCPRefreshRequestTimeout-time.Second || got > agentDockMCPRefreshRequestTimeout+time.Second {
			t.Fatalf("runtime request deadline = %s, want about %s", got, agentDockMCPRefreshRequestTimeout)
		}
	})
}

func TestRuntimeUnavailableRecognizesRuntimeError(t *testing.T) {
	if !isRuntimeUnavailable(agentDockRuntimeError{Code: "AGENTDOCK_RUNTIME_UNREACHABLE"}) {
		t.Fatal("agentDockRuntimeError should be recognized")
	}
	if isRuntimeUnavailable(errors.New("other")) {
		t.Fatal("plain error should not be recognized")
	}
}

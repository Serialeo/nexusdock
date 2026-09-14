package httpx

import (
	"encoding/json"
	protocol "github.com/Serialeo/agentdock-protocol"
	"strings"
	"testing"
)

func TestComputerDeploymentDisabledEvenWithNodeCapability(t *testing.T) {
	for _, full := range []bool{false, true} {
		for _, level := range []protocol.ComputerPermission{protocol.ComputerPermissionNone, protocol.ComputerPermissionObserve, protocol.ComputerPermissionControl} {
			deployment := protocol.Deployment{Permissions: protocol.DeploymentPermissions{Files: protocol.FileCapabilityNone, Computer: level, FullAccess: full}}
			err := validateComputerDeploymentCapability(deployment, nil)
			if level == protocol.ComputerPermissionNone {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), protocol.ErrorComputerUnsupported) {
				t.Fatalf("missing native implementation accepted: %v", err)
			}
			if err := validateComputerDeploymentCapability(deployment, []string{protocol.ComputerCapability}); (err == nil) != (level == protocol.ComputerPermissionNone) {
				t.Fatalf("release computer permission %s with full_access=%v: %v", level, full, err)
			}
			encoded, err := json.Marshal(deployment)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"computer":"`+string(level)+`"`) {
				t.Fatalf("computer field lost: %s", encoded)
			}
		}
	}
}
func TestComputerHistoricalGatewayBoundary(t *testing.T) {
	for _, name := range []string{protocol.ToolComputerStatus, protocol.ToolComputerStop, protocol.ToolComputerSession, protocol.ToolComputerObserve, protocol.ToolComputerAct} {
		for _, args := range []map[string]any{nil, {"operation_id": "op-1"}, {"operation_id": "  "}} {
			if got, want := allowsHistoricalTargetControl(name, args), protocol.AllowsHistoricalComputerControl(name, args); got != want {
				t.Fatalf("%s %v got %v want %v", name, args, got, want)
			}
		}
	}
}

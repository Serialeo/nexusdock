package httpx

import (
	"fmt"
	protocol "github.com/Serialeo/agentdock-protocol"
)

// All Nodes receive the current snapshot unchanged. This checks only whether a
// requested desktop capability is actually implemented, not a client version.
func validateComputerDeploymentCapability(deployment protocol.Deployment, capabilities []string) error {
	if deployment.Permissions.Computer == protocol.ComputerPermissionNone {
		return nil
	}
	if containsString(capabilities, protocol.ComputerCapability) {
		return nil
	}
	return fmt.Errorf("%s: Node has no native computer backend for computer=%s", protocol.ErrorComputerUnsupported, deployment.Permissions.Computer)
}

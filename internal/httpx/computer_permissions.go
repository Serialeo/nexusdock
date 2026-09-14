package httpx

import (
	"fmt"
	protocol "github.com/Serialeo/agentdock-protocol"
)

// Computer use 尚未完成，当前发行版关闭工具发布和权限启用。
const computerUseEnabled = false

func validateComputerDeploymentCapability(deployment protocol.Deployment, capabilities []string) error {
	if deployment.Permissions.Computer == protocol.ComputerPermissionNone {
		return nil
	}
	if !computerUseEnabled {
		return fmt.Errorf("%s: computer use is disabled in this release", protocol.ErrorComputerUnsupported)
	}
	if containsString(capabilities, protocol.ComputerCapability) {
		return nil
	}
	return fmt.Errorf("%s: Node has no native computer backend for computer=%s", protocol.ErrorComputerUnsupported, deployment.Permissions.Computer)
}

package agentdock

import (
	"context"
	"fmt"
	"strings"
)

// RequiredVersion 随 Nexus 配套发布更新；协议号相同并不代表运行时契约相同。
// 只接受这一版本，不从在线节点推断版本，也不接受旧版或未知版本。
const RequiredVersion = "0.9.5"

func (n Node) IsCurrent() bool {
	return strings.TrimSpace(n.Version) == RequiredVersion &&
		strings.TrimSpace(n.ProtocolVersion) == ConnectionProtocolVersion
}

// 拒绝的 Hello 只记录版本，不解析或转换旧版工具、资源契约。
func (s *Store) recordRejectedHello(ctx context.Context, nodeID, envelopeVersion string, hello Hello) error {
	node, err := s.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(hello.DeviceID) != node.DeviceID {
		return invalid("设备身份与配对记录不一致")
	}
	if envelopeVersion != ConnectionProtocolVersion {
		hello.ProtocolVersion = envelopeVersion
	}
	_, err = s.db.ExecContext(ctx, `UPDATE agentdock_devices SET version = ?, protocol_version = ? WHERE id = ?`,
		strings.TrimSpace(hello.Version), strings.TrimSpace(hello.ProtocolVersion), nodeID)
	if err != nil {
		return fmt.Errorf("记录 AgentDock 拒绝版本: %w", err)
	}
	return nil
}

package agentdock

import protocol "github.com/Serialeo/agentdock-protocol"

const ConnectionProtocolVersion = protocol.ConnectionProtocolVersion

type Hello = protocol.Hello
type ToolDescriptor = protocol.ToolDescriptor
type UIResourceCapability = protocol.UIResourceCapability
type RemoteError = protocol.RemoteError
type connectionMessage = protocol.Message

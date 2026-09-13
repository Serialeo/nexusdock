# WorkSession Continuation

WorkSession Continuation 让用户明确授权的 Project 工作，在指定命令结束后请求 Host 开始下一轮。AgentDock 保存执行事实，NexusDock 保存 WorkSession 的待处理状态，独立 MCP App 请求 Host 继续对话。它复用现有 WorkSession / Target，不创建另一套 Agent、Conversation 或 Task / Run 系统。

本功能首先用于通过 NexusDock 接入的 MCP Host。AgentDock 独立 MCP 入口不提供这个中央续接控制器。能否在具体 ChatGPT 客户端自动开始新一轮，必须完成本文的真实 Host 验收；本文件不是已经完成该验收的声明。

## 使用条件与升级

- NexusDock 和执行节点均使用包含此功能、依赖同一新版 `agentdock-protocol` 契约的构建。仅升级其中一个应用不足以启用完整链路。
- 节点通过现有出站 Bridge v4 连接，声明可选能力 `bridge.command.outcomes.v1`。没有此能力的旧节点不会自动拥有持久命令结果收集能力。
- NexusDock 开启 MCP Apps；Host 支持 App 的服务端工具调用和文本消息。控制器检查 `serverTools` 与 `message.text`，不支持时提示手动继续。
- 先通过 `project_open` / `project_context` 取得当前、属于此 MCP 身份的 WorkSession 和 Target。命令权限与 Project / Deployment revision 仍按原执行边界校验。
- 保留 Nexus 和 AgentDock 的数据目录；容器更新不能丢弃状态目录。

新源码通过本地多仓 Go workspace 编译，只能证明联调构建包含契约。正式发布应先发布包含新增契约的协议 module，再更新两个应用依赖、完成检查并构建部署；旧版本标签或 `latest` 名称不能证明运行的是新代码。连接后还应刷新 Host 缓存的工具 Schema。

## 一次完整的续接

以下是 MCP `tools/call` 的 `name` 和 `arguments` 示例。ID 必须替换成工具真实返回的值，不得按“最近的会话”猜测 WorkSession，也不得自行选择 `node_id` 或覆盖权限。

### 1. 明确开启有界自动续接

用户明确提出自动继续后，模型调用：

```json
{
  "name": "work_continuation",
  "arguments": {
    "action": "enable",
    "work_session_id": "<work_session_id>",
    "confirmed": true,
    "max_rounds": 5,
    "max_failures": 2
  }
}
```

`confirmed` 记录已有明确授权，不能用来替用户推断同意。普通命令结果、进度卡片或 `present_work_continuation` 都不会隐式开启自动续接。轮次上限允许 1–100，连续失败上限允许 1–10。

每次成功提交 `prepare` 的持久发送边界消耗一轮，之后投递被拒绝或结果未知也不会退还。状态返回 `rounds_used`、`max_rounds`、`failure_count`、`max_failures`。正常 `settle` 清零连续失败数；普通 `enable` 不清空历史轮次，也不能绕过熔断。预算耗尽后必须由用户明确决定是否恢复，不能无提示地重开额度。

### 2. 在当前对话显式呈现控制器

```json
{
  "name": "present_work_continuation",
  "arguments": {"work_session_id": "<work_session_id>"}
}
```

只有这个工具绑定 `ui://agentdock/work-continuation` 资源，渲染契约为 `agentdock.work-continuation.v1`。命令、领取结果和保存检查点不会再生成控制器卡片。

用户在卡片中检查 WorkSession 范围，再点击“在此视图开启”。这是对当前视图的发送授权，不替代第一步的 WorkSession 开启。卡片中的“暂停续跑”停止后续自动消息，但不取消已启动命令。需要终止命令时，使用原有命令会话操作，并检查实际结果。

### 3. 提交命令，明确交接等待来源

使用现有 `exec_command`，提供服务端绑定的 `work_session_id` / `target_id`，以及为这次逻辑提交保持稳定的 `request_id`。从命令结果取得真实命令会话 ID，再交给 `await`：

```json
{
  "name": "work_continuation",
  "arguments": {
    "action": "await",
    "work_session_id": "<work_session_id>",
    "sources": [
      {"target_id": "<target_id>", "command_session_id": "<command_session_id>"}
    ]
  }
}
```

一次 `await` 提供 1–32 个不同来源，设置本次等待集合。一个 Wake 最多合并 32 个来源，来源编码合计不超过 1 MiB；超过上限会进入后续待处理 Wake。它表示模型明确交出这一轮的指定命令来源，不是 Host 已空闲的证明。第一版只等待显式指定的命令终态；stdout 增量、心跳和任意一次工具完成都不是触发器。命令先结束、随后才调用 `await`，也应通过持久执行事实发现结果。

若提交响应丢失，应以相同 `request_id` 和相同参数核对原提交，不生成新 ID 重跑；参数不同会产生冲突。去重依赖仍被保留的命令记录，不是永久的 shell exactly-once 承诺。

Nexus 对尚未 `await` 的普通命令结果全文保留最近 30 天、每节点最近 1024 条以内的窗口；已明确等待或未结算来源不参与淘汰。窗口外结果只保留去重身份与摘要，再等待它会明确返回 `CONTINUATION_SOURCE_EXPIRED`，需要检查节点上仍保留的持久结果，不能静默重跑。已结算来源也会压缩，最近 32 个已结算 Wake 保留在会话历史中。AgentDock 自身结果的保留规则独立于这个中央窗口。

### 4. 新一轮先领取精确唤醒

可见且已授权的控制器领取待处理 Wake。服务端持久化发送边界后，一次性返回完整 `automatic_message`；App 原样通过 `ui/message` 转交。消息中的恢复信封只包含精确身份和单次凭据，不包含待执行 shell、cwd 或权限覆盖：

```json
{
  "protocol_version": 1,
  "work_session_id": "<work_session_id>",
  "endpoint_id": "<endpoint_id>",
  "controller_generation": 1,
  "wake_id": "<wake_id>",
  "attempt_id": "<attempt_id>",
  "consume_token": "<single_use_token>"
}
```

模型收到消息后，必须以信封的全部原始字段调用 `consume_work_wake`，不能补猜 ID 或从通知推导命令。权威结果来自该调用返回的 `outcomes`、绑定 Target 和 Wake，而不是通知文本。凭据单次使用；示例中的占位值不能用于调用。

### 5. 检查结果并结算

领取成功只证明这一轮已接手，不证明工作完成。检查结果、完成必要后续工作后，以非空检查点结算精确 Wake：

```json
{
  "name": "work_continuation",
  "arguments": {
    "action": "settle",
    "work_session_id": "<work_session_id>",
    "wake_id": "<wake_id>",
    "checkpoint": "已核对本次结果；已完成的操作和仍需人工处理的事项……"
  }
}
```

正常结算要求先 `consume`。领取后超时仍可在核对后结算；不要把“已经 consume”当作结算。后续另有命令要等待时，显式调用下一次 `await`，不在先前副作用不确定时重复执行。

## 状态、暂停与恢复

查看状态或暂停不要求重新呈现卡片：

```json
{"name":"work_continuation","arguments":{"action":"status","work_session_id":"<work_session_id>"}}
```

```json
{"name":"work_continuation","arguments":{"action":"pause","work_session_id":"<work_session_id>"}}
```

恢复前应核对准确 WorkSession、对话中已收到的消息及未结算结果。有可用精确信封时先领取并正常结算。投递未知、凭据丢失或 Target 已撤销等情况下，先人工核对执行结果及副作用；用户明确确认后，才能用精确 `wake_id` 和非空检查点人工结清：

```json
{
  "name": "work_continuation",
  "arguments": {
    "action": "recover",
    "work_session_id": "<work_session_id>",
    "confirmed": true,
    "wake_id": "<inspected_wake_id>",
    "checkpoint": "已人工核对原命令结果、投递和后续副作用；无需重发旧消息……"
  }
}
```

`recover` 不执行命令、不重排旧 Wake，也不重发原消息。存在未决工作时必须先解决；没有未决 Wake、仅需恢复预算时，可省略 `wake_id` 和 `checkpoint`。恢复成功会清零轮次及失败数并重新开启 WorkSession，必须有用户明确授权。

之后，如需替换已经存在的控制器，在目标对话显式调用：

```json
{"name":"present_work_continuation","arguments":{"work_session_id":"<work_session_id>","recover":true}}
```

这会更新控制器 generation，旧视图失效；新视图仍需用户点击开启。`present` 的 `recover:true` 与 `work_continuation` 的 `action:"recover"` 职责不同：前者替换视图，后者处理会话恢复。已有发送边界的未决 Wake 会阻止更换 Endpoint，应先检查处理，不能通过刷新页面或重复 `prepare` 自动重试。

## 可靠性与授权边界

| 环节 | 保证与边界 |
| --- | --- |
| 命令提交 | 启动进程前保存会话身份和执行上下文，避免快速完成时丢失归属。稳定请求 ID 与参数摘要支持提交去重。 |
| 命令结果 | 终态与待上报标记一起持久化。输出是有界快照，独立于观察游标，可能截断；完整大产物仍应使用文件或 Artifact。 |
| 中央收集 | 不依赖 iframe。通过已认证节点连接读取结果，同一事务保存来源收据与唤醒状态，提交成功后才 ACK；按认证节点身份和 `event_id` 去重。 |
| 唤醒合并 | 未准备发送的 Wake 可以合并来源；准备后来源冻结，后来的结果进入后续待处理状态。 |
| 发送边界 | `pending → claimed → prepared`。准备前租约过期可重新领取；准备后即使响应丢失也不再返回发送消息。 |
| Host 回执 | `dispatch_accepted` 只表示 Host 接受请求，不证明启动模型轮次。显式错误是 `delivery_rejected`，超时或断连是 `delivery_unknown`。 |
| 领取与结算 | 精确凭据可领取 prepared、已接受或未知投递的 Wake。先 consume 后到达的 finish 不能回退领取状态。`consumed` 与 `settled` 分开保存。 |
| App 生命周期 | 只有前台可见视图可领取、准备和请求消息。隐藏时允许有限状态检查和心跳；浏览器可能节流或关闭 iframe，不保证后台常驻自动聊天。 |
| AgentDock 重启 | 已保存终态可再次读取；未完成记录变成 `outcome_unknown`，不自动重跑，也不保证恢复或接管原进程。 |
| NexusDock 重启 | SQLite 保留会话续接状态，重新启动后继续收集结果；保留发送边界，不因重启重新发送未知消息。 |

当前视图租约为 90 秒，领取租约为 30 秒；状态检查发现准备/接受消息超过 45 秒未领取时转入未知投递，领取后超过 5 分钟未结算时标记需要检查。这些阈值用于发现未决状态，不是自动重发计时器，也不是浏览器后台运行保证。

`OwnerKey` 是 MCP 认证身份，不是可验证的 ChatGPT conversation ID。多个客户端共用固定 MCP Token 时，可能具有相同所有者。控制器使用显式呈现的 Endpoint、generation、视图绑定及私有呈现凭据确定操作范围，不能把“最近活动会话”或对话标题当成授权。

呈现凭据只放在工具结果私有 `_meta`，不进入模型可见结果。`binding_id` 标识视图实例，不是登录凭据。操作仍需检查所有者、有效绑定及相关 Target / Wake / Attempt；`ui.visibility` 也不是身份认证。关闭 MCP Apps 时，app-only 工具下架且残留调用被拒绝，不会因移除 UI 元数据变成模型工具。

## 真实 Host 验收

单元测试或模拟 Host 能证明状态迁移，不能证明特定 ChatGPT 客户端会启动新一轮。以下 P0/P3 项目需在真实部署与目标 Host 上执行，记录客户端版本、两个应用版本和 commit、协议版本、准确 WorkSession 及脱敏状态转移。私有绑定材料和 consume token 不应写入公开日志或截图。

### P0：证明最小闭环

1. 部署新契约对应的应用版本对，确认节点连接并声明命令结果能力，刷新 Host 工具目录。
2. 在测试 Project 获取 WorkSession / Target，明确授权最多一轮自动继续。
3. 呈现控制器、点击“在此视图开启”，保持卡片可见。
4. 在测试目录执行无破坏性的延迟命令，记录真实会话 ID 后显式 `await`。例如用支持的 shell 等待数秒，再打印固定测试标识。
5. 观察命令终态进入中央状态、控制器提交消息；分别记录 Host 接受请求和实际是否开始新模型轮次，不能只看“已请求续跑”。
6. 确认新轮次调用精确信封的 `consume_work_wake`、读取原命令结果，并为正确 Wake 保存检查点、完成 `settle`。
7. 确认没有额外控制器、没有重复执行命令、单轮预算未绕过。Host 仅接受但未启动轮次应记录为 P0 未通过，继续使用手动工作流。

### P3：故障矩阵

在隔离测试环境逐项注入故障。主动丢弃特定响应需要测试代理或 Host 测试适配器；切换标签页本身不能证明复现了特定网络故障。

| 场景 | 操作与验收观察 |
| --- | --- |
| 完成早于 await | 命令结束后才登记来源；仍得到一次待处理结果，无需重跑。 |
| 提交回复丢失 | 相同请求 ID 和参数再次核对；只有一次逻辑执行，参数变化明确冲突。 |
| Nexus 提交后 ACK 丢失 | 丢弃 ACK 再重连；重复事件不产生第二份来源或唤醒。 |
| prepare 回复丢失 | 丢弃准备响应再恢复；发送边界保留，不再次 prepare 并发送同一消息。 |
| Host 拒绝与超时 | 分别显式报错和不响应；区分 rejected / unknown，不当作新轮次成功，也不自动重发。 |
| consume 早于 finish | 延迟 finish，让模型先领取；后到回执不回退 consumed。 |
| 领取后模型中断 | consume 后不结算；保留未结算来源和检查点，恢复时不自动重复副作用。 |
| 双视图与旧 generation | 打开双卡片，再显式恢复 Endpoint；只有当前有效绑定操作成功，旧视图和晚到响应不能发送。 |
| 隐藏或关闭对话 | 命令运行时隐藏/关闭卡片；中央仍收集结果，隐藏视图不请求消息；返回前台后按当前绑定和状态处理。 |
| Nexus 重启 | 在 pending、prepared、consumed 阶段重启；状态保留，prepared 不重发，consumed 不被视作 settled。 |
| AgentDock 重启 | 分别完成后和运行中重启；终态可读，未完成记录报告未知结果，不自动重跑。 |
| 撤销 Target 或改配置 | 等待期间停用 Deployment、撤销权限或改变 revision；失效 Target 不能继续派发工作。 |
| 关闭 MCP Apps | 存在卡片时关闭设置并尝试旧调用；app-only 工具下架且残留调用失败，普通命令仍按原权限运行。 |
| 暂停和预算 | 等待时暂停，分别耗尽轮次/失败预算；后续消息停止，不把运行中命令宣称为已终止。 |

验收记录应区分“代码检查通过”“模拟 Host 通过”和“目标 ChatGPT 客户端通过”，并列出未执行项目。应用或 Host 行为变化后，重新执行 P0 和受影响的故障场景。

# 节点临时会话（Node Session）

## 解决的问题

节点工具的入参由网关重写为必填 `work_session_id` + `target_id`（`internal/httpx/mcp_gateway.go` `nodeInputSchema`），Target 只能由 `project_open` 从 Deployment 派生，而 `/v1/projects` 只接受浏览器会话或 `NEXUS_AUTH_TOKEN`，`/mcp` 不可用。结果是外部 Host 想在某个节点上执行任何一次性任务，唯一可行路径是挑一个无关 Project 挂靠。代价是真实的：`project_open` 不传 `targets` 时默认绑定该 Project 全部 enabled Deployment；被挂靠 Project 的 `AGENTS.md` 正文进入模型上下文并占用共享的 `MaxProjectPromptContextBytes` 预算；该 Project 的 Deployment 一改 revision，无关会话立刻 `REVISION_CONFLICT`；`work_sessions`、`command_source_receipts` 与 Sessions 面板被无关记录污染。

节点临时会话提供第二个入口 `node_open`，在**不经过任何用户可见 Project** 的前提下拿到一个绑定到指定节点的 Target。

## 不做的事

不恢复无 Target 的单节点 Runtime fallback，不引入第二套路由。`protocol.ExecutionContext` 字段不变，仍由 Nexus 从已绑定 Target 生成，仍是唯一可信路由来源。AgentDock 侧不改代码：`working_folder` 留空时 `instructions.Loader.Load` 返回 `Complete=true`、零 Source 的 Prompt，`LoadProjectPrompt` 返回 `SourceProvenance{Kind: none}`，恰好满足 Nexus `loadProjectPrompt` 的完整性校验。`node_open` 不是 `project_open` 的降级形态，两者不互相 fallback。

## 数据模型

`projects` 增加两列：

```sql
kind TEXT NOT NULL DEFAULT 'project' CHECK (kind IN ('project', 'node'))
node_id TEXT REFERENCES agentdock_devices(id) ON DELETE CASCADE
CREATE UNIQUE INDEX idx_projects_node_session ON projects(node_id) WHERE kind = 'node'
```

`kind='node'` 的 Project 是系统持有的容器，不是产品概念上的 Project：每个节点至多一个，`name` 跟随节点名，`orchestration_policy` 恒为空，不出现在 `project_list`、Projects 列表和 Project 详情路由中。它下面恒有且只有一个 Deployment，`node_id` 指向本节点，`working_folder` **恒为空**且不可配置——这正是"无 `AGENTS.md` 自动发现、无 provenance 根、cwd 取节点默认目录"的既有语义，不需要新规则。

WorkSession、WorkTarget、`work_continuations`、`command_source_receipts`、`project_context_deliveries` 全部零改动复用。

## 权限来源：设置 UI

临时会话权限不继承 Full Access，也不隐式推导，由人在 **设置 → 系统与节点 → 节点编辑** 中显式配置，与同一对话框里的 Full Access 开关并列。默认全部关闭，即未配置的节点无法开启临时会话。

权限本身仍存放在 `project_deployments` 的现有列（files/shell/browser/dynamic MCP/ACP），不新增权限表；"是否启用临时会话"复用该 Deployment 的 `enabled`。因此 revision 递增、`RevokeTargetsForDeployment`、`tryApplyProjectDeployment`、重连补 apply（`projectDeploymentsApplyForNode`）全部沿用既有实现。

Full Access 与临时会话权限的关系与 Project 场景完全一致：`effectiveProjectPermissions` 用节点 Full Access 覆盖细粒度项，且不扩大 Prompt 搜索边界（临时会话的边界本来就为空）。节点 Full Access 变更时，已有的临时会话 Target 与 Project Target 一样被撤销。

新增两个受保护端点：

- `GET /v1/runtime/nodes/{nodeID}/session` — 读取当前临时会话开关与权限；从未配置时返回未启用与全关默认值。
- `PUT /v1/runtime/nodes/{nodeID}/session` — 写入 `{enabled, permissions}`；首次写入惰性创建 `kind='node'` 的 Project 与其唯一 Deployment，随后递增 desired revision、撤销存量 Target、触发 apply。

## MCP 入口

新增中心工具 `node_open`：

- 入参 `node_id`（必填）、`client_request_id`（必填，与 `project_open` 同样的幂等键）、`cwd_rel`（可选，相对节点默认目录）。
- 返回结构与 `project_open` 同形：`work_session_id`、`status`、`context_revision`、`delivery`、`targets`；`project` 字段省略，改为 `node`（`node_id`、`name`、`online`）。始终只有一个 Target。
- 节点未在设置中启用临时会话时返回 `NODE_SESSION_DISABLED`，错误信息指向设置页，不自动创建、不回退到 Project。
- 节点发现沿用 `agentdock_context`：它本来就把 `node_id` 返回给模型，但此前那个 id 对模型没有任何用处；`node_open` 正好闭合这个断口，不需要新增枚举工具。

`project_context` 对临时会话 Target 照常可用（刷新 cwd 与 revision），返回 `node` 而不是泄露内部 `project` 容器。`project_list` 增加 `kind='node'` 过滤。

## UI

- 设置 → 系统与节点：节点编辑对话框新增"临时会话"分组（启用开关 + 五项细粒度权限），文案需说明它与 Project 无关、不做 `AGENTS.md` 发现、仍受节点 OS 身份约束。
- 工作 → Sessions：临时会话单列一组展示（节点名、cwd、context revision、状态），不混入任何 Project 的会话列表。
- Projects 页面与 `project_list` 不展示 `kind='node'` 的容器。

## 协议与版本

`node_open` 的 Input/Output/Annotation 契约需加入 `agentdock-protocol/mcpcontract`（`canonicalCentralToolWithApps` 缺契约会 panic），发布为 `v0.11.0`。该改动只在 Nexus 侧使用的包内新增常量与 schema，Bridge wire 与 Bridge v4 握手不变，AgentDock 无需同步改代码。Nexus 侧需执行 `make contracts` 并提交 `internal/httpx/web_dist` 产物。

## 迁移

只做结构迁移：为 `projects` 增列并建立部分唯一索引，存量行 `kind` 取默认值 `'project'`、`node_id` 为 NULL。不创建任何临时会话容器——首次在设置中保存临时会话配置时才惰性创建。删除节点时先撤销 Target 并清理临时会话 WorkSession，再删除其内部 Project 与 Deployment；普通 Project Deployment 仍会阻止误删在用节点。

## 验证要点

- 未启用临时会话的节点调用 `node_open` 返回 `NODE_SESSION_DISABLED`，且不落库任何 Project/Deployment 行。
- 同一 `client_request_id` 重复 `node_open` 幂等返回同一 WorkSession；不同 `cwd_rel` 的请求哈希冲突按 `project_open` 既有语义报 `REVISION_CONFLICT`。
- 临时会话 Target 的 Prompt 恒为零 Source、`Complete=true`、`SourceProvenance.Kind=none`，不占用 Project Prompt 预算。
- 在设置中调整权限或关闭开关后，存量临时会话 Target 被撤销，后续工具调用返回 `SESSION_TARGET_DENIED`。
- 节点 Full Access 变更同样撤销临时会话 Target，且不扩大 Prompt 搜索边界。
- `project_list` 与 `/v1/projects` 均不返回 `kind='node'` 容器；`/v1/projects/{id}` 直接访问该容器 id 返回未找到。
- 节点离线重连后，临时会话 Deployment 与 Project Deployment 一样被补 apply。
- 删除节点后临时会话 Project、Deployment、WorkSession、Target 全部清理，Recall 与节点无关数据不受影响。

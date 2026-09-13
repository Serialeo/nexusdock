# NexusDock 开发约束

## 产品边界

NexusDock 是个人多设备 AgentDock 汇总与 Project 工作入口。一级产品区域按当前主线组织为“工作（Projects / Sessions）”“知识（Recall / Workflow）”“运行环境（Nodes / Skills / MCP）”“系统（设置与账号）”；节点管理页可以显式选择单个 AgentDock，但 Project 工作流必须通过服务端绑定的 WorkSession / Target 路由到 Deployment，不能把前端全局 Node 选择器当作执行授权。Recall 等 Nexus 自有工具只公开一次。节点通过 Device Token 主动建立出站 WebSocket，不要求 AgentDock 具备公网入口。

Project / Deployment / Target 与 `docs/project-workspace-refactor/` 的 D01–D11 是当前产品基线：不恢复 Global Instructions、Node Guidance、Direct Instructions 或 Node FileAccess 作为项目工作指导/权限来源；不引入 legacy Host Project、`project://`、External Resources、`validation_scope` 或目录 ACL DSL。`working_folder` 是**可选**的默认 cwd、Project Prompt 搜索边界与源码 provenance 根，不是 OS 沙箱；留空时使用 Node AgentDock 默认 cwd 且不自动发现 Project `AGENTS.md`。Node 的 Full Access 与 `working_folder` 正交：开启后覆盖 Deployment 细粒度执行能力，但不扩大 Project Prompt 搜索边界，真实访问仍受 Node OS 身份权限约束。硬能力必须由 Nexus 与 AgentDock 的运行时检查执行。此次断代**不兼容旧代码路径、旧协议请求或旧 UI 行为**：旧入口应删除或明确报错，不得 fallback、自动翻译或维护双轨；数据库迁移只负责结构一致性与保留 Recall、秘密、节点身份等无关数据。不要恢复独立 Task / Run 业务系统、Skill Registry、任意权限 scope、SSE/EventBus、旧无 Target 的单节点 Runtime fallback 或 Nexus 主动回连 AgentDock 的拓扑。

## 代码原则

- 修改前先理解现有目录、数据模型、接口契约、错误处理和测试方式。
- 主流程保持连贯，优先具体类型和清楚的数据流；不要为形式上的分层提前增加 interface、helper 或通用抽象。
- 动态 JSON 只停留在 HTTP/Runtime 边界，进入核心流程后尽快转成明确结构。
- 错误必须显式返回并包含定位上下文，不吞错，不只记录日志后继续。
- 非显然业务约束、兼容原因和安全边界使用中文注释说明“为什么”。
- 测试描述真实业务行为，优先覆盖状态变化、错误路径、安全边界和曾经出现过的问题。

## 认证与秘密

- 浏览器管理员账号只存储在 Nexus SQLite 中，通过本地 `nexusdock admin` 命令初始化或重置。
- `NEXUS_AUTH_TOKEN` 仅用于程序化 `/v1` API，不得用于 `/mcp`；不要恢复 `NEXUS_USERNAME`、`NEXUS_PASSWORD`、`NEXUS_PASSWORD_HASH` 或 `NEXUS_ACCESS_FILE`。
- NexusDock 固定 MCP Token 独立存放在 `NEXUS_DATA_DIR/secrets/mcp-access-token`，仅允许 `/mcp`，可由管理员设置页查看和重置；重置后旧 Token 必须立即失效。
- AgentDock 配对使用短时单次码；Device Token 只保存在 AgentDock，Nexus 仅保存哈希。不得要求用户向 Nexus 提供 AgentDock 的 `/mcp` Token 或公网地址。

## 验证与提交

- 常规验证执行 `make check`；完整交付执行 `make ci`。
- 修改公共 API 后必须更新生成器并执行 `make contracts`。
- 修改前端后必须提交 `internal/httpx/web_dist` 中对应的嵌入式产物。
- 提交标题使用 `type(scope): 中文说明`；默认从最新 `develop` 创建任务分支，验证后归并 `develop`。

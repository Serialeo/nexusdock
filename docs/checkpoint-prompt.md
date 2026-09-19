# 任务 checkpoint 提示词

任务页的“Checkpoint 提示词”编辑所有连接节点共享的 checkpoint 保存时机、摘要内容与交接要求。支持精确保存、撤销编辑、恢复默认、8 KiB UTF-8 上限和编辑版本冲突检测。恢复默认同样递增版本，旧编辑器不能覆盖新值。

配置存储在 Nexus SQLite 的 checkpoint_prompt_settings 表。GET /v1/settings/checkpoint 允许管理员和有效的已配对 Device Token 读取；PUT 只允许管理员/API 管理凭据修改。它不属于 MCP 接入设置，不进入 Project AGENTS.md 或项目上下文。

AgentDock 任务服务在 create/get/resume 前获取当前提示词，并随 checkpoint_policy 交付给调用方；固定参数和状态约束保持由任务代码负责。MCP 和 Nexus 输出精简必须保留这个字段。未连接 Nexus 时使用内置提示词；读取失败时返回 warning 和内置提示词，避免阻止离线任务恢复。保存 checkpoint 本身不增加远端请求。

交付策略只能保证提示词在任务结果中可见，不能证明模型已经理解或遵循。checkpoint 的 task_id、非空 summary、步骤字段互斥仍由 schema 与任务状态机校验。

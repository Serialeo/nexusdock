# 内置能力管理与升级边界

入口为「运行环境 → Nodes → 选择节点 → 内置能力」。Nexus 通过现有 `runtime.request` 向选定 AgentDock 读取或提交选择，持久化仍由 AgentDock 完成。节点离线时不能写入，不排队回放。开关不授予 Deployment 权限。

面板每次读取结束后才安排下一次刷新，写入期间不读取；切换、页面隐藏或离开时取消旧读取并递增请求代次。即使旧请求忽略取消，结果也不能覆盖新状态。读取失败清除失效快照，后续轮询成功后恢复。原生面板同样将开关和应用配置、重启互斥。

Bridge 对单个节点串行提交完整快照和连接代次，不同节点使用独立锁。`node.ready` 网络写入不占用全局锁，同连接重复快照按内容哈希去重。AgentDock 仍在远程开关结果返回前发送最新快照，并在同连接上去重通知；没有取消这项顺序保证。

## 配套升级

本次按三个仓库 `feat/builtin-capabilities` 的配套实现统一重部署，不支持混合旧版 AgentDock/Nexus，也不迁移旧开关。共享协议依赖为 `v0.9.2-0.20260914075145-b0beb69cdadb`，不是正式 `0.9.2` 发布。连接协议版本号本身不能证明节点支持热更新。

先停止旧服务并备份需要保留的数据，清理 AgentDock 启动定义中的旧 browser/ACP 环境开关和 CLI 参数。准备配套构建，先启动 Nexus，再启动所有新版 AgentDock；统一升级完成前暂停工作流流量。首次启用通过 GUI 或 AgentDock 的 `builtins` CLI 选择。详细 stdio 管理命令、损坏配置恢复与启动超时策略见 AgentDock 仓库 `docs/builtin-capabilities.md`。数据库清理或重建由部署人员处理，本次代码修补不执行数据库操作。

Computer Use 及其专用 worker 已从仓库删除。外部服务使用通用动态 MCP：Nexus 将配置和调用转发给目标 AgentDock，由该节点建立 `stdio` 或 `streamable_http` 连接。外部 MCP 不受内置 browser/ACP 开关控制，仍受独立动态 MCP 权限约束。

## 本次修补的验证

- AgentDock：全新 stdio home 的启用、工具列表和重启持久化；控制端点不能被第二实例接管；默认浏览器缺失或默认 CDP 故障时允许按调用指定 CDP；探测的直接连接、超时、错误及过大响应。
- 生命周期：慢 handler 忽略取消时，请求 deadline 可以结束等待，清理继续且 Core 等待旧调用退出；工具定义的可变内容不泄漏到缓存；内置能力关闭后动态 MCP 的检索、检查、调用和权限仍正常。
- Nexus：延迟 GET 与 POST 交错、慢轮询、页面隐藏、断线恢复；慢节点回调不阻塞其他节点；重复快照只协调一次。
- 本地通过 AgentDock `GOWORK=off make check`、相关 race 与 Docker tag 测试，以及 Nexus `GOWORK=off make ci`、`npm test --prefix web`；嵌入式 Web 产物同步提交。原生构建和真实浏览器测试另由 AgentDock 功能分支 CI 执行。

AgentDock 在 Linux amd64、Go 1.26.8、Ryzen 9 9950X3D 上的一次修补后基准（非前后对比，不代表端到端吞吐）：

| 操作 | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| ToolNames | 369.3 | 352 | 1 |
| CatalogSnapshot | 137633 | 225237 | 1483 |
| 并发调用准入 | 410.6 | 256 | 5 |

复现：`go test ./internal/app -run '^$' -bench '^BenchmarkBuiltin' -benchmem -benchtime=1s`。完整快照仍复制定义以保护共享缓存；复制在准入锁外完成，工具名称查询不再构造 Schema。

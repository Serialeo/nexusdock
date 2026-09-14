#!/usr/bin/env python3
"""NexusDock HTTP contract definitions used by scripts/check-contracts.py."""

from __future__ import annotations

from typing import Any


def scalar(kind: str | list[str], description: str, **extra: Any) -> dict[str, Any]:
    return {"type": kind, "description": description, **extra}


def enum(description: str, values: list[str]) -> dict[str, Any]:
    return scalar("string", description, enum=values)


def ref(name: str) -> dict[str, Any]:
    return {"$ref": f"#/components/schemas/{name}"}


def array(description: str, items: dict[str, Any]) -> dict[str, Any]:
    return scalar("array", description, items=items)


def obj(
    description: str,
    properties: dict[str, Any],
    required: tuple[str, ...] = (),
    *,
    additional: bool | dict[str, Any] = False,
) -> dict[str, Any]:
    value: dict[str, Any] = {
        "type": "object",
        "description": description,
        "additionalProperties": additional,
        "properties": properties,
    }
    if required:
        value["required"] = list(required)
    return value


ID = scalar("string", "全局唯一标识符。", format="uuid")
TIMESTAMP = scalar("string", "RFC 3339 UTC 时间。", format="date-time")
VERSION = scalar("integer", "资源乐观锁版本，从 1 开始。", minimum=1)

def build_schemas() -> dict[str, dict[str, Any]]:
    schemas: dict[str, dict[str, Any]] = {}
    schemas["BuiltinCapability"] = obj("AgentDock 节点拥有的可选内置工具组，与外部 MCP 分离。", {
        "id": scalar("string", "工具组。", enum=["browser", "acp"]),
        "provided": scalar("boolean", "当前发行包是否提供。"),
        "enabled": scalar("boolean", "节点持久化的用户选择。"),
        "ready": scalar("boolean", "后端是否就绪。"),
        "available": scalar("boolean", "provided、enabled、ready 且不在切换中。"),
        "transitioning": scalar("boolean", "正在切换，拒绝新调用。"),
        "reason": scalar("string", "不可用或切换中的原因。"),
        "tools": array("本组工具归属。", scalar("string", "工具名。")),
    }, ("id", "provided", "enabled", "ready", "available", "transitioning", "reason", "tools"))
    schemas["BuiltinUpdate"] = obj("只修改一个指定节点的用户选择。", {
        "id": scalar("string", "工具组。", enum=["browser", "acp"]),
        "enabled": scalar("boolean", "用户选择；不会授予 Deployment 权限。"),
    }, ("id", "enabled"))
    schemas["BuiltinSnapshot"] = obj("AgentDock 实时返回的状态，不在 Nexus 缓存或回放配置。", {
        "ok": scalar("boolean", "请求成功。"),
        "source": scalar("string", "配置真源。", enum=["agentdock"]),
        "node_id": scalar("string", "目标节点。"),
        "builtins": array("实时内置能力状态。", ref("BuiltinCapability")),
    }, ("ok", "source", "node_id", "builtins"))
    schemas["JsonObject"] = obj("通用结构化对象。", {}, additional=True)
    schemas["ErrorDetail"] = obj(
        "字段级错误详情。",
        {
            "field": scalar("string", "字段路径。"),
            "reason": scalar("string", "稳定原因标识。"),
            "message": scalar("string", "可读错误说明。"),
        },
        ("reason", "message"),
    )
    schemas["ErrorResponse"] = obj(
        "统一错误响应。",
        {
            "code": scalar("string", "稳定错误码。"),
            "message": scalar("string", "面向调用方的错误说明。"),
            "request_id": scalar("string", "请求关联 ID。"),
            "details": array("可选字段级错误。", ref("ErrorDetail")),
        },
        ("code", "message", "request_id"),
    )
    schemas["LegacyErrorEnvelope"] = obj(
        "Nexus 与浏览器接口使用的错误信封。",
        {
            "ok": scalar("boolean", "固定为 false。"),
            "request_id": scalar("string", "请求关联 ID。"),
            "error": obj(
                "错误详情。",
                {
                    "code": scalar("string", "稳定错误码。"),
                    "message": scalar("string", "可读错误说明。"),
                },
                ("code", "message"),
            ),
        },
        ("ok", "request_id", "error"),
    )
    schemas["RuntimeErrorEnvelope"] = obj(
        "AgentDock Runtime 不可用或拒绝请求时的错误信封。",
        {
            "ok": scalar("boolean", "固定为 false。"),
            "available": scalar("boolean", "固定为 false，表示目标 Runtime 当前不可用。"),
            "source": scalar("string", "错误来源，固定为 agentdock-runtime-api。"),
            "request_id": scalar("string", "请求关联 ID。"),
            "error": obj(
                "稳定 Nexus 错误及可选的上游错误码。",
                {
                    "code": scalar("string", "Nexus 稳定错误码。"),
                    "message": scalar("string", "可读错误说明。"),
                    "upstream_code": scalar("string", "AgentDock Runtime 返回的原始错误码。"),
                    "category": scalar("string", "AgentDock Runtime 返回的错误分类。"),
                    "retryable": scalar("boolean", "AgentDock Runtime 是否标记该错误可重试。"),
                    "details": obj("AgentDock Runtime 返回的结构化错误详情。", {}, additional=True),
                },
                ("code", "message"),
            ),
        },
        ("ok", "available", "source", "request_id", "error"),
    )
    schemas["OperationOK"] = obj(
        "无附加数据的成功响应。",
        {"ok": scalar("boolean", "固定为 true。")},
        ("ok",),
    )
    schemas["AuthStatusResponse"] = obj(
        "管理员认证初始化状态。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "initialized": scalar("boolean", "管理员凭据是否已初始化。"),
        },
        ("ok", "initialized"),
    )
    schemas["AuthLoginRequest"] = obj(
        "管理员登录凭据。",
        {
            "username": scalar("string", "管理员用户名。", minLength=1),
            "password": scalar("string", "管理员密码。", minLength=1, maxLength=1024),
            "remember_me": scalar("boolean", "是否创建最长 30 天的记住登录会话。"),
        },
        ("username", "password"),
    )
    schemas["WebSession"] = obj(
        "已脱敏的管理员浏览器会话。",
        {
            "id": scalar("string", "会话 ID。"),
            "user_id": scalar("string", "管理员用户 ID。"),
            "username": scalar("string", "管理员用户名。"),
            "display_name": scalar("string", "管理员显示名称。"),
            "remember_me": scalar("boolean", "是否为记住登录会话。"),
            "ip_prefix": scalar("string", "脱敏后的客户端网络前缀。"),
            "user_agent_summary": scalar("string", "脱敏后的客户端摘要。"),
            "created_at": TIMESTAMP,
            "last_seen_at": TIMESTAMP,
            "idle_expires_at": TIMESTAMP,
            "absolute_expires_at": TIMESTAMP,
            "must_change_password": scalar("boolean", "是否必须先更新管理员密码。"),
            "csrf_token": scalar("string", "仅当前会话返回的 CSRF Token。"),
            "current": scalar("boolean", "是否为当前浏览器会话。"),
        },
        (
            "id", "user_id", "username", "display_name", "remember_me", "ip_prefix",
            "user_agent_summary", "created_at", "last_seen_at", "idle_expires_at",
            "absolute_expires_at", "must_change_password",
        ),
    )
    schemas["WebSessionResponse"] = obj(
        "当前或新创建的管理员浏览器会话。",
        {"ok": scalar("boolean", "请求是否成功。"), "session": ref("WebSession")},
        ("ok", "session"),
    )
    schemas["WebSessionListResponse"] = obj(
        "管理员活动浏览器会话列表。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "items": array("活动浏览器会话。", ref("WebSession")),
        },
        ("ok", "items"),
    )
    schemas["AuthCredentialUpdateRequest"] = obj(
        "管理员密码更新请求。",
        {
            "current": scalar("string", "当前密码。", minLength=1, maxLength=1024),
            "next": scalar("string", "符合策略的新密码。", minLength=12, maxLength=1024),
        },
        ("current", "next"),
    )
    schemas["AuthCredentialUpdateResponse"] = obj(
        "管理员密码更新结果。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "reauthenticate": scalar("boolean", "是否必须重新登录。"),
        },
        ("ok", "reauthenticate"),
    )
    schemas["WebSessionRevokeOthersResponse"] = obj(
        "撤销其他浏览器会话的结果。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "revoked": scalar("integer", "已撤销的会话数量。", minimum=0),
        },
        ("ok", "revoked"),
    )
    schemas["HealthResponse"] = obj(
        "服务健康状态。",
        {
            "ok": scalar("boolean", "服务是否健康。"),
            "service": scalar("string", "服务名称。"),
        },
        ("ok", "service"),
    )
    schemas["SystemStatus"] = obj(
        "Nexus 系统与数据存储状态。",
        {
            "ok": scalar("boolean", "系统是否健康。"),
            "service": scalar("string", "服务名称，固定为 nexusdock。"),
            "database": scalar("string", "SQLite 健康状态。"),
            "schema_version": scalar("integer", "数据库 Schema 版本。", minimum=0),
            "nexus_data_dir": scalar("string", "Nexus 系统状态目录。"),
            "recall_repo_dir": scalar("string", "Recall Git Markdown 仓库目录。"),
        },
        ("ok", "service", "database", "schema_version", "nexus_data_dir", "recall_repo_dir"),
    )
    schemas["RecallEntry"] = obj(
        "Markdown 召回条目。",
        {
            "path": scalar("string", "召回相对路径。"),
            "content": scalar("string", "Markdown 或文本内容。"),
            "size_bytes": scalar("integer", "内容字节数。", minimum=0),
            "modified_at": TIMESTAMP,
        },
        ("path",),
    )
    schemas["RecallFileEntry"] = obj(
        "Recall 仓库中的文件或目录条目。",
        {
            "path": scalar("string", "Recall 相对路径。"),
            "name": scalar("string", "文件或目录名。"),
            "type": enum("条目类型。", ["file", "directory"]),
            "size_bytes": scalar("integer", "条目字节数。", minimum=0),
            "modified": TIMESTAMP,
        },
        ("path", "name", "type"),
    )
    schemas["RecallRecord"] = obj(
        "读取或写入后的完整 Recall 文本记录。",
        {
            "path": scalar("string", "Recall 相对路径。"),
            "content": scalar("string", "包含 Frontmatter 的完整文本。"),
            "body": scalar("string", "移除 Frontmatter 后的正文。"),
            "frontmatter": obj(
                "解析后的 Frontmatter 字符串字段。",
                {},
                additional=scalar("string", "Frontmatter 字段值。"),
            ),
            "size_bytes": scalar("integer", "文本字节数。", minimum=0),
        },
        ("path", "content", "body", "frontmatter", "size_bytes"),
    )
    schemas["RecallWriteRequest"] = obj(
        "创建或覆盖 Recall 文本记录。",
        {
            "path": scalar("string", "Recall 相对路径；PATCH 时由路径参数覆盖。", minLength=1),
            "content": scalar("string", "Markdown 或文本内容。"),
            "type": scalar("string", "可选的 Recall 类型。"),
            "scope": enum("Recall 作用域。", ["profile", "global", "project", "device", "agent", "ops", "inbox"]),
            "status": enum("Recall 状态。", ["inbox", "active", "verified", "stale", "archived", "rejected", "conflicted", "unverified", "deprecated"]),
            "project": scalar("string", "项目标识。"),
            "device": scalar("string", "设备标识。"),
            "agent": scalar("string", "Agent 标识。"),
            "skill": scalar("string", "Skill 标识。"),
            "source": scalar("string", "信息来源。"),
            "confidence": enum("可信度。", ["unknown", "low", "medium", "high"]),
            "verified_at": TIMESTAMP,
            "verification_run_id": scalar("string", "验证运行 ID。"),
            "source_device": scalar("string", "验证来源设备。"),
            "source_agent": scalar("string", "验证来源 Agent。"),
            "tags": array("Recall 标签。", scalar("string", "标签。")),
            "confirmed": scalar("boolean", "写入受保护目录时的确认标记。"),
            "overwrite": scalar("boolean", "是否覆盖已有文件。"),
        },
        ("content",),
    )
    schemas["RecallRecordResponse"] = obj(
        "Recall 读取或写入结果。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "recall": ref("RecallRecord"),
        },
        ("ok", "recall"),
    )
    schemas["RecallWritePreviewResponse"] = obj(
        "Recall 写入预检结果；只执行真实写入会使用的路径、内容和覆盖校验，不产生持久化副作用。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "path": scalar("string", "规范化后的 Recall 相对路径。"),
            "proposed_content": scalar("string", "真实写入时会持久化的规范化内容。"),
            "overwrite": scalar("boolean", "预检使用的覆盖语义。"),
            "dry_run": scalar("boolean", "固定为 true，表示未执行持久化写入。"),
            "confirmed": scalar("boolean", "请求携带的确认标记；预检本身不要求确认。"),
        },
        ("ok", "path", "proposed_content", "overwrite", "dry_run", "confirmed"),
    )
    schemas["RecallSearchResult"] = obj(
        "Recall 关键词搜索命中。",
        {
            "path": scalar("string", "Recall 相对路径。"),
            "title": scalar("string", "Markdown 标题。"),
            "snippet": scalar("string", "命中位置附近的文本片段。"),
            "frontmatter": obj(
                "命中文档 Frontmatter。",
                {},
                additional=scalar("string", "Frontmatter 字段值。"),
            ),
            "matched_terms": array("命中的查询词。", scalar("string", "查询词。")),
            "matched_fields": array("命中的文档字段。", scalar("string", "字段名。")),
        },
        ("path", "snippet", "frontmatter"),
    )
    schemas["RecallContextIndexRequest"] = obj(
        "为 agentdock_context 构造无查询的紧凑 Recall 启动索引。",
        {
            "project": scalar("string", "项目标识；为空时只返回全局可用条目。"),
            "max_bytes": scalar("integer", "索引 items 的最大 JSON 字节预算；无效值使用服务默认值。", minimum=2, maximum=32000),
        },
    )
    schemas["RecallContextIndexItem"] = obj(
        "紧凑 Recall 启动索引中的单条候选。",
        {
            "kind": enum("候选类别。", ["profile", "project", "verified_fact", "runbook", "card"]),
            "path": scalar("string", "可直接交给 recall_read 的 Recall 相对路径。"),
            "title": scalar("string", "用于判断相关性的短标题。"),
            "summary": scalar("string", "仅对可安全独立理解的类别返回的短摘要。"),
            "keywords": array("用于路由到完整文档的关键词。", scalar("string", "关键词。")),
            "aliases": array("用于路由到完整文档的别名。", scalar("string", "别名。")),
            "tags": array("卡片标签。", scalar("string", "标签。")),
            "card_type": scalar("string", "经验卡片类型。"),
            "status": enum("进入启动索引的 Recall 生命周期状态。", ["active", "verified"]),
            "confidence": enum("候选可信度。", ["low", "medium", "high"]),
            "verified_at": TIMESTAMP,
        },
        ("kind", "path"),
    )
    schemas["RecallContextIndex"] = obj(
        "按类别配额和总字节预算裁剪后的 Recall 启动索引。",
        {
            "project": scalar("string", "规范化后的项目标识。"),
            "items": array("按 profile、project、verified_fact、runbook、card 顺序排列的候选。", ref("RecallContextIndexItem")),
            "total_bytes": scalar("integer", "items 实际 JSON 编码字节数。", minimum=0),
            "max_bytes": scalar("integer", "items 允许的最大 JSON 字节预算。", minimum=1, maximum=32000),
            "truncated": scalar("boolean", "是否因预算或候选不可读而省略了条目。"),
            "omitted_count": scalar("integer", "因预算或候选不可读而省略的条目数。", minimum=0),
        },
        ("items", "total_bytes", "max_bytes", "truncated"),
    )
    schemas["RecallContextIndexResponse"] = obj(
        "紧凑 Recall 启动索引响应。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "context_index": ref("RecallContextIndex"),
        },
        ("ok", "context_index"),
    )
    schemas["RecallCardRequest"] = obj(
        "捕获或写入一张可复用 Recall 卡片。",
        {
            "title": scalar("string", "卡片标题。", minLength=1),
            "content": scalar("string", "卡片正文。"),
            "summary": scalar("string", "content 为空时使用的摘要正文。"),
            "type": enum("卡片类型。", ["preference", "runbook", "bug_pattern", "deploy_note", "project_trap", "architecture", "decision", "anti_pattern"]),
            "scope": enum("卡片作用域。", ["global", "project", "device"]),
            "project": scalar("string", "项目标识；为空时使用 global。"),
            "status": enum("卡片状态。", ["inbox", "active", "verified", "stale", "archived", "rejected", "conflicted", "unverified", "deprecated"]),
            "confidence": enum("卡片可信度。", ["unknown", "low", "medium", "high"]),
            "tags": array("卡片标签。", scalar("string", "标签。")),
            "source": scalar("string", "卡片信息来源。"),
            "evidence": scalar("string", "验证卡片内容的证据。"),
            "boundary": scalar("string", "卡片适用边界。"),
            "path": scalar("string", "可选的 recall/managed/cards/ 自定义路径。"),
            "confirmed": scalar("boolean", "真实写入时必须为 true。"),
            "overwrite": scalar("boolean", "是否覆盖同路径卡片。"),
            "allow_warnings": scalar("boolean", "是否在已审阅后接受规范警告。"),
            "max_results": scalar("integer", "捕获阶段相似项最大数量。", minimum=1, maximum=50),
        },
        ("title",),
    )
    schemas["RecallCard"] = obj(
        "规范化后的 Recall 卡片。",
        {
            "title": scalar("string", "卡片标题。"),
            "content": scalar("string", "卡片正文。"),
            "type": scalar("string", "卡片类型。"),
            "scope": scalar("string", "卡片作用域。"),
            "project": scalar("string", "项目标识。"),
            "status": scalar("string", "卡片状态。"),
            "confidence": scalar("string", "卡片可信度。"),
            "tags": array("规范化标签。", scalar("string", "标签。")),
            "source": scalar("string", "信息来源。"),
            "evidence": scalar("string", "验证证据。"),
            "boundary": scalar("string", "适用边界。"),
            "path": scalar("string", "最终 Recall 相对路径。"),
        },
        ("title", "content", "type", "scope", "project", "status", "confidence", "source", "path"),
    )
    schemas["RecallCardSummary"] = obj(
        "经验卡片列表使用的只读摘要。",
        {
            "path": scalar("string", "卡片 Recall 相对路径。"),
            "title": scalar("string", "从卡片正文标题解析出的展示标题。"),
            "project": scalar("string", "项目标识。"),
            "status": scalar("string", "卡片生命周期状态。"),
            "card_type": scalar("string", "卡片类型。"),
            "scope": scalar("string", "卡片作用域。"),
            "confidence": scalar("string", "卡片可信度。"),
            "tags": array("卡片标签。", scalar("string", "标签。")),
            "size_bytes": scalar("integer", "卡片文件大小。", minimum=0),
            "modified": scalar("string", "卡片文件最近修改时间。"),
        },
        ("path", "title", "project", "status", "card_type"),
    )
    schemas["RecallCardCaptureResponse"] = obj(
        "卡片写入前的规范化、风险提示和去重计划。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "card": ref("RecallCard"),
            "warnings": array("需人工审阅的规范警告。", scalar("string", "警告。")),
            "capture_plan": ref("JsonObject"),
            "similar_results": array("关键词相似的已有卡片。", ref("RecallSearchResult")),
            "similar_count": scalar("integer", "相似卡片数量。", minimum=0),
        },
        ("ok", "card", "capture_plan", "similar_count"),
    )
    schemas["RecallCardWriteResponse"] = obj(
        "卡片及其 Recall 文件写入结果。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "card": ref("RecallCard"),
            "warnings": array("已接受的规范警告。", scalar("string", "警告。")),
            "recall": ref("RecallRecord"),
            "index_policy": scalar("string", "卡片索引策略说明。"),
        },
        ("ok", "card", "recall", "index_policy"),
    )
    schemas["RecallCardListResponse"] = obj(
        "Recall 卡片文件列表和展示摘要。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "entries": array("卡片目录下的文件和目录。", ref("RecallFileEntry")),
            "cards": array("只读卡片摘要。", ref("RecallCardSummary")),
            "count": scalar("integer", "卡片数量。", minimum=0),
            "prefix": scalar("string", "固定为 recall/managed/cards。"),
        },
        ("ok", "entries", "cards", "count", "prefix"),
    )
    schemas["RecallCardSearchRequest"] = obj(
        "在 Recall 卡片目录中执行关键词搜索。",
        {
            "query": scalar("string", "关键词查询。", minLength=1),
            "max_results": scalar("integer", "最大结果数。", minimum=1, maximum=200),
        },
        ("query",),
    )
    schemas["RecallCardSearchResponse"] = obj(
        "Recall 卡片关键词搜索结果。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "query": scalar("string", "原始查询。"),
            "results": array("搜索命中。", ref("RecallSearchResult")),
            "count": scalar("integer", "结果数量。", minimum=0),
            "prefix": scalar("string", "固定为 recall/managed/cards。"),
        },
        ("ok", "query", "results", "count", "prefix"),
    )
    schemas["RuntimeSecretUpdate"] = obj(
        "运行时 AI 密钥更新指令；服务永不回显密钥明文。",
        {
            "action": enum("密钥更新动作：保留、替换或清空。", ["keep", "replace", "clear"]),
            "value": scalar("string", "仅 action=replace 时提交的新密钥。", maxLength=65536),
        },
        ("action",),
    )
    schemas["RuntimePromptUpdate"] = obj(
        "Stage 3 System Prompt 更新指令；keep 保留覆盖，replace 保存自定义值，reset 恢复 bundled default。",
        {
            "action": enum("Prompt 更新动作。", ["keep", "replace", "reset"]),
            "value": scalar("string", "仅 action=replace 时提交的完整 System Prompt；服务端按 UTF-8 bytes 限制为 64 KiB。", maxLength=65536, **{"x-maxBytes": 65536}),
        },
        ("action",),
    )
    schemas["EmbeddingSettingsInput"] = obj(
        "向量检索与 Embedding 运行时配置。",
        {
            "enabled": scalar("boolean", "是否启用向量检索。"),
            "endpoint": scalar("string", "OpenAI 兼容 Embeddings HTTP(S) 地址。"),
            "model": scalar("string", "Embedding 模型名称。"),
            "timeout_seconds": scalar("integer", "Embedding 请求超时秒数。", minimum=1, maximum=300),
            "api_key": ref("RuntimeSecretUpdate"),
        },
        ("enabled", "endpoint", "model", "timeout_seconds", "api_key"),
    )
    schemas["Stage3SettingsInput"] = obj(
        "Nexus Stage 3 外部模型运行时配置。",
        {
            "enabled": scalar("boolean", "是否启用 Stage 3 辅助进化。"),
            "endpoint": scalar("string", "OpenAI 兼容 Chat Completions HTTP(S) 地址。"),
            "model": scalar("string", "Stage 3 模型名称。"),
            "timeout_seconds": scalar("integer", "模型请求超时秒数。", minimum=1, maximum=300),
            "interval_minutes": scalar("integer", "Stage 3 执行间隔分钟数。", minimum=60, maximum=10080),
            "api_key": ref("RuntimeSecretUpdate"),
            "system_prompt": ref("RuntimePromptUpdate"),
            "review_node_id": scalar("string", "用于多节点或无唯一 evidence 来源 Stage 3 候选的显式审阅 AgentDock node_id；空值表示不派发此类候选。"),
        },
        ("enabled", "endpoint", "model", "timeout_seconds", "interval_minutes", "api_key"),
    )
    schemas["RuntimeAISettingsUpdateRequest"] = obj(
        "保存 Nexus Stage 3 与向量检索运行时配置。",
        {
            "embedding": ref("EmbeddingSettingsInput"),
            "stage3": ref("Stage3SettingsInput"),
            "expected_revision": scalar("string", "可选 AI 设置 revision；If-Match 也可提供。", pattern="^rev-[0-9]+$"),
        },
        ("embedding", "stage3"),
    )
    schemas["EmbeddingSettingsView"] = obj(
        "已脱敏的向量检索运行时配置。",
        {
            "enabled": scalar("boolean", "是否启用向量检索。"),
            "endpoint": scalar("string", "当前 Embeddings 地址。"),
            "model": scalar("string", "当前 Embedding 模型。"),
            "timeout_seconds": scalar("integer", "请求超时秒数。", minimum=1, maximum=300),
            "api_key_configured": scalar("boolean", "是否已配置 API Key；不返回明文。"),
        },
        ("enabled", "endpoint", "model", "timeout_seconds", "api_key_configured"),
    )
    schemas["Stage3SettingsView"] = obj(
        "已脱敏的 Stage 3 外部模型运行时配置。",
        {
            "enabled": scalar("boolean", "是否启用 Stage 3。"),
            "endpoint": scalar("string", "当前模型地址。"),
            "model": scalar("string", "当前模型名称。"),
            "timeout_seconds": scalar("integer", "请求超时秒数。", minimum=1, maximum=300),
            "interval_minutes": scalar("integer", "执行间隔分钟数。", minimum=60, maximum=10080),
            "api_key_configured": scalar("boolean", "是否已配置 API Key；不返回明文。"),
            "configured": scalar("boolean", "Stage 3 是否具备运行所需的启用、地址和模型配置。"),
            "system_prompt": scalar("string", "当前实际用于 Stage 3 model request 的 effective System Prompt。", maxLength=65536, **{"x-maxBytes": 65536}),
            "bundled_system_prompt": scalar("string", "当前版本内置的 bundled default System Prompt。", maxLength=65536, **{"x-maxBytes": 65536}),
            "system_prompt_source": enum("effective System Prompt 来源。", ["bundled_default", "custom"]),
            "system_prompt_updated_at": scalar("string", "自定义 System Prompt 最近保存时间；bundled default 时省略。", format="date-time"),
            "review_node_id": scalar("string", "当前显式 Stage 3 审阅 AgentDock node_id；空值表示无 fallback。"),
        },
        ("enabled", "endpoint", "model", "timeout_seconds", "interval_minutes", "api_key_configured", "configured", "system_prompt", "bundled_system_prompt", "system_prompt_source", "review_node_id"),
    )
    schemas["RuntimeAISettingsView"] = obj(
        "Nexus 当前已脱敏的 AI 与向量检索配置。",
        {
            "embedding": ref("EmbeddingSettingsView"),
            "stage3": ref("Stage3SettingsView"),
            "persisted": scalar("boolean", "是否已保存 SQLite 覆盖配置；false 表示当前来自环境变量或默认值。"),
            "revision": scalar("string", "运行时 AI 设置资源 revision。", pattern="^rev-[0-9]+$"),
            "updated_at": scalar("string", "最近一次持久化更新时间。", format="date-time"),
        },
        ("embedding", "stage3", "persisted", "revision"),
    )
    schemas["RuntimeAISettingsResponse"] = obj(
        "运行时 AI 设置响应。",
        {"ok": scalar("boolean", "请求是否成功。"), "settings": ref("RuntimeAISettingsView")},
        ("ok", "settings"),
    )
    schemas["MCPAccessTokenResponse"] = obj(
        "Nexus MCP 固定访问 Token。仅管理员接口返回明文。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "token": scalar("string", "用于 Nexus /mcp 的 Bearer Token。", minLength=64, maxLength=64),
        },
        ("ok", "token"),
    )
    schemas["MCPSettingsResponse"] = obj(
        "Nexus MCP 接入设置。管理员接口会同时返回固定访问 Token 与 Apps UI 开关状态。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "token": scalar("string", "用于 Nexus /mcp 的 Bearer Token。", minLength=64, maxLength=64),
            "mcp_apps_enabled": scalar("boolean", "是否发布 MCP Apps 资源及 App 专用工具；关闭会下架 App 专用工具并拒绝续接投递，保留模型工具的可见性限制。"),
            "persisted": scalar("boolean", "是否已保存 SQLite 覆盖配置；false 表示当前来自环境变量或默认值。"),
            "updated_at": scalar("string", "最近一次持久化更新时间。", format="date-time"),
        },
        ("ok", "token", "mcp_apps_enabled", "persisted"),
    )
    schemas["MCPSettingsUpdateRequest"] = obj(
        "更新 Nexus MCP Apps UI 开关。",
        {"mcp_apps_enabled": scalar("boolean", "是否启用 MCP Apps UI。")},
        ("mcp_apps_enabled",),
    )
    schemas["RuntimeAIConnectionTestResponse"] = obj(
        "Stage 3 或向量服务的脱敏连接测试结果。",
        {
            "ok": scalar("boolean", "连接测试是否成功。"),
            "target": scalar("string", "测试目标。", enum=["stage3", "embedding"]),
            "model": scalar("string", "测试使用的模型名称。"),
            "message": scalar("string", "脱敏后的测试结果说明。"),
            "latency_ms": scalar("integer", "测试耗时毫秒。", minimum=0),
        },
        ("ok", "target", "message", "latency_ms"),
    )

    schemas["EmbeddingIndexSummary"] = obj(
        "Recall 向量索引摘要。",
        {
            "model": scalar("string", "索引使用的嵌入模型。"),
            "dimension": scalar("integer", "向量维度。", minimum=0),
            "count": scalar("integer", "索引文档数量。", minimum=0),
            "updated_at": TIMESTAMP,
        },
        ("model", "count", "updated_at"),
    )
    schemas["EmbeddingStatusResponse"] = obj(
        "Recall 嵌入服务及索引状态。",
        {
            "ok": scalar("boolean", "状态查询是否成功。"),
            "enabled": scalar("boolean", "嵌入服务是否启用。"),
            "configured": scalar("boolean", "嵌入端点是否配置。"),
            "model": scalar("string", "当前嵌入模型。"),
            "endpoint": scalar("string", "当前嵌入端点。"),
            "index_path": scalar("string", "本地索引文件路径。"),
            "index": ref("EmbeddingIndexSummary"),
            "reachable": scalar("boolean", "嵌入端点是否可达。"),
            "reason": scalar("string", "未启用原因。"),
            "error": scalar("string", "最近一次探测错误。"),
        },
        ("ok", "enabled", "configured"),
    )
    schemas["EmbeddingReindexRequest"] = obj(
        "重建 Recall 向量索引。",
        {
            "prefix": scalar("string", "要索引的 Recall 路径前缀。"),
            "max_entries": scalar("integer", "最多索引条目数。", minimum=1, maximum=2000),
        },
    )
    schemas["EmbeddingReindexResponse"] = obj(
        "Recall 向量索引重建结果。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "enabled": scalar("boolean", "嵌入服务是否启用。"),
            "model": scalar("string", "索引使用的嵌入模型。"),
            "endpoint": scalar("string", "嵌入端点。"),
            "index_path": scalar("string", "索引文件路径。"),
            "prefix": scalar("string", "已索引路径前缀。"),
            "count": scalar("integer", "索引文档数量。", minimum=0),
            "dimension": scalar("integer", "向量维度。", minimum=0),
            "updated_at": TIMESTAMP,
        },
        ("ok", "enabled", "model", "index_path", "prefix", "count", "updated_at"),
    )
    schemas["EmbeddingSearchRequest"] = obj(
        "使用 Recall 向量索引执行语义搜索。",
        {
            "query": scalar("string", "语义查询。", minLength=1),
            "prefix": scalar("string", "可选的 Recall 路径前缀。"),
            "max_results": scalar("integer", "最大结果数。", minimum=1, maximum=50),
        },
        ("query",),
    )
    schemas["EmbeddingSearchHit"] = obj(
        "Recall 向量搜索命中。",
        {
            "path": scalar("string", "Recall 相对路径。"),
            "title": scalar("string", "Markdown 标题。"),
            "score": scalar("number", "余弦相似度。", minimum=-1, maximum=1),
            "snippet": scalar("string", "文档文本片段。"),
            "frontmatter": obj(
                "命中文档 Frontmatter。",
                {},
                additional=scalar("string", "Frontmatter 字段值。"),
            ),
        },
        ("path", "score", "snippet"),
    )
    schemas["EmbeddingSearchResponse"] = obj(
        "Recall 向量搜索结果。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "enabled": scalar("boolean", "嵌入服务是否启用。"),
            "model": scalar("string", "查询使用的嵌入模型。"),
            "query": scalar("string", "原始语义查询。"),
            "results": array("按相似度降序排列的命中。", ref("EmbeddingSearchHit")),
            "count": scalar("integer", "返回命中数量。", minimum=0),
            "index": ref("EmbeddingIndexSummary"),
        },
        ("ok", "enabled", "model", "query", "results", "count", "index"),
    )
    schemas["PrivateNoteSummary"] = obj(
        "私密笔记安全元数据；不包含正文或正文片段。",
        {
            "path": scalar("string", "notes/ 下的私密笔记相对路径。"),
            "encrypted_path": scalar("string", "对应 age 密文相对路径。"),
            "category": scalar("string", "私密笔记分类。"),
            "title": scalar("string", "私密笔记标题。"),
            "summary": scalar("string", "人工维护的安全简介。"),
            "tags": array("安全标签。", scalar("string", "标签。")),
            "updated_at": TIMESTAMP,
            "contains_secret": scalar("boolean", "正文是否被标记为含敏感信息。"),
            "score": scalar("integer", "元数据检索匹配分数。", minimum=0),
        },
        ("path", "encrypted_path", "contains_secret"),
    )
    schemas["PrivateNoteSearchRequest"] = obj(
        "私密笔记元数据检索请求。",
        {
            "query": scalar("string", "仅匹配标题、简介、标签、分类和路径的查询。"),
            "max_results": scalar("integer", "最大结果数。", minimum=1, maximum=100),
        },
        ("query",),
    )
    schemas["PrivateNoteSearchResponse"] = obj(
        "私密笔记元数据检索结果。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "action": scalar("string", "固定为 search。"),
            "query": scalar("string", "原始查询。"),
            "root": scalar("string", "Nexus 私密笔记根目录。"),
            "results": array("仅含安全元数据的结果。", ref("PrivateNoteSummary")),
            "count": scalar("integer", "结果数。", minimum=0),
            "metadata_only": scalar("boolean", "固定为 true。"),
            "policy": scalar("string", "检索安全策略说明。"),
        },
        ("ok", "action", "query", "root", "results", "count", "metadata_only"),
    )
    schemas["PrivateNoteReadRequest"] = obj(
        "显式读取私密笔记正文的请求。",
        {
            "path": scalar("string", "notes/ 下的私密笔记相对路径。"),
            "max_bytes": scalar("integer", "最大返回字节数。", minimum=1, maximum=1048576),
        },
        ("path",),
    )
    schemas["PrivateNoteReadResponse"] = obj(
        "显式私密笔记明文读取结果。",
        {
            "action": scalar("string", "固定为 read。"),
            "root": scalar("string", "Nexus 私密笔记根目录。"),
            "path": scalar("string", "明文相对路径。"),
            "encrypted_path": scalar("string", "age 密文相对路径。"),
            "content": scalar("string", "私密笔记明文，仅显式读取接口返回。"),
            "truncated": scalar("boolean", "正文是否被截断。"),
            "contains_secret": scalar("boolean", "正文是否被标记为含敏感信息。"),
        },
        ("action", "root", "path", "encrypted_path", "content", "truncated", "contains_secret"),
    )
    schemas["PrivateNoteWriteRequest"] = obj(
        "创建或覆盖私密笔记请求。",
        {
            "path": scalar("string", "可选的 notes/ 相对路径。"),
            "category": scalar("string", "未传 path 时使用的分类。"),
            "title": scalar("string", "标题，也可用于生成路径。"),
            "summary": scalar("string", "可安全检索的人工简介。"),
            "tags": array("可安全检索的标签。", scalar("string", "标签。")),
            "content": scalar("string", "私密笔记正文。"),
            "confirmed": scalar("boolean", "真实写入必须为 true。"),
            "overwrite": scalar("boolean", "是否覆盖已有笔记。"),
        },
        ("content", "confirmed"),
    )
    schemas["PrivateNoteWriteResponse"] = obj(
        "私密笔记明文与 age 密文原子写入结果。",
        {
            "action": scalar("string", "固定为 write。"),
            "root": scalar("string", "Nexus 私密笔记根目录。"),
            "path": scalar("string", "明文相对路径。"),
            "encrypted_path": scalar("string", "age 密文相对路径。"),
            "written": scalar("boolean", "明文是否写入。"),
            "encrypted": scalar("boolean", "密文是否写入。"),
            "algorithm": scalar("string", "加密算法。"),
        },
        ("action", "root", "path", "encrypted_path", "written", "encrypted", "algorithm"),
    )
    schemas["PrivateNoteDeleteRequest"] = obj(
        "同时删除私密笔记明文和 age 密文的请求。",
        {
            "path": scalar("string", "notes/ 下的私密笔记相对路径。"),
            "confirmed": scalar("boolean", "真实删除必须为 true。"),
        },
        ("path", "confirmed"),
    )
    schemas["PrivateNoteDeleteResponse"] = obj(
        "私密笔记明文与 age 密文删除结果。",
        {
            "action": scalar("string", "固定为 delete。"),
            "root": scalar("string", "Nexus 私密笔记根目录。"),
            "path": scalar("string", "明文相对路径。"),
            "encrypted_path": scalar("string", "age 密文相对路径。"),
            "deleted_plaintext": scalar("boolean", "明文是否删除。"),
            "deleted_encrypted": scalar("boolean", "密文是否删除。"),
        },
        ("action", "root", "path", "encrypted_path", "deleted_plaintext", "deleted_encrypted"),
    )
    schemas["PrivateNoteStatusRequest"] = obj(
        "读取私密笔记状态或安全元数据列表。",
        {"action": enum("状态动作。", ["check", "list"])},
        ("action",),
    )
    schemas["PrivateNoteStatusResponse"] = obj(
        "私密笔记加密和 Git 忽略状态。",
        {
            "action": scalar("string", "执行的状态动作。"),
            "root": scalar("string", "Nexus 私密笔记根目录。"),
            "notes": array("私密笔记安全元数据。", ref("PrivateNoteSummary")),
            "count": scalar("integer", "列表项数。", minimum=0),
            "notes_count": scalar("integer", "明文笔记数。", minimum=0),
            "missing_encrypted": array("缺失的 age 密文路径。", scalar("string", "密文路径。")),
            "encrypted_backup_ok": scalar("boolean", "每条明文是否都有密文。"),
            "plaintext_git_ignored": scalar("boolean", "notes/ 是否由仓库规则忽略。"),
            "keys_git_ignored": scalar("boolean", ".keys/ 是否由仓库规则忽略。"),
        },
        ("action", "root", "encrypted_backup_ok", "plaintext_git_ignored", "keys_git_ignored"),
    )
    schemas["PrivateNoteMaintenanceRequest"] = obj(
        "私密笔记加密维护请求。",
        {"action": enum("维护动作。", ["init", "init-encryption", "sync-encrypted", "encrypt-all"])},
        ("action",),
    )
    schemas["PrivateNoteMaintenanceResponse"] = obj(
        "私密笔记加密初始化或全量重加密结果。",
        {
            "action": scalar("string", "执行的维护动作。"),
            "root": scalar("string", "Nexus 私密笔记根目录。"),
            "recipient": scalar("string", "age 公钥接收者。"),
            "identity_created": scalar("boolean", "是否新建 identity。"),
            "encrypted_count": scalar("integer", "生成的密文数量。", minimum=0),
            "algorithm": scalar("string", "加密算法。"),
        },
        ("action", "root", "algorithm"),
    )
    schemas["AgentDockNode"] = obj(
        "与 Nexus 配对的一台 AgentDock 节点。",
        {
            "id": scalar("string", "Nexus 分配的稳定节点 ID。"),
            "device_id": scalar("string", "AgentDock 生成的稳定设备 ID。"),
            "name": scalar("string", "节点显示名称。", minLength=1, maxLength=100),
            "enabled": scalar("boolean", "节点是否允许连接和 Runtime 请求。"),
            "full_access": scalar("boolean", "Node 级 Project Full Access。开启时覆盖 Deployment 细粒度执行权限，但不改变 Project Folder/Prompt 边界。"),
            "version": scalar("string", "最近握手的 AgentDock 版本。"),
            "protocol_version": scalar("string", "节点连接协议版本。"),
            "os": scalar("string", "节点操作系统。"),
            "arch": scalar("string", "节点架构。"),
            "capabilities": array("节点报告的工具能力。", scalar("string", "工具名。")),
            "tool_contract_hash": scalar("string", "节点工具契约摘要。"),
            "online": scalar("boolean", "节点是否保持反向连接。"),
            "last_seen_at": TIMESTAMP,
            "created_at": TIMESTAMP,
            "updated_at": TIMESTAMP,
        },
        ("id", "device_id", "name", "enabled", "full_access", "capabilities", "online", "created_at", "updated_at"),
    )
    schemas["AgentDockPairingCode"] = obj(
        "短时单次 AgentDock 配对码。",
        {
            "code": scalar("string", "单次配对码。", writeOnly=True),
            "expires_at": TIMESTAMP,
        },
        ("code", "expires_at"),
    )
    schemas["AgentDockPairingCodeResponse"] = obj(
        "AgentDock 配对码响应。",
        {"ok": scalar("boolean", "请求是否成功。"), "pairing": ref("AgentDockPairingCode")},
        ("ok", "pairing"),
    )
    schemas["AgentDockPairRequest"] = obj(
        "AgentDock 使用单次码换取固定设备身份。",
        {
            "code": scalar("string", "单次配对码。", minLength=1),
            "device_id": scalar("string", "AgentDock 本地生成的稳定设备 ID。", minLength=8, maxLength=128),
            "name": scalar("string", "节点显示名称。", minLength=1, maxLength=100),
        },
        ("code", "device_id", "name"),
    )
    schemas["AgentDockNodeUpdateRequest"] = obj(
        "更新 AgentDock 节点显示信息、启用状态或 Project Full Access。",
        {
            "name": scalar("string", "节点显示名称。", minLength=1, maxLength=100),
            "enabled": scalar("boolean", "节点是否启用。"),
            "full_access": scalar("boolean", "是否对该 Node 的 Project Target 开启 Full Access。与 Project Folder 独立。"),
        },
    )
    schemas["RuntimeTaskStep"] = obj(
        "当前任务步骤摘要。",
        {"id": scalar("string", "步骤标识。"), "title": scalar("string", "步骤标题。"), "status": scalar("string", "步骤状态。")},
        ("id", "title", "status"),
    )
    schemas["RuntimeTaskSummary"] = obj(
        "目标 AgentDock 的任务摘要。",
        {
            "id": scalar("string", "任务标识。"),
            "title": scalar("string", "任务标题。"),
            "goal": scalar("string", "任务目标。"),
            "status": scalar("string", "任务状态。"),
            "phase": scalar("string", "当前阶段。"),
            "review_status": scalar("string", "复审状态。"),
            "summary": scalar("string", "任务摘要。"),
            "blocker": scalar("string", "阻塞原因。"),
            "current_step": ref("RuntimeTaskStep"),
            "completed_step_count": scalar("integer", "已完成步骤数。", minimum=0),
            "updated_at": scalar("string", "任务更新时间，RFC3339。"),
            "created_at": scalar("string", "任务创建时间，RFC3339。"),
            "template_id": scalar("string", "工作流模板标识。"),
            "template_version": scalar("string", "工作流模板版本。"),
            **{name: scalar("integer", description, minimum=0) for name, description in {
                "condition_count": "条件数量。", "step_count": "步骤数量。", "attempt_count": "尝试数量。", "event_count": "事件数量。",
            }.items()},
            "file_name": scalar("string", "详情和删除接口使用的任务标识，与 id 相同。"),
        },
        ("id", "title", "goal", "status", "phase", "review_status", "completed_step_count", "updated_at", "created_at", "condition_count", "step_count", "attempt_count", "event_count", "file_name"),
    )
    schemas["RuntimeTaskCounts"] = obj(
        "满足搜索和时间范围的任务数量；忽略 status 筛选和分页。",
        {name: scalar("integer", description, minimum=0) for name, description in {
            "all": "所有状态合计。", "active": "进行中任务数量。", "blocked": "阻塞任务数量。", "completed": "已完成任务数量。",
        }.items()},
        ("all", "active", "blocked", "completed"),
    )
    schemas["RuntimeTaskListResponse"] = obj(
        "由目标 AgentDock 在所有筛选完成后分页的任务列表；旧节点缺少分页元数据时返回明确错误。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "node_id": scalar("string", "稳定 Nexus node_id。"),
            "items": array("本页任务。", ref("RuntimeTaskSummary")),
            "count": scalar("integer", "本页返回数量，等于 items 长度。", minimum=0),
            "total": scalar("integer", "满足 status、搜索和时间范围的全部任务数。", minimum=0),
            "offset": scalar("integer", "请求的分页偏移；超出 total 时仍回显原值。", minimum=0),
            "limit": scalar("integer", "请求的单页上限。", minimum=1, maximum=200),
            "has_more": scalar("boolean", "当前页之后是否还有符合筛选的任务。"),
            "counts": ref("RuntimeTaskCounts"),
            "source": enum("数据来源。", ["agentdock-runtime-api"]),
        },
        ("ok", "node_id", "items", "count", "total", "offset", "limit", "has_more", "counts", "source"),
    )
    schemas["RuntimeFileBrowseEntry"] = obj(
        "Node 文件浏览器返回的一个直接子项；不包含文件正文。",
        {
            "name": scalar("string", "目录项基础名称。"),
            "path": scalar("string", "由目标 AgentDock 返回的完整规范路径；客户端不应自行拼接路径。"),
            "type": enum("目录项类型。", ["directory", "file", "symlink", "other"]),
            "size_bytes": scalar("integer", "目录项大小。", minimum=0),
            "modified": TIMESTAMP,
            "is_hidden": scalar("boolean", "目标平台是否将该目录项视为隐藏项。"),
        },
        ("name", "path", "type", "size_bytes", "modified", "is_hidden"),
    )
    schemas["RuntimeFileBrowseResponse"] = obj(
        "指定 AgentDock 节点的一层只读目录浏览结果。结果按页返回，不读取文件正文。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "node_id": scalar("string", "稳定 Nexus node_id。"),
            "path": scalar("string", "目标 AgentDock 解析后的当前目录完整规范路径。"),
            "parent_path": scalar("string", "父目录完整规范路径；文件系统根目录为空字符串。"),
            "entries": array("本页直接子项。", ref("RuntimeFileBrowseEntry")),
            "offset": scalar("integer", "当前分页偏移。", minimum=0, maximum=10000),
            "limit": scalar("integer", "当前页请求上限。", minimum=1, maximum=500),
            "next_offset": scalar("integer", "存在下一页时的分页偏移。", minimum=1),
            "truncated": scalar("boolean", "当前页之后是否仍有可浏览子项。"),
            "partial": scalar("boolean", "是否因目标平台权限或瞬时文件变化跳过了部分子项。"),
            "skipped_count": scalar("integer", "本次扫描中无法读取元数据而跳过的直接子项数量。", minimum=0),
            "source": enum("数据来源。", ["agentdock-runtime-api"]),
        },
        ("ok", "node_id", "path", "parent_path", "entries", "offset", "limit", "truncated", "partial", "skipped_count", "source"),
    )
    schemas["RuntimeSkillEnvironmentEntry"] = obj(
        "Skill 隔离环境变量元数据；不返回值。",
        {
            "key": scalar("string", "环境变量名。"),
            "configured": scalar("boolean", "当前值是否为非空字符串。"),
        },
        ("key", "configured"),
    )
    schemas["RuntimeSkillEnvironmentResponse"] = obj(
        "指定节点已安装 Skill 的隔离环境元数据。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "node_id": scalar("string", "稳定 Nexus node_id。"),
            "skill_id": scalar("string", "Skill 名称。"),
            "source": enum("可管理 Skill 来源。", ["agentdock-api"]),
            "items": array("环境变量键与配置状态。", ref("RuntimeSkillEnvironmentEntry")),
            "count": scalar("integer", "环境变量数量。", minimum=0),
        },
        ("ok", "node_id", "skill_id", "source", "items", "count"),
    )
    schemas["RuntimeSkillManageRequest"] = obj(
        "管理目标 AgentDock 上已安装 Skill 的版本选择或隔离环境。",
        {
            "action": enum("管理动作。", ["activate", "rollback", "env_set", "env_unset"]),
            "version": scalar("string", "activate 时必需的已安装版本。"),
            "key": scalar("string", "env_set/env_unset 时必需的环境变量名。"),
            "value": scalar("string", "仅 env_set 时存在；允许显式空字符串，且不会在响应中回显。"),
        },
        ("action",),
    )
    schemas["RuntimeSkillManageResponse"] = obj(
        "Skill 节点设置操作结果；不包含环境变量值。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "node_id": scalar("string", "稳定 Nexus node_id。"),
            "skill_id": scalar("string", "Skill 名称。"),
            "source": enum("可管理 Skill 来源。", ["agentdock-api"]),
            "action": enum("完成的操作。", ["activate", "rollback", "env_set", "env_unset"]),
            "key": scalar("string", "环境变量名。"),
            "configured": scalar("boolean", "env_set 后值是否非空。"),
            "removed": scalar("boolean", "env_unset 是否移除了键。"),
            "result": ref("JsonObject"),
        },
        ("ok", "node_id", "skill_id", "source", "action"),
    )
    schemas["DeploymentPermissions"] = obj(
        "Project 执行的有效能力。full_access 来自 Node；Full Access 覆盖细粒度权限，但仍受 Node OS 权限与本地停止机制约束；working_folder 不是沙箱。",
        {
            "full_access": scalar("boolean", "Node Full Access 是否生效。true 时允许该 Node 已暴露的全部 Project 执行能力。"),
            "files": enum("Full Access 关闭时的内置文件能力。", ["none", "read_only", "read_write"]),
            "shell": scalar("boolean", "Full Access 关闭时是否允许新命令执行。"),
            "browser": scalar("boolean", "Full Access 关闭时是否允许 Browser 能力。"),
            "dynamic_mcp": scalar("boolean", "Full Access 关闭时是否允许动态 MCP 能力。"),
            "acp": scalar("boolean", "Full Access 关闭时是否允许 ACP 能力。"),
        },
        ("full_access", "files", "shell", "browser", "dynamic_mcp", "acp"),
    )
    schemas["Project"] = obj(
        "Nexus Project desired state。Project ID 稳定，不随重命名变化。",
        {
            "id": scalar("string", "稳定 Project ID。"),
            "name": scalar("string", "Project 显示名称。"),
            "orchestration_policy": scalar("string", "多 Target 编排说明；不会扩展硬权限。"),
            "revision": scalar("string", "Project CAS revision。", pattern="^rev-[0-9]+$"),
            "enabled": scalar("boolean", "是否允许建立新的 Project 工作会话。"),
            "created_at": TIMESTAMP,
            "updated_at": TIMESTAMP,
            "deployments": array("可选内嵌 Deployment 列表。", ref("ProjectDeployment")),
        },
        ("id", "name", "orchestration_policy", "revision", "enabled", "created_at", "updated_at"),
    )
    schemas["ProjectDeployment"] = obj(
        "一个 Project 在一个 AgentDock Node 上的 desired/applied Deployment 状态。",
        {
            "id": scalar("string", "稳定 Deployment ID；不等同于 node_id。"),
            "project_id": scalar("string", "所属 Project ID。"),
            "node_id": scalar("string", "绑定的 AgentDock Node ID。"),
            "working_folder": scalar("string", "可选 Project Folder。非空时必须是目标 Node 原生绝对路径；空字符串表示使用 Node AgentDock 默认 cwd 且不自动发现 Project AGENTS.md。"),
            "role": scalar("string", "用户可读环境角色标签，不是授权角色。"),
            "purpose": scalar("string", "该环境用途说明。"),
            "permissions": ref("DeploymentPermissions"),
            "desired_revision": scalar("string", "Nexus 当前 desired revision。", pattern="^rev-[0-9]+$"),
            "applied_revision": scalar("string", "AgentDock 已确认应用的 revision；尚未应用时为空字符串。", pattern="^(|rev-[0-9]+)$"),
            "enabled": scalar("boolean", "是否允许该 Deployment 成为新 Target。"),
            "apply_status": enum("desired/applied 同步状态。", ["draft", "pending", "applied", "failed", "disabled"]),
            "last_error": scalar("string", "最近一次 apply/remove 安全错误；无错误时省略。"),
            "created_at": TIMESTAMP,
            "updated_at": TIMESTAMP,
        },
        ("id", "project_id", "node_id", "working_folder", "role", "purpose", "permissions", "desired_revision", "applied_revision", "enabled", "apply_status", "created_at", "updated_at"),
    )
    schemas["ProjectCreateRequest"] = obj(
        "创建 Project。",
        {
            "name": scalar("string", "Project 显示名称。", minLength=1),
            "orchestration_policy": scalar("string", "可选编排说明。"),
            "enabled": scalar("boolean", "是否启用；省略时默认为 true。"),
        },
        ("name",),
    )
    schemas["ProjectUpdateRequest"] = obj(
        "使用 CAS 更新 Project。",
        {
            "expected_revision": scalar("string", "调用方看到的 Project revision。", pattern="^rev-[0-9]+$"),
            "name": scalar("string", "Project 显示名称。", minLength=1),
            "orchestration_policy": scalar("string", "编排说明。"),
            "enabled": scalar("boolean", "是否启用。"),
        },
        ("expected_revision", "name", "enabled"),
    )
    schemas["ProjectDeleteRequest"] = obj(
        "使用 CAS 删除 Project desired state；不会删除 Node working_folder。",
        {"expected_revision": scalar("string", "调用方看到的 Project revision。", pattern="^rev-[0-9]+$")},
        ("expected_revision",),
    )
    schemas["ProjectResponse"] = obj(
        "单个 Project 响应。",
        {"ok": scalar("boolean", "请求是否成功。"), "project": ref("Project")},
        ("ok", "project"),
    )
    schemas["ProjectListResponse"] = obj(
        "Project 列表响应。",
        {"ok": scalar("boolean", "请求是否成功。"), "projects": array("Projects。", ref("Project")), "count": scalar("integer", "Project 数量。", minimum=0)},
        ("ok", "projects", "count"),
    )
    schemas["ProjectDeleteResponse"] = obj(
        "Project desired state 删除结果。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "project_id": scalar("string", "已删除 Project ID。"),
            "deleted": scalar("boolean", "固定为 true。"),
            "deployment_removals": scalar("integer", "为 Node 暂存的 Deployment 撤销数量。", minimum=0),
        },
        ("ok", "project_id", "deleted", "deployment_removals"),
    )
    schemas["ProjectDeploymentCreateRequest"] = obj(
        "创建 Project Deployment desired state。",
        {
            "node_id": scalar("string", "绑定 AgentDock Node ID。", minLength=1),
            "working_folder": scalar("string", "可选 Project Folder；空字符串表示使用 Node 默认 cwd 且没有 Project Prompt 搜索根。"),
            "role": scalar("string", "可选环境角色标签。"),
            "purpose": scalar("string", "可选用途说明。"),
            "permissions": ref("DeploymentPermissions"),
            "enabled": scalar("boolean", "是否启用；省略时默认为 true。"),
        },
        ("node_id", "working_folder", "permissions"),
    )
    schemas["ProjectDeploymentUpdateRequest"] = obj(
        "使用 CAS 更新 Project Deployment desired state。",
        {
            "expected_revision": scalar("string", "调用方看到的 desired revision。", pattern="^rev-[0-9]+$"),
            "working_folder": scalar("string", "可选 Project Folder；允许清空为 Node 默认 cwd 模式。"),
            "role": scalar("string", "环境角色标签。"),
            "purpose": scalar("string", "用途说明。"),
            "permissions": ref("DeploymentPermissions"),
            "enabled": scalar("boolean", "是否启用。"),
        },
        ("expected_revision", "working_folder", "permissions", "enabled"),
    )
    schemas["ProjectDeploymentDeleteRequest"] = obj(
        "使用 CAS 移除 Deployment 授权；不会删除 working_folder。",
        {"expected_revision": scalar("string", "调用方看到的 desired revision。", pattern="^rev-[0-9]+$")},
        ("expected_revision",),
    )
    schemas["ProjectDeploymentResponse"] = obj(
        "单个 Project Deployment 响应。",
        {"ok": scalar("boolean", "请求是否成功。"), "deployment": ref("ProjectDeployment")},
        ("ok", "deployment"),
    )
    schemas["ProjectDeploymentListResponse"] = obj(
        "Project Deployment 列表响应。",
        {"ok": scalar("boolean", "请求是否成功。"), "deployments": array("Deployments。", ref("ProjectDeployment")), "count": scalar("integer", "Deployment 数量。", minimum=0)},
        ("ok", "deployments", "count"),
    )
    schemas["ProjectDeploymentDeleteResponse"] = obj(
        "Deployment desired state 删除结果。",
        {"ok": scalar("boolean", "请求是否成功。"), "deployment_id": scalar("string", "已删除 Deployment ID。"), "deleted": scalar("boolean", "固定为 true。")},
        ("ok", "deployment_id", "deleted"),
    )
    schemas["ProjectPromptSource"] = obj(
        "一个适用的 AGENTS.md Project Prompt 来源。",
        {
            "path": scalar("string", "相对 Deployment working_folder 的 AGENTS.md 路径。"),
            "scope": scalar("string", "该规则文件生效的 Project 相对子目录；根目录为 .。"),
            "sha256": scalar("string", "规则正文 SHA-256 revision。", pattern="^sha256:[0-9a-f]{64}$"),
            "bytes": scalar("integer", "UTF-8 正文字节数。", minimum=0),
            "content": scalar("string", "完整规则正文；不会用摘要替代。"),
        },
        ("path", "scope", "sha256", "bytes", "content"),
    )
    schemas["ProjectPrompt"] = obj(
        "Node 对一个 cwd 完整解析出的 Project Prompt 链。",
        {
            "prompt_revision": scalar("string", "完整来源链 revision。", pattern="^sha256:[0-9a-f]{64}$"),
            "complete": scalar("boolean", "是否完整读取且通过预算校验。"),
            "bytes": scalar("integer", "完整来源链总字节数。", minimum=0),
            "sources": array("root 到 cwd 的完整适用 AGENTS.md 来源。", ref("ProjectPromptSource")),
        },
        ("prompt_revision", "complete", "bytes", "sources"),
    )
    schemas["ProjectPromptLoadResult"] = obj(
        "Project Prompt 预览结果。",
        {
            "deployment_id": scalar("string", "Deployment ID。"),
            "cwd_rel": scalar("string", "Node 规范化后的 Project 相对 cwd。"),
            "prompt": ref("ProjectPrompt"),
        },
        ("deployment_id", "cwd_rel", "prompt"),
    )
    schemas["ProjectPromptWriteRequest"] = obj(
        "只允许管理面在指定 Project scope 创建或 CAS 更新真实 AGENTS.md。",
        {
            "scope": scalar("string", "Deployment working_folder 内的相对子目录；根目录为 .。"),
            "content": scalar("string", "完整 UTF-8 AGENTS.md 正文，最大 64 KiB。", maxLength=65536),
            "expected_sha256": scalar("string", "更新已有规则时必须匹配的来源 SHA-256；创建时为空或省略。"),
            "create": scalar("boolean", "true 表示仅当 AGENTS.md 不存在时创建。"),
        },
        ("scope", "content", "create"),
    )
    schemas["ProjectPromptWriteResult"] = obj(
        "写入后重新解析得到的 Project Prompt 与实际来源。",
        {
            "deployment_id": scalar("string", "Deployment ID。"),
            "scope": scalar("string", "Node 规范化后的写入 scope。"),
            "source": ref("ProjectPromptSource"),
            "prompt": ref("ProjectPrompt"),
            "created": scalar("boolean", "是否创建了新 AGENTS.md。"),
        },
        ("deployment_id", "scope", "source", "prompt", "created"),
    )
    schemas["ProjectPromptPreviewResponse"] = obj(
        "Project Prompt 管理预览响应；不等同于 MCP Host 已接收 context。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "project_id": scalar("string", "Project ID。"),
            "deployment": ref("ProjectDeployment"),
            "working_folder": scalar("string", "目标 Node 真实 working_folder。"),
            "result": ref("ProjectPromptLoadResult"),
        },
        ("ok", "project_id", "deployment", "working_folder", "result"),
    )
    schemas["ProjectPromptWriteResponse"] = obj(
        "Project Prompt AGENTS.md 写入响应。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "project_id": scalar("string", "Project ID。"),
            "deployment": ref("ProjectDeployment"),
            "working_folder": scalar("string", "目标 Node 真实 working_folder。"),
            "result": ref("ProjectPromptWriteResult"),
        },
        ("ok", "project_id", "deployment", "working_folder", "result"),
    )
    schemas["ProjectContextDeliveryEvidence"] = obj(
        "管理员控制台可观察的 Project Context delivery 证据。returned 只表示 Nexus 已返回完整模型可见 tool result；host_consumed 只表示同一 MCP Host 显式确认该 revision。",
        {
            "work_session_id": scalar("string", "WorkSession ID。"),
            "target_id": scalar("string", "Target ID；session-level project_open delivery 时省略。"),
            "context_revision": scalar("string", "该 delivery 证据对应的 context revision。"),
            "status": enum("真实 delivery 状态。", ["returned", "host_consumed"]),
            "returned_at": TIMESTAMP,
            "host_consumed_at": TIMESTAMP,
            "updated_at": TIMESTAMP,
        },
        ("work_session_id", "context_revision", "status", "returned_at", "updated_at"),
    )
    schemas["ProjectWorkSession"] = obj(
        "管理员控制台可观察的 Project WorkSession；MCP owner 与 request hash 不序列化。",
        {
            "work_session_id": scalar("string", "WorkSession ID。"),
            "project_id": scalar("string", "Project ID。"),
            "client_request_id": scalar("string", "MCP Host 提供的幂等 request ID。"),
            "project_revision": scalar("string", "该会话当前绑定的 Project revision。"),
            "status": enum("WorkSession 状态。", ["preparing", "ready", "running", "completed", "partial", "failed", "cancelled"]),
            "context_revision": scalar("string", "当前聚合 Project context revision；未准备完成时可为空。"),
            "created_at": TIMESTAMP,
            "updated_at": TIMESTAMP,
            "delivery": ref("ProjectContextDeliveryEvidence"),
        },
        ("work_session_id", "project_id", "client_request_id", "project_revision", "status", "context_revision", "created_at", "updated_at"),
    )
    schemas["ProjectPromptScopeRevision"] = obj(
        "Target 已交付过的 Prompt scope/revision。",
        {"scope": scalar("string", "Project 相对 scope。"), "prompt_revision": scalar("string", "Prompt revision。")},
        ("scope", "prompt_revision"),
    )
    schemas["ProjectWorkTargetValue"] = obj(
        "WorkSession 内一个 server-bound Target 的公开观察状态。",
        {
            "target_id": scalar("string", "Target ID。"),
            "work_session_id": scalar("string", "WorkSession ID。"),
            "project_id": scalar("string", "Project ID。"),
            "deployment_id": scalar("string", "Deployment ID。"),
            "node_id": scalar("string", "AgentDock Node ID。"),
            "cwd_rel": scalar("string", "该 Target 独立 Project 相对 cwd。"),
            "deployment_revision": scalar("string", "Target 绑定的 applied Deployment revision。"),
            "context_revision": scalar("string", "Target context revision。"),
            "status": enum("Target 状态。", ["preparing", "ready", "running", "idle", "unavailable", "context_error", "revoked"]),
            "permissions": ref("DeploymentPermissions"),
            "prompt": ref("ProjectPrompt"),
        },
        ("target_id", "work_session_id", "project_id", "deployment_id", "node_id", "cwd_rel", "deployment_revision", "context_revision", "status", "permissions", "prompt"),
    )
    schemas["ProjectWorkTarget"] = obj(
        "Target 及其 Prompt delivery scopes；只读用于管理员观察。",
        {
            "target": ref("ProjectWorkTargetValue"),
            "prompt_scopes": array("Target 已交付 Prompt scopes。", ref("ProjectPromptScopeRevision")),
            "last_error": scalar("string", "Target 最近准备/撤销错误。"),
            "created_at": TIMESTAMP,
            "updated_at": TIMESTAMP,
            "delivery": ref("ProjectContextDeliveryEvidence"),
        },
        ("target", "prompt_scopes", "created_at", "updated_at"),
    )
    schemas["ProjectWorkSessionListResponse"] = obj(
        "Project 范围的 WorkSession 只读列表。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "project_id": scalar("string", "Project ID。"),
            "sessions": array("WorkSessions。", ref("ProjectWorkSession")),
            "count": scalar("integer", "WorkSession 数量。", minimum=0),
        },
        ("ok", "project_id", "sessions", "count"),
    )
    schemas["ProjectWorkSessionDetailResponse"] = obj(
        "Project WorkSession 与 Target 只读详情。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "project_id": scalar("string", "Project ID。"),
            "session": ref("ProjectWorkSession"),
            "targets": array("该 WorkSession 的 Targets。", ref("ProjectWorkTarget")),
        },
        ("ok", "project_id", "session", "targets"),
    )
    schemas["AgentDockNodeListResponse"] = obj(
        "AgentDock 节点列表。",
        {
            "ok": scalar("boolean", "请求是否成功。"),
            "nodes": array("AgentDock 节点。", ref("AgentDockNode")),
            "count": scalar("integer", "节点数量。", minimum=0),
        },
        ("ok", "nodes", "count"),
    )
    schemas["AgentDockNodeResponse"] = obj(
        "单个 AgentDock 节点响应。",
        {"ok": scalar("boolean", "请求是否成功。"), "node": ref("AgentDockNode")},
        ("ok", "node"),
    )
    return schemas

def response(schema: dict[str, Any], description: str = "成功。") -> dict[str, Any]:
    return {"description": description, "content": {"application/json": {"schema": schema}}}


def build_openapi(schemas: dict[str, Any]) -> dict[str, Any]:
    error = response({"oneOf": [ref("ErrorResponse"), ref("LegacyErrorEnvelope"), ref("RuntimeErrorEnvelope")]}, "错误。")
    generic = ref("JsonObject")

    def path_param(name: str, description: str, *, uuid: bool = True) -> dict[str, Any]:
        schema: dict[str, Any] = {"type": "string"}
        if uuid:
            schema["format"] = "uuid"
        return {"name": name, "in": "path", "required": True, "description": description, "schema": schema}

    def query_param(
        name: str,
        description: str,
        kind: str = "string",
        *,
        required: bool = False,
        **schema_options: Any,
    ) -> dict[str, Any]:
        return {
            "name": name,
            "in": "query",
            "required": required,
            "description": description,
            "schema": {"type": kind, **schema_options},
        }

    parameters = {
        "SessionId": path_param("sessionID", "浏览器 Session ID。", uuid=False),
        "RecallPath": path_param("path", "URL 编码后的召回相对路径。", uuid=False),
        "EvolutionId": path_param("evolutionID", "Evolution 生命周期记录 ID。", uuid=False),
        "RuntimeNodeId": path_param("nodeID", "Nexus 中登记的 AgentDock 节点 ID。", uuid=False),
        "RuntimeTaskId": path_param("taskID", "AgentDock Runtime task ID。", uuid=False),
        "RuntimeSkillSource": path_param("source", "AgentDock Runtime skill source。", uuid=False),
        "RuntimeSkillId": path_param("skillID", "AgentDock Runtime skill ID。", uuid=False),
        "RuntimeSkillFilePath": path_param("filePath", "AgentDock Runtime Skill 文件相对路径。", uuid=False),
        "RuntimeMCPName": path_param("name", "AgentDock 动态 MCP 服务名称。", uuid=False),
        "ProjectId": path_param("projectID", "稳定 Project ID。", uuid=False),
        "DeploymentId": path_param("deploymentID", "稳定 Deployment ID。", uuid=False),
        "WorkSessionId": path_param("workSessionID", "Project WorkSession ID。", uuid=False),
        "WorkflowTemplateId": path_param("templateID", "Nexus 工作流模板 ID。", uuid=False),
        "WorkflowTemplateVersion": path_param("version", "Nexus 工作流模板版本。", uuid=False),
    }

    def body(schema: dict[str, Any] = generic) -> dict[str, Any]:
        return {"required": True, "content": {"application/json": {"schema": schema}}}

    def ok(schema: dict[str, Any] = generic, description: str = "成功。") -> dict[str, Any]:
        return response(schema, description)

    def operation(
        operation_id: str,
        summary: str,
        *,
        success: dict[str, Any] | None = None,
        request: dict[str, Any] | None = None,
        params: list[dict[str, Any]] | None = None,
        success_code: str = "200",
        additional_success: dict[str, dict[str, Any]] | None = None,
    ) -> dict[str, Any]:
        responses = {success_code: success or ok(), "400": error, "401": error, "403": error, "404": error, "409": error, "412": error}
        if additional_success:
            responses.update(additional_success)
        value: dict[str, Any] = {
            "operationId": operation_id,
            "summary": summary,
            "responses": responses,
        }
        if request is not None:
            value["requestBody"] = request
        if params:
            value["parameters"] = params
        return value

    p = lambda name: {"$ref": f"#/components/parameters/{name}"}
    q = query_param
    no_content = {"description": "已接受，无响应体。"}
    paths: dict[str, Any] = {
        "/health": {"get": operation("getHealth", "读取服务健康状态", success=ok(ref("HealthResponse")))},
        "/v1/system/status": {"get": operation("getSystemStatus", "读取 Nexus 与 SQLite 状态", success=ok(ref("SystemStatus")))},
        "/v1/settings/ai": {
            "get": operation("getRuntimeAISettings", "读取已脱敏的 Stage 3 与向量检索配置", success=ok(ref("RuntimeAISettingsResponse"))),
            "put": operation(
                "updateRuntimeAISettings",
                "保存并立即应用 Stage 3 与向量检索配置",
                request=body(ref("RuntimeAISettingsUpdateRequest")),
                success=ok(ref("RuntimeAISettingsResponse")),
            ),
        },
        "/v1/settings/mcp": {
            "get": operation("getMCPSettings", "读取 Nexus MCP 接入设置", success=ok(ref("MCPSettingsResponse"))),
            "put": operation(
                "updateMCPSettings",
                "更新 Nexus MCP Apps UI 开关",
                request=body(ref("MCPSettingsUpdateRequest")),
                success=ok(ref("MCPSettingsResponse")),
            ),
        },
        "/v1/settings/mcp-token": {
            "get": operation("getMCPAccessToken", "读取 Nexus MCP 固定访问 Token", success=ok(ref("MCPAccessTokenResponse")))
        },
        "/v1/settings/mcp-token/reset": {
            "post": operation("resetMCPAccessToken", "重置 Nexus MCP 固定访问 Token 并立即使旧 Token 失效", success=ok(ref("MCPAccessTokenResponse")))
        },
        "/v1/settings/ai/test/stage3": {
            "post": operation("testStage3Connection", "使用已保存配置测试 Stage 3 模型连接", success=ok(ref("RuntimeAIConnectionTestResponse")))
        },
        "/v1/settings/ai/test/embedding": {
            "post": operation("testEmbeddingConnection", "使用已保存配置测试 Embedding 服务连接", success=ok(ref("RuntimeAIConnectionTestResponse")))
        },
        "/v1/auth/status": {
            "get": operation("getAuthStatus", "读取管理员初始化状态", success=ok(ref("AuthStatusResponse")))
        },
        "/v1/auth/login": {
            "post": operation(
                "login",
                "登录管理员会话",
                request=body(ref("AuthLoginRequest")),
                success=ok(ref("WebSessionResponse")),
            )
        },
        "/v1/auth/session": {
            "get": operation("getCurrentSession", "读取当前浏览器会话", success=ok(ref("WebSessionResponse")))
        },
        "/v1/auth/logout": {
            "post": operation("logout", "退出当前浏览器会话", success=ok(ref("OperationOK")))
        },
        "/v1/auth/credential": {
            "post": operation(
                "updateCredential",
                "更新管理员凭据",
                request=body(ref("AuthCredentialUpdateRequest")),
                success=ok(ref("AuthCredentialUpdateResponse")),
            )
        },
        "/v1/auth/sessions": {
            "get": operation("listSessions", "列出管理员浏览器会话", success=ok(ref("WebSessionListResponse")))
        },
        "/v1/auth/sessions/{sessionID}": {
            "delete": operation(
                "revokeSession",
                "撤销指定浏览器会话",
                params=[p("SessionId")],
                success=ok(ref("OperationOK")),
            )
        },
        "/v1/auth/sessions/logout-others": {
            "post": operation(
                "logoutOtherSessions",
                "撤销其他浏览器会话",
                success=ok(ref("WebSessionRevokeOthersResponse")),
            )
        },
        "/v1/recall": {
            "get": operation(
                "listRecall",
                "列出召回条目",
                params=[
                    q("prefix", "只列出该 Recall 相对路径前缀下的条目。"),
                    q("max_entries", "最大条目数；无效值使用服务默认值。", "integer", minimum=1),
                ],
            ),
            "post": operation(
                "writeRecall",
                "创建召回条目",
                request=body(ref("RecallWriteRequest")),
                success=ok(ref("RecallRecordResponse")),
            ),
        },
        "/v1/recall/preview": {
            "post": operation(
                "previewRecallWrite",
                "预检 Recall 写入且不持久化",
                request=body(ref("RecallWriteRequest")),
                success=ok(ref("RecallWritePreviewResponse")),
            )
        },
        "/v1/recall/move": {"post": operation("moveRecall", "移动召回条目", request=body())},
        "/v1/recall/search": {"post": operation("searchRecall", "搜索召回内容", request=body())},
        "/v1/recall/context-index": {
            "post": operation(
                "buildRecallContextIndex",
                "构造紧凑 Recall 启动索引",
                request=body(ref("RecallContextIndexRequest")),
                success=ok(ref("RecallContextIndexResponse")),
            )
        },
        "/v1/recall/cards": {
            "get": operation(
                "listRecallCards",
                "列出 Recall 卡片",
                params=[q("max_entries", "最大卡片条目数；无效值使用服务默认值。", "integer", minimum=1)],
                success=ok(ref("RecallCardListResponse")),
            ),
            "post": operation(
                "writeRecallCard",
                "确认并写入 Recall 卡片",
                request=body(ref("RecallCardRequest")),
                success=ok(ref("RecallCardWriteResponse")),
            ),
        },
        "/v1/recall/cards/capture": {
            "post": operation(
                "captureRecallCard",
                "规范化 Recall 卡片并生成写入前审阅计划",
                request=body(ref("RecallCardRequest")),
                success=ok(ref("RecallCardCaptureResponse")),
            )
        },
        "/v1/recall/cards/search": {
            "post": operation(
                "searchRecallCards",
                "在 Recall 卡片目录执行关键词搜索",
                request=body(ref("RecallCardSearchRequest")),
                success=ok(ref("RecallCardSearchResponse")),
            )
        },
        "/v1/evolution/lifecycle": {
            "get": operation("listEvolutionLifecycle", "只读列出 Evolution 生命周期记录")
        },
        "/v1/evolution/lifecycle/{evolutionID}": {
            "get": operation("getEvolutionLifecycle", "只读读取 Evolution 生命周期记录详情", params=[p("EvolutionId")])
        },
        "/v1/embeddings/status": {
            "get": operation("getEmbeddingStatus", "读取 Recall 嵌入服务和索引状态", success=ok(ref("EmbeddingStatusResponse")))
        },
        "/v1/embeddings/reindex": {
            "post": operation(
                "reindexEmbeddings",
                "重建 Recall 向量索引",
                request=body(ref("EmbeddingReindexRequest")),
                success=ok(ref("EmbeddingReindexResponse")),
            )
        },
        "/v1/embeddings/search": {
            "post": operation(
                "searchEmbeddings",
                "使用 Recall 向量索引执行语义搜索",
                request=body(ref("EmbeddingSearchRequest")),
                success=ok(ref("EmbeddingSearchResponse")),
            )
        },
        "/v1/recall/{path}": {
            "get": operation("readRecall", "读取召回条目", params=[p("RecallPath")], success=ok(ref("RecallRecordResponse"))),
            "patch": operation(
                "patchRecall",
                "修改召回条目",
                params=[p("RecallPath")],
                request=body(ref("RecallWriteRequest")),
                success=ok(ref("RecallRecordResponse")),
            ),
            "delete": operation(
                "deleteRecall",
                "删除召回条目",
                params=[p("RecallPath"), q("confirmed", "破坏性删除确认标记。", "boolean")],
            ),
        },
        "/v1/private-notes/search": {
            "post": operation(
                "searchPrivateNotes",
                "只按标题、简介、标签、分类和路径检索私密笔记",
                request=body(ref("PrivateNoteSearchRequest")),
                success=ok(ref("PrivateNoteSearchResponse")),
            )
        },
        "/v1/private-notes/read": {
            "post": operation(
                "readPrivateNote",
                "显式读取一条私密笔记明文",
                request=body(ref("PrivateNoteReadRequest")),
                success=ok(ref("PrivateNoteReadResponse")),
            )
        },
        "/v1/private-notes/write": {
            "post": operation(
                "writePrivateNote",
                "原子写入私密笔记明文与 age 密文",
                request=body(ref("PrivateNoteWriteRequest")),
                success=ok(ref("PrivateNoteWriteResponse")),
            )
        },
        "/v1/private-notes/delete": {
            "post": operation(
                "deletePrivateNote",
                "同时删除私密笔记明文与 age 密文",
                request=body(ref("PrivateNoteDeleteRequest")),
                success=ok(ref("PrivateNoteDeleteResponse")),
            )
        },
        "/v1/private-notes/status": {
            "post": operation(
                "getPrivateNoteStatus",
                "读取私密笔记加密和 Git 忽略状态",
                request=body(ref("PrivateNoteStatusRequest")),
                success=ok(ref("PrivateNoteStatusResponse")),
            )
        },
        "/v1/private-notes/maintenance": {
            "post": operation(
                "maintainPrivateNotes",
                "初始化或重新生成私密笔记 age 密文",
                request=body(ref("PrivateNoteMaintenanceRequest")),
                success=ok(ref("PrivateNoteMaintenanceResponse")),
            )
        },
        "/v1/git/diff": {"get": operation("getGitDiff", "读取召回仓库变更")},
        "/v1/git/log": {
            "get": operation(
                "getGitLog",
                "读取召回仓库提交历史",
                params=[q("limit", "最大提交数量；无效值使用服务默认值。", "integer", minimum=1)],
            )
        },
        "/v1/git/commit": {
            "get": operation(
                "getGitCommit",
                "读取召回仓库提交详情",
                params=[q("hash", "Git 提交哈希。", required=True, minLength=1)],
            ),
            "post": operation("recordGitVersion", "记录当前 Recall 本地版本", request=body()),
        },
        "/v1/projects": {
            "get": operation("listProjects", "列出管理员可管理的 Projects", success=ok(ref("ProjectListResponse"))),
            "post": operation(
                "createProject",
                "创建 Project desired state",
                request=body(ref("ProjectCreateRequest")),
                success=ok(ref("ProjectResponse")),
                success_code="201",
            ),
        },
        "/v1/projects/{projectID}": {
            "get": operation("getProject", "读取 Project desired state", params=[p("ProjectId")], success=ok(ref("ProjectResponse"))),
            "put": operation(
                "updateProject",
                "使用 CAS 更新 Project desired state",
                params=[p("ProjectId")],
                request=body(ref("ProjectUpdateRequest")),
                success=ok(ref("ProjectResponse")),
            ),
            "delete": operation(
                "deleteProject",
                "使用 CAS 删除 Project desired state 并暂存 Node 授权撤销；不会删除 working_folder",
                params=[p("ProjectId")],
                request=body(ref("ProjectDeleteRequest")),
                success=ok(ref("ProjectDeleteResponse")),
            ),
        },
        "/v1/projects/{projectID}/deployments": {
            "get": operation(
                "listProjectDeployments",
                "列出 Project Deployments",
                params=[p("ProjectId")],
                success=ok(ref("ProjectDeploymentListResponse")),
            ),
            "post": operation(
                "createProjectDeployment",
                "创建 Project Deployment desired state；在线 Node 会立即尝试 Bridge v4 apply",
                params=[p("ProjectId")],
                request=body(ref("ProjectDeploymentCreateRequest")),
                success=ok(ref("ProjectDeploymentResponse")),
                success_code="201",
            ),
        },
        "/v1/projects/{projectID}/deployments/{deploymentID}": {
            "get": operation(
                "getProjectDeployment",
                "读取 Project Deployment desired/applied 状态",
                params=[p("ProjectId"), p("DeploymentId")],
                success=ok(ref("ProjectDeploymentResponse")),
            ),
            "put": operation(
                "updateProjectDeployment",
                "使用 CAS 更新 Project Deployment desired state 并尝试 Bridge v4 apply",
                params=[p("ProjectId"), p("DeploymentId")],
                request=body(ref("ProjectDeploymentUpdateRequest")),
                success=ok(ref("ProjectDeploymentResponse")),
            ),
            "delete": operation(
                "deleteProjectDeployment",
                "使用 CAS 删除 Deployment desired state 并撤销 Node 授权；不会删除 working_folder",
                params=[p("ProjectId"), p("DeploymentId")],
                request=body(ref("ProjectDeploymentDeleteRequest")),
                success=ok(ref("ProjectDeploymentDeleteResponse")),
            ),
        },
        "/v1/projects/{projectID}/deployments/{deploymentID}/apply": {
            "post": operation(
                "applyProjectDeployment",
                "重试将当前 Deployment desired revision 通过 Bridge v4 应用到目标 AgentDock",
                params=[p("ProjectId"), p("DeploymentId")],
                success=ok(ref("ProjectDeploymentResponse")),
            ),
        },
        "/v1/projects/{projectID}/deployments/{deploymentID}/prompt": {
            "get": operation(
                "previewProjectPrompt",
                "从目标 AgentDock 读取指定 cwd 的完整 Project Prompt 规则链；这是管理预览，不代表 Host 已接收 context",
                params=[p("ProjectId"), p("DeploymentId"), q("cwd_rel", "Deployment working_folder 内的 Project 相对 cwd；省略时为 .。")],
                success=ok(ref("ProjectPromptPreviewResponse")),
            ),
            "put": operation(
                "writeProjectPromptSource",
                "只在目标 Node Project scope 内创建或 CAS 更新真实 AGENTS.md，并返回重新解析后的完整 Prompt",
                params=[p("ProjectId"), p("DeploymentId")],
                request=body(ref("ProjectPromptWriteRequest")),
                success=ok(ref("ProjectPromptWriteResponse")),
            ),
        },
        "/v1/projects/{projectID}/sessions": {
            "get": operation(
                "listProjectWorkSessions",
                "只读列出该 Project 的 MCP WorkSessions；不暴露 owner binding 或 request hash",
                params=[p("ProjectId"), q("limit", "最大 WorkSession 数量。", "integer", minimum=1, maximum=500)],
                success=ok(ref("ProjectWorkSessionListResponse")),
            ),
        },
        "/v1/projects/{projectID}/sessions/{workSessionID}": {
            "get": operation(
                "getProjectWorkSession",
                "只读读取该 Project WorkSession 与 Targets；不创建 Nexus AI 会话",
                params=[p("ProjectId"), p("WorkSessionId")],
                success=ok(ref("ProjectWorkSessionDetailResponse")),
            ),
        },
        "/v1/runtime/nodes": {
            "get": operation("listAgentDockNodes", "列出 Nexus 管理的 AgentDock 节点", success=ok(ref("AgentDockNodeListResponse"))),
        },
        "/v1/runtime/nodes/pairing-codes": {"post": operation("createAgentDockPairingCode", "生成短时单次 AgentDock 配对码", success=ok(ref("AgentDockPairingCodeResponse")), success_code="201")},
        "/v1/nodes/pair": {"post": operation("pairAgentDockNode", "AgentDock 使用单次码换取 Device Token", request=body(ref("AgentDockPairRequest")), success_code="201")},
        "/v1/nodes/connect": {"get": operation("connectAgentDockNode", "AgentDock 使用 Device Token 升级为反向 WebSocket 连接")},
        "/v1/runtime/nodes/{nodeID}": {
            "get": operation("getAgentDockNode", "读取 AgentDock 节点", params=[p("RuntimeNodeId")], success=ok(ref("AgentDockNodeResponse"))),
            "patch": operation("updateAgentDockNode", "更新 AgentDock 节点", params=[p("RuntimeNodeId")], request=body(ref("AgentDockNodeUpdateRequest")), success=ok(ref("AgentDockNodeResponse"))),
            "delete": operation("deleteAgentDockNode", "删除 AgentDock 节点并撤销 Device Token", params=[p("RuntimeNodeId")]),
        },
        "/v1/runtime/nodes/{nodeID}/overview": {"get": operation("getRuntimeOverview", "读取指定 AgentDock 节点的 Runtime 概览", params=[p("RuntimeNodeId")])},
        "/v1/runtime/nodes/{nodeID}/files": {
            "get": operation(
                "browseRuntimeFiles",
                "分页浏览指定 AgentDock 节点当前目录的直接子项，不读取文件正文",
                params=[
                    p("RuntimeNodeId"),
                    q("path", "目标节点 Host 目录路径；省略时使用 AgentDock 默认目录。"),
                    q("offset", "目录分页偏移。", "integer", minimum=0, maximum=10000),
                    q("limit", "单页直接子项上限。", "integer", minimum=1, maximum=500),
                    q("include_hidden", "是否包含目标平台定义的隐藏项。", "boolean"),
                ],
                success=ok(ref("RuntimeFileBrowseResponse")),
            )
        },
        "/v1/runtime/nodes/{nodeID}/tasks": {
            "get": operation(
                "listRuntimeTasks",
                "在目标 AgentDock 完成状态、搜索及时间筛选后分页列出任务",
                params=[
                    p("RuntimeNodeId"),
                    q("status", "按任务状态过滤；空值或 all 表示不过滤。", enum=["", "all", "active", "completed", "blocked"]),
                    q("q", "在任务 ID、标题、目标、状态、摘要、阻塞原因和当前步骤中搜索。"),
                    q("time_field", "时间筛选字段；默认 updated_at。", enum=["updated_at", "created_at"], default="updated_at"),
                    q("from", "包含此 RFC3339 时间的范围起点；须早于 to。", format="date-time"),
                    q("to", "不包含此 RFC3339 时间的范围终点。", format="date-time"),
                    q("offset", "筛选后的分页偏移，默认 0。", "integer", minimum=0, default=0),
                    q("limit", "单页任务数上限，默认 200。", "integer", minimum=1, maximum=200, default=200),
                ],
                success=ok(ref("RuntimeTaskListResponse")),
                additional_success={"502": error},
            )
        },
        "/v1/runtime/nodes/{nodeID}/tasks/{taskID}": {
            "get": operation("getRuntimeTask", "读取指定 AgentDock 节点的任务详情", params=[p("RuntimeNodeId"), p("RuntimeTaskId")]),
            "delete": operation("deleteRuntimeTask", "删除指定 AgentDock 节点的任务", params=[p("RuntimeNodeId"), p("RuntimeTaskId")]),
        },
        "/v1/runtime/nodes/{nodeID}/skills": {"get": operation("listRuntimeSkills", "列出指定 AgentDock 节点的 Skill", params=[p("RuntimeNodeId")])},
        "/v1/runtime/nodes/{nodeID}/skills/{source}/{skillID}": {"get": operation("getRuntimeSkill", "读取指定 AgentDock 节点的 Skill 详情", params=[p("RuntimeNodeId"), p("RuntimeSkillSource"), p("RuntimeSkillId")])},
        "/v1/runtime/nodes/{nodeID}/skills/{source}/{skillID}/files/{filePath}": {"get": operation("getRuntimeSkillFile", "读取指定 AgentDock 节点的 Skill 文件", params=[p("RuntimeNodeId"), p("RuntimeSkillSource"), p("RuntimeSkillId"), p("RuntimeSkillFilePath")])},
        "/v1/runtime/nodes/{nodeID}/skills/{source}/{skillID}/environment": {
            "get": operation("getRuntimeSkillEnvironment", "读取目标 AgentDock 已安装 Skill 的隔离环境变量元数据", params=[p("RuntimeNodeId"), p("RuntimeSkillSource"), p("RuntimeSkillId")], success=ok(ref("RuntimeSkillEnvironmentResponse")))
        },
        "/v1/runtime/nodes/{nodeID}/skills/{source}/{skillID}/manage": {
            "post": operation("manageRuntimeSkill", "管理目标 AgentDock 已安装 Skill 的激活版本或隔离环境", params=[p("RuntimeNodeId"), p("RuntimeSkillSource"), p("RuntimeSkillId")], request=body(ref("RuntimeSkillManageRequest")), success=ok(ref("RuntimeSkillManageResponse")))
        },
        "/v1/runtime/nodes/{nodeID}/builtins": {
            "get": operation("listRuntimeBuiltins", "读取指定节点的内置能力实时状态", params=[p("RuntimeNodeId")], success=ok(ref("BuiltinSnapshot"))),
            "post": operation("updateRuntimeBuiltin", "设置指定节点的内置能力开关，配置保存在 AgentDock", params=[p("RuntimeNodeId")], request=body(ref("BuiltinUpdate")), success=ok(ref("BuiltinSnapshot"))),
        },
        "/v1/runtime/nodes/{nodeID}/mcp": {
            "get": operation("listRuntimeMCPServers", "列出指定 AgentDock 节点的动态 MCP 服务", params=[p("RuntimeNodeId")]),
            "post": operation("manageRuntimeMCPServer", "管理指定 AgentDock 节点的动态 MCP 服务", params=[p("RuntimeNodeId")], request=body()),
        },
        "/v1/runtime/nodes/{nodeID}/mcp/{name}": {"get": operation("getRuntimeMCPServer", "读取指定 AgentDock 节点的动态 MCP 服务", params=[p("RuntimeNodeId"), p("RuntimeMCPName")])},
        "/v1/runtime/nodes/{nodeID}/mcp/{name}/environment": {"get": operation("getRuntimeMCPEnvironment", "读取指定 AgentDock 节点的 MCP 隔离环境元数据", params=[p("RuntimeNodeId"), p("RuntimeMCPName")])},
        "/v1/workflow-templates": {
            "get": operation(
                "listWorkflowTemplates",
                "列出 Nexus 工作流模板",
                params=[
                    q("status", "按模板状态过滤。", enum=["active", "retired"]),
                    q("q", "在模板摘要中搜索。"),
                    q("include_history", "未指定状态时是否返回全部历史版本。", "boolean"),
                    q("view", "history 等价于 include_history=true。", enum=["history"]),
                ],
            )
        },
        "/v1/workflow-templates/publish": {"post": operation("publishWorkflowTemplate", "发布 Nexus 工作流模板", request=body())},
        "/v1/workflow-templates/match": {"post": operation("matchWorkflowTemplates", "匹配 Nexus 工作流模板", request=body())},
        "/v1/workflow-templates/reindex": {"post": operation("reindexWorkflowTemplates", "重建 Nexus 工作流模板向量索引", request=body())},
        "/v1/workflow-templates/vector-index": {"get": operation("getWorkflowTemplateVectorIndex", "读取 Nexus 工作流模板向量索引状态")},
        "/v1/workflow-templates/{templateID}/{version}": {"get": operation("getWorkflowTemplate", "读取 Nexus 工作流模板详情", params=[p("WorkflowTemplateId"), p("WorkflowTemplateVersion")])},
        "/v1/workflow-templates/{templateID}/{version}/retire": {"post": operation("retireWorkflowTemplate", "退役 Nexus 工作流模板", params=[p("WorkflowTemplateId"), p("WorkflowTemplateVersion")])},
    }

    return {
        "openapi": "3.1.0",
        "info": {
            "title": "NexusDock API",
            "version": "1.0.0",
            "description": "个人 NexusDock 控制台的当前 HTTP 契约，覆盖 Recall、账号会话和 AgentDock Runtime 视图。",
        },
        "servers": [{"url": "/", "description": "当前 Nexus 实例。"}],
        "paths": paths,
        "components": {"parameters": parameters, "schemas": schemas},
    }

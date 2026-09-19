# NexusDock

**NexusDock 是 AgentDock 的自托管中心服务。**

如果你在多台 Mac、Windows 或服务器上使用 AgentDock，可以用 NexusDock 提供一个统一的 Web 控制台和 MCP 入口，集中管理设备、Recall、Workflow 与常用运行时能力。

- AgentDock：<https://github.com/Serialeo/agentdock>
- GitHub Container Registry：<https://github.com/Serialeo/nexusdock/pkgs/container/nexusdock>
- Releases：<https://github.com/Serialeo/nexusdock/releases>

## 能做什么

- **管理多台 AgentDock**：查看在线状态、版本和能力，完成设备配对、重命名、停用或移除。
- **查看设备运行时**：选择具体节点后查看它的任务、Skill 和动态 MCP；这些状态仍保留在 AgentDock 本机。
- **集中使用 Recall 与 Workflow**：管理长期记忆、经验卡片、版本历史和可复用工作流模板。
- **提供统一 MCP 入口**：支持 OAuth，也可以为不支持 OAuth 的客户端使用独立 MCP Access Token。
- **有界工作会话续接**：用户明确开启后，在指定命令结束时通过可见的 MCP App 请求 Host 继续；支持暂停、持久结果和精确领取。[使用方法、边界与真实 Host 验收](docs/work-session-continuation.md)。
- **集中配置 AI 与向量能力**：在 Web 中配置 Embedding 和可选模型，并用于 Recall 与 Workflow 的语义能力。

NexusDock 不替代 AgentDock。命令执行、文件操作、浏览器、Skill、动态 MCP 等设备能力仍由对应的 AgentDock 节点执行；NexusDock 负责中心管理、共享数据和路由。

## 快速开始

普通部署直接使用 Serialeo 公共 GHCR 镜像即可，不需要安装 Go 或 Node.js。

容器发布到：

```text
ghcr.io/serialeo/nexusdock
```

镜像仅发布 `linux/amd64`。下面示例为了简洁使用 `latest`；生产环境优先使用 [Serialeo Releases](https://github.com/Serialeo/nexusdock/releases) 对应的固定 release tag，关键部署可进一步固定 image digest。AgentDock 与 NexusDock 应按同一 release manifest 中验证过的 Bridge generation 成对升级。Package 设为 Public 后可匿名拉取，不需要 GitHub 登录或 package token。

### 1. 创建目录

```bash
mkdir nexusdock
cd nexusdock
mkdir -p nexus-data recall
```

Linux 使用宿主机目录时，首次启动前建议让容器用户拥有写权限：

```bash
sudo chown -R 10001:10001 nexus-data recall
```

### 2. 创建配置

生成一个随机的程序化 API Token：

```bash
openssl rand -hex 32
```

新建 `.env`：

```dotenv
NEXUS_AUTH_TOKEN=<粘贴刚才生成的随机值>
NEXUS_PUBLIC_URL=
NEXUS_AUTH_ALLOW_INSECURE_HTTP=true
NEXUS_TRUSTED_PROXIES=127.0.0.1,::1
```

这里的 `NEXUS_AUTH_ALLOW_INSECURE_HTTP=true` 只用于本机 `http://127.0.0.1` 首次试用。通过域名访问时应使用 HTTPS，并改回 `false`。

### 3. 创建 Compose 文件

新建 `compose.yaml`：

```yaml
services:
  nexusdock:
    image: ghcr.io/serialeo/nexusdock:latest
    container_name: nexusdock
    restart: unless-stopped
    read_only: true
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    ports:
      - "127.0.0.1:18777:18777"
    tmpfs:
      - /tmp:rw,noexec,nosuid,size=64m,uid=10001,gid=10001,mode=0700
    volumes:
      - ./nexus-data:/var/lib/nexus
      - ./recall:/recall
    environment:
      NEXUS_AUTH_TOKEN: ${NEXUS_AUTH_TOKEN}
      NEXUS_REQUIRE_AUTH: "true"
      NEXUS_AUTH_ALLOW_INSECURE_HTTP: ${NEXUS_AUTH_ALLOW_INSECURE_HTTP:-false}
      NEXUS_PUBLIC_URL: ${NEXUS_PUBLIC_URL:-}
      NEXUS_DATA_DIR: /var/lib/nexus
      RECALL_REPO_DIR: /recall
      NEXUS_TRUSTED_PROXIES: "${NEXUS_TRUSTED_PROXIES:-127.0.0.1,::1}"
      NEXUS_LOG_LEVEL: ${NEXUS_LOG_LEVEL:-info}
```

### 4. 创建管理员

```bash
docker compose run --rm nexusdock admin init myadmin
```

`myadmin` 只是示例用户名，可以换成你自己的用户名。终端会要求输入并确认管理员密码。密码至少需要 12 个字符，不能与用户名相同，也不能使用常见弱密码。管理员账号不需要写入 `.env`。

### 5. 启动

```bash
docker compose pull
docker compose up -d
curl http://127.0.0.1:18777/health
```

然后打开：

```text
http://127.0.0.1:18777
```

使用刚才创建的管理员账号登录。

## 远程访问

远程使用时，建议继续让 Docker 只监听 `127.0.0.1:18777`，再通过 Caddy、Nginx、Traefik、Cloudflare Tunnel 等提供 HTTPS。

例如：

```dotenv
NEXUS_PUBLIC_URL=https://nexus.example.com
NEXUS_AUTH_ALLOW_INSECURE_HTTP=false
NEXUS_TRUSTED_PROXIES=127.0.0.1,::1
```

需要注意：

- `NEXUS_PUBLIC_URL` 必须是完整的 HTTPS Origin，例如 `https://nexus.example.com`，不能带路径、查询参数或 Fragment。
- `NEXUS_TRUSTED_PROXIES` 只填写实际反向代理地址或必要网段。反代通过 Docker 网络访问 NexusDock 时，需要把对应代理来源加入这里。
- NexusDock 只信任受信任代理提供的 `X-Forwarded-*`。配置错误时，浏览器登录会提示需要 HTTPS 或拒绝请求来源（`HTTPS_REQUIRED` / `ORIGIN_REJECTED`）。

## 连接 AgentDock

登录 Web 控制台后进入 **设置 → 系统与节点**，点击 **配对设备**。

NexusDock 会生成一个短时、单次使用的配对码，并根据当前浏览器地址给出类似命令：

```bash
agentdock nexus pair --endpoint https://nexus.example.com --code pair_xxx
```

在目标设备执行命令并按提示重启 AgentDock。之后 AgentDock 会主动连接 NexusDock，不需要为节点开放入站公网端口。

接入 NexusDock 不会改变 AgentDock 原有的本地 MCP、认证和工具行为；每台设备仍然可以独立使用。

## Web 控制台

登录后的主线按 Project-first 组织：

- **工作**：Projects / Sessions。Project 关联一个或多个 Node Deployment；Session 展示本次工作实际绑定的 Target、cwd、上下文 revision 与执行状态。
- **知识**：Recall / Workflow。用于管理长期记忆与可复用工作流模板。
- **运行环境**：Nodes / Skills / MCP。节点页用于设备与运行环境管理，不作为 Project 执行授权的全局选择器。
- **系统**：账号、MCP 接入、AI 与向量等设置。

### Project 工作上下文与权限

未指定 Project 时，MCP Host 使用节点临时会话：从 `agentdock_context` 获取节点 ID，用 `node_open` 创建会话，再通过返回的 `work_session_id` / `target_id` 调用节点工具。只有用户明确指定了 Project，才使用 `project_open`；`project_list` 用于用户主动查找项目。`project_context` 可以刷新两类已绑定会话，不改变会话类型。

每个 Deployment 固定绑定目标 Node，并可选保存 `working_folder`、自由文本 `role` / `purpose` 和细粒度能力。`working_folder` 只负责默认 cwd、Project Prompt 搜索边界与源码 provenance 根，不是 OS 沙箱；它可以留空，留空时 Target 的相对路径/默认命令目录从该 Node 的 AgentDock 默认目录开始，同时不自动发现任何 Project `AGENTS.md`。

Node 管理页提供独立的 **Full Access** 开关。开启后，该 Node 的 Project Target 可使用节点已经暴露的全部执行能力，仍受 AgentDock 服务进程真实 OS 权限约束；Full Access 与 Project Folder 正交，不会把文件/命令限制在 `working_folder` 内，也不会扩大 Project Prompt 的自动发现范围。关闭 Full Access 后，执行回到各 Deployment 保存的 files / shell / browser / dynamic MCP / ACP 细粒度权限。

Project Prompt 只在 Deployment 配置了 Project Folder 时来自该目录内当前适用的 `AGENTS.md` 链。外部 MCP Host 通过 `project_open` / `project_context` 获取模型可见的完整 Project Context，包括 Target、revision、协作策略、来源与规则正文；NexusDock 不创建第二套 AI 会话，也不把返回结果宣称为替换了 Host 的 system prompt。

旧 **Global Instructions / Node Guidance / Direct Instructions / Node FileAccess** 已从当前产品边界移除。对应管理路由不再注册，旧 prompt 数据不会自动迁移到 `AGENTS.md` 或 Project；升级时只清理这些 legacy 表/状态，并保留 Node identity、Project、Recall、秘密与其他无关数据。Stage 3 的独立 System Prompt 仍属于 AI 设置，不是 Project 指导来源。

Runtime → Skills 中，已安装的 `agentdock-api` Skill 可以管理激活版本、回滚和隔离环境变量；Common Skills 保持只读且与已安装同名时由 AgentDock 在索引层直接 shadow。环境 API 只返回变量名和 configured 状态，不回显值。


## 连接 MCP 客户端

统一 MCP 地址是：

```text
https://你的-nexus-域名/mcp
```

支持 OAuth 的 MCP 客户端可以直接连接并通过浏览器完成授权。

对于不支持 OAuth、需要固定 Token 的客户端，可以在 **设置 → MCP 接入** 中查看专用 MCP Access Token：

```text
Authorization: Bearer <Access Token>
```

NexusDock 中常见的三类凭据用途不同：

| 凭据 | 用途 |
| --- | --- |
| 管理员用户名和密码 | 登录 Web 控制台 |
| `NEXUS_AUTH_TOKEN` | 程序化访问受保护的 `/v1` 管理 API |
| MCP Access Token | 访问 `/mcp` |

重置 MCP Access Token 后旧 Token 会立即失效；OAuth 客户端不受影响。

## Recall、Workflow 与 AI

Recall 是 NexusDock 的长期记忆工作区，可以在 Web 中浏览、搜索和编辑内容，也可以查看本地 Git 版本历史。NexusDock 不会自动配置或操作 Recall 仓库的 Git remote，远端备份方式由你自己决定。

Workflow 用于集中保存和匹配可复用任务模板。即使没有配置 Embedding，也可以正常使用基本模板能力。

需要语义召回或语义匹配时，进入 **设置 → AI 与向量** 配置兼容的 Embedding 服务；如有需要，也可以配置可选的外部模型。Web 中可以测试连接和重建索引，保存后的配置会直接应用，无需重启容器。

Stage 3 的 System Prompt 也在该页面完整可见。默认内容来自随当前 NexusDock 版本交付的 bundled resource；编辑并保存后来源显示为 **Custom**，点击 **恢复 Bundled Default** 会删除自定义覆盖并重新使用当前版本的 bundled default。实际发送给 Stage 3 模型的 system message 就是页面展示的 effective prompt。多节点或无唯一 evidence 来源的候选可以显式选择一个 `review_node_id`；未配置时这类候选不会派发，未知 device 也不会回退到第一台节点。候选引用只有在唯一属于目标节点时才作为 AgentDock evidence 传递，其他有效引用只留在 Nexus 的 proposal audit 中作为非权威 provenance。

不配置 AI 或 Embedding 时，NexusDock 的节点管理、MCP、Recall 文件浏览、关键词搜索、版本历史和基础 Workflow 仍然可以使用。

## 工具媒体与 Artifact

通用 `file_publish` 已移除；NexusDock 不再提供把任意本地文件或目录发布成下载地址的模型工具。Browser screenshot、`view_image` 等工具产生的必要媒体仍可沿现有 Artifact/Bridge 通道返回，这与任意文件发布是不同边界。

当保留的媒体需要临时外部访问地址时，应正确设置 `NEXUS_PUBLIC_URL`；相关 Node 可能需要保持在线。旧 Artifact URL 的失效策略属于独立媒体安全边界，不能因为 `file_publish` 已删除就推断所有历史 URL 都自动失效。

## 升级与备份

升级前建议完整备份：

```text
nexus-data/
recall/
```

不要只备份 SQLite 数据库或单个密钥文件。`nexus-data` 中还包含账号、设备、会话和 NexusDock 自身需要的密钥；`recall` 中包含长期记忆和本地版本历史。

升级：

```bash
docker compose pull
docker compose up -d
curl http://127.0.0.1:18777/health
```

如果使用固定版本，先把 `image:` 调整到同一 release manifest 验证过的版本对。Project-first 源码使用 **Bridge v4**（`ConnectionProtocolVersion = "4"`），与 Bridge v3 及更早 wire 不兼容；AgentDock 与 NexusDock 必须同时使用包含同一 Bridge v4 契约的 `agentdock-protocol` release。AgentDock `v0.9.5` 与 NexusDock `v0.9.5` 均固定依赖 `agentdock-protocol v0.11.0`，请按此版本对统一升级；内置能力开关和 stdio 管理步骤见 [内置能力管理与升级边界](docs/builtin-capabilities.md)。升级到 Project-first 版本时会删除旧 Global/Node Instructions 表，而不会迁移其中正文；稳定 Node identity、Project/Deployment/WorkSession、Recall、秘密与其他无关状态继续保留。

不支持只回滚 AgentDock 或只回滚 NexusDock 到旧 Bridge generation。需要跨 v4 边界回滚时，应同时回滚 A/N，并优先使用升级前的 `nexus-data` 备份恢复控制面数据；不要依赖新版数据库继续为旧运行时代际提供协议兼容。Nexus 使用 `internal/agentdock/version.go` 中的 `RequiredVersion` 严格校验节点版本（当前为 `0.9.5`），发布新版时必须同步更新此值；同为 Bridge v4 的旧版、未知版和其他未配套版本也会被拒绝。非当前节点只在管理员目录保留升级诊断信息，不进入远端 AI 的节点、工具、资源、Project Target 或历史续接入口。启动时不恢复持久 node-tool 发布缓存，当前版本节点完成 Hello 后才重建工具目录。

不要运行两个 NexusDock 实例同时写同一份 `nexus-data`；当前配置 revision/mutex 设计只承诺单 writer 进程，不宣称多个 NexusDock 进程共享同一 SQLite 时有跨进程顺序保证。

## 常用配置

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `NEXUS_HOST` | `127.0.0.1` | HTTP 服务监听地址 |
| `NEXUS_PORT` | `18777` | HTTP 服务端口 |
| `NEXUS_PUBLIC_URL` | 空 | 对外 HTTPS Origin，例如 `https://nexus.example.com` |
| `NEXUS_DATA_DIR` | `./nexus-data` | NexusDock 状态与密钥目录；容器内使用 `/var/lib/nexus` |
| `NEXUS_AUTH_TOKEN` | 空 | 程序化 `/v1` API Bearer Token |
| `NEXUS_REQUIRE_AUTH` | `false` | 开启后，没有 `NEXUS_AUTH_TOKEN` 时拒绝启动 |
| `NEXUS_AUTH_ALLOW_INSECURE_HTTP` | `false` | 是否允许通过 HTTP 提交浏览器登录；仅建议本机调试 |
| `NEXUS_TRUSTED_PROXIES` | `127.0.0.1,::1` | 允许提供可信 `X-Forwarded-*` 的代理地址 |
| `NEXUS_LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error` |
| `RECALL_REPO_DIR` | `./recall` | Recall 仓库目录；容器内使用 `/recall` |

基础环境变量示例见 [`.env.example`](./.env.example)。AI 与向量能力更适合直接通过 Web 控制台配置。

## 安全部署

远程部署建议：

- Docker 端口只绑定 `127.0.0.1`；
- 使用 HTTPS 反向代理或 Tunnel；
- 正确设置 `NEXUS_PUBLIC_URL`；
- 保持 `NEXUS_AUTH_ALLOW_INSECURE_HTTP=false`；
- 只信任实际反向代理；
- 使用高强度管理员密码和随机 `NEXUS_AUTH_TOKEN`；
- 限制 `nexus-data` 和 `recall` 的宿主机访问权限。

官方镜像默认使用 UID/GID `10001:10001`，并采用只读根文件系统、丢弃 Linux capabilities 等容器安全设置。

## 管理员密码恢复

忘记管理员密码时，在部署主机执行：

```bash
docker compose run --rm nexusdock admin recover
```

默认会恢复已配置的管理员；如果需要，也可以在命令末尾显式指定用户名。新密码遵循与初始化相同的强度规则。恢复后该管理员现有的 Web 会话会失效，需要重新登录。

## 常见问题

**本机打开页面后无法登录？**

确认本机 HTTP 试用时设置了 `NEXUS_AUTH_ALLOW_INSECURE_HTTP=true`。正式远程部署应使用 HTTPS 并保持它为 `false`。

**反向代理后登录提示“仅允许 HTTPS”或拒绝请求来源（`HTTPS_REQUIRED` / `ORIGIN_REJECTED`）？**

确认反向代理正确传递 `X-Forwarded-Proto` / `X-Forwarded-Host`，并确认代理实际来源已包含在 `NEXUS_TRUSTED_PROXIES` 中。

**Linux 启动时报 `permission denied`？**

确认 `nexus-data` 和 `recall` 对 UID/GID `10001:10001` 可写。

**AgentDock 配对后没有上线？**

确认 NexusDock 的 HTTPS/WSS 地址可从目标设备访问，然后按配对提示重启目标 AgentDock。设备不需要开放入站端口。

**Runtime 页面没有任务、Skill 或 MCP 数据？**

先确认顶部已选择目标 AgentDock，并确认该节点在线。

**容器显示 unhealthy？**

```bash
docker compose logs --tail=200 nexusdock
curl http://127.0.0.1:18777/health
```

## 从源码开发

这一部分只面向希望修改 NexusDock 本身的开发者。普通部署不需要执行这些步骤。

```bash
git clone https://github.com/Serialeo/nexusdock.git
cd nexusdock
make web-deps
make build
```

源码构建依赖公开的 `github.com/Serialeo/agentdock-protocol` module，不需要额外仓库凭据或 `GOPRIVATE` 配置。

开发检查：

```bash
make check
make ci
```

仓库自带的 `docker-compose.yml` 是生产拉取配置，不再隐式执行源码 build。开发 Docker build 使用显式 override，且不需要 GitHub token：

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml build nexusdock
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d
```

更多开发约束见 [`AGENTS.md`](./AGENTS.md)。

发行由 GitHub Actions 自动执行：

- 推送到 `main` 后，**CI** 完成 `make ci`、嵌入式前端一致性检查和容器构建，再自动发布该次已验证 commit 的 `ghcr.io/serialeo/nexusdock:sha-<前7位commit>` 候选镜像。候选不会更新 `latest`，也不会创建 GitHub Release。其他分支和 Pull Request 只执行检查。
- 推送已存在的版本标签，例如 `v1.2.3`，会触发 **Publish images and releases**。该流程独立执行 `make ci` 并检查嵌入式前端，然后发布镜像；匿名按 digest 拉取并通过容器健康检查后，创建该标签的 GitHub Release。流程不会自行创建 Git tag。
- 稳定版本镜像包括 `v1.2.3`、`1.2.3`、`1.2`、`latest` 和对应 `sha-*` 标签。预发布标签例如 `v1.2.3-rc.1` 只发布精确版本与 `sha-*`，GitHub Release 标为 prerelease，不更新稳定版本标签或 `latest`。版本标签接受 `vMAJOR.MINOR.PATCH[-PRERELEASE]`，不接受带 `+build` 的标签。
- GitHub Release 附带 `nexusdock-<版本>-linux-amd64.tar.gz` 与 `SHA256SUMS`。压缩包包含已嵌入 Web UI 的静态 Linux amd64 二进制、README、`.env.example` 和记录版本/commit 的 `BUILD_INFO`；可执行 `sha256sum --check SHA256SUMS` 校验下载文件。源码运行仍需要 Git 来管理 Recall 的本地版本历史。

AgentDock 与 NexusDock 的候选、正式标签各自发布，不会相互创建标签或自动推进另一仓。需要成对验收时，在本仓手动运行 **Verify paired public images**，传入 AgentDock runtime/dev/browser 与 NexusDock 的四个精确 SHA 标签或 digest；该流程检查匿名拉取、平台 manifest、凭据泄漏、variant 启动、真实 A↔N 配对、Bridge v4 握手、旧指导路由退役、Stage 3 设置和数据卷持久化。正式发布前应确保 `Serialeo/agentdock` 和 `Serialeo/nexusdock` 两个 GHCR container package 均已设为 Public。

### 节点内置能力

在「运行环境 → Nodes → 内置能力」查看并切换指定节点的 browser/ACP。
界面显示发行包提供、用户选择和后端就绪的实时结果；Docker ACP 不可开启。开关由 AgentDock 持久化，Nexus 仅通过现有 Runtime Bridge
代理管理请求，离线时不排队写入。开启不会授予 Deployment 权限或恢复旧任务。

节点使用 `node.updated` 发送完整工具快照，汇总目录随之增删并通过 SDK 通知
已订阅 `subscriptions/listen` 的 MCP 客户端。未订阅的客户端需重新列出工具，
缓存工具调用仍受当前版本和 AgentDock 运行时门禁约束。最后一个在线当前版本 provider
断线后立即下架工具；重连只采用新 Hello。旧节点不会参与契约合并，也不会阻止最新
工具参数和使用说明发布。同一当前版本的平台可选参数保留，契约冲突显式撤下并记录错误。

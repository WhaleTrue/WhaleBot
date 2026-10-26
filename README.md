# WhaleMesh

**Capability-Based Autonomous Container Mesh**

Everything is Docker. Every Docker is an Equal Mesh Node. The Architecture Emerges from Capabilities.

[English](README.en.md) · [架构](docs/architecture/overview.md) · [协议与 Schema](docs/architecture/schema-contracts.md) · [开发指南](docs/developer-guide.md)

**分支约定：** `dev` 保留重构前的 WhaleBot（`e71dbdf`）；`v3` 承载 WhaleMesh 全量重构及后续开发。两个版本不兼容，重构内容不再合并回 `dev`。

WhaleMesh 将 WhaleBot `dev` 的中心化注册、代理与 UserDocker 协议整体替换为同一种 Node 协议。每个节点独立维护可信成员、能力与依赖视图。节点按能力、版本、作用域和策略直接协作；没有全局必需的 orchestrator、数据库、Executor、Relay 或管理面板。

协议平等不等于权限相同。只有受授权的 Executor 能访问 Docker Engine。声明 `container.manage` 不会获得 Docker Socket 或管理员权限。

## 启动

需要 Docker Engine 与 Compose v2.24+ / v5。默认部署不需要外部 API Key。

```sh
docker compose up -d --build
docker compose exec web cat /identity/admin-password
```

浏览器访问 `http://localhost:18000`。用户名默认 `admin`，密码由 bootstrap 随机生成并持久化。每个节点持有独立 Ed25519 私钥；没有共享管理员私钥或默认固定密码。

默认 `LLM_MOCK=1`，回复明确标记为开发 Mock，不代表连接了真实模型。真实推理需要在模型配置中保存 OpenAI-compatible 模型，并关闭 Mock。Telegram 需要真实 Bot Token 与用户白名单。

默认只把 Web 端口绑定到宿主机回环地址。开发 Docker bridge 使用签名 HTTP；它没有加密能力。Compose 显式允许此隔离网络的明文 Secret 传输并打印警告。跨主机必须使用经过验证的 HTTPS，关闭该例外。

### 最小聊天系统

不需要管理面板、不需要 Docker Executor，也没有 orchestrator：

```sh
docker compose -f compose.minimal.yml up -d --build
docker compose -f compose.minimal.yml run --rm chat '你好，WhaleMesh'
```

终端适配器解析 `agent.run`；Agent 解析 `session.store` 与 `llm.invoke`。Docker DNS 只解析 bootstrap 地址，不承担能力发现。

### 可选扩展

```sh
docker compose --profile multi-provider up -d
# 第二个独立模型 Provider。
docker compose --profile telegram up -d
# Telegram 配置与白名单。
docker compose --profile executor up -d
# 只有此容器挂载 Docker Socket。
docker compose --profile example build hello
docker compose --profile example up -d hello
# 普通第三方 Hello Capability。
docker compose --profile relay up -d
# 仅本地 Relay；公网使用 TLS 配置。
```

知识节点可以独立启动、停止和重启。Agent 对可选 Memory、Skills、事件和统计合理降级。必需能力缺失时返回明确错误。状态存储不会因为另一个 Provider 可用而自动迁移。

## 核心设计

- **统一身份**：namespace、logical name、principal、host、运行实例与公开元数据明确分离。重启生成新 instance ID。
- **统一声明**：Node、Capability、Deployment 使用严格、带版本的 JSON Schema。公开声明不携带凭据。
- **对等发现**：可信签名的成员交换、每个实例的递增序列、本地 TTL、优雅 draining、旧状态抑制与恢复重连。
- **本地解析**：按接口版本、readiness、namespace、权限、属性与显式策略选择 Provider。选择结果可解释；不依赖 map 遍历顺序。
- **业务接口保持类型**：不同能力有自己的 HTTP endpoint 与操作描述。公共层处理身份、授权、追踪、deadline、取消、错误与流式通信，不强制万能 `/invoke`。
- **工具显式导出**：只有声明 Tool Export 的能力进入模型工具集。模型提供的工具名称、所有者或确认布尔值不构成授权。
- **分层权限**：节点调用、浏览器用户、IM 用户、工具执行、Secret 与 Engine 权限分别校验。
- **可选 Relay**：出站 yamux 连接提供远端可达性；原始目标仍独立验证签名和权限。Relay 不选择业务 Provider。

## 产品能力

Agent 的 ReAct、工具调用、上下文、检索注入、秘密引用、审批、结构化事件、usage 和 Benchmark 均使用 Mesh SDK。模型 Provider 支持独立多实例、多个持久配置模型、OpenAI tool calling、SSE 与超时。推理不自动重放，包括 HTTP 429、传输失败与部分流响应；不会偷偷返回 Mock 或切换到收费模型。

持久能力包括 Session、Memory Notes/Search、加密 Secret、版本化 Skills/ZIP 导入、隔离 Workspace/Artifact、事件与统计。Svelte WebUI 提供真实动态拓扑、能力/依赖/绑定详情、聊天、会话、模型与适配器配置，以及存储、Docker、日志、统计、Benchmark 管理。Telegram 保留命令、会话轮换、进度、错误和产物发送。

Executor 接收 Deployment 期望状态，只操作自身授权标签的资源。动态容器使用与固定容器相同的 Node 协议，使用预先授权的独立身份池。默认禁止宿主机目录、Docker Socket 转交、特权容器和危险网络。外部镜像需要不可变 digest 与可信管理员签名审批；模型不能自行宣称用户批准。

## 目录

```text
cmd/                 node、bootstrap、终端 chat
pkg/mesh/            角色无关 SDK
schemas/             严格 JSON Schema
internal/agent/      Agent 与 Benchmark
internal/llm/        模型 Provider
internal/providers/  持久业务能力
internal/executor/   Docker Engine 权限边界
internal/relay/      可选出站多机传输
internal/web/        用户认证与浏览器能力客户端
internal/telegram/   Telegram 适配器
web/                 Svelte 前端
examples/hello/      独立 Capability 示例
scripts/             真实 Docker 验收
 deployment/         身份计划与测试部署
```

## 验证

本地源码开发需要 Go 1.26、Node 24。命令本身不是已经通过的证明；实际结果以执行输出和验收报告为准。

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
cd web
npm ci
npm run build
node --test src/api.test.js src/topology.test.js
cd ..
export DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
python3 scripts/acceptance.py
```

验收使用单独 Compose project 与隔离网络，验证无 orchestrator 聊天、动态知识、多模型 Provider、故障恢复、真实动态容器、权限边界和 Relay 重连。真实 Telegram 与外部模型质量验证需要自己的凭据，不会用 Mock 成功替代。

## 文档

- [单机部署与身份配置](docs/single-host.md)
- [多机与 Relay](docs/multi-host.md)
- [安全模型](docs/security.md)
- [发现与依赖解析](docs/discovery-resolver.md)
- [Schema 合同](docs/architecture/schema-contracts.md)
- [Capability 开发](docs/developer-guide.md) 与 [Hello 示例](examples/hello/README.md)
- [Executor](internal/executor/README.md)
- [Benchmark 批量模型评测与历史分数](docs/benchmark-automation.md)
- [故障排查](docs/troubleshooting.md)
- [旧实现审计](docs/architecture/legacy-audit.md) 与 [ADR](docs/architecture/adr/)

本次是无兼容层的整体切换。旧 API、旧注册与旧数据库不继续运行；不会自动删除或迁移用户旧数据卷。修改项目时同步更新 `AGENTS.md`、受影响组件 README 与运行文档。代码探索前必须使用 graphify，修改后运行 `graphify update .`。

MIT License，见 [LICENSE](LICENSE)。

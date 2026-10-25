# Benchmark 批量模型评测与历史分数

本文适用于 WhaleMesh，不适用于已删除的 orchestrator、`/api/v1` 或 UserDocker API。通过已认证 Web 节点的 `/api/call` 调用实际发布的 Mesh operation；不存在免认证兼容入口。

## 前提

- 启动当前 Compose 部署，在 `http://localhost:18000` 登录，侧栏打开 **Benchmark**。端口以本地 `.env` 和 `docker compose port web 8080` 为准。
- 用户需要 `mesh.inspect`、`benchmark.read`、`benchmark.run`；删除历史另需 `benchmark.delete`。模型目录读取需要其 operation 发布的 `llm.models.list` 权限，配置读取/写入分别需要 `llm.config.read` / `llm.config.write`。Web 节点自身也必须有对应 Mesh trust grants。
- 真实模型评测须关闭 `LLM_MOCK`，配置可达的 OpenAI-compatible 模型。默认 `LLM_MOCK=1` 是明确标注的开发输出，不能证明真实模型质量。上游连接拒绝必须报告为失败，不能替换成 Mock。
- llama.cpp 使用支持工具调用的模型模板并以 `--jinja` 启动。模型服务应监听容器可达地址；容器里的 `localhost` 不是宿主机。使用当前 Compose 支持的宿主地址或受信网络，不依赖旧实现自动重写地址。
- 先检查实际上游 `/health`，再执行模型评测。每个 profile 设置适合本地模型速度的 `timeout_seconds`，首次加载通常更慢。WhaleMesh 不自动重放失败推理、不偷偷切换收费模型。
- 一次只运行一个 benchmark。启动冲突返回 409；等待当前任务结束，不并行灌入队列。

## 管理页面

Benchmark 页面选择真实 benchmark provider 和模型。模型下拉框显示 provider、profile 名称和费用类别；自动选择仅允许明确标记为 free 的激活模型。收费或费用未知模型必须显式选择。每次任务记录实际 provider、profile、上游模型及冻结的目录 revision；执行期间配置变化会失败，而不是更换模型。

页面保留异步启动、运行中每 3 秒轮询、详情/逐项证据、单条删除、清空确认、单条和全部 JSON、CSV。高级 capability JSON 控制台保留所有已发布操作、schema、精确 scope 与原始返回值。历史按开始时间倒序展示，不改变存储中的分数。

稳定历史列：

- **Daily score**：`scores.total`，0–100；`plan_gate`、`tool_call`、`react` 作为子项显示。权重分别为 25%、35%、40%。
- **Bonus**：`scores.bonus`，0–100，困难状态追踪、错误恢复、安全边界与证据判断；不计入 Daily score。
- **E2E**：`scores.e2e`，0–100，真实执行及独立产物核验；不计入 Daily score。未运行没有该分数；逐项结果在 `cases` 的 `e2e` 类别。
- **Case set**：当前实现为 `mesh-v4.1`。最新一条有版本记录的 case set 高亮；其他版本保留但弱化。Best 只比较该版本中已完成、具有有效 Daily score 的任务。最新版本还没有已完成任务时不把旧版本标成 Best。
- **Usage / evidence**：`usage` 提供实际 token 数；`cases[]` 提供 category、name、pass、score、detail、latency_ms，单项 score 是 0–1，界面显示百分比。完整 JSON 可查看其余诊断。

0 分不是缺失；未完成、失败或没有该项的分数显示为破折号。失败任务的部分证据仍在详情/JSON 中，不当作已完成成绩。CSV 保留 case_set 和模型 revision；文本转义引号及换行，并防止模型名称或错误内容被表格软件解释为公式。不要跨 case_set 汇总最高分。

历史属于当前浏览器用户与选定 agent 的持久数据边界。重启从该 provider 的持久卷恢复；另一个 provider 的数据不自动合并。新界面不会迁移旧 WhaleBot 数据卷，也不会重算历史记录。

## 已认证能力调用

先从 `GET /api/topology` 的 `manifest` 与 `peers[].manifest` 读取实际 capability 和 operations；不要硬编码服务 URL 或实例 ID。`node` 使用规范的转义身份键：

```js
[identity.namespace, identity.principal, identity.name, identity.instance_id]
  .map(encodeURIComponent).join('/')
```

HTTP operation 是目标 Mesh 操作；浏览器统一向 `POST /api/call` 提交 envelope。以下操作名对应当前发布合同，自动化仍应核对 topology：

| 用途 | capability / operation | 目标 method / path | input |
| --- | --- | --- | --- |
| 读取模型目录 | `llm.models.list` / `llm.models.list` | `GET /llm/models` | `{}` |
| 读取配置 | `llm.models.list` / `config.read` | `GET /llm/config` | `{}` |
| 替换配置 | `llm.models.list` / `config.write` | `PUT /llm/config` | 完整配置，以发布 schema 为准 |
| 启动 | `benchmark.run` / `benchmark.run` | `POST /benchmark/run` | `{model,provider,include_e2e}`，E2E 另需下节字段 |
| E2E 准备 | `benchmark.e2e.prepare` / `benchmark.e2e.prepare` | `POST /benchmark/e2e/prepare` | `{session_id,container_id}`，scope 为 `benchmark.run` |
| 历史 | `benchmark.results` / `benchmark.results` | `GET /benchmark/runs` | `{}`，返回 `{runs:[...]}` |
| 单条删除 | `benchmark.results` / `delete.one` | `DELETE /benchmark/runs/{id}` | `{}`，envelope 的 `path` 带 URL-escaped id |
| 全部删除 | `benchmark.results` / `delete` | `DELETE /benchmark/runs` | `{}`，有运行中任务时拒绝 |

登录 `POST /api/auth/login` 返回 cookie 和 `csrf`。后续 mutation 必须同时携带 cookie、`X-CSRF-Token` 及同源 `Origin`。不要在命令行参数、日志或提交中暴露密码、cookie 或 token。使用权限为 0600 的临时文件存放登录请求和 cookie，结束后删除。

已有安全保存的 cookie 文件和 CSRF token 后，shell 调用示例：

```sh
# WEB、COOKIE_JAR、CSRF、AGENT_NODE 均来自当前已认证会话/拓扑。
# 显式 MODEL_PROVIDER 使用模型 provider 的规范身份键，MODEL_ID 为目录 profile id。
body=$(jq -n --arg node "$AGENT_NODE" --arg model "$MODEL_ID" --arg provider "$MODEL_PROVIDER" \
  '{capability:"benchmark.run",operation:"benchmark.run",node:$node,input:{model:$model,provider:$provider,include_e2e:false}}')
curl --fail-with-body --silent --show-error "$WEB/api/call" \
  -H "Origin: $WEB" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -b "$COOKIE_JAR" --data "$body"
```

返回 HTTP 202 和完整 run（含 `id`），不是旧 `run_id` 包装。历史与删除都使用同一个 agent node 身份；不要把 owner 或批准布尔值塞进 input。Web 后端从已认证账户派生 owner，并校验 operation、node、path 和 scope。

## 多个 GGUF 模型顺序评测

1. 记录模型显示名、GGUF 路径、profile id、上游 model 字符串、实际监听地址和费用类别。不要将不同 GGUF 覆盖成无法区分的同一 profile 名称。
2. 启动一个 `llama-server -m <实际GGUF路径> --port 18081 --jinja`；根据机器配置 GPU 参数，检查真实 health endpoint。只持有自己创建的 PID，不能批量杀掉无关模型进程。
3. 在 Models 页面创建/选择该 profile，或读取已发布 `/llm/config` 再按其 schema 完整写回。保留其他模型、cost_class、capabilities、timeout_seconds 与 active_model_id；空 active_model_id 表示主动清空激活模型。密钥是 write-only，更新现有 profile 时省略 api_key 保留原密钥，不把屏蔽字符串写成新密钥。
4. 刷新 Benchmark 模型目录，显式选择该 provider/profile，运行模拟层；也可按上节提交 `include_e2e:false`。记录返回的 id。
5. 每约 3–10 秒调用 `benchmark.results`，只检查该 id 的 status。`running` 继续等待；`completed` 记录 scores/usage/case_set；`failed` 保留 error 与完整证据。运行期间不换上游权重、不修改该 profile。
6. 导出 JSON/CSV，停止自己启动的 llama-server 并等待退出，再加载下一个 GGUF。真实 E2E 按下节重新准备与审批，不能复用前一个任务的 nonce 或一次性批准。
7. 汇总时按 case_set 分组，分别报告 Daily score、Bonus、E2E；不要把模拟成功写成 Docker 成功，也不要将不同版本的最高分混为排名。

## 真实 E2E：准备、审阅、两次精确批准、运行

先按 [单机部署](single-host.md) 配置 Executor。Linux 启动前执行 `export DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)`；保持 Executor UID 10001 与最小 socket supplementary group，不能 `chmod 666`、改 root 或把 socket 挂载到其他节点。

创建或选择一个自己拥有、可执行 Go 构建的受管容器及专用 session。容器必须遵守 Executor 资源/镜像政策，外部镜像仍需 digest 与可信管理员签名审批。Benchmark 不创建旧式特殊容器，不绕过所有权。

1. 在 Benchmark 勾选 **Include real E2E**，输入原始 session ID 和容器 ID，点击 **Prepare E2E command**。调用选定 agent 发布的 `POST /benchmark/e2e/prepare`，输入 `{session_id,container_id}`。
2. 返回 `session_id`、`container_id`、`e2e_nonce`、`tool`、`arguments`、`verification_tool`、`verification_arguments`。在界面完整审阅两条命令、容器、路径和参数。
3. 明确同意后点击 **Approve both exact calls once each**。界面向同一 agent 的 `agent.approve` operation（`POST /agent/approvals`）分别发送 `{session_id,tool,arguments}` 与 `{session_id,tool:verification_tool,arguments:verification_arguments}`。用户必须有 `agent.approve`；前端成功标记不是权限，服务端保存并消费精确批准。
4. 点击 Run，将原始模型选择与 `{include_e2e:true,session_id,container_id,e2e_nonce}` 一起提交。更改 agent/session/container 或新任务后必须重新准备与审批。
5. 本版本使用**一个**已有批准容器，在独立 build/run 目录编译和运行；核验静态 ELF、源码归档 `src.tar.gz`、`BUILD.md` 与新鲜 nonce 证据。这不是跨两个容器的部署证明。历史旧文件或 marker 不能让新任务通过。
6. 检查 `cases` 中所有 E2E checkpoint 的真实结果与 detail。命令批准缺失、导出工具不可用、容器不属当前用户、构建失败或独立读取失败均应保留可见错误，不能显示伪成功。

## 常见失败

- **409 benchmark_busy**：当前 provider 有运行中任务；等待，不自动重发可能产生副作用的调用。
- **401/403**：检查 cookie、CSRF、Origin、用户 scope 与节点 trust grants。不要换成免认证 API。
- **模型不可用/目录 revision 变化**：重新读取目录并显式选择；已开始任务失败不允许切到别的模型继续计算同一成绩。
- **连接拒绝或超时**：先修复实际模型进程、容器可达地址与 profile 超时。未观察到真实调用成功前，不宣称模型通过。
- **工具调用输出只是文本**：检查 `--jinja`、GGUF 模板和 tools 能力配置。
- **E2E 批准失效**：用同一原始 session/owner 和精确参数重新 prepare、审阅、批准。不要增加 `approved:true`、自动无条件批准或重用旧 nonce。
- **Executor socket permission denied**：核对宿主 socket 数字 GID 与 Compose 的 DOCKER_GID，重建该服务；不放宽 socket 权限。
- **进程中断**：节点重启把未完成任务记为 failed。保留 error 供诊断；不能删除 running 记录来掩盖仍在执行的工作。

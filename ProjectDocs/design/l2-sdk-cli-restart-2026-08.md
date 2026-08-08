# L2 工具循环重启 — SDK CLI 路线（已实测可行）

| Field | Value |
|---|---|
| **Document** | L2 重启决策 + kill-check 实测记录 + 工程铺设计划 |
| **Date** | 2026-08-09 |
| **Status** | 决策已定（用户 2026-08-09 拍板走 SDK CLI），kill-check 全过，L2a 待开工 |
| **Related** | `l2-local-tool-loop-sinkdown-2026-08.md`（下沉方案，仍有效）、memory `workmax-l2-sdk-blockers` |

## 0. 决策

L2 本地工具循环走 **Claude Agent SDK + claude CLI** 路线，不采用 langchaingo 自实现轻量循环。原两个 blocker 的处置：

1. **CLI 打包** → 照做（独立工程，见 §4 L2d）。
2. **协议限制** → 接受：工具循环仅 `anthropic_compatible`；`openai_compatible`（Ollama 等）保持 L1 纯对话。`server_agent_turn.go` 按 protocol 分发。

## 1. kill-check 结果（2026-08-09，一次全过）

方法与 Wails 迁移相同：架构承诺之前，先用最小可测程序逐条击杀疑点。**完全封闭**：假 Anthropic endpoint（本地 python，讲 Messages SSE 协议）+ 假 API key + 全新空 HOME —— 零真实凭据、零真实流量。

| # | 疑点 | 结果 | 证据 |
|---|---|---|---|
| ① | `WithCLIPath` 能否指定二进制、绕过 PATH 发现 | **PASS** | 直接指向 `~/.local/share/claude/versions/2.1.226`（arm64 Mach-O），cmux shim 未被触碰 |
| ② | `WithEnv` 注入的 `ANTHROPIC_BASE_URL`/`API_KEY` 是否真达子进程 | **PASS** | 假 endpoint 收到 2 个请求，`x-api-key: sk-fake-l2-spike`，路径 `/v1/messages?beta=true` |
| ③ | 完整消息循环能否经 SDK iterator 回流 | **PASS** | `AssistantMessage(tool_use)` → `ResultMessage{is_error:false, turns:2}` |
| ④ | 工具是否真在 `WithCwd` 工作区执行 | **PASS** | CLI 的 Write 工具在工作区物理写出 `l2-proof.txt`，内容逐字匹配 |

**附带确认的事实**：

- **多轮对话由 CLI 自己管理**：第 2 个请求携带完整历史（`user → assistant:tool_use → user:tool_result`），引擎无需自己维护工具循环状态。
- 请求路径带 `?beta=true`；本地 anthropic_compatible endpoint（如 llama.cpp server、vLLM 的 Anthropic 适配）需容忍 query 参数。
- 隔离配方（engine 必须复刻）：`HOME=<独立目录>` + `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` + `DISABLE_TELEMETRY=1` + `DISABLE_AUTOUPDATER=1` —— 防止读取用户的 `~/.claude`（hooks/settings/登录态）与外呼。
- spike 用 `PermissionModeBypassPermissions` + `WithAllowedTools("Write")`；产品形态的权限模型见 L2b。

spike 源码：scratchpad `l2spike/`（main.go + fake_anthropic.py，~150 行）。L2a 落地时其断言将转为 repo 内测试（mock transport + 假 endpoint 两层）。

## 2. 架构（不变项）

- 新包 `server/desktop/local_agent/`：SDK engine 实现 `TurnRunner`（duck-typed，与 `*cloudproxy.Proxy`、`*localinference.Engine` 并列第三个实现）。
- `server_agent_turn.go` 分发：`preferred_route=local` 且 protocol=`anthropic_compatible` → L2 engine；`openai_compatible` → L1。
- 工作区：`<DataDir>/agent_workspace/thread_<uuid>/`（对齐 sinkdown 文档 §4）。
- CacheWriter / SSE / turn intent / 恢复机制**零改动复用**（与 L1 同一接缝）。
- RAG hooks（IndexTurn/Retrieve）同样挂在 L2 turn 前后。

## 3. 新的 renderer 价值（L2 独有）

工具循环产生**文件产物** —— Deliverables 面板第一次对本地路由有内容可放：turn 结束后列出工作区新增/修改的文件。工具活动（`tool_use` 名称流）作为非终端 SSE 事件上行，Run overview 的 "Agent execution" 步骤从二值变为实时活动。

## 4. 里程碑

| 里程碑 | 内容 | 依赖 |
|---|---|---|
| **L2a** | engine 骨架：Query 装配（CLIPath/Env/Cwd/MaxTurns/Stderr）、protocol 分发、文本回流 SSE+Cache、ctx 取消→子进程终止实测、mock transport 单测 + 假 endpoint 集成测 | 无（CLI 用本机开发路径 + `WORKMAX_CLAUDE_CLI_PATH` env） |
| **L2b** | 安全：PreToolUse 路径校验 hook 下沉（`agent_turn_hooks.go`+`path_validator.go`）、AllowedTools 白名单、权限模式定型（bypass+白名单 vs CanUseTool 回调）、工作区逃逸负向测试 | L2a |
| **L2c** | renderer：tool_use 事件流入 Run overview、turn 后工作区 diff → Deliverables 列表（+打开文件=走 open-external？本地文件需新验证路径） | L2a |
| **L2d** | CLI 分发：首次使用时下载 claude-code release 到 `<DataDir>/resources/claude/<ver>`（checksum 钉死、断点续传、quarantine xattr 处理）、无 CLI 时的降级文案、打包 e2e | L2a–c 可并行 |

## 5. 已知残余风险

1. **CLI 版本漂移**：spike 钉在 2.1.226；SDK↔CLI 协议兼容窗口未知。L2d 必须 checksum+版本双钉，升级走显式动作。
2. **子进程生命周期**：ctx cancel → SIGKILL 子进程树是否干净（孤儿 node 进程？）——L2a 必测项。
3. **`?beta=true`**：个别本地推理服务器可能 404 带 query 的路径；引擎报错文案要能指认这一情形。
4. **签名/公证**：下载的二进制在 macOS 的 Gatekeeper 语义（quarantine、`disable-library-validation` 是否需要）→ 与 RAG 的 ONNX dylib 同题，L2d 与资产分发工程合并处理。

# L2 本地工具循环 — 下沉方案（重启 L2 时参考）

| Field | Value |
|---|---|
| **Document** | L2 workagent sink-down plan（重启 L2 的工程方案，避免重新探索） |
| **Date** | 2026-08-08 |
| **Status** | 暂缓（blocker 见 memory `workmax-l2-sdk-blockers`）— 本文记录探索后的下沉方案 |
| **Related** | `workmax-local-first-architecture`、`workmax-l2-sdk-blockers`、L1/L3a/L3b/L3d 已实现 |

## 0. blocker（重启前必须解，详见 memory）

1. **CLI 打包**：Claude Agent SDK `exec.CommandContext` spawn `claude`（Claude Code CLI）二进制；打包 Desktop 不带 → 需跨平台下载 `github.com/anthropics/claude-code` releases + 签名公证 + `WithCLIPath` 注入。
2. **协议限制**：SDK/CLI 仅 Anthropic Messages 协议 → 工具循环仅 `anthropic_compatible` 本地模型可用；`openai_compatible`（Ollama）只能 L1 纯对话。

## 1. workagent 下沉分类（service/tools/workagent/，~34759 行非测试）

### A. 可直接搬 sidecar（~9000 行，0 改动）
- `agent_processor.go` 核心循环 `processAgentConversationInternal`（仅删 2 处 pool 调用 :244-253 GetActiveAccount / :275-310 RecordSuccess/Failure）
- `skills/`（embed.FS，`registry.go:18-22` 注释明示无 globals 依赖）、`prompts/`（embed）、`detectors/`（纯 Go）、`i18n/`
- `claude_agent_go_client.go`（`baseOptions` :146）、`agent_files_context.go` + `files_context_cache.go`
- `agent_turn_hooks.go`（:32）+ `path_validator.go`（安全 PreToolUse）、`tool_duration_tracker.go`
- `knowledge_retriever.go`/`indexer.go`/`normalizer.go`（已 SQLite 兼容，内置 lexical+local-vector）
- artifact browser + render worker（本地 Chrome）

### B. 轻改（~23000 行）
- thread/message/file/plan 仓库：换 SQLite 驱动
- `file_service.go`：`mysqlDuplicateKey=1062` → `gorm.ErrDuplicatedKey`（`thread_lifecycle_service.go:166` 已有 SQLite fallback）
- `agent_client_manager.go`：`BuildClientForAccount`（:283）→ `BuildClient(单账号)`，**保留 `buildEnvVarsFromAccount`（:97-143，SDK 靠它注入 endpoint/key）**

### C. 重写（<100 行）
- `AgentAccountPool.GetActiveAccount` → 单账号 config 读取器（~30 行）
- `agent_reservation` 三元组 → no-op

### D. 丢弃（~2500 行 / 7%）
- `agent_account_pool*.go`（~1900 行，保留 ~50 行 env 装配后整组删）
- `agent_error_notifier.go`（453 行，SMTP）
- `knowledge_pgvector.go` + `qdrant.go` + `pinecone.go`（746 行）
- `api/` 处理器层 `CreditReservationService` 调用

## 2. SDK 集成要点
- `claudecode.Query(ctx, prompt, options...)`（`agent_processor.go:591`）
- endpoint 经 `ANTHROPIC_BASE_URL`/`ANTHROPIC_API_KEY` env（`buildEnvVarsFromAccount` :97-143）
- 工具循环在 SDK 子进程内完成（Read/Write/Bash/Edit/Glob/Grep/WebSearch/WebFetch/Python）
- PreToolUse 安全钩子（`agent_turn_hooks.go:32` + `path_validator.go`）
- options：`WithCwd`(:545,workspace)、`WithHook`(:553)、`WithMcpServers`(:566-582,删 MySQL 的 BuildExternalMcpServers)、`WithResume`(:585)
- 消息循环 `agent_processor.go:628-790`（AssistantMessage/UserMessage/ResultMessage）
- **无 SDK mock**：现有无 fake Query/iterator，L2 测试需新建（抽象 Query 到接口或子进程二进制覆盖）

## 3. L1/L2 整合
- `TurnRunner` 接口（L1 建，`desktop/turn_runner.go`，`Chat(ctx, ChatRequest, SSEWriter) error`）
- L2 新建 SDK engine 实现 `TurnRunner`（与 `*cloudproxy.Proxy`、`*localinference.Engine` duck-typed）
- 按 protocol 分发（`server_agent_turn.go`）：`openai_compatible` → L1 HTTP 纯对话；`anthropic_compatible` → L2 SDK 工具循环

## 4. workspace 本地化
- 云端 `<workspaceRoot>/uid/<uid>/YYYYMMDD/thread_<uuid>/{uploads,outputs}`（`agent_client_manager.go:191`）
- sidecar 映射 `<DataDir>/agent_workspace/...`（DataDir 已有 `db.go:41` ResolveDataDir）
- SDK 子进程 cwd = workspace（`WithCwd`）

## 5. 重启路径（解 blocker 后）
1. 解 CLI 打包（claude 二进制 extraResources + `WithCLIPath`），或自实现轻量工具循环（`langchaingo` 已在 go.mod，支持 OpenAI 兼容但丢 SDK 内置工具生态）
2. 下沉 workagent 核心（A 类可搬，~9000 行）
3. 新 `desktop/local_agent/`（或扩展 local_inference）：SDK engine 实现 TurnRunner
4. `server_agent_turn.go` 按 protocol 分发
5. 测试：新建 SDK mock + packaged e2e

## 6. 关键文件锚点（service/tools/workagent/）
- `agent_processor.go`:591(Query) / :545(Cwd) / :553(hooks) / :580(MCP) / :585(Resume) / :628-790(消息循环) / :475(systemPrompt)
- `claude_agent_go_client.go`:146(baseOptions) / :207-208(Cwd 注释)
- `agent_client_manager.go`:97-143(buildEnvVarsFromAccount) / :159,173-199(EnsureThreadWorkspace) / :283-331(BuildClientForAccount)
- `agent_turn_hooks.go`:32-55(hook 装配) / :81-85(安全决策)
- 打包：`desktop/electron/electron-builder.yml:41-61`（extraResources，无 claude）+ `desktop/scripts/build-mac.sh:327`
- SDK 源码：`~/go/pkg/mod/github.com/jonnyquan/claude-agent-sdk-go@*/pkg/claudesdk/query.go:26`、`internal/subprocess/transport.go:198`(exec)、`internal/cli/discovery.go:29-79`(FindCLI)

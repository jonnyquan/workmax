# WorkMax Agent 引擎本地化设计

| Field | Value |
|---|---|
| **Document** | Agent Engine Localization（sidecar 本地执行 agent） |
| **Date** | 2026-08-08 |
| **Status** | Planning（决策已拍板，L1 待实施） |
| **Related** | `oss-local-desktop-runtime-mode-2026-08.md` · `local-model-route-settings-2026-08.md` · `writer-work-agent-plugin-platform-design-2026-08.md` |
| **Scope** | `server/desktop/`（sidecar）+ `server/service/tools/workagent/`（agent 引擎） |

---

## 1. 目标

让 Desktop + 本地 Server（sidecar `workagent-desktop`）**同机独立运行 agent**：sidecar 用 SQLite，本地完整执行 agent（模型调用 + 工具循环 + 技能），**无云端依赖**。云端（MySQL + `main.go` 云端 server + 计费/多租户）归商业版 WorkMax Plus，最终整体移出本仓。

> 与 `oss-local-desktop-runtime-mode` 的关系：该文档定义"本地 SQLite + 可选云端登录 + Local∥Official 双模型路由"的运行模式；本文是把其中的 **Local route 本地执行** 从"配置就绪、执行未接"补到"真正可在本地跑通 agent"的工程方案。

---

## 2. 关键决策（2026-08-08 拍板）

| ID | 决策 | 含义 / 影响 |
|---|---|---|
| **D1** | **完整本地执行** | 模型 + 工具循环 + 技能全在本地（非"仅本地调模型、工具循环留云端"的混合形态）。→ L1–L3 都要做 |
| **D2** | **身份双模式** | 已登录云端 OAuth → 部分信息可同步云端、UID 来自云端 JWT；**未登录 → 完全本地**（本地 UID，无云端 session）。→ 需引入"本地用户"概念；agent 本地执行在两种模式下都可用 |
| **D3** | **RAG 用 sqlite-vec + 本地 embedding** | 不用 workagent 自带的 64 维哈希 local-vector。→ **技术约束**：sqlite-vec 是 C 扩展，与当前纯 Go 的 `glebarez/sqlite` 不兼容，SQLite 驱动选型需早定（见 §7） |

---

## 3. 现状（代码取证）

### 3.1 sidecar 是"转发型瘦客户端"，不是本地 agent 运行时

- 24 个 loopback 路由：10 纯本地（SQLite/settings/diagnostics）、6 转发云端（agent turn/skills/sync）、8 auth 控制面。
- **agent 执行 100% 转发**：`server/desktop/server_agent_turn.go:231` 的 `streamLegacyAgentTurn` 无条件调 `Proxy.Chat` → `workmax.app/api/work-agent/chat/agent`，无本地分支。
- **local route 配置层 ~90% / 执行层 0%**：`LoadAPIKey()` 无生产调用者；`preferred_route=local` 持久化后**完全不生效**（空开关）。

### 3.2 workagent 引擎对本地化高度友好（~93% 可复用）

| 利好 | 证据 | 本地化成本 |
|---|---|---|
| 模型走 Claude Agent SDK（CLI 子进程），endpoint 经 env 注入 | `agent_processor.go:591` `claudecode.Query`；`ANTHROPIC_BASE_URL/API_KEY` | **0 改动** |
| 无 Redis / 无对象存储 / 无 CDN，文件全本地磁盘 | 全目录 grep 0 命中 | **0 改动** |
| knowledge 内置 lexical + local-vector 双后端 | `knowledge_retriever.go` | 砍 pgvector/qdrant/pinecone（746 行）零损失（D3 升级到 sqlite-vec） |
| 核心推理循环零云端依赖 | `processAgentConversationInternal`（386–847 行） | 循环内核直接可搬 |

workagent 逻辑自包含在 `service/tools/workagent/` 内，当前只被云端 `main.go` build 拉入，**不在 sidecar 的 `-tags desktop` 闭包**。"下沉" = 让 sidecar import 它（剥离云端专属部分后）。

---

## 4. 改造清单（按难度）

**A. 可直接搬到 sidecar（~0 改动）** — ~9,000 行
`agent_processor.go` 核心循环（删 2 处 pool 调用）、整个 `skills/` `prompts/` `detectors/` `i18n/` 子包、`claude_agent_go_client.go`、`agent_files_context.go`、安全 hooks、`knowledge_retriever/indexer/normalizer.go`、artifact browser + render worker（本地 Chrome）。

**B. 轻量改造（GORM 换 SQLite / 剥 SaaS 逻辑）** — ~23,000 行
thread/message/file/plan 仓库换 SQLite 驱动；`file_service.go` 的 `mysqlDuplicateKey` 改 `gorm.ErrDuplicatedKey`（已有 SQLite fallback）；`agent_client_manager.go` 改单账号但**保留 `buildEnvVarsFromAccount`**。

**C. 重写（接口保留）** — <100 行
`AgentAccountPool.GetActiveAccount` → 单账号 config 读取器；`agent_reservation` → no-op。

**D. 丢弃（云端专属）** — ~2,500 行（7%）
`agent_account_pool*.go`（~1,900 行，保留 ~50 行 env 装配后整组删）、`agent_error_notifier.go`（SMTP）、`knowledge_pgvector.go`+`qdrant.go`+`pinecone.go`、`api/` 层 `CreditReservationService` 调用。

---

## 5. 路线（L1–L4）

| 阶段 | 目标 | 完成判据 |
|---|---|---|
| **L1 · 最小本地推理** | preferred_route=local 时跑通最简 turn（纯模型对话流，先不做工具循环） | 用户配本地模型 → 发消息 → 收到本地模型流式回复 |
| **L2 · 本地工具循环** | agent 本地调工具（Read/Write/Bash 等）+ 技能 | 下沉 `agent_processor` 核心循环 + skills/prompts/hooks；接 SDK 子进程本地 workspace |
| **L3 · 本地线程/文件/知识库 + 双模式身份** | 端到端本地体验 | 本地线程创建、本地文件存储（填 `local_render/`）、sqlite-vec + 本地 embedding、剥离 account pool/reservation、D2 双模式身份 |
| **L4 · 云端剥离** | 开源仓只剩 desktop + 本地 server + SQLite | `main.go` + MySQL + PG 整体移至 Plus；删云端契约依赖 |

> L1 是验证整个方向的**最小闭环**。L1–L3 不动 Plus 仓，L4 才触仓库边界。

---

## 6. L1 详细拆解（第一个里程碑）

L1 范围：preferred_route=local 时，纯模型对话流跑通（不接工具循环、不接技能）。三块改动：

1. **本地推理客户端**（新文件，`server/desktop/local_inference/`）：读 `ModelSettings.Get()` + `LoadAPIKey()` → 按协议（`openai_compatible` → `POST {base_url}/v1/chat/completions`；`anthropic_compatible` → `POST {base_url}/v1/messages`）发起**流式** HTTP 调用。
2. **路由分支**：`handleAgentChat` / `streamLegacyAgentTurn` 加 `if preferred_route == local { localChat } else { proxy.Chat }`。`Proxy` 结构加 `ModelSettings` 字段。
3. **SSE 事件格式转换**：把 OpenAI/Anthropic 的流式帧统一转成 sidecar 现有的 `cloudproxy.SSEEvent`，让 `CacheWriter` 和 renderer 协议无关（**缓存/幂等/恢复全部免费复用**）。

L1 不碰：工具循环、技能、线程云端同步、身份双模式、知识库。

---

## 7. 待定技术选型（需在对应阶段前定）

| 项 | 何时需要 | 选项 / 倾向 |
|---|---|---|
| **SQLite 驱动**（D3 约束） | L3（sqlite-vec），但影响全局 | 当前 `glebarez/sqlite`（纯 Go）**不支持 load_extension**，无法加载 sqlite-vec。选项：(a) 换 `mattn/go-sqlite3`（cgo，支持 load_extension）；(b) sqlite-vec 官方 Go binding；(c) 向量存独立文件/进程。需尽早 spike |
| **本地 embedding 模型来源**（D3） | L3 | 用户 local endpoint 的 `/v1/embeddings`？还是 sidecar 内置轻量模型？前者零依赖但依赖用户配置 |
| **本地 UID（D2 未登录模式）** | L3 | device-local 稳定 ID；线程命名空间与云端 UID 隔离 |

---

## 8. 风险

- **Claude Agent SDK 对非 Anthropic 模型的兼容性**：SDK 走 Claude Code CLI 子进程协议，本地模型若非 Anthropic 兼容，工具循环（L2）可能行为异常。L1（纯对话）无此风险，L2 需验证。
- **sqlite-vec + cgo 跨平台打包**：macOS/Windows/Linux 的 Electron 分发 + cgo 是已知麻烦点。
- **双模式状态机复杂度**（D2）：登录/未登录切换时的线程归属、同步冲突需仔细设计。

---

## 9. 相关代码索引

- sidecar 路由表：`server/desktop/route_policy.go:84-109`
- agent 转发出口：`server/desktop/server_agent_turn.go:231`、`cloud_proxy/proxy.go:109`
- local route 配置：`server/desktop/local_model_settings.go`、`server_local_model.go`、`migrations_desktop/0004_local_model_settings.sql`
- agent 引擎核心：`server/service/tools/workagent/agent_processor.go`、`claude_agent_go_client.go`、`agent_client_manager.go`
- 云端专属（待弃）：`agent_account_pool*.go`、`knowledge_pgvector.go`、`agent_error_notifier.go`

# L2 工具循环专项研究：Claude Agent SDK + Pi Agent 双 runtime（2026-08）

结论：不自研 agent loop。L2 = 一个统一的 `AgentRuntime` 适配层 + 两个子进程 runtime——
**Claude runtime**（我们自己的 `jonnyquan/claude-agent-sdk-go`，已在 `local_agent` 使用，保留并升级用法）
和 **Pi runtime**（pi.dev / earendil-works/pi，`pi --mode rpc`，MIT，新增）。
两者同构：都是「Go 驱动子进程 + stdin/stdout JSONL」，适配层收敛在一个接口上。
本文取代优化方案 Phase 2 的「自研循环」路线。

## 0. 为什么是这个组合

| | Claude runtime（我们的 SDK） | Pi runtime |
|---|---|---|
| 协议/厂商 | Anthropic（现有 anthropic_compatible 链路继续用） | **43 provider / 9 种原生 API**：OpenAI Completions+Responses、Gemini、Bedrock Converse、Mistral、任意 OpenAI 兼容端点（Ollama/vLLM/LM Studio）；`models.json` 自定义 baseUrl/apiKey/兼容旋钮，热重载 |
| 工具审批 | SDK 内建 `WithCanUseTool` 回调（Allow/Deny 结果对象） | 无内建；靠**扩展 + `extension_ui_request/response` stdout/stdin 往返**（官方模式，有 permission-gate.ts 参考实现） |
| 会话续写 | `WithResume/WithForkSession/WithSessionStore`（现 engine 没用，每轮全新会话——直接修复） | `--session <path|id>` / `switch_session` / `fork` / `clone`；会话是**树**（JSONL 树结构，entry id 稳定游标 `get_entries {since}`） |
| 压缩 | CLI 自动 compact | 内建自动 + `compact` 命令 + start/end 事件，可扩展接管 |
| 图片 | SDK 支持（现 engine 丢弃图片——修复） | `prompt.images: [{type:"image", data:base64, mimeType}]` |
| MCP / Skills | MCP 一等公民；skills/plugins/subagents | **明确不做 MCP**；Skills 走 agentskills.io 标准，可直接复用 `~/.claude/skills` |
| 打包 | CLI 需捆绑：SDK 的 `_bundled/` 发现机制（discovery.go:30 第一优先级）或 `WithCLIPath` 指向应用资源；商用打包被 Anthropic Commercial ToS 允许（不得用 "Claude Code" 品牌） | **官方 bun 单二进制，6 平台**（darwin/linux/windows × arm64/x64，带 SHA256SUMS），像内嵌 ripgrep 一样内嵌，不要求用户装 Node |
| 许可 | SDK 是我们自己的；CLI 商业条款 | MIT（根 LICENSE，分发时带上） |
| 成本/用量 | usage 事件 | `get_session_stats`（tokens/cost/contextUsage），内建跨厂商成本核算 |

互补关系清晰：Claude 侧吃 Anthropic 生态（MCP/skills/官方模型），Pi 侧吃「其它一切协议」——
正好接住现在 `openai_compatible 永远走 L1 纯对话` 的缺口。

## 1. 路由设计（turn_runner 演进）

现状：`turn_runner.go:30` 按 `protocol == anthropic_compatible && LocalAgent != nil` 二分 L1/L2。
目标：模型设置增加 `engine` 维度（默认按协议推导，用户可显式覆盖）：

```
preferred_route == local:
  chat_mode 需要工具?
    engine=claude (默认当 protocol=anthropic_compatible) → ClaudeRuntime
    engine=pi     (默认当 protocol=openai_compatible 等) → PiRuntime
  纯对话 → L1 localinference（保留，轻量路径不动）
else → cloud_proxy（不动）
```

现有 CLI-L2（`local_agent/engine.go`）重构为 ClaudeRuntime 的实现体，不是并行第三套。

## 2. 统一 AgentRuntime 接口（最小公共面）

```go
type AgentRuntime interface {
    // 每 thread 一个运行中实例；sessionRef 由 runtime 自解释
    //（Claude: session_id；Pi: 会话 jsonl 路径）
    Start(ctx, StartOptions) error         // cwd/model/工具允许集/系统提示/sessionRef
    Send(ctx, UserInput) error             // text + images + mode(new|steer|followup)
    Interrupt(ctx) error
    Events() <-chan RuntimeEvent           // 统一事件，见 §3
    ResolveApproval(id string, d Decision) // 审批回执
    Close() error
}
```

会话连续性：`w_workagent_thread` 增加 `agent_session_ref TEXT`（runtime 自解释），
turn 结束时回写。两侧都因此获得真 resume，废除「历史压成单字符串」的现状。

## 3. 事件映射（两侧 → 统一 → SSE）

统一事件集（同时是 renderer SSE 协议扩展的依据，对应优化方案 4.2）：
`TurnStart / TextDelta / ThinkingDelta / ToolCallStart / ToolCallArgsDelta / ToolCallEnd /
ToolResult / ApprovalRequest / TurnEnd / Settled / Usage / CompactionStart|End / Error`

| 统一事件 | Claude（stream-json / SDK 消息流） | Pi（RPC 事件） |
|---|---|---|
| TextDelta | partial messages（`WithIncludePartialMessages`，现在没开） | `message_update.assistantMessageEvent.text_delta` |
| ThinkingDelta | thinking block delta | `thinking_delta`（同构 Anthropic thinking block） |
| ToolCall* | tool_use block（start/delta 需开 partial） | `toolcall_start/delta/end`（delta 是原始 JSON 分片，自行拼接） |
| ToolResult | user/tool_result | `tool_execution_end {result, isError}` |
| ApprovalRequest | **`WithCanUseTool` 回调**（阻塞等待 → 转 SSE + resolve） | **`extension_ui_request`**（select/confirm）→ stdin `extension_ui_response` |
| TurnEnd/Settled | `result/*` | `agent_end`（可能 willRetry）/ **`agent_settled` 才是完成判据** |
| Usage | result.usage/cost | `message_end.message.usage` + `get_session_stats` |

## 4. Pi 适配器实现要点（来自 rpc-types.ts 源码级摸底）

1. 帧：严格 LF-JSONL（只按 `\n` 切、剥 `\r`）；`bufio.Reader.ReadBytes('\n')` + 大 buffer
   （工具结果/base64 图片可达 MB 级）。
2. 三通道分流：`{type:"response"}` 按 `id` 路由 pending map（响应可乱序）；
   `{type:"extension_ui_request"}` 走审批；其余为事件广播。**未知 type/事件必须静默容忍**
   （事件表比文档多：`entry_appended`/`session_info_changed` 等；以 `rpc-types.ts` 为准）。
3. `prompt` 的 response ≠ 回合完成（仅表示被接受/入队）；完成判据 = `agent_settled`。
   接受后的失败只走事件流。
4. 流式中发消息必须带 `streamingBehavior: "steer"|"followUp"`，否则报错。
5. 持续消费 stdout（Pi 有背压等待）；stderr 单独收集；stdin EOF = 优雅关闭。
6. 多会话并发 = 多进程（一个 RPC 进程一个活动会话）；进程池按 thread 管理，
   空闲超时回收。
7. 启动参数基线：`--mode rpc --session <path> --session-dir <DataDir>/pi_sessions
   --tools read,write,edit,bash -e <resources>/workmax-permissions.ts
   --no-approve` + env `PI_OFFLINE=1 PI_TELEMETRY=0 PI_SKIP_VERSION_CHECK=1`
   （本地优先产品三件套）+ `PI_CODING_AGENT_DIR` 指到我们的数据目录（不碰用户 `~/.pi`）。
8. 模型配置：由 sidecar 从 `w_desktop_model_settings` 生成 `models.json`
   （baseUrl/apiKey/兼容旋钮），写入我们私有的 config dir；`OpenAICompletionsCompat`
   的 11 个兼容开关正是接国产/自建端点的关键。
9. 版本策略：钉死 pi 版本随包分发；宽松解析；升级走我们的回归夹具
   （录制 RPC 会话帧做 golden test）。

## 5. workmax-permissions.ts（Pi 路线的核心自研件）

随包分发的 TypeScript 扩展（jiti 直跑，无需编译），职责：
1. **审批**：`pi.on("tool_call")` → 按规则判定 → 需要人批时 `await ctx.ui.confirm/select`
   （RPC 下即 stdout `extension_ui_request` → 我们 SSE 转发给 renderer → 用户选择 →
   HTTP 回 sidecar → stdin `extension_ui_response`）→ 拒绝返回 `{block:true, reason}`。
2. **路径守卫镜像**：把 `pathguard.go` 的规则在扩展侧复刻（bash 命令 + 文件路径），
   双保险（`--tools` allowlist 是第一道粗闸）。
3. **（二期）反向工具桥**：`pi.registerTool` + 扩展内 `fetch("http://127.0.0.1:<port>/...")`
   调 sidecar——把我们的知识检索（L3c）、文件库等暴露成 pi 工具。RPC 协议没有
   「工具转发客户端执行」机制，这是官方认可的替代路（参考 examples/extensions/ssh.ts）。

参考实现直接可抄：`examples/extensions/permission-gate.ts`、`rpc-demo.ts`、
`protected-paths.ts`、`confirm-destructive.ts`。

## 6. Claude runtime 升级清单（全是用起来自己 SDK 的既有能力）

现 `local_agent/engine.go` 只用了 SDK 的一小角。升级：
1. `WithCanUseTool` 替代 `PermissionModeBypassPermissions`——审批流与 Pi 侧共用同一
   SSE/存储/UI（规则持久化 SQLite：`Tool(glob)`/`Tool=literal`，deny>ask>allow）。
2. `WithResume` + thread 的 `agent_session_ref`——废除历史拼接大字符串。
3. `WithIncludePartialMessages`——TextDelta 流式增量（现在整 TextBlock 一发）。
4. `WithSystemPrompt`——sidecar 本地路径现在完全没有 system prompt。
5. 图片：接 SDK 的图片输入（现在直接丢弃并道歉）。
6. 打包：claude CLI 放应用资源目录，`WithCLIPath` 指过去（SDK 的 `_bundled` 发现机制
   也可用；是自己的库，discovery 规则可改）。品牌合规：呈现为 "Powered by Claude"，
   不用 "Claude Code" 字样。
7. 待验证项：调研中有说法称 CLI 不尊重 `ANTHROPIC_BASE_URL`——与我们现有实现
   （engine.go 注入该 env 且线上在用）矛盾，按现状为准，升级 CLI 版本时回归验证。

## 7. 能力面差异的产品处理

- **MCP**：Claude 侧原生、Pi 侧无。产品层按引擎声明能力（引擎选择 UI 标注），
  不强行对齐；后续如需可在 workmax-permissions.ts 里加 MCP client 扩展。
- **沙箱**：两侧都无真沙箱。统一靠：`--tools`/`allowedTools` allowlist +
  审批 + pathguard/扩展镜像；优化方案 2.4 的 confine/检查点捕获照做。
- **会话树/fork**（Pi 特有）与 **subagents**（Claude 特有）：一期不暴露，
  适配层预留事件透传。

## 8. 里程碑

- **R0 接口抽取**（3-5 天）：定义 `AgentRuntime` + 统一事件；`local_agent/engine.go`
  重构为 ClaudeRuntime；SSE 协议扩展 reasoning/tool 参数/approval 事件（renderer 同步
  最小改造：审批卡 + 思维链字幕，形状按 Reasonix 前端清单）。
- **R1 Claude 升级**（1 周）：§6 的 1-5；审批规则表；打包 CLI（§6.6）。
- **R2 Pi MVP**（1.5-2 周）：进程管理 + JSONL 编解码 + 事件映射 + per-thread 会话文件
  + models.json 生成 + 二进制打包（6 平台产物进 extraResources）；先带 `--tools
  read,grep,find,ls` 只读档冒烟，再开写档。
- **R3 审批扩展**（1 周）：workmax-permissions.ts（审批 + 路径守卫镜像）+ 双 runtime
  审批走同一 UI/规则存储；e2e：无 claude CLI 环境下 Ollama/DeepSeek 端点跑通
  读写工具循环且审批默认 ask。
- **R4 增强**（按需）：反向工具桥（知识检索进 pi）、`bash` 直执行通道接终端 UI、
  `get_session_stats` 接成本面板、会话树 UI。

## 9. 风险清单

| 风险 | 应对 |
|---|---|
| pi 版本节奏快（0.84.x） | 钉版本随包分发；宽松解析；RPC golden 帧回归 |
| 文档与源码不同步 | 以 `rpc-types.ts`/`agent-session.ts` 为准（已发现 3 处漂移） |
| 扩展分发被用户篡改 | 扩展放应用资源目录 + 启动校验 hash；`--no-approve` 关闭 pi 自己的信任提示，信任决策收敛到我们侧 |
| 两 runtime 事件语义漂移 | 统一事件层加契约测试：同一脚本化对话在两侧跑，断言事件序列同构 |
| claude CLI 打包体积/更新 | 资源目录按需下载亦可（SDK discovery 支持路径注入）；与 pi 二进制同一套资源分发机制（复用 L3c assets 通道） |

## 10. 材料索引

- pi 仓库（scratchpad 克隆）：`packages/coding-agent/docs/rpc.md`（协议 1578 行）、
  `src/modes/rpc/rpc-types.ts`（权威类型）、`jsonl.ts`（分帧，照抄）、
  `docs/{providers,models,extensions,skills,session-format,compaction,security}.md`、
  `examples/extensions/permission-gate.ts`。
- 我们的 SDK：`~/go/pkg/mod/github.com/jonnyquan/claude-agent-sdk-go@v0.0.0-20260725181726/`
  （`pkg/claudesdk/options.go` 全部 With* 选项、`permissions.go`、`internal/cli/discovery.go`）。
- 对比文章存档：`.firecrawl/pi-vs-claude-sdk.md`。

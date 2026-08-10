# WorkMax 代码优化方案（2026-08）

输入：三份代码摸底（server agent 链路 / knowledge 存储检索 / desktop 壳与渲染层）
× Reasonix 借鉴清单（`reasonix-borrowables-2026-08.md`、`reasonix-frontend-borrowables-2026-08.md`，
均已本地 git-exclude）。

## 现状总评

- **壳层与持久化是强项**：Wails capability origin + 反代 + CSP、bridge 类型化路由 + 双侧策略表、
  SQLite 迁移/幂等/崩溃恢复/SessionLease——这几块接近完善，本轮不动。
- **认知修正**：L2 本地工具循环**已实现**（`server/desktop/local_agent/`，借 claude CLI，
  `WORKMAX_CLAUDE_CLI_PATH` 门控默认关闭）；「sidecar 只转发」仅对云端路由成立。
- **三个当下就在流血的缺陷**（P0）：embedding 批处理越界 panic 会崩整个 sidecar、
  多本地账号 RAG 索引串号（隐私回归）、SSE 解析有静默丢帧哑弹。
- **两大性能债**：前端每 token O(n²) 重设全文 + 强制布局、每轮结束全量重绘 transcript。
- **一个战略缺口**：Go 侧没有自研工具循环/provider 抽象扩展位——这是 L2 所有限制
  （仅 Anthropic、依赖外部 CLI、无 resume、丢图片）的共同根因。

---

## Phase 0 — 止血（正确性/隐私/崩溃，1 周内，可并行）

优先级即列表顺序。每项独立可交付。

### 0.1 EmbedBatch 越界 panic（🔴 崩溃）
`server/desktop/knowledge/embed.go:155-176` 假设 batch 内 encoding 等长，sugarme 默认不 pad
→ 「第一个 chunk 不是最长」即 panic，且跑在裸 goroutine 里 → **整个 sidecar 崩溃**。
方案：tokenizer 显式 `WithPadding(BatchLongest)` + `WithTruncation`；或按 `max(len(enc.Ids))`
计算 seqLength。同时给 `go IndexFile/IndexTurn` 的 goroutine 加 recover + 错误上报
（Reasonix 的教训：第三方库 `log.Fatal` 也能杀进程，调用侧要兜底）。

### 0.2 多账号 RAG 串号（🔴 隐私）
vec0 表无 uid 列，A 账号文件内容会被注入 B 账号对话（与 0005 迁移的隔离承诺冲突）。
方案：与 3.1 的 schema 重建合并解决（uid 做 metadata 列参与 KNN 过滤）。
**在 schema 重建落地前的临时闸**：多账号（`w_desktop_local_account` 行数 > 1）时禁用检索注入，
只保留索引写入（chunk_uid 已含来源，可事后迁移）。一行判断，先堵住隐私面。

### 0.3 SSE 解析双缺陷 + 解析器合一（🔴 正确性）
- `cloud_proxy/sse_pipe.go:216` 只认 `"data: "`（带空格），`data:{...}` 帧被静默丢弃
  （注释说 pass through 但代码不存在）；`local_inference` 的解析器是对的——行为已分叉。
- 上游 `http.Client{Timeout:0}` 且无任何读 deadline（注释与实现不符），半开连接 = turn 永久挂。
方案：抽一个共享 SSE reader 包（两处手写解析器合一），修 data 前缀；加空闲看门狗
（Reasonix 形状：独立 goroutine，120s 无数据 → `resp.Body.Close()` 强制解阻塞，标记为
可重试的 stream-interrupted）。顺带修 `proxy.go:423` 死分支。

### 0.4 前端上传两个 bug（🔴 功能）
`renderer.js:944-955`：上传 Promise 无 `.catch`（reject 后 chip 永远 uploading）；
`uploadGeneration` 全局单值 → 多选上传只有最后一个生效。
方案：per-upload 的局部 token 代替全局计数器 + 补 catch 三态。

### 0.5 小额止血
- `agentTurnLocks` 按 turn_uuid 无界增长（`server/desktop/server.go:146`）：turn 终结时删除条目。
- composer 草稿保护：`refresh()`/session 变更前若 `chatInput.value` 非空则保留或暂存
  （新建线程草稿已有同类保护，抄同一模式）。
- 中文预算修正：`maxHistoryChars`、`maxRetrievalContextChars` 按 rune 计数（当前按字节，
  中文有效容量只有英文 1/3）；注入预算 `break`→`continue` + 计数对齐。

## Phase 1 — 流式渲染性能（renderer，1-2 周）

目标：长会话下每 token 成本 O(1)、每轮结束成本 O(1)。全部借 Reasonix 已验证的形状。

### 1.1 token 流 rAF 合帧（最高 ROI）
`renderer.js:4083-4093` 每帧 `textContent=全文` + `querySelector` + `scrollTop=scrollHeight`。
方案：移植 rafBatch 模式（~50 行）——delta 入队，rAF flush 一次；DOM 只
`Text.appendData(delta)` 追加；`#jump-latest` 引用缓存；滚动写入合并进同一帧。
**非 delta 事件（tool_use/done）到达先 drain**，保因果序。

### 1.2 消除「每轮结束全量重绘」
`finishActiveTurn → reconcileCompletedTurn → renderCachedMessages` 清空重建整个列表，
成本 O(会话长度) 落在每一轮，还引出 work log「幸存副本」补丁。
方案：对账改为**原位替换本轮两条消息**（快照按 uuid/幂等键定位），其余 DOM 不动；
列表滚动锚定不变。虚拟化（P2）之前这一步就能把长会话救回来。

### 1.3 Markdown 流式中间态（体验）
现状流式全程纯文本、终结瞬间整体形变 + 全量解析尖峰。
方案：移植 `streamingCommitTarget` 思路——已完成块（空行/闭合围栏/标题）增量解析提交，
进行中块留纯文本尾巴；未闭合围栏例外。我们的手写 markdown 渲染器是逐块函数，
天然适配「按块追加」。终结时只解析未提交的尾部，尖峰消失。

### 1.4 性能基线
优化前先加一个最小基准（Node stub 套件里加长会话 fixture 计时 + 手动 Performance 面板
剧本），否则无法证明收益、无法防退化。z-index token 化、bundle 体积断言这类门禁
顺手加进 `check-bundled-renderer.sh`。

## Phase 2 — L2 双 runtime 适配（方向已改，专项方案见 `l2-agent-runtime-study-2026-08.md`）

> 2026-08 决策：**不自研 agent loop**。L2 = 统一 `AgentRuntime` 适配层 + 两个子进程
> runtime：Claude（我们自己的 claude-agent-sdk-go，升级用法：CanUseTool/Resume/
> partial messages/system prompt/捆绑 CLI）+ Pi（pi.dev `--mode rpc`，MIT 单二进制，
> 覆盖 OpenAI-compatible 等其余全部协议）。里程碑 R0-R4 见专项文档。
> 本节以下的 2.1-2.3（自研 provider/工具/循环）**作废**；2.4 安全底座与 2.5 上下文管理
> 的原则保留，落点改为专项文档 §5-§7（审批/confine/检查点捕获跟随 runtime 落地）。

原方案（作废，留档）：现有 L2（claude CLI 方案）的四个限制同根：Go 侧没有自己的循环。
按 Reasonix 形状重建，云端路由与现有 CLI-L2 保持不动，新循环作为第三个 TurnRunner
并行落地，跑通后再切。

### 2.1 provider 抽象扩容（先行，独立有收益）
现 `protocolAdapter` 4 方法无 tools/system/params 位。
重定义为 Reasonix 形：`Provider{Name(); Stream(ctx, Request) <-chan Chunk}` +
可选接口能力协商 + `Register(kind, Factory)`。Chunk 含 ToolCallStart/ArgsDelta。
迁移现有 openai/anthropic 两个 adapter 进新接口（L1 即刻受益：system prompt、
max_tokens 可配、usage 回读、prompt cache 标记 `cache_control`）。
整文件移植：`schema_canonicalize.go`、两层重试（header 退避 + body 冻结重放）、
半开连接检测。

### 2.2 工具层
`Tool{Name/Description/Schema/Execute/ReadOnly}` + Registry（Add 时 canonicalize、
Schemas 字典序）。首批工具对齐现 L2 白名单：Read/Write/Edit/Glob/Grep（Bash 仍缓）。
`Previewer` 可选接口留出 diff 预览位。输出 32KB 头尾截断。

### 2.3 循环与守卫
runToolLoop：步数预算(24 沿用) + grace round + 空答复上限 + 重复失败签名 + 只读并行。
上下文组装弃「压成单字符串」，改结构化 messages + 历史裁剪复用 L1 逻辑。
带类型的 pause 错误区分「失败」与「可继续」。

### 2.4 安全底座（与循环同步，不后补）
- pathguard 黑名单之上加 confine 白名单层（写根解析最深存在祖先、敏感文件读黑名单）；
- 工具审批事件：SSE 增加 `approval_request` 事件 + `/agent/turns/:uuid/approve` 端点，
  renderer 出四选项卡（允许一次/本会话/始终/拒绝）；规则持久化到 SQLite
  （`Tool(glob)`/`Tool=literal`，deny>ask>allow）。默认档 ask，现 CLI-L2 的 bypass 模式
  收敛为显式选项。
- 最小检查点：写工具执行前快照 before 内容（内容寻址 blob 目录 + SQLite 元数据表），
  turn 级 rewind 后置到 Phase 4，但**捕获从第一天开始**（没有数据就永远做不了回滚）。

### 2.5 上下文管理（可与 2.3 并行）
先做无 LLM 的分级：0.6 剪陈旧 tool 输出（snip 标记可反解）→ 0.8 才考虑付费摘要
（首版可以只做 snip + 「压缩不可用就硬截断」）。token 计数接入已有 tokenizer
（`utils/tokenizer.go` 现在无人调用）。canonical 消息表不动，投影另存——沿用
Reasonix「投影 sidecar」形状，SQLite 一张表即可。

## Phase 3 — 知识/L3c 补课（2-3 周，与 Phase 2 可并行）

### 3.1 vec0 schema 重建（0.2 的正解，也是一切过滤检索的前置）
现表全部 aux 列（`+` 前缀）不能参与 KNN 过滤，且脱离 migration 体系。
方案：新表 `uid/scope/source_type/created_at` 用 **metadata 列**；建 vec0 专用版本表
（`_local_meta` 里记 schema 版本，不匹配 → 重建 + 后台重索引）；`DeleteBySource`
走 metadata 过滤。同时加启动自检（建表+插入+KNN 一条），防 glebarez×modernc
脆弱耦合在升级后静默失效。

### 3.2 交付链修复（RAG 现在生产上是关的）
`knowledge/assets` 包代码完整但零调用方、manifest platforms 为空。
方案：定模型资源分发渠道（打包进 extraResources 或首次下载），接线 assets 包，
CI 加一条带真实资源的 embed 冒烟（现在 embed_test 永远 skip）。

### 3.3 检索质量
- chunker 中文修正（按 rune + 中文按 ~200 字，超 tokenizer 截断线的静默丢弃是硬伤）；
- Search 加绝对相似度下限 + 相对阈值裁剪（KeepTopRelativeScore 形状）+
  通用 turn 抑制（"继续/ok" 不召回）；
- FTS5 影子表（modernc 内置，bm25 白送）+ RRF 融合 vec KNN——中文用 unigram+bigram
  分词器；
- 注入改型：从「拼 user text 头部」改为「turn 尾部低权威块」，每条带 score/来源，
  措辞明确「可能过时，不得覆盖当前请求」。

### 3.4 写入策略与评测
- turn 无条件自动入库改为：文件默认索引、对话轮入库加质量门（长度/信息量），
  为将来「事实模型（subject_key/activation/volatility）」留列位但本期不做完整记忆系统；
- 加 `WORKMAX_EXPERIMENT_NO_RAG=1` 反事实开关 + retrieval 事件落遥测，
  为「检索是否净收益」积累数据（Reasonix 方法论：Task Pass 差值，不是 recall@k）。

## Phase 4 — renderer 架构与产品补全（渐进，穿插进行）

1. **模块化**：renderer.js 5857 行全局脚本拆 ES modules（CSP `script-src 'self'` 允许
   `type=module`；白名单脚本改为允许 `lib/` 目录或产物清单）。拆分次序：
   markdown / sse-events / thread-list / composer / context-panel。generation 计数器
   收敛成统一 fence 工具（8 个散落计数器 + 注释规则 → 一个抽象）。
2. **协议扩展**：SSE 增加 reasoning 通道与工具参数/结果/耗时字段（tool 事件现在只有
   name+basename）；前端上思维链单行活字幕 + 工具卡状态（Reasonix 形状）。
3. **虚拟化/分页**：消息接口分页 + 列表虚拟化（长会话打开千级节点）。
4. **主题切换**：令牌已齐，media query 改 `:root[data-theme]` + 三态设置。
5. **代码高亮**：CSP 内打包 shiki 预编译或极简 tokenizer；Editor Seam 形状预留
   （30 行 lazy 契约文件，成本为零）。
6. 双份 CSP（header vs index.html meta）合一；`os.DirFS`→`embed.FS`（W4）；
   `main.go` 五模式拆文件。

## 排序逻辑与依赖

```
Phase 0（全部）──┬─→ Phase 1（1.1→1.2→1.3→1.4）
                └─→ Phase 3.1（schema，0.2 的正解）→ 3.2/3.3/3.4
Phase 2.1（provider 扩容）→ 2.2/2.3 ←同步→ 2.4 安全底座；2.5 可并行
Phase 4 穿插，2 的协议扩展（4.2）依赖 2.2/2.3 落地
```

- Phase 0 全部是修真实缺陷，无架构风险，先做。
- Phase 1 与 Phase 3 互不依赖，可双线。
- Phase 2 是唯一的战略重构，前置条件是 0.3 的 SSE 公共层（循环要复用）。
  **明确不做**：双模型 planner、evidence ledger、extension protocol、完整记忆事实模型
  ——Reasonix 清单里标了「不抄/后置」的按原判断执行。

## 验收口径

- P0：sidecar 连续索引 1000 个混合长度 chunk 不崩；双账号互查无串号；
  `data:` 无空格帧回归测试；断网 turn 在 120s 内以可重试错误终结。
- P1：8k token 回答流式期间主线程无 >50ms long task；500 轮会话每轮结束重绘 <16ms。
- P2：无 claude CLI 环境下 OpenAI-compatible 端点跑通 Read/Write/Edit 工具循环，
  审批默认 ask 且规则可持久化。
- P3：RAG 在打包产物上开箱可用；"继续" 不触发注入；中文 chunk 无 tokenizer 截断丢失。

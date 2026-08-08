# WorkMax Desktop → Wails 迁移：实施设计（含评估记录）

| Field | Value |
|---|---|
| **Document** | Electron → Wails shell 迁移：**实施设计**（Part A 为原评估记录） |
| **Date** | 2026-08-08（rev.3，Wails 转正为主线 + 最快路径排期） |
| **Status** | **已采纳（Adopted）**——Wails 为主线 shell，Electron 降级为回滚热备；按 §0.5 最快路径执行。Wails 侧仍 0 行代码，P0 待开工 |
| **Related** | `oss-local-desktop-runtime-mode-2026-08.md`（本地 Desktop 运行时模式）· `design/l2-local-tool-loop-sinkdown-2026-08.md`（L2 暂缓）· `desktop/README.md`（当前架构）· `desktop/electron/src/*`（现 shell）· `server/desktop/*`、`server/cmd/workagent-desktop/main.go`（Go sidecar）· `server/desktop/knowledge/*`（L3c RAG，cgo） |
| **调查方法** | 三路并行代码测绘：① renderer↔backend 桥接面 ② Go sidecar 进程内嵌入可行性 ③ 安全/登录/打包链 |

> **代码基线校准（rev.2）**：本文首版写于 L3b 之前。L3c（本地 RAG）与 L3d（未登录本地路由）落地后，两条事实已变：**① sidecar 不再是纯 Go——`server/desktop/knowledge` 带 `//go:build desktop && cgo` 且被 `cmd/workagent-desktop/main.go` 无条件 import，`CGO_ENABLED=0` 直接编译失败**（见 §1、§6、附录 B）；**② 未登录也能跑全套本地功能**，改变了 D4 的残余风险面（见 §9 D4）。
>
> 为避免锚点再次漂移，本文的代码引用一律采用**函数/常量名锚点**而非行号；仅在指向大段连续代码时给出"约 N 行"量级。

---

## 0. TL;DR（结论先行）

**技术上强烈可行，且比绝大多数 Electron→X 迁移都干净**：核心 Go 后端可直接进程内嵌入，renderer 契约几乎不动。真实成本不在"搬代码"，而在"重做安全信任边界 + 重建打包链"。

**前置标定（最重要的一句）**：Wails 只解决 **shell 层性能**（内存/体积/冷启动），**不解决旗舰脑图画布的性能**——画布卡顿是 renderer 渲染层问题（DOM/SVG vs Canvas/WebGL），换 shell 一点都不改变它。

- 痛点是 **app 太重（内存/安装包/启动慢）** → Wails 是对的、干净的根治方案，现在 `0.1.0-p1-ea` 阶段是迁移最便宜的窗口。
- 痛点是 **画布/交互卡顿** → 别迁 Wails，先把画布搬到 `<canvas>`/WebGL，那跟 shell 无关。（具体画布库选型与长历史虚拟化见 §1.5.2，无论迁不迁都适用。）

**推荐架构**：Option A（保留嵌入式 loopback HTTP server），不是 Option B（路由全改 Wails 绑定）。理由见 §3。

**rev.2 增量结论（不改变上面的推荐）**：L3c 让 sidecar 变成 cgo 依赖，迁移多了一块**原生资源打包与签名**的工作（§6.1）；L3d 让"未登录也能全本地使用"，扩大了 D4 token 暴露的残余面（§9 D4.1）。两者都**不构成终止条件**。

**rev.3 决策（已拍板）**：**采纳迁移，Wails 转正为主线 shell**，按 §0.5 的最快路径执行——首版 Wails 包**不带 RAG**（rev.16 起已不成立：RAG 可用且懒加载，未使用时零成本），把 cgo/native 资源整块移出关键路径，目标 **~4 周出首个签名公证的 EA 包**。Electron 从"唯一出货通道"降级为**回滚热备**，shell 层冻结。

**rev.5 修订（kill ① 已通过）**：**W1 kill check ① PASS —— WKWebView 能逐字节承载 SSE，迁移继续。** 但实测推翻了 D2 的传输方式并**反转了 D4**：renderer 不能跨源直连 loopback（Origin 被拒 + 无 CORS 预检），也不能走 `wails://` 自定义 scheme（WebKit 的 `WKURLSchemeHandler` **丢弃 POST body**）。可行方案是 **renderer 经真实 loopback HTTP 加载、API 在同源下由 Go 反向代理**——同源、无 CORS、body 完整，且 **token 不再下发 JS**（D4/A2 放弃的机密性回来了）。详见 §0.5.9。

**rev.4 修订（已开工）**：RAG 回到首版范围，但**不进安装包**——原生资源改为**首次启用知识库时下载**到 `<DataDir>/resources`，SHA-256 逐个校验（§0.5.6）。安装包因此保持 ~26MB，瘦身目标全额兑现。Electron **删除**（不只是冻结），执行点仍卡在 W1 三条 kill 检查全绿之后。W1 的 Bootstrap 抽取与 `--serve-only` 已落地并验证，见 §0.5.7。

---

## 0.5 最快路径（Fast Track）——rev.3 主线排期

### 0.5.0 当前状态与本节导航

§0.5.1–0.5.6 是**计划**（rev.3–4 定的最快路径与决策）；§0.5.7 起是**实测记录**，按发生顺序排列。计划里凡与实测冲突的，以实测为准，且冲突处都在计划正文里就地标注了。

| 阶段 | 状态 | 依据 |
|---|---|---|
| W1 Bootstrap + `--serve-only` | ✅ | §0.5.7 |
| W1 kill ① SSE 传输 | ✅ PASS | §0.5.9 |
| W1 kill ② v3 mac 稳定度 | ✅ | §0.5.10 |
| W1 kill ③ 导航收束 | ✅ PASS（带一条明确降级） | §0.5.10 |
| W2 安全 | ✅ | §0.5.12/13/18，外链见 §0.5.23 |
| W3 renderer shim | ✅ 验收通过 | §0.5.14 |
| Electron 退役 | ✅ 已删除 | §0.5.15/16 |
| W4 entitlements + bundle 检查器 | ✅ | §0.5.19 |
| W4 `.app` 组装 + 性能实测 | ✅ | §0.5.20/21 |
| W4 签名公证实跑 | ⏸ 需 Developer ID 证书 | — |
| W4 x64 / universal | ⏸ 需 Intel 工具链 | — |
| RAG 资源托管（模型/分词器 URL） | ⏸ **待决策** | §0.5.8 |

**检查分层**（哪一层保护什么）：契约与边界 `check-desktop-boundaries.mjs`（14 条保证 + 路由表 + 网关动词）· renderer 逻辑 `check-bundled-renderer-behavior.mjs` · shell 单元 `desktop/wails` Go 测试 · 打包 `inspect-mac-package.test.sh` + `notarize-mac.test.sh` —— 以上均在 CI；另有三条**需要真 webview、只能手工跑**：`--kill-check`（传输）、`--verify-shim`（桥接契约）、`--verify-app`（整条用户路径）。

### 0.5.1 核心杠杆：首版 Wails 二进制不 import `knowledge`，我们这边就没有 cgo 债

代码事实（已核）：`server/desktop/knowledge` **只被 `cmd/workagent-desktop/main.go` 一处 import**；它的两个消费点都是**结构化接口**——`desktop.FileIndexer`（`server.go` 定义，注释明确写了"so the desktop package does not depend on the cgo knowledge package"）和 `localinference.KnowledgeHooks`（`engine.go` 定义，注释写了 `nil=关闭`）。

所以新写的 `desktop/wails/main.go` 只要**不 import** `knowledge`，`ServerConfig.FileIndexer` 与 `NewEngine(..., hooks=nil)` 两处传 nil，整个 Wails 二进制在我们的代码层面**零 cgo**，§6.1 那一整节（native 资源 staging、逐个 codesign、library validation、模型许可）**全部移出首版关键路径**。RAG 关闭是既有的一等公民状态，不是 hack。

> **但仍要清楚 cgo 门槛并没有消失，只是换了来源**：Wails 在 **macOS/Linux 上自身就用 cgo** 绑定 Cocoa/WebKit（Windows 侧 v2/v3 走纯 Go WebView2）。所以：
> - **`CGO_ENABLED=1` 在 mac 上无论如何都要开** → §6.1 表格首行"构建带 CGO"在 Wails 路线下是**零增量成本**；
> - **"纯 Go 单文件随便交叉编译"这个属性，迁 Wails 之后本来就保不住** → §18-7（是否要为保纯 Go 而换掉 ONNX）的紧迫性**大幅下降**，可以从待决策降级为长期观察项；
> - 唯一仍然成立的增量：**Windows 目标下** L3c 会引入一个 Wails 本身不需要的 cgo 依赖。等真要做 Windows 时再算这笔账。

### 0.5.2 首版范围（In / Out）

| | 内容 |
|---|---|
| **In（首版必须）** | 单 Wails v3 二进制（含 `--serve-only`）· `Bootstrap` 抽取 · 25 条 HTTP 路由（login×4 由反代拒绝）· 同源 SSE（D2 修订）· `/login/` 网关（D3 落地形态）· renderer shim（SSE parser + typed bridge）· 导航收束（能力路径 + CSP + 外链交给系统浏览器）· 单实例 · macOS **arm64** 签名公证 · 契约对等 + smoke + 性能基准 |
| **Out（明确推迟，不是忘了）** | **native 资源随包**（§6.1 整节，已被下载方案取代）· **x64 / universal**（CGO 交叉编译修复，§13 原 P-1）· Windows / Linux · 自动更新 · 多窗口 / modal OAuth · D2-alt（token 代理）· Option B（路由改绑定）· 画布重构（§1.5.2，与 shell 正交） |

### 0.5.3 四周排期

| 周 | 内容 | 出口标准（做不到就停下来谈） |
|---|---|---|
| **W1** | `Bootstrap` 抽取（双文件 cgo 拆分，见 §10）+ 最小 Wails v3 binary + `--serve-only` + **三条 kill 检查** | ① WKWebView 下 `/agent/chat` SSE 完整一轮 `text_delta→done`；② v3 mac 无 beta-blocker，原生 server build 可用；③ embed.FS bundle-only 导航收束成立 + 外链走系统浏览器。**任一不过 → 停，回 Electron** |
| **W2** | 安全：`/login/` 网关 + 能力路径 + 反代拒绝凭据路由 + CSP + 单实例 | 契约 checker 全绿；`uiserver_test.go` 覆盖 |
| **W3** | renderer shim（SSE parser + typed bridge + fetch 包装 + helper 重指向）· 与 W2 可并行 | 与 Electron 版行为对等（§14 全套 smoke） |
| **W4** | 打包：entitlements 重写 + preflight 重写 + Developer ID 签名 + notarytool + stapler + spctl · 性能基准落数字 | 出 1 个可分发 arm64 EA 包；§5 估算区间被实测替换 |

**为什么 W4 的打包比 rev.2 估的 1 周更轻**：首版是"单个可执行文件 + embed.FS 资产"——没有 asar、没有 Helper apps、没有 Frameworks、**没有 native dylib 要逐个签**。这是 macOS 公证最简单的形态。entitlements 也变成**纯减法**（去 allow-jit / unsigned-exec-mem / dyld-env，只留 `network.client`），§6.1 里"可能要加 `disable-library-validation`"的分支随 RAG 一起推迟。

### 0.5.4 "以 Wails 为主"的组织含义

采纳之后立刻生效的三条工作纪律，否则会持续制造 parity 债：

1. **Electron shell 层冻结**：`desktop/electron/src/*`（`main.ts` / `preload.ts` / `security-helpers.ts` …）只接受**安全修复**，不接受新功能。新 shell 能力一律在 Wails 侧做。
2. **`renderer/` 仍是共享真源**：`renderer.js` 由两套 shell 共同消费（Electron 走 preload，Wails 走 shim）。改 renderer 时**两边都要过契约 checker**，直到 Electron 退役。
3. **RAG 继续在 `--serve-only` 模式下开发**：L3c 后续（indexer wiring、检索注入）不受迁移阻塞——它在 `cmd/workagent-desktop` 和 `wails --serve-only` 上都能跑，只是首版 GUI 包里不启用。RAG 打包在 Wails 出 EA 包之后作为独立里程碑收口。

### 0.5.5 与 rev.2 排期的差异

| | rev.2（含 RAG 完整路径） | **rev.3 最快路径** |
|---|---|---|
| P-1 CGO 交叉编译修复 | 2–3 天，前置 | **移出**（首版 arm64-only） |
| P0 kill 检查 | 4 条（含 cgo/ONNX 退出路径） | **3 条**（RAG-off ⇒ 无 ONNX 析构问题） |
| P2 native 资源 | 0.5–1 周 | **移出**（随 RAG 里程碑） |
| 合计到可分发包 | 4–7.5 周 | **~4 周** |

> 代价是诚实的：首版 EA 包**只有 arm64**。Intel Mac 用户在 cutover 前继续用 Electron 通道。（rev.4：RAG 已回到首版范围，见下。）

### 0.5.6 RAG 资源：首次运行下载（rev.4 决策）

原生资源（`libonnxruntime.dylib` ~33MB + MiniLM `model.onnx` ~90MB + `tokenizer.json`）**不进安装包**，改为用户首次启用知识库时下载到 `<DataDir>/resources`。安装包保持 ~26MB，§0.5.3 的四周排期不变，而 RAG 回到首版范围。

| 事项 | 决定 |
|---|---|
| 落盘位置 | `<DataDir>/resources/`（`~/.workmax/resources`）。**不是**相对工作目录的 `"resources"`——打包后 cwd 通常是 `/`，旧默认必然解析不到 |
| 完整性 | 每个资源在 `manifest.json` 里按 **SHA-256 钉死**。哈希不符即丢弃并报错，不重试——重试只会重新下载同样的错字节。URL 不是信任边界，哈希才是 |
| 中断恢复 | 下载写 `.part`，校验通过才 rename。下次运行用 `Range` 续传，**绝不会留下一个看起来完整但损坏的资源** |
| 签名影响 | dylib 落在 bundle 之外 ⇒ hardened runtime 需要 **`com.apple.security.cs.disable-library-validation`**。这推翻了 §0.5.3 "entitlements 纯减法"的说法：仍去掉 JIT/unsigned-exec-mem/dyld-env，但要**加**这一条 |
| 离线 | 启用知识库需要一次联网。与"本地优先"有张力，但换来的是绝大多数不用知识库的用户不必为 124MB 买单 |
| **未决** | **模型与分词器目前没有可钉的公开 URL**——L3c 验证用的 `model.onnx` 是本地 SentenceTransformer 导出（pytorch 2.0.1，`sentence_embedding` 输出），HF 上同名文件大小/哈希都不同。ONNX Runtime 可直接钉微软官方 release。见 §0.5.8 |

### 0.5.7 W1 已落地（2026-08-08）

| 项 | 状态 |
|---|---|
| `desktop.Bootstrap` / `Boot` / `Shutdown` 抽取 | ✅ `server/desktop/bootstrap.go`；`cmd/workagent-desktop/main.go` 已改薄入口复用它 |
| cgo 依赖方向（双文件拆分） | ✅ `bootstrap_knowledge.go`（seam，无 cgo）+ `bootstrap_cgo.go`（`desktop && cgo`）。**`CGO_ENABLED=0 go build -tags desktop ./...` 通过**，desktop 包测试在无 cgo 下可跑 |
| PID 锁下沉 + 测试迁移 | ✅ `desktop/sidecar_lock.go` + `sidecar_lock_test.go`（从 `cmd/` 迁出，好在 M+3 删除 `cmd/` 时不丢覆盖） |
| Wails v3 二进制 | ✅ 独立模块 `desktop/wails/`（`replace server => ../../server`），v3.0.0-beta.5，**云端 server 的依赖图完全不受影响** |
| `--serve-only` | ✅ 端到端验证：`/health` 带 token 200 / 无 token 403、diagnostics 正常、SIGTERM/SIGINT 退出码 0 |
| 资源下载器 | ✅ `desktop/knowledge/assets`（无 cgo）：manifest + SHA-256 校验 + `.part` 续传 + 完整单测 |
| RAG 端到端 | ✅ 用 L3c spike 资源实测 `knowledge: local RAG enabled (384-dim embeddings)` |
| **ONNX 析构 abort** | ✅ **真实复现并修复**：`Cancel` 与 `Shutdown` 曾共用同一个 `sync.Once`，信号先调 `Cancel` 消耗掉 Once ⇒ teardown 整个不跑 ⇒ 退出 `mutex lock failed` + SIGABRT（退出码 2）。已拆成两个 Once，RAG-on 下 SIGTERM/SIGINT 均退出 0，并加了回归测试锁住语义 |

> 这条 abort 值得记住：**它只在 RAG 启用时复现**。任何没有原生模型的测试运行都看不到它——所以回归测试直接断言 `closeEmbedder` 被调用次数，而不是依赖退出码。

### 0.5.8 W1 剩余（一条 kill 检查 + 一个待决）

- ~~**kill ①** WKWebView 下 SSE 字节级跑通~~ —— ✅ **PASS**，见 §0.5.9。
- ~~**kill ②** v3 mac 稳定度~~ —— ✅ 窗口模式连续三次开窗、执行 JS、正常退出，无 beta-blocker。
- ~~**kill ③** 导航收束~~ —— ✅ **PASS（带一条明确降级）**，见 §0.5.10。

**W1 三条 kill 检查全部通过。** 按 §18-8 的决策，Electron 删除的前置条件已满足。

### 0.5.9 kill check ① 实测结果（2026-08-08）

**结论：PASS。SSE 通过 WKWebView 的 `fetch` + `ReadableStream` 逐字节还原，迁移继续。**

harness（`desktop/wails/killcheck.go`，`--kill-check`）用一个 stub OpenAI 兼容端点驱动真实 `/agent/chat` 本地路由turn，把同一条流回放两次——一次从 Go（控制组），一次从 webview 内——再逐字节比对。载荷刻意选来打穿"看起来对"的实现：中文（多字节）、`🚀`（4 字节，JS 里是代理对）、内嵌换行、字面量 `data:` 前缀、4096 字符长串。

| 探针 | 结果 |
|---|---|
| `go`（控制组） | **PASS** — 8 帧 / 4151 字节 / terminal=done |
| `direct`（D2 原设计：webview 跨源直连 loopback） | **FAIL** — `TypeError: Load failed`，status=0 |
| `proxy`（同源 + Go 反代） | **PASS** — 8 帧 / 4151 字节 / terminal=done，与控制组完全一致 |

连续三次运行结果一致。控制组先跑是刻意的：Go 读不出流就说明 harness 自己坏了，此时任何 webview 失败都不构成对 WKWebView 的判决（harness 用退出码 2 表达这一点）。

**两个把 D2 打掉的实测障碍**：

1. **Origin 被无条件拒绝**。`route_policy.go` 对**任何**携带 `Origin` 头的请求 403。webview 的源实测为 `wails://localhost`（自定义 scheme）或 `http://127.0.0.1:<port>`（HTTP 加载），跨源 fetch 必然带 Origin；而带 `X-Local-Token` 的 JSON POST 还会先触发预检，sidecar 没有 OPTIONS 路由 ⇒ 预检失败 ⇒ fetch 抛 `TypeError`，JS 连 403 都看不到。**Electron 从不踩这个坑，是因为 Chromium 对 `file://` preload 的 fetch 省略 Origin**——这是个从未被写下来的隐式依赖。
2. **`wails://` 自定义 scheme 丢 POST body**。实测：请求到达 Go 侧 asset handler 时 `content-length=0`，而 `Content-Type` 还在。这是 WebKit `WKURLSchemeHandler` 的已知行为。**后果是 `embed.FS` + 自定义 scheme 这条路（原 §9/§11/§13-W4 的方案）对本仓不可用**——renderer 一旦要 POST 就废。

**采纳的形态（D2 修订）**：

```
WKWebView ──加载──> http://127.0.0.1:<uiPort>/          （Go 服务 embed.FS 的 renderer）
          ──fetch─> http://127.0.0.1:<uiPort>/api/...   （同源；Go 反代到 sidecar，注入 token）
                                    │
                                    └─> 127.0.0.1:<sidecarPort>   （周界一字不改）
```

- **同源** ⇒ 无 CORS、无预检、body 完整、`ReadableStream` 正常；
- 反代用 `httputil.ReverseProxy` + `FlushInterval: -1` 保证 SSE 不被缓冲，并在 Go 侧 `Del("Origin")`——**sidecar 的"拒绝一切 Origin"周界一个字都不用改**；
- **renderer 的 fetch/SSE 代码与 Electron 版保持字节级同形**，只是 base URL 从 `http://127.0.0.1:<sidecarPort>` 变成同源相对路径。

**D4 反转（好消息）**：token 由 Go 侧反代注入，**不再下发给 JS**。§9 D4/D4.1 接受的"renderer 能读 token"这一削弱**不再需要**——回到 Electron 今天"renderer 物理读不到 token"的强度。§9 的 D2/D4 两行、§11 的 token 隔离行需按此改写。

**kill ③ 导航收束的新形态**：不再是"embed.FS + 自定义 scheme ⇒ 无处可导航"，而是"**Go 的 UI listener 只服 embed.FS 里的资产**，且 sidecar 周界继续拒绝一切 Origin"。等价强度，但要重新论证并补上：UI listener 也需绑 `127.0.0.1:0` + 严格 CSP + 外链走 `browser.OpenURL`。这条仍需 W1 收尾验证。
- **待决：模型与分词器托管在哪**。ONNX Runtime 钉微软官方 release 即可；模型/分词器需要我们自己发一个 GitHub Release 承载（可控、许可可标注），或改 `embed.go` 去适配 HF 公开导出（要在 Go 里自己做 pooling+normalize）。`manifest.json` 现为空 platforms，`Current()` 返回 `ErrUnsupportedPlatform`，RAG 干净降级——**填数据即可启用，无需改代码**。

---
### 0.5.10 kill check ③ 导航收束实测（2026-08-08）

**结论：PASS，但有一条必须写进安全对等清单的降级。**

**降级项：macOS 上没有可取消的导航钩子。** 查证 Wails v3.0.0-beta.5 源码：`decidePolicyForNavigationAction` **只在 iOS 实现**（`webview_window_ios.m`），mac 侧没有；`pkg/events` 里 mac 的导航事件（`WebViewDidStartProvisionalNavigation` 等）**全是通知，不可取消**。也没有 `WKUIDelegate.createWebViewWithConfiguration`。所以 Electron `will-navigate` + `setWindowOpenHandler` 那种**阻止式**收束在 mac 上无法复刻——这正是 §11 标注的"最大残余风险"，答案是：钩子不存在。

**补偿控制（实测全部生效）**：

| 控制 | 实测结果 | 说明 |
|---|---|---|
| CSP `connect-src 'self'` | ✅ `cspBlocksForeignOrigin = true` | 探针打的是一个**活着的**本机异源（stub 端点），排除"主机不可达"的假阳性 |
| 只服 asset FS | ✅ 未知路径 `404` | 无 fall-through |
| 路径穿越 | ✅ `/../../etc/passwd` → `404` | |
| `window.open` | ✅ 返回 `null` | 无 `WKUIDelegate` ⇒ WebKit 默认拒绝开新窗 |
| CSP `script-src 'self'` | ✅ **反向验证**：harness 页第一版用内联 `<script>`，被静默拦下、什么都没执行 | 策略是真的在生效，不是摆设 |

**为什么这个补偿组合是可接受的**：现实注入面是**模型输出渲染进 DOM**，而 `script-src 'self'` 直接阻断其执行——注入脚本跑不起来，也就无从发起导航。导航收束在本仓是 CSP 之后的纵深防御，不是第一道闸。而且 CSP 现在由**我们自己的 Go listener 以响应头下发**，比 Electron 今天只能靠 `file://` 页面里的 meta 标签更强。

已核对现有 renderer（`renderer/en/desktop/index.html`）：单个外部脚本、**零**内联事件处理器、**零**内联 style，且本就带 CSP meta。所以生产 CSP 可以直接收紧，不需要为存量代码开 `'unsafe-inline'`（现 `style-src` 里那条可在 W3 去掉）。

**残余风险（须入安全对等清单）**：顶层导航（`location.href = ...`）在 mac 上无法被阻止，只能通过 `WebViewDidStartProvisionalNavigation` 事后检测并 `SetURL` 回退——存在一个极短的"已开始导航"窗口。前提是攻击者已能执行脚本，而这被 CSP 挡在前面。

### 0.5.11 W2 绑定可用性：**不可用**（rev.7 更正）

> **rev.6 曾在此断言"绑定可以照常用"，那是错的。** 当时只观察到 `window.wails` 对象存在，没有真的发起一次调用。补测后结论相反。

实测（`wailsCallApi = undefined`，`wailsCall = "no Call.ByName on this runtime"`）：页面经普通 loopback HTTP 加载时，Wails 运行时对象**被注入**，但**调用能力不可用**。原因在 runtime.js 里写死：它把调用 POST 到 `window.location.origin + "/wails/runtime"`——在采纳的同源形态下，那个 origin 是**我们自己的 UI server**，它没有这个路由。

**对 D3 的影响与选路**：

| 方案 | 说明 |
|---|---|
| **(a) 代理 `/wails/*`** | `Assets.Middleware` 会把 Wails 默认 asset handler 作为 `next` 传进来，而它正是服务 `/wails/runtime` 的那个。把它挂到 UI server 上即可。**未验证**，且依赖 beta 框架内部形状 |
| **(b) `Options.Transport`** | v3 支持自定义 IPC 传输，是为此设计的扩展点。改造面更大 |
| **(c) UI server 上的能力受保护端点**（倾向） | 登录端点直接挂在 UI server 上，在 Go 内**进程内**调用 `boot.LoginCoordinator`。凭据**不进 sidecar 的路由表**（D3 的实际目标达成），且受 §0.5.13 的能力路径保护——只有页面能到达。不依赖任何 beta 内部形状 |

**倾向 (c)**：能力路径落地后，它的安全性质与绑定等价甚至更好（凭据只经过我们自己的进程内调用），而且少一个对 beta 框架内部的依赖。W2 开工时定。

### 0.5.12 D3 在同源形态下的强化（W3 开工时落地）

同源反代有一个必须立刻堵上的副作用：**它让所有 sidecar 路由对 renderer 可达**，包括 4 条 login-transaction 路由——正是 preload 当年用 `isPrivilegedLoginTransactionURL` 专门挡掉的那些。

已落地：**反代在服务端拒绝这些路径（403）**，而不是靠 renderer 侧自律。这比 Electron 的做法更强——preload 的守卫和它约束的代码住在同一个进程里，而反代是 renderer 够不到的地方。登录仍按 D3 走 Wails `LoginAPI` 绑定（§0.5.11 已确认可用），凭据路径从不成为 renderer 能命名的 HTTP 面。

连带对齐了一条姿态：**UI listener 拒绝任何非规范化路径**（不做 301 清理重定向），理由与 sidecar 关掉 gin 的 `RedirectTrailingSlash`/`RedirectFixedPath` 完全一致——"重定向之后才做的特权检查"，其结果取决于客户端是否跟随重定向，那就不算检查。`uiserver_test.go` 覆盖：特权路由 403、非规范拼写不被重定向放行、Origin 被剥离、token 由 Go 注入、CSP 头齐全、asset FS 外一律 404。

### 0.5.13 UI origin 必须有自己的凭据（rev.7，修一个我引入的回归）

同源反代有一个我一开始漏掉的后果：**反代替调用方注入 token**，所以只要 UI 端口没有自己的门禁，**任何本地进程扫到这个端口就能全权驱动 sidecar**——token 周界被整个绕过。这与附录 C 的威胁模型直接冲突（"loopback 端口本身可被本地其他进程连，**token 才是真保护**"）。

我自己写的第一版代理测试就演示了这一点：一个不带任何凭据的请求拿到 200。已加回归测试 `TestUIOriginRequiresItsOwnCapability` 钉死。

**修法：能力路径（capability URL）**。每次启动新生 32 字节随机段，整个 UI 面（资产 + `/api/*`）挂在它下面，其余一律 404 且不给任何"换个路径就行"的暗示。选路径段而非请求头，是因为**第一个请求是顶层导航，带不了头**；renderer 全用相对 URL，所以"知道能力"等同于"知道自己的 URL"，无需任何特殊处理。

### 0.5.14 W3 renderer shim 已落地并通过验收（2026-08-08）

**做法：复用而非重写。** `desktop-bridge.ts` 是 typed facade 契约的唯一真源，且是纯浏览器兼容代码（无 Node/Electron 导入）。renderer 消费的是它的**生成产物** `renderer/en/desktop/lib/desktop-bridge.js`（`desktop/scripts/build-renderer-lib.sh`），不是手抄移植——两套 shell 并存期间契约不可能漂移。生成脚本断言源里恰好 2 个顶层 `export`，多一个就红，不会静默出半成品。

产物是**经典脚本**而非 ES module：renderer 的脚本按序同步加载，module 会被推迟到 `renderer.js` 之后，且 module 在 `file://` 下根本加载不了（Electron 仍在用）。

`shim.js` 提供 `createDesktopBridge` 的依赖：同源 `request` 适配、从 preload 移植的 SSE 消费循环与解析器、经 `/login/` 网关的四个登录动词。**在 Electron 下它检测到 preload 已装好 `window.workmaxLocal` 就直接退场**，所以两套 shell 共用同一个 `index.html` 期间都能跑。

**登录网关（D3 的落地形态）**：renderer 只能说出四个动词之一（begin/status/password/cancel），Go 决定它变成哪条 sidecar 路由、用什么方法。这是 Electron `ipcMain` 网关的直接对应物——凭据仍走 loopback HTTP 到 sidecar（**Electron 今天也是如此**，`login-transaction.ts` 从 Main 发同样两条路径），但 renderer 无法命名那些路径（反代拒绝），服务端的凭据卫生（fresh copy、拒重复键、4KiB 上限）全部照旧生效。更强的 D3（注销路由 + 进程内调 coordinator）仍是严格改进，只是不捆进 shell 迁移。

**验收（`workmax-desktop --verify-shim`，连续三次通过）**：加载**真实 renderer 目录**（即出货的那两个文件）经生产 UI 路由进 WKWebView，断言：

| 检查 | 结果 |
|---|---|
| `window.workmaxLocal.fetch` + `window.desktopBridge` 存在，version `1.0.0-alpha.7` | ✅ |
| renderer.js 守卫函数要求的 **13 个方法**全部为 function | ✅ |
| **token 未进入页面**（用真实长度随机 token 搜整个桥接对象） | ✅ D4 反转经验证 |
| `workmaxLocal.fetch("/auth/status")` | ✅ HTTP 200 |
| `desktopBridge.agent.startTurn` 完整流式 turn | ✅ 8 帧 / 4151 字节 / terminal=done，与 Go 控制组逐字节一致 |

> 验收过程本身抓到一次契约执行：给 `startTurn` 传线上形状（`thread_uuid`/`payload`）被 `assertAllowedKeys` 拒绝——typed bridge 要求调用方只说意图（`threadUUID`/`userText`/`chatMode`），线上形状由它自己构造。契约在干活。

**既有问题（非本次引入）**：`check-bundled-renderer.sh` 的行为检查子脚本在 main 上就是红的（`renderer.js:3152` 拿不到 `attachButton`，DOM stub 与 renderer 已漂移）。文件白名单那一关已补上 `shim.js` 与 `lib/desktop-bridge.js`。

### 0.5.15 Electron 删除的真实前置：契约 checker 迁移（2026-08-08）

开始执行退役时发现，"删掉 `desktop/electron/`" 不是删一个目录：**`check-desktop-boundaries.mjs` 断言的是 Electron 源码里的字符串**——`ipcMain.handle(...)`、`contextBridge.exposeInMainWorld(...)`、`event.sender !== mainWindow.webContents`、preload 的 `isPrivilegedLoginTransactionURL` 等等。直接删除会把 §14 指定为 parity 真源的那道闸门一起删掉，而那正是迁移期最不该失去的东西。

顺带排除了"先把 `desktop-bridge.ts` 挪进 `renderer/src/`"这条看似安全的中间步：`preload.ts` import 它，一挪 Electron 构建立刻红。**删除是原子操作，前置是 checker 迁移。**

**采用的做法：增量、始终保持绿。** 先给 checker 加上 Wails 侧断言，与 Electron 侧**并存**；退役时只需删掉 Electron 那一半，而不是重写闸门。

契约里新增 `wailsShell` 段，把每条保证写成"**为什么重要 + 哪个文件必须仍然携带它**"，checker 按数据驱动校验（加一条保证是改数据，不是改代码）。当前 **14 条保证 + 4 个登录动词**（rev.18 加入外链两条；写下本节时是 11 条）：

| 保证 | 它挡住什么 |
|---|---|
| `capability-path` | 反代替调用方注入 token，没有每次启动新生的能力段，任何本地进程扫到 UI 端口即可全权驱动 sidecar |
| `privileged-routes-refused-server-side` | renderer 不能命名凭据路由——在 renderer 够不到的地方强制，不像 preload 的同进程守卫 |
| `origin-stripped-before-sidecar` | 保持 sidecar"拒绝一切 Origin"的规则原样严格，而不是为了适配去放松它 |
| `no-redirect-based-checks` | 只有在客户端跟随 301 时才成立的特权检查，不算检查 |
| `sse-streams-unbuffered` | 会缓冲的代理会把分钟级流式变成一个迟到的整包响应 |
| `containment-headers` | mac 上没有可取消的导航钩子，CSP `script-src 'self'` 是阻止模型输出执行的第一道闸 |
| `token-never-in-renderer` | D4 反转的验证；按**带引号的字符串字面量**匹配，让规则约束代码而非注释措辞 |
| `renderer-globals-installed` / `request-hygiene-preserved` / `login-through-gate-only` / `turn-cancel-is-local-and-remote` | 从 preload 逐条继承的行为 |

**闸门经负向验证**（不能只看它变绿）：把 `FlushInterval: -1` 改成 `0`、给登录网关偷加一个动词、在 shim 里直接用 token 头 —— 三次都被精确报出，且错误信息带上"为什么重要"。

### 0.5.16 Electron 已退役（2026-08-08）

**已删除**：`desktop/electron/`（22 个源文件 + 3.6G 未跟踪产物）、`server/cmd/workagent-desktop/`、`build-mac.sh`、`inspect-mac-package.sh`、`smoke-packaged-app.sh` 及其三个测试。

**已迁移**：`desktop-bridge.ts` → `desktop/renderer/src/`，它 import 的两个类型抽成 `login-types.ts`（顺带去掉 `NodeJS.Platform`——renderer 是网页，那个值只是信息性的）。renderer 现在有自己的极小 npm 项目，且**只在构建期需要**：生成产物已入库，改 renderer 不需要任何工具链。

**契约 checker 完成迁移**，每条 Electron 断言都换成了同职能的 Wails 断言，而不是删掉：

| 原断言（Electron） | 现断言（Wails） |
|---|---|
| `ipcMain.handle(...)` 命令集 | `loginGateRoutes` 动词集（双向钉死） |
| `contextBridge.exposeInMainWorld("x")` | `shim.js` 里 `window.x =` |
| `preload` 的 `isPrivilegedLoginTransactionURL` | `uiserver.go` 的 `privilegedSidecarPaths`（**服务端**拒绝，renderer 够不到） |
| `event.sender !== mainWindow.webContents` | `mintCapability`（Electron 能问 OS 调用者是谁，loopback origin 上不能，等价物是"必须已持有能力才能寻址"） |
| `preload` 的 `credentials/redirect` 卫生 | `shim.js` 同名断言（请求在哪儿构造，卫生就在哪儿） |
| Main 的 flow ID / 凭据清理 | 服务端保证 + "flow ID 不得出现在页面" |
| 边界扫描仅 `.js/.ts` | **加扫 `.go`**——shell 搬进 Go 后不加会静默变成空转（负向验证过） |

契约里 `ipc` 段改名为 `privilegedGate`（`ipcMethods`→`gateMethods`、`ipcId`→`gateId`、`command`→`verb`）——留着 IPC 的名字描述一个不再是 IPC 的东西，是会误导下一个读它的人的那种技术债。

**保留并去耦**：`notarize-mac.sh` 的 codesign/notarytool/stapler/spctl 管道是跨 shell 复用的（§6 早就这么说），已把 electron-builder 的目录布局从硬编码改成输入。它委托的 bundle 检查器随 Electron 删除了，所以改成**失败关闭**：dry-run 仍可用，真实公证在 W4 写出 Wails 布局的检查器之前一律拒绝——不静默丢掉"renderer 未打包就不许公证"这条闸门。

**验证**：契约 checker、`notarize-mac.test.sh`、server（cgo/非 cgo）、云端构建、`desktop/wails` 测试全绿；`dev.sh --serve-only` 与 `dev.sh --verify-shim` 端到端通过。

**文档与审计链已收口**（rev.12）：`desktop/README.md` 逐节重写（Release build 段改为诚实的"打包待 W4 + 哪些事实仍成立"；Version Pins 收敛到 Go 单一真源；Testing 段换成真实可跑的命令，含两条要真 webview 的手工检查）；顶层 `README.md`/`RELEASING.md`/`THIRD_PARTY_NOTICES.md` 同步；`scripts/license-audit.sh` 从 Electron 依赖树改指 renderer 的构建期 npm 树，并**新增 `desktop/wails` 模块的 go-licenses 审计**——它才是真正装到用户机器上的那个模块，之前没有任何一遍审计覆盖它。全仓残留的 "Electron" 只出现在解释某个决定为何如此的对照说明里。
### 0.5.17 修复行为闸门，并因此抓到一个真实缺陷（2026-08-08）

`check-bundled-renderer-behavior.mjs` 在 main 上长期红着，所以它**什么都没在检查**。修好之后立刻付了回报。

**闸门为什么倒的**：它用一份**硬编码的元素 ID 清单**造假 DOM，而 L1（模型设置）与 L3b（附件）陆续给 `index.html` 加了 20 个元素，清单没跟上 ⇒ `getElementById` 返回空 ⇒ renderer 一进来就抛。修法不是补清单，而是**从 `index.html` 派生**元素集合（连 tag 名与初始 `hidden` 也从真实标记读，而不是从 id 后缀猜）——这样 stub 结构上不可能再落后于出货的标记。

闸门倒下期间**三处独立漂移**无人察觉：① 元素清单；② 22 个 agent mock 缺 `uploadThreadFile`（L3b 收紧了 `desktopAgentBridge()` 守卫）；③ 下面这个真缺陷。

**抓到的缺陷（用户可见，L3b 引入）**：`submitChat` 里 `state.pendingFiles = []` 执行在 `startTurn` 读取它**之前**，所以 `fileIDs` **永远是空数组**——附件 chip 正常显示、上传正常成功、模型从来收不到文件。已修（先取 id 再清托盘），并加了回归测试 `testStagedAttachmentsAreSentWithTheTurn`；把 bug 改回去验证过它确实会红。

> 这条值得记住的地方在于失败模式：功能**看起来**是work 的。没有异常、没有报错，只是模型收到的上下文里少了东西。这类缺陷只有断言"发出去的东西"的测试才抓得到，而那个测试当时正好被一个无关的 DOM 漂移堵在门外。

### 0.5.18 W2 安全收尾（2026-08-08）

| 项 | 落地 |
|---|---|
| **CSP 去掉 `'unsafe-inline'`** | 出货 renderer 实测零内联脚本、零 style 属性、零运行时 `.style` 改动，所以那条通常"为存量代码"开的口子根本不需要——**开一次就再也收不回来**，所以现在不开。harness 页面自己的 `<style>` 也挪成了外部 CSS，否则它测的就不是生产 CSP |
| **DevTools 默认关闭** | 原本无条件 `DevToolsEnabled: true`。窗口持有已认证会话，且 API 由反代代为鉴权——出货构建带调试面等于开了一扇别处都不给的门。改为仅 `WORKMAX_DESKTOP_DEVTOOLS=1` 开启，**应用内部无法打开** |
| **单实例（两层）** | Wails `SingleInstanceOptions` + `<DataDir>/sidecar.pid` 锁。实测同一 data dir 起第二个 `--serve-only`，被明确拒绝（`another sidecar instance is already running (pid ...)`），第一个仍干净退出 |
| **登录网关** | 已随 §0.5.16 落地并由契约钉死四个动词 |

两条新属性都进了契约（现 **12 条保证**）：`containment-headers` 增加 `'unsafe-inline'` 的**缺席**断言，新增 `devtools-off-by-default`。`uiserver_test.go` 也独立断言 CSP 不含 `unsafe-inline`/`unsafe-eval`——闸门与单测各一遍，因为这是那种"某天为了赶工加一下"的口子。

> 写这条断言时又踩了一次同样的坑：`'unsafe-inline'` 出现在我自己的注释里，把闸门弄红了。上次（token）靠"匹配带引号的字符串字面量"区分代码与散文，这次代码里它本来就带引号，区分不了——所以改了注释措辞。**规则要约束代码，就不能让它顺带约束了描述该规则的句子。**

### 0.5.19 W4 起步：entitlements 重写 + bundle 检查器（2026-08-08）

**entitlements 从 4 条减到 1 条**。去掉 `allow-jit` 与 `allow-unsigned-executable-memory`（Electron 的 V8 才需要；Wails 用系统 WebView，JS 引擎跑在自带授权的系统进程里，我们的二进制既不生成也不执行代码）与 `allow-dyld-environment-variables`（Wails 构建不需要 `DYLD_*`，资源路径由可执行文件位置显式推导）。只留 `network.client`。

**`disable-library-validation` 刻意暂不添加**——它要到 RAG 里程碑才有正当理由（ONNX 库下载到数据目录、由别的团队签名，库验证下加载不了）。提前加等于为一个还没启用的功能削弱每一个构建。plist 里把**每一条缺席的理由也写下来了**，因为公证拒绝的理由通常是"授权与行为不符"，而"当初为什么没加"是最容易丢失的信息。

**`inspect-mac-package.sh` 按 Wails 布局重写**，补上了我上一轮故意留的失败关闭。Wails 布局比 Electron 小得多（一个可执行文件 + 一个 renderer 目录），所以检查是穷举式的：Resources 顶层与 renderer 内部都是白名单，任何未列出的条目即失败。专门检查的项：bundle id 必须是 `ai.workmax.desktop`（它决定能否看见用户已有的 Keychain 会话）、可执行文件必须携带 Info.plist 声明的版本标记（否则是"用新版本号发旧代码"）、**不得存在打包的 sidecar 二进制**（那意味着打包步骤还在按退役的布局走）、CSP 的每个 `connect-src` 源逐个校验、以及 entitlements 集合与 plist 完全一致。

16 项负向测试逐条钉住（`inspect-mac-package.test.sh`），已接回 CI。

> 写这个检查器时自己踩了两次"重叠即死"：① 一条 `connect-src` 规则用了 `grep -E` 不支持的负向先行断言，还把 stderr 丢了 —— **它从写下那天起就什么都没匹配过**，测试是唯一发现方式；② source map 检查与文件白名单重叠、签名检查与 notarize 重叠，两处都删掉了弱的那个。**两条重叠的检查里，总有一条永远不会是构建失败的原因，于是也没人维护它。**

### 0.5.20 首个打包产物与性能实测（2026-08-08）

`build-mac.sh` 按 Wails 布局重写（手工组装，无打包框架——这个布局没有需要框架去拦的东西），**每次构建强制过 `inspect-mac-package.sh`**。产出的 arm64 `.app` 端到端可用：`--serve-only` 从 bundle 内起、`/health` 200、SIGTERM 退出码 0，`--verify-shim` 加载 **bundle 内**的 renderer 通过（8 帧/4151 字节）。

**建出真实产物立刻抓到一个不一致**：`resolveResourcesDir()` 在打包构建里总是返回 bundle 的 `Contents/Resources`，而 rev.4 决定 RAG 资源是**下载到 `<DataDir>/resources`** 的——下载到一处、查找在另一处，装完也用不上。已改为"bundle 里确实有资源才用 bundle，否则让 Bootstrap 走 DataDir 默认"，两种分发方式都成立。这类错位只有真的打出包才会显形。

**实测数字（§5 估算 vs 现实）**：

| 指标 | §5 估算 | 实测 | |
|---|---|---|---|
| 安装包 | 15–25 MB | **22 MB** | ✅ 命中 |
| 冷启动 | "单进程直起" | **~48 ms** 到 sidecar 就绪 / **~100 ms** 到 UI 就绪 | ✅ 远好于握手+spawn |
| 稳态内存 | **40–80 MB** | **~109–126 MB**（app + WebKit 辅助进程，2–3 个进程） | ❌ **估算偏低约 50%** |

内存这条要说清楚：**§5 的 40–80 MB 是错的**。真实值约 110–126 MB。它仍然显著低于 Electron，但不是当初写下的那个量级——`base memory` 里 Go runtime、WebKit 框架映射页、SQLite、gin 都要算。任何引用 §5 内存数字做决策的地方都应改用这里的实测值。

**一个必须承认的流程错误**：§14 把"性能基准（**同机对照**）"列为切换门槛，而 Electron 在基线被采集之前就删掉了。源码仍可从 git 重建（已验证：`git worktree` + `npm ci` 能装出旧 shell），但一次粗测（dev 模式约 650 MB）与 Wails 的打包产物口径不同，反复尝试做干净配对测量都卡在进程归属上。**结论：Wails 的绝对数字可信，"对 Electron 快多少省多少"这个比值目前没有可引用的实测。** 正确顺序应当是"先测旧的，再删"。

> 这条留在文档里不是自责，是给下一个删除动作的清单加一行：**凡是被列为门槛的对照测量，必须在被对照的一方消失之前采集。**

### 0.5.21 RAG on/off 基准，以及它抓出的两个缺陷（2026-08-08）

§14 要求性能基准**分 RAG on/off 两组**（"否则 ONNX 的体积/内存会污染 shell 瘦身的归因"）。补测之后，这条要求兑现了它的价值——两组数字直接推翻了 §5 的 RAG 说法，并牵出两个缺陷。

**测量（`--serve-only`，仅 sidecar）**：

| | 就绪 | 稳态 RSS |
|---|---|---|
| RAG off | ~48 ms | **32 MB** |
| RAG on（改前） | 136–251 ms | **255 MB** |

§5 写的 RAG 增量是"**+数十 MB**"，实测 **+223 MB**——差一个数量级。而且这是**刚启动、一次检索都没做**的地板值：ONNX Runtime 环境加模型会话本身就这么大。

**缺陷一：急切加载**。embedder 原本在 `Bootstrap` 里直接构造，所以只要用户下载过资源，**哪怕从不使用知识库，每次启动都付这 223 MB**。改为懒加载（`lazyKnowledge`，首次调用时构造）后，资源在场但未使用的成本回到 **32 MB / 48 ms——与 RAG-off 完全相同**。资源是否存在决定检索**可用**，第一次真实调用决定它何时**加载**。

**缺陷二：关闭竞态（真实崩溃，非懒加载引入）**。本地推理引擎在**后台 goroutine** 里索引已完成的 turn（`indexCompletedTurn`），而 `Boot.Shutdown` 同时销毁 ONNX 环境——`session.Run()` 打在已销毁的环境上，**SIGSEGV**。用户侧就是"回答刚出来就关窗 → 退出崩溃"。

这个组合（RAG 在场 + 完成一次 turn + 立即关闭）此前从没被跑过：带资源时只跑过 `--serve-only`（无 turn），跑 kill-check 时又没带资源。**是补这组基准把它逼出来的。**

修法是给 `lazyKnowledge` 记在途调用数，`Close` 先停止接单、再等待、最后才销毁；等待超时则**故意不销毁**——进程马上要退出了，泄漏几毫秒远好于把运行中的原生代码抽掉。复现场景连跑 3 次，退出码全 0、无崩溃迹象。

> 值得记下的模式：这两个缺陷都不是"功能不工作"。一个是**每次启动多付 223 MB 而没人察觉**，一个是**只在特定关闭时序下崩溃**。它们都只能被"照着要求把基准补齐"抓到——而那条要求当时看起来只是个记数字的杂活。

### 0.5.22 `--verify-app`：第一次跑用户实际走的那条路（2026-08-08）

此前所有检查验的都是**零件**：行为套件在 VM 里用 mock 驱动 `renderer.js`，`--verify-shim` 验桥接面和一次 turn，`--kill-check` 验传输。**没有一个跑过用户实际拿到的组合**——未经修改的 `index.html` + 出货的 shim + 真 sidecar + 真 webview。

`--verify-app` 补上这一条。关键约束是**不能为了观测而改动页面**：一旦往 `index.html` 注入探针，被测的就不再是出货的东西（何况 `script-src 'self'` 也会拒绝）。所以断言从**反代侧**做——`renderer.js` 启动时打了什么，从外面看得见。

**只有一条必需请求，而这不是弱检查**：`GET /auth/status` 能到达，意味着 `shim.js` 装好了全局、`renderer.js` 认可了它、fetch 在能力路径下解析正确、反代注入了 token、sidecar 应答了。这条链之后的东西不构成新种类的证明。

第一次跑就失败了——但**是我的期望写错了**：我把 `/agent/threads` 也列成必需，而 `renderer.js` 只在 `/auth/status` 报 `authenticated` 后才加载 threads/skills/recoverable。harness 没有云会话，所以它们缺席是 renderer 的**正确行为**。改成会话依赖的可选项后两次连过。

> 这个失败本身有价值：它说明"照抄启动时序当断言"是不够的，得知道每一步的**前置条件**。写下来的期望如果不区分"必须发生"和"取决于状态"，红灯就会指向错误的地方。


### 0.5.23 复审发现：外链会把 app 导航走（2026-08-08，rev.18）

一次对照代码的完整复审，抓到一个**真实功能缺口**、三处文档与实现不符、一段死代码。

**功能缺口（已修）**：`index.html` 有一个指向 GitHub 的 `<a href>`，而 `renderer.js` 不拦截它，`desktop/wails/` 里也**从没实现过 `OpenURL`**——尽管文档在四处写着"外链拦截 → `browser.OpenURL`"。后果是具体的：点它 → WKWebView 顶层导航走 → 因为 UI origin 只服能力路径下的内容，**app 变成远程页面且回不来**。

这正是 §0.5.10 记录的残余风险落到实处。当时 kill ③ 测的是 `window.open`（返回 null），**同标签页的 `<a>` 导航是另一种形态，没被覆盖**。教训：把"没有可取消的导航钩子"记成风险之后，要把**每一种能触发导航的形态**都列出来逐个验，否则记下来的只是其中一种。

修法两半，缺一不可：renderer 在**捕获阶段**拦截（这样 stopPropagation 也绕不过去）并把 URL 交给 Go；Go **重新校验**后交系统浏览器。校验从已删的 `security-helpers.ts` 移植，规则不变——只允许 http/https、拒绝 URL 内凭据、拒绝一切 local/private/link-local。Go 版用解析后的 IP 判断而非字符串模式（原版最可能漏拼写的就是那部分），15 条拒绝逐条有测试，另有测试证明**没有能力路径就够不到这个端点**。

**文档与实现不符（已对齐）**：① D3 从未实现——`DropLoginTransactionHTTP` 不存在，4 条 login 路由仍注册，实际由反代服务端拒绝；原设计的前提（"D2 暴露 token ⇒ 必须移出路由表"）随 D4 反转而消失，所以不实现是合理的，只是文档没跟上。② 路由数多处写 21，实际 25。③ 保证数写 11，实际已 14。④ 多处仍称"首版 RAG-off"，而 RAG 自 rev.16 起可用（懒加载）。

**死代码（已删）**：`RuntimeAPI` 服务。同源形态下 renderer 用相对 URL，无需被告知坐标；而 Wails 绑定在 loopback origin 上本来也不可用（§0.5.11）。

**一个 31.8 MB 的二进制被误提交**：`desktop/wails/wails`，进入版本库于 `8d889fa`。`go build` 不带 `-o` 会把产物写成与包目录同名的文件，而 `.gitignore` 只挡了 `bin/`、`release/`、`resources/`。已从索引移除并加规则；**blob 仍留在历史里**，除非重写那次提交——是否重写由你定。

**结构（已修）**：§0.5 的小节此前是 `1–8 → 10 → 12 → 11 → 22 → 21 → … → 9` 的倒序穿插——每次往前插新章节的累积后果。已按编号重排，并新增 §0.5.0 作为状态索引。

---

## 1. 为什么这次迁移"异常干净"

| 维度 | 发现 | 对迁移的意义 |
|---|---|---|
| **Go sidecar 可进程内嵌入** | `//go:build desktop` 与云服务干净分离（147 文件带 tag，sidecar 零依赖 cloud 包）；HTTP server 就是标准 `net.Listen("tcp","127.0.0.1:0")`+gin（`desktop.NewServer`） | 没有架构性障碍，Wails binary 直接 import `server/desktop` 跑同样代码 |
| **⚠️ 但已不再是纯 Go（L3c 起）** | `server/desktop/knowledge/*`（ONNX Runtime 嵌入）全部 `//go:build desktop && cgo`，且被 `cmd/workagent-desktop/main.go` **无条件 import**；`dev.sh` / `build-mac.sh` 均已改 `CGO_ENABLED=1`。实测 `CGO_ENABLED=0 go build -tags desktop ./cmd/workagent-desktop` → `build constraints exclude all Go files in server/desktop/knowledge` | **不影响"能否嵌入"，影响"怎么构建与打包"**：Wails 构建链必须带 CGO；native 资源（dylib/模型/分词器）要进 bundle 并签名；交叉编译需 CC 覆盖。详见 §6 与 §13-P2 |
| **无"必须独立进程"硬假设** | PID 锁、stdout 握手、stdin 守护都可跳过/删除；**没有**检查 ppid 或"被 Electron 拉起" | 删 ~150 行生命周期代码即可，其余 verbatim 复用 |
| **Renderer 契约小且 SSE 全封装** | 实际只用 ~13 个方法；SSE 解析全在 preload，`renderer.js` 零流式代码；`system.*`/`history.*`/`revealDataDir` 在 renderer 里是死的 | renderer 几乎不用改，旗舰 agent 流式链路不受影响 |
| **OAuth 窗口是死代码** | `src/oauth-window.ts` 源码已删，`dist/oauth-window.js` 是僵尸文件；登录今天只有密码"login transaction" | 少移植一大块，且不用纠结 Wails 无 BrowserWindow |
| **仅 macOS、无自动更新** | `electron-builder.yml` 只有 `mac:`；无 electron-updater；`publish:null` | 分发侧是**净新增**工作而非转换已有 Win/Linux/更新管线——Wails 反而让 Win/Linux 更容易 |

---

## 1.5 生态定位与能力补强（外部参考）

> 来源：Gemini 分享《Wails 在桌面 Agent 开发中》（2026-08-07，3.6 Flash）。以下为对本仓决策有价值的提炼，**非本仓代码结论**；市占数字为社区感知量级，非实测。

### 1.5.1 生态站位：为什么 Wails 在 Agent 领域站得住

桌面 AI Agent 技术栈当前呈"三足鼎立"：**Electron（Node+Web，绝对主流，代表 Cursor / Cherry Studio / TypingMind）· Tauri（Rust+Web，新锐主流，代表各类轻量原生 Agent / 大模型本地客户端）· Wails（Go+Web，强力挑战者，~10–15% 感知份额）**。Wails 在 Agent 场景受青睐的根因与本仓高度吻合：

- **Go 并发红利**：goroutine/channel 天然适合 Agent 的异步 Task 调度、流式响应、多 Agent 协作、本地向量/DB、工具调用——正是本仓 sidecar 已在做的事。
- **极低体积/内存**：二进制 ~10–20MB、运行时内存小（与 §5 估算一致）。
- **Go 生态**：SQLite/BadgerDB、LangChain-go、ollama 本地接口等可直接引入。

> 对本仓：后端已是 Go，选 Wails 是顺水推舟；选 Tauri 反而要为 Rust 壳再 spawn Go sidecar，徒增一层。

### 1.5.2 重前端 / 画布补强——与 shell 无关，且库可平移（呼应 §0 标定）

旗舰脑图画布的性能是 **renderer 层问题，与 Electron/Wails 无关**：两者都是 webview，同一套 NPM 画布库都能用。**迁不迁 Wails，画布方案不变；若迁，画布库直接平移。** 成熟选项（按场景）：

| 场景 | 推荐库 | 说明 |
|---|---|---|
| 节点式工作流编排（类 Dify/n8n） | React Flow / Vue Flow | 节点-边画布，最成熟 |
| 自由无限画布 / 白板 / 多模态标注 | Tldraw / Excalidraw | 无限自由度 |
| 高性能 2D（高频动画 / 多层 overlay / 上千节点） | PixiJS / Konva.js | WebGL/Canvas，不卡 |
| 代码块 / 语法高亮 | Monaco / CodeMirror 6 | |
| 富文本 / Copilot 文档 | ProseMirror / Tiptap | |
| **长对话历史防 DOM 膨胀** | Virtuoso（虚拟列表） | **本仓 `renderer.js` 是 vanilla JS 处理 thread/message，长历史会 DOM 膨胀——无论迁不迁都值得做** |

> 当前 renderer 是 vanilla JS 无框架；旗舰画布要做大通常意味着给 renderer 上轻框架（Preact/Svelte/React）。这是**独立的 renderer 架构决策，与 Wails 迁移正交**——可先在 Electron 上做，再随迁移平移。

### 1.5.3 Go 系统能力补强（前瞻，非当前路线图）

Wails 在"复杂 OS 自动化"上的传统短板，可由 Go 后端补齐（全交 Go，经绑定暴露给前端）。**本仓当前是 chat/agent 客户端用不到；但若未来加 computer-use / 屏幕理解 / 全局唤起，这是 Wails 的加分项**：键鼠/窗口/热键（`robotgo`、`golang.design/x/hotkey`）、屏幕截图（`kbinani/screenshot`）、OCR（CGO PaddleOCR/Tesseract 或系统原生 Win `Windows.Media.Ocr` / Mac `Vision.framework`）、透明置顶 overlay 与系统托盘（Wails 原生）。

### 1.5.4 DX 与流式（呼应 §11 / D2-alt）

- **Go→TS 类型自动生成**：`wails dev` 把 Go struct/方法生成前端 TS 定义与调用函数——对 Agent State / Tool Call 参数等复杂结构 DX 友好（本仓现 preload 是手写桥接，迁移后可省）。
- **流式 token**：`EventsOn`/`EventsEmit` 是 Wails 官方流式通道（即 §11 D2-alt 所述 SSE 改造成本所在）；若未来有超重实时流（音频 / 连续屏幕帧），可在 Go 起本地 WebSocket（Gorilla）前端直连，绕开 bridge 序列化开销。

---

## 2. 当前数据通路（迁移前基线）

```
Renderer (vanilla JS, 无框架)  ── window.workmaxLocal.fetch(path) ──┐
                                                                     │ preload 注入 X-Local-Token（闭包持有，renderer 读不到）
                                                                     ▼
                              HTTP loopback  http://127.0.0.1:<ephemeral-port>
                                                                     │
                              Go sidecar (workagent-desktop)  ──→ SQLite（WAL，纯 Go modernc.org/sqlite）
```

- Agent 流式输出已是 **SSE**：`Accept: text/event-stream` + `ReadableStream.getReader()`，全在 preload 层（`preload.ts` 的 `sidecarFetch` / `AgentSSEParser`），renderer 侧零流式代码。
- IPC（`ipcMain.handle`）只用于**登录交易**（begin/status/password/cancel）+ reveal-data-dir，刻意保持 Main-only 以护住 token 与凭据。
- 安全周界：loopback + `X-Local-Token`（每次 spawn 新生 32 字节 CSPRNG）+ Origin 拒绝 + 每 route 的 body/content-type/size 策略（`route_policy.go` 的 `requireSidecarRequestPolicy`）。
- **本地数据面（L3b/L3c/L3d 新增，迁移需一并搬）**：附件落盘 `<DataDir>/thread_files`（`local_render.Store`）；RAG 向量库在同一 SQLite（`knowledge.Store`，sqlite-vec `vec0` 虚表，仍纯 Go）；embedding 走 cgo 的 `knowledge.Embedder`（dlopen `libonnxruntime`）。

---

## 3. 架构选择：Option A vs Option B

| | **Option A：保留嵌入式 loopback HTTP server**（推荐） | Option B：路由全部改 Wails Go 绑定 |
|---|---|---|
| Renderer 改动 | **零**（仍 `fetch('http://127.0.0.1:port/...')`） | 重写 25 路由的调用层 |
| 25 路由 + route_policy 安全周界 | **全保留** | 重写为绑定，周界失效 |
| **SSE 流式**（`/agent/chat`、`/agent/turns/:uuid/replay`、`/system/network-state`） | **字节级不变**，preload 解析器照常工作 | 改成 Go `EventsEmit` + renderer `EventsOn`，**重铺流式管线 = 最大风险** |
| Go 侧工作量 | **纯减法**：删 `WriteHandshake`/`watchStdinShutdown`，其余复用 | 重写 ~25 handler 的传输 + 流式 |
| 安全周界 | token + Origin + body 策略**保留** | 进程内调用，周界失效（损失纵深防御） |

**Option A 明显更优**：减法、renderer 不动、旗舰 SSE 路径零风险、安全周界存活，且是 Option B 的严格子集——以后想逐命名空间迁绑定也行。唯一例外：若硬性要求"完全不开 loopback socket"（例如怕 Windows 防火墙弹窗），才被迫走 B。

> Option A 下 Go 侧要做的，本质是：新建薄 Wails boot 路径——`desktop.NewServer(cfg)` 后读 `srv.Port()`，跳过 `WriteHandshake` 与 `watchStdinShutdown`，token 走进程内变量；`cmd/workagent-desktop/main.go` 里 `func main` 到 `desktop.NewServer(...)` 之间那段 wiring（约 240 行：OpenLocalDB、keychain、proxy、syncers、network watcher、model settings、local files、**knowledge/RAG**、local inference、ServerConfig）**逐字复用**。

---

## 4. 真正的成本：安全信任边界（要重设计的部分）

Electron 给了三样 Wails 没有的东西：**硬件级 IPC sender 身份**、**隔离的 preload 世界**、**统一的导航 hook**。但关键标定——**在 Option A 下，这个成本被显著缩小**：

| 安全件 | 现状（Electron） | Option A 下 | 实成本 |
|---|---|---|---|
| **token 隔离**（renderer 读不到 X-Local-Token） | preload 闭包持有（`preload.ts` 的 `sidecarFetch` 闭包） | loopback + token 周界**原样存活**；token 放 Go 内存 | **低**——财产保住 |
| **登录交易 Main-only** | `ipcMain.handle` + sender 校验（`main.ts` 的 login-transaction handlers + `assertTrustedSender`） | 变成 Go-only 函数，flow ID 留在 Go | **低且更好** |
| **导航/弹窗/权限收束** | `will-navigate`/`setWindowOpenHandler`/`setPermissionRequestHandler`（`main.ts` 的 window hardening 段） | Wails 的 `OnBeforeRequest`/`OnNavigation` 更薄且**按 webview 引擎分平台**；SSRF-hardened 外链逻辑（`security-helpers.ts` 的 `normalizeExternalHTTPURL`）可作纯 Go 搬过去 | **中**——要重写 + 重测 |
| **"只有我的 bundled 页面能调特权"** | `sender===mainWindow.webContents` + mainFrame 校验 | 无 sender 概念；靠"只加载 bundled 资产 + 单实例"近似 | **中**——单窗口 bundled app 下威胁面小，但要重新论证 |

### 4.1 必须在早期决定的设计岔路：token 隔离 ↔ SSE 流式

Option A 下两者有张力：

- 要 **token 不暴露给 JS** → 流式得走 Wails 事件（Option B 的成本渗进来）；
- 要 **SSE 直连不变** → 得把 token 给受信的 bundled webview。

**推荐后者（详见 Part B §9 决策 D2/D3/D4）**：把 webview 当作可信（只加载 bundled 资产、单源单窗口），token 暴露给 JS，直接 fetch loopback 含 SSE。这把"renderer 物理上读不到 token"弱化为"renderer 能读 token 但 webview 受信"。token 的本职——挡住**其他本地进程**连 loopback 端口——依然成立。是**有界削弱**而非崩溃。

**但暴露 token 会连带把 4 条 login-transaction HTTP 路由重新暴露给 renderer**（今天靠 preload 屏蔽 + IPC gate 让它够不到；服务端本身照常接受，见 `route_policy.go` 中 `auth.login-transaction.*` 四条 policy）。为保住"凭据不经 renderer 可直达"的属性，Part B 采 **D3**：这 4 条路由在嵌入式 server 里**不注册**，登录改走 Wails Go 绑定直接调 `loginCoordinator`（进程内，凭据不过 HTTP）——这其实比今天的 Main-only 更严。token-secrecy 若为硬要求，则退到 D2-alt（token 留 Go、经绑定代理 fetch），代价是 SSE 要改走 Wails 事件。

---

## 5. 性能收益（具体，且标定边界）

| 指标 | Electron（现） | Wails Option A | 来源 |
|---|---|---|---|
| 基础内存 | ~150–250MB | ~40–80MB | 不再捆绑 Chromium |
| 安装包 | ~100MB+ | ~15–25MB | 系统 webview（WKWebView/WebView2） |
| 冷启动 | 拉 Chromium + spawn sidecar + 5s 握手 | 单进程直起（删掉 spawn+握手） | 进程内嵌入的直接收益 |
| **脑图画布帧率** | 不变 | **不变** | ⚠️ 两者都是 webview，画布性能是 renderer 层问题 |

> 数值为工程估算范围，非实测；P0 spike 应顺便落一个 cold-start/RAM 基准对照。
>
> **RAG 开启后的增量（两边同担，非 Wails 引入）**：`libonnxruntime` + MiniLM 模型权重会让安装包 **+~100MB 量级**、常驻内存 **+数十 MB**、并在 `knowledge.NewEmbedder` 处增加一次 dylib 加载与会话初始化的冷启动开销。今天 RAG 在 dev 与 packaged 里都还没启用（资源未打包，见 §6），所以基准要**分 RAG on/off 两组测**，否则 §5 的瘦身收益会被 RAG 的体积吃掉一部分而看不清归因。

---

## 6. 分发与打包

- **可复用**：`notarize-mac.sh` 里的 `codesign`/`notarytool`/`stapler`/`spctl` 是 shell 管道，换路径输入即可；**`appId: ai.workmax.desktop` 必须保留**（绑 Keychain service，`server/desktop/cloud_proxy/keychain.go`）。
- **要改**：`build-mac.sh` 的 preflight 校验器按新 bundle 布局重写；bundle 布局变（无 asar）；entitlements 改写——见下面 §6.1，**不是单纯做减法**。
- **净新增**：Windows/Linux 打包——今天本来就没有，Wails 的 Win/Linux 比 Electron 更省事。
- **自动更新**：打平——现在就没有（`publish:null`，无 electron-updater）；Wails 不更差；将来要做得自己写（GitHub releases 校验 + 签名），两边都一样。
- **已知跨平台债（既有，非 Wails 引入）**：keychain 只实现了 macOS（shell `security` CLI，`keychain_darwin.go`；`keychain_other.go` 是报错 stub）。Win/Linux 上要做 Credential Manager/libsecret——无论 Electron 还是 Wails 都得做。

### 6.1 CGO / ONNX Runtime 带来的打包增量

> **rev.3：本节整体推迟到 RAG 里程碑，不在首版 Wails 包的关键路径上。** 依据见 §0.5.1——首版 Wails main 不 import `knowledge`，`FileIndexer`/`KnowledgeHooks` 传 nil，RAG 关闭是既有的一等公民状态。本节保留为**RAG 收口时的执行清单**。
>
> 另外两处随 rev.3 更新：① 表格首行"构建带 CGO"在 Wails 路线下是**零增量**（Wails 自身在 mac/linux 就用 cgo）；② "交叉编译"一行随 x64 目标一起推迟（首版 arm64-only）。

L3c 之后 sidecar 需要 cgo 与三份 native 资源，这块在 Electron 侧**本身就还没做完**，RAG 收口时是"顺带补齐 + 换承载方式"两件事叠在一起：

| 事项 | 现状 | Wails 下要做 |
|---|---|---|
| **构建带 CGO** | `dev.sh` / `build-mac.sh` 已 `CGO_ENABLED=1`；CI 用默认（native，隐式为 1） | Wails v3 的 Taskfile 构建任务必须显式带 `CGO_ENABLED=1` + `-tags desktop`，否则退化成"编译不过"而非"少个功能" |
| **交叉编译（既有隐患）** | `build-mac.sh` 用 `GOARCH=$target_goarch CGO_ENABLED=1 go build` 但**没有 `CC` 覆盖**——在 Apple Silicon 上出 x64 包大概率直接失败 | 修法一样（`CC="clang -arch x86_64"` 或 `-target`）。**建议在迁移前先在 Electron 侧修掉并验证**，别把一个未验证的构建路径带进新 Taskfile |
| **native 资源入包** | `electron-builder.yml` 的 `extraResources` 里**根本没有** `libonnxruntime.dylib` / `knowledge/model.onnx` / `knowledge/tokenizer.json` | Wails 无 `extraResources` 等价物：要在构建脚本里把三份资源摆进 `Contents/Resources/`，并**逐个 `codesign`**（notarization 会逐 Mach-O 检查） |
| **资源定位** | `knowledge.ResolveResources()` 读 `WORKMAX_RESOURCES_DIR`，缺省是**相对 cwd 的 `"resources"`**；全仓**没有任何地方设置过这个变量**（Electron 也没有）→ 打包后必然解析不到，RAG 静默关闭 | 进程内嵌入后没有"给子进程设 env"这一步了，应改为 `BootstrapConfig.ResourcesDir` 显式传入（由 Wails 侧按可执行文件路径推导 `Contents/Resources`），env 仅作 dev 覆盖 |
| **entitlements** | 现为 allow-jit / allow-unsigned-executable-memory / allow-dyld-environment-variables / network.client | 去掉 JIT 与 unsigned-exec-mem（Electron V8 专属）；**但 dlopen 第三方 dylib 在 hardened runtime 下有新要求**：要么用同一 Developer ID 重签 `libonnxruntime.dylib`（首选），要么新增 `com.apple.security.cs.disable-library-validation`（次选，扩大攻击面）。`allow-dyld-environment-variables` 可以去掉——资源定位改走显式参数而非 `DYLD_*` |
| **许可清单** | `go-licenses` 已跑 | 除去 Electron/Chromium、加 Wails/webview 外，还要**补 ONNX Runtime（MIT）与 MiniLM 模型权重的许可**——模型权重不是 Go 依赖，`go-licenses` 抓不到，需手工登记 |

> **净影响（rev.3 修订）**：本节全部计入首版之后的里程碑——交叉编译一行归 **M+1**（2–3 天），其余归 **M+2 RAG 收口**（0.5–1 周）。首版关键路径不含本节。

---

## 7. 成本量级 + 阶段化

> **rev.3：本节为原始估算，实际执行以 §0.5.3 的四周排期为准**（首版 RAG-off / arm64-only）。下表的阶段划分仍然有效，只是 P2 变轻、P-1 移出。

**约 3–6 周（一个专注工程师）**，主要被"安全重证 + 打包重建 + WKWebView 怪癖"吃掉，**不是被代码量吃掉**。

| 阶段 | 内容 | 目的 |
|---|---|---|
| **P0 Spike（几天）** | 最小 Wails binary：import `server/desktop`（带 CGO）、起嵌入式 server、加载 bundled renderer、fetch 跑通 **+ WKWebView 下 SSE 字节级验证 + ONNX 生命周期验证** | 砍掉最大未知（WKWebView fetch loopback / SSE / 资产收束 / cgo 退出路径） |
| **P1 安全** | token 代理绑定（或决策暴露 token）、登录交易 Go-only、导航收束、单实例 | 重建信任边界 |
| **P2 打包** | 新 entitlements + native 资源入包与签名 + preflight 重写 + 公证流跑通；保留 `appId` | 出可分发 macOS 包 |
| 切换 | Electron 作为出货通道直到 P2 全绿，再 cut over | 不中断现网 |

**关键去风险点 = P0 spike 里的「WKWebView 下 SSE 是否字节级跑通」**——这是整个迁移唯一可能让你回头的技术未知。W1 第一件事就做它。

> ONNX Runtime 退出析构（§10 生命周期）在 rev.2 里是次高风险。rev.3 曾以"首版 RAG-off"推迟它——**而它在 rev.16 真的发生了**：后台索引与环境销毁并发导致 SIGSEGV，见 §0.5.21。已修。

---

## 8. 最终建议 + 决策条件

> **rev.3 状态**：本节的推荐**已被采纳**，下面的"先不迁"条件保留为决策记录（供日后复盘决策依据，不再是待判定项）。逐条现状：条件 1（画布痛）——不成立，痛点是 app 重；条件 2（出货优先）——用 §0.5 的最快路径化解（4 周而非 7.5 周）；条件 3（modal OAuth）——不成立，已是死代码；条件 4（RAG 出货）——用 §18-6 的 **C 方案**化解（首版 RAG-off）。

**推荐：迁移到 Wails Option A**——后端可嵌入已被代码证实，迁移是一次罕见的干净窗口。

**满足任一条件，建议先不迁**：

1. 真实性能痛是**画布/交互** → 先做 Canvas/WebGL，那与 shell 无关。
2. 现阶段**出货速度 / 赚钱功能**优先级高于瘦身 → 迁移是纯基建，不推进旗舰；内存/体积现在没用户投诉就先不动。
3. **需要 modal 内嵌 OAuth webview** → 那段已死代码若要复活，Electron 的 BrowserWindow 比 Wails（只能开系统浏览器 + loopback 回调）省事。
4. **（rev.2）L3c RAG 要在最近一个版本出货** → 先把 native 资源打包在 Electron 侧跑通再迁；否则等于同时调试"新 shell + 第一次 native 签名"两个未知。**rev.3 判定：RAG 可延后，故取 §18-6 的 C 方案（首版 RAG-off），本条不成立。**

**满足下面条件则趁早迁**：

- 痛点是 app 太重/启动太慢、或有 Windows/Linux 计划（Wails 让跨平台更便宜）、且现在处于 p1-ea（用户少、renderer 还没膨胀）。**越晚迁移，要重证的安全面和 renderer 契约越大。**

---

---

# Part B — 实施设计（Option A，**已采纳**）

> **rev.3：本部分已从"假定采纳"转为实施设计**，是当前执行依据。执行排期见 §0.5.3 / §13.1；决策台账见 §18。Part A（§1–§8）保留为评估记录，用于复盘决策依据。

## §9 目标拓扑与关键决策

```
┌──────────────────────── 单一 Wails 二进制 (workmax-desktop) ─────────────────────────┐
│                                                                                        │
│   Go 主进程                                                                            │
│   ┌────────────────────────────────────────────────────────┐                           │
│   │ desktop.Bootstrap(cfg)  ← 抽自 cmd/workagent-desktop     │  SQLite (WAL, 纯 Go)     │
│   │   OpenLocalDB · keychain · tokenStore · cloudClient     │   ├ 业务表 + w_workagent_*│
│   │   proxy · loginCoordinator · syncers · networkWatcher   │   └ vec0 向量表 (L3c)     │
│   │   modelSettings · localFiles · knowledge(cgo) ·         │                           │
│   │   localInference                                        │  Contents/Resources/      │
│   │   → desktop.NewServer(ServerConfig{...})                │   ├ libonnxruntime.dylib  │
│   │   → 绑 127.0.0.1:0 （25 条路由；login×4 由反代拒绝）  │   └ knowledge/{model,tok} │
│   └────────────────────────────────────────────────────────┘                           │
│            ▲ Wails Go 绑定 (login*)            ▲ 直接 fetch (token)                     │
└────────────┼──────────────────────────────────┼─────────────────────────────────────────┘
             │                                  │
   ┌─────────┴──────────────────────────────────┴─────────────────┐
   │  WKWebView  ·  只加载 embed.FS bundled 资产 (renderer/en/desktop) │
   │  · renderer 侧 JS shim 暴露 window.workmaxLocal / desktopBridge  │
   │  · SSE：直接 fetch('http://127.0.0.1:port/agent/chat', ...)      │
   └─────────────────────────────────────────────────────────────────┘
```

| 决策 | 选择 | 理由 |
|---|---|---|
| **D1 shell 版本** | **Wails v3**（已定；P0 spike 验证，v2 作退路） | v3 原生 server build 直接实现 D4 双模式（省手写）；托盘/可取消事件 hook/可定制 Taskfile 构建对齐需求；避免将来 v2→v3 二次迁移。beta 风险由 EA 阶段 + mac 优先（避开 Windows #4559）+ P0 spike 验证兜底 |
| **D2 传输** | token 暴露给受信 bundled webview，renderer 直接 fetch loopback（含 SSE） | SSE 字节级不变；契约零改动。代价见 D4 |
| **D2-alt**（退路） | token 留 Go，renderer 经 Wails 绑定 `Fetch()` 代理 | 保住 token-secrecy；但 3 条 SSE 流要改走 `EventsEmit`（Option B 成本渗入）。仅当 secrecy 为硬要求 |
| **D3 登录交易**（rev.18 按实现改写） | 4 条 `/auth/login-transaction*` **仍在 sidecar 注册**；反代**服务端拒绝**渲染器访问它们，登录只能走 UI server 上的 `/login/` 网关（四个动词，Go 决定路由与方法） | 原设计是"不注册 + Wails 绑定"，前提是"D2 暴露 token ⇒ 必须移出路由表"。**D4 反转后 token 不再下发，这个前提消失了**，且 §0.5.11 实测绑定在 loopback origin 上不可用。当前形态达成同一目标：renderer 无法命名凭据路由，而服务端拒绝比 preload 的同进程守卫更强。"不注册"仍是可选的严格化，不是欠账 |
| **D4 token secrecy** | **A2 已接受**：token 下发 renderer（直连 loopback 含 SSE）；由"renderer 物理读不到"降级为"webview 受信（bundle-only/单源/单实例）" | 有界削弱——token 够不到**云凭据/keychain**（那条路经 D3 绑定，不过 HTTP）；缓解：严格 CSP、只服 bundled 资产、模型输出转义。**残余面见 D4.1** |
| **D4.1 残余面（L3d 后扩大）** | 显式承认：一把 token = 全部**本地**数据面 | L3d 让未登录用户也能用 `localSingleUserUID (1<<62)` 建 thread / chat / 传文件。所以 token 泄漏的后果不再是"只能冒充已登录用户调云代理"，而是**即使从未云登录，也能读写全部本地 thread/message/附件（及 L3c RAG 索引）**。这仍在"本机本地"边界内（攻击者已在本机 = 本来就能读 SQLite 文件），故不推翻 A2；但安全对等清单（§14）必须逐字写上，不能只写"够不到云凭据" |
| **D5 死面** | 丢弃 `system.*`/`history.*` typed/`revealDataDir`/`capabilities`，保留 raw fetch | renderer 本就没用；少移植、少攻击面 |

## §10 Wails 应用结构与启动装配

**目录**：新建 `desktop/wails/`（Go package main，`-tags desktop`）。**单入口双模式**：只产出一个 Wails 二进制，靠 `--serve-only` 标志切换——正常启动 = 桌面 app（起 webview + 嵌入式 server）；`--serve-only` = 不起窗口、只跑嵌入式 server（取代今天的独立 sidecar，供 dev `curl` 调试 + smoke 靶子）。`server/cmd/workagent-desktop/` 在 **Electron 退役后删除**（迁移期间仍保留：Electron 仍 spawn 它、smoke 仍依赖它）。两种模式共享同一 `desktop.Bootstrap()`。

**关键重构：抽出共享 Bootstrap**。把 `server/cmd/workagent-desktop/main.go` 里 `func main` 到 `desktop.NewServer(...)` 之间那段（dataDir 锁 → OpenLocalDB → token → OAuth/keychain/tokenStore/cloudClient/proxy/loginCoordinator → networkWatcher/syncers/rendererLogger/modelSettings → localFiles → **knowledge/RAG（best-effort）** → localInference → `desktop.NewServer(ServerConfig{...})`）抽成：

```go
// server/desktop/bootstrap.go  (新，-tags desktop)
package desktop

type BootstrapConfig struct {
    // 实现说明（rev.18）：原设计有 DropLoginTransactionHTTP，用于把 login 路由移出
    // HTTP 表。它没有被实现，因为 D4 反转（token 不再下发）之后该开关的前提消失了；
    // 凭据路由改由 UI 反代在服务端拒绝。下面是实际存在的字段。
    LocalToken string   // 空 → 继承 env 或新生
    ResourcesDir             string // native 资源根（.app/Contents/Resources）；空 → 回落 env/相对 cwd
    // ...DataDir 覆盖、token 注入等 dev 开关
}

type Boot struct {
    Server       *Server
    LocalToken   string
    DataDir      string
    DB           *gorm.DB
    LoginCoord   *cloudproxy.LoginTransactionCoordinator
    MessagesSyncer *desktopsync.MessagesSyncer
    ThreadsSyncer  *desktopsync.SyncWorker
    NetworkWatcher *NetworkStateWatcher
    // ...Shutdown 顺序需要的一切
    closeEmbedder func() error   // knowledge.Embedder.Close（cgo，见下方生命周期）
    cancel context.CancelFunc
}

// Bootstrap 跑通所有 sidecar wiring 并起好嵌入式 HTTP server（不含 login-transaction HTTP 路由）。
// 调用方决定如何暴露 port/token、何时 Shutdown。
func Bootstrap(cfg BootstrapConfig) (*Boot, error) { /* = main.go 的 wiring 段 */ }
func (b *Boot) Shutdown(ctx context.Context) error { /* = main.go 的有序关闭段 */ }
```

> **cgo 依赖方向——rev.3 的关键设计约束**：`server/desktop` 目前**刻意不 import** `server/desktop/knowledge`，用结构化接口 `FileIndexer` 解耦（`server.go` 注释：*"Defined here as an interface so the desktop package does not depend on the cgo knowledge package"*）；`localinference` 侧同理用 `KnowledgeHooks`（`nil=关闭`）。**全仓 import `knowledge` 的只有 `cmd/workagent-desktop/main.go` 一处。**
>
> 因此 Bootstrap 必须拆双文件，**把 knowledge 的构造留在 cgo 变体里**：
>
> - `bootstrap.go`（`//go:build desktop`）：全部 wiring，**不 import knowledge**；`FileIndexer` / `KnowledgeHooks` 由 `BootstrapConfig` 注入，缺省 nil。
> - `bootstrap_cgo.go`（`//go:build desktop && cgo`）：`knowledge.ResolveResources` → `NewEmbedder` → `NewStore` → `NewIndexer`，best-effort 构造后注入；并把 `Embedder.Close` 挂到 `Boot.closeEmbedder`。
>
> 这样 **首版 Wails main 只需不引用 cgo 变体，整个二进制在我们的代码层面零 cgo**（§0.5.1），`desktop` 包在 `CGO_ENABLED=0` 下仍可编译（CI/测试受益），RAG 收口时只需把 `bootstrap_cgo.go` 接上而不动任何既有 wiring。
>
> 备选：Bootstrap 放新包 `server/desktop/boot` 同时 import 两者——依赖方向更干净，但要导出若干 unexported 装配细节。**W1 按双文件做，成本更低。**

两种模式都调它，wiring 永不漂移：
- `desktop/wails/main.go`（正常模式）：**删** `WriteHandshake`/`watchStdinShutdown`；**保留** `acquireSidecarLock`（无害）+ 信号处理（或换 Wails shutdown hook）；起 webview + 绑定。
- `desktop/wails/main.go`（`--serve-only` 模式）：只 `Bootstrap` + `srv.Serve()` 阻塞到信号——等价今天的独立 sidecar，但不开窗、不经 Wails runtime。**v3 有原生 server build（"run the same app/services without a desktop window"）**，优先用框架能力实现此模式；P0 spike 确认具体机制（build tag vs 运行时标志），下方骨架为示意。
- `server/cmd/workagent-desktop/main.go`：**迁移期间不动**（Electron 仍出货 + smoke 仍用）；Electron 退役、smoke 切到 `wails --serve-only` 后**整体删除**。

**Wails main 骨架**：

```go
//go:build desktop
package main

func main() {
    boot, err := desktop.Bootstrap(desktop.BootstrapConfig{
        ResourcesDir: resolveResourcesDir(),     // 空 → <DataDir>/resources（下载目的地）
    })
    if err != nil { log.Fatalf("bootstrap: %v", err) }
    defer boot.Shutdown(context.Background())

    // --serve-only（v3 优先用原生 server build；此处为示意）：不起 webview，只跑 server
    // （dev 调试 + smoke 靶子；取代 cmd/workagent-desktop）
    if serveOnly() {
        boot.ServeUntilSignal()                  // 阻塞到 SIGINT/SIGTERM，等价旧 sidecar
        return
    }

    app := app.New(
        app.Bind(&LoginAPI{coord: boot.LoginCoord}),           // D3：直调 coordinator
        app.Bind(&RuntimeAPI{port: boot.Server.Port(), token: boot.LocalToken}), // 给 webview 坐标
        // 资产：embed.FS 只暴露 renderer/en/desktop（bundle-only → 无别处可导航）
    )
    app.OnBeforeClose(func() { boot.Shutdown(ctx) })
    app.Run()
}
```

**生命周期**：Wails `OnBeforeClose`/shutdown → `boot.cancel()` → `srv.Shutdown(ctx)` → syncers Drain → **`emb.Close()`** → DB close，复刻今天 `main.go` 的有序关闭（messages/threads Drain 与 SQLite-WAL 竞态的兜底不变）。

> **⚠️ ONNX Runtime 析构是硬约束（rev.3：首版 RAG-off 时不触发，RAG 收口时必验）**。今天 `main.go` 有一段带注释的 `defer`：
>
> > *"Release the ONNX Runtime environment during shutdown, before the Go runtime exits. Otherwise onnxruntime's C++ static destructors abort the process on exit (`mutex lock failed`)."*
>
> 也就是说 **`Embedder.Close()` 漏掉 = 每次退出都可能 abort（非零退出码 + 崩溃报告）**。今天它靠 `main` 正常返回时的 `defer` 保证；Wails 下 `app.Run()` 之后的控制流由框架掌握，**`defer` 是否一定跑到无法假定**。因此：
> - `Boot.Shutdown` 必须**自己**调用 `closeEmbedder`，不能依赖调用方的 `defer`（**这条现在就要写进 W1 的 Bootstrap，即使首版 RAG-off、`closeEmbedder` 为 nil**——留好挂载点，别等 RAG 收口时再回来改关闭顺序）；
> - RAG 收口时显式验证：正常关窗、Cmd+Q、Dock 退出、以及 `--serve-only` 收到 SIGTERM 四条路径下退出码均为 0；
> - 若 Wails 的退出路径绕过 `OnBeforeClose`（beta 期有此风险），退路是注册 `runtime.Quit` 前置 hook 或自行接管信号，**并把这一条记进 §15 风险登记**。
>
> 这条约束在 `--serve-only` 模式下同样成立——它今天就是 smoke 的靶子，退出码非 0 会直接染红 smoke。

## §11 安全重证（逐件）

| Electron 件 | Wails 重证实现 | 残余风险 |
|---|---|---|
| 导航/弹窗/权限收束 | ① 只服 `embed.FS` bundled 资产 → webview **无别处可导航**；② 外链拦截→`browser.OpenURL`，前置 SSRF-hardened `normalizeExternalHTTPURL`（`security-helpers.ts` 移植成纯 Go）；③ bundled 资产加严格 CSP | Wails 导航 hook 比 Electron 薄且按 webview 引擎分平台——**P0 必须验证所选 Wails 版本+平台的 hook 能力**（最大残余风险） |
| token 隔离 | D2：token 经 `RuntimeAPI` 绑定下发 webview；loopback 仍由 `RequireLocalToken` 中间件保护，挡住**其他本地进程** | D4/D4.1：renderer 可读 token（webview 受信兜底）；泄漏后果含全部本地数据面（L3d 后未登录亦然） |
| 登录交易 Main-only | D3：login-transaction 不进 HTTP 表；`LoginAPI` 绑定直调 `loginCoordinator`，flow ID 留 Go；凭据卫生（fresh copy + finally 置空）原样移植 | 低——比现状更严 |
| "只有我的页能调特权" | bundle-only 资产 + 单实例 + token 周界共同近似 | 无 `mainFrame` 模拟；单窗口 bundled app 下威胁面小，但需论证并存档 |
| 单实例 | Wails 单实例锁；或保留 `acquireSidecarLock` + 平台 named mutex | 低 |

> **token-secrecy 硬要求场景**：退到 D2-alt（token 留 Go、`Fetch()` 绑定代理），login 路由也可继续注册（代理屏蔽）。代价：3 条 SSE 改 `EventsEmit`——属 Option B 成本，需单列工时。

## §12 Renderer 改动（具体）

Wails **无 preload 世界**，故 `desktop/electron/src/preload.ts`（`AgentSSEParser`、`sidecarFetch` 包装、typed bridge、login IPC）需整体重新落地为 **renderer 侧 JS shim**，随 bundled 资产下发，暴露同样的 `window.workmaxLocal` / `window.desktopBridge` 形状。契约不变 → `renderer.js` 几乎不动。

| 现状（preload） | Wails 下落点 | 改动 |
|---|---|---|
| `sidecarFetch`（注入 token） | renderer shim：从 `RuntimeAPI` 拿 port/token 后直接 `fetch(loopback)` | 小——逻辑不变，token 来源换 |
| SSE parser + `getReader` | renderer shim 内（plain JS fetch + ReadableStream） | 移植，**WKWebView 下 ReadableStream 字节级验证=P0 kill 检查** |
| typed `desktopBridge`（auth/agent/...） | renderer shim 同形重建；底层从 fetch 改直连 loopback | 中——机械移植，契约不变 |
| login IPC（begin/status/password/cancel） | 改调 Wails `LoginAPI` 绑定 | 小 |
| 死面（system/history/reveal/capabilities） | 不重建（D5） | 减负 |

`renderer.js` 实际改动集中在几个读 `window.workmaxLocal` / `window.desktopBridge` 的 helper + 自身的 `sidecarFetch` 的**来源重指向**，业务逻辑零改。（这些 helper 在 `renderer.js` 中集中成块，可用 `grep -n "window.desktopBridge\|window.workmaxLocal" renderer.js` 一次定位。）

## §13 任务分解

> **rev.3：按 §0.5.3 的四周排期重排。** 首版关键路径 = W1–W4；RAG / x64 / 跨平台在"首版之后"分组，**不阻塞出包**。

### 13.1 首版关键路径（W1–W4）

| 周 | 任务 | 关键文件 | 完成标准 |
|---|---|---|---|
| **W1** | 抽 `Bootstrap`（双文件 cgo 拆分，首版只用非 cgo 变体）；最小 Wails v3 binary 起嵌入式 server（含 `--serve-only`）；WKWebView 加载 bundled renderer；fetch `/health` 跑通 | `server/desktop/bootstrap{,_cgo}.go`(新)、`desktop/wails/main.go`(新) | 正常模式 `/health` 200；`--serve-only` 可独立起且 smoke 可驱动；**`desktop` 包在 `CGO_ENABLED=0` 下可编译** |
| **W1 kill ①** | WKWebView 下 `/agent/chat` SSE 字节级跑通（ReadableStream + parser 移植） | renderer shim SSE | 完整一轮 text_delta→done 到达；**不过则终止迁移** |
| **W1 kill ②** | v3 在 macOS 稳定度：无 beta-blocker 崩溃；原生 server build 可用并跑通 `/health` | `desktop/wails/` | v3 mac 关键路径稳定；**严重 beta 崩溃→回退 v2（D1 退路）** |
| **W1 kill ③** | 导航收束可行性：embed.FS bundle-only + 外链 OpenURL + CSP | `desktop/wails/` | 外链走系统浏览器，无任意导航；**强度不足→评估 D2-alt 或保留 Electron** |
| **W2 安全** ✅ | `/login/` 网关（D3 落地形态）；能力路径；反代拒绝凭据路由；CSP 收紧（无 `unsafe-inline`）；DevTools 仅 env 开启；单实例两层 | `desktop/wails/uiserver.go`、`main.go` | 契约 checker 14 条保证绿；`uiserver_test.go` 覆盖 |
| **W3 renderer shim** | 移植 SSE parser + typed bridge + fetch 包装；renderer helper 重指向（可与 W2 并行） | `renderer/en/desktop/shim.js`(新) | 与 Electron 版行为对等（见 §14） |
| **W4 打包** | entitlements **纯减法**（去 JIT/unsigned-exec-mem/dyld-env，留 `network.client`）；preflight 校验器按新 bundle 重写；公证流跑通；**保留 `appId ai.workmax.desktop`** | `desktop/wails/build/`、`scripts/` | arm64 包过 Developer ID 签名 + notarytool + stapler + spctl |
| **W4 资产** | `go-licenses` 重跑；THIRD_PARTY 更新（去 Electron/Chromium，加 Wails/webview 依赖） | `scripts/` | 许可清单完整 |
| **W4 基准** | 冷启动 / 稳态 RSS / 包体积同机对照 | — | §5 的估算区间被实测数字替换 |

### 13.2 首版之后（不阻塞出包）

| 里程碑 | 任务 | 完成标准 |
|---|---|---|
| **M+1 x64 / universal** | 修 CGO 交叉编译（`CC="clang -arch x86_64"` 或原生 runner），出 x64 包 | arm64 + x64 双包，或 universal |
| **M+2 RAG 收口** | 接上 `bootstrap_cgo.go`；native 资源进 `Contents/Resources/` 并逐个 codesign（或 `disable-library-validation`）；preflight 断言三份资源非空；ONNX 四条退出路径验证；手工登记 ONNX + MiniLM 许可 | packaged app 日志出现 `knowledge: local RAG enabled`，退出码全为 0，公证通过 |
| **M+3 退役** | smoke 切到 `wails --serve-only`；删除 `server/cmd/workagent-desktop/` + Electron 构建脚本 + `dist/oauth-window.js` 残留 | 无残留引用；smoke 全绿 |
| **M+n 跨平台** | Windows/Linux（连带 keychain Credential Manager/libsecret；注意 Windows 下 L3c 会引入 Wails 本身不需要的 cgo 依赖，见 §0.5.1） | 届时单独评估 |

## §14 验证与对等（parity）

- **契约对等**：`desktop/contracts/desktop-boundaries.v0.json` 是 renderer↔后端合同的真源（当前 `loopbackRoutes` **25 条**，与 `route_policy.go` 的 `currentSidecarRoutePolicies` 由 `check-desktop-boundaries.mjs` 双向校验）——Wails 版必须满足同一边界校验。把 `desktop/scripts/check-desktop-boundaries.mjs` 复用到 Wails 构建。
  > D3 落地时注意：4 条 login-transaction 从 HTTP 表移除后，`route_policy.go` 与契约 JSON 会**双双少 4 条**（25→21）。这不是"契约破坏"，但 checker 会红——需要在契约里给这 4 条加显式的"迁往 Wails 绑定"标记，而不是静默删除，否则丢失"这些能力仍然存在、只是换了传输"的可审计性。
- **smoke 复用**：`desktop/scripts/smoke-local.sh`（`--check-pid-lock`/`--sidecar-binary`）和 `smoke-packaged-app.sh` 的 renderer reporter 逻辑重定向到 Wails 包，验 cached thread/message 可见、local-token 拒绝、diagnostics ok。**新增两条断言**：退出码为 0（ONNX 析构，见 §10）、packaged 日志出现 `knowledge: local RAG enabled`。
- **性能基准**（同机对照）：冷启动时间、稳态 RSS、安装包大小——**落数字**，验证 §5 的估算区间。**必须分 RAG on/off 两组**，否则 ONNX 的体积/内存会污染 shell 瘦身的归因。
- **安全对等清单**：逐条列 Electron 保证（§附录 C），标 Wails 如何满足；残余项显式标红供评审。**必须含 D4.1**（token 泄漏 ⇒ 全部本地数据面，未登录亦然）。

## §15 风险登记 + 终止条件

| 风险 | 等级 | 缓解 | 触发动作 |
|---|---|---|---|
| WKWebView 下 SSE 不稳/不支持所需 ReadableStream 行为 | **高** | P0 第一周验证 | **不过→终止迁移，留 Electron** |
| 导航收束在 WKWebView/WebView2/webkitgtk 上不可达 Electron 等强度 | 中-高 | embed.FS bundle-only + CSP + 外链拦截；P0 验证 | 强度不足→评估 D2-alt 或保留 Electron |
| macOS 公证对非 Electron bundle 的差异（无 asar、WKWebView 框架） | 中 | 复用 notarize-mac.sh 的 codesign/notarytool/stapler； entitlements 重写 | 打通即解 |
| renderer shim 移植 bug（SSE parser/typed bridge 行为偏移） | 中 | 契约对等测试 + smoke | 回归测试兜底 |
| Wails v3 beta 风险（API 收尾中、Windows webview 崩溃 issue #4559） | 中 | mac 优先避开 #4559；P0 spike 验证 mac 稳定度（§13 kill 检查）；v2 作 D1 退路 | spike 见 beta-blocker→回 v2 |
| ONNX Runtime 静态析构在 Wails 退出路径上未被释放 → 退出 abort（`mutex lock failed`） | 中 → **首版不适用**（RAG-off） | `Boot.Shutdown` 自己调 `Close()`，不依赖调用方 `defer`（挂载点 W1 就留好）；**M+2 验四条退出路径的退出码** | 框架绕过 `OnBeforeClose` → 自行接管退出流程（+工时，不终止迁移） |
| native 资源（dylib/模型/分词器）签名或 library validation 卡公证 | 中 → **首版不适用** | 首选用同一 Developer ID 重签 dylib；退路加 `disable-library-validation`；**M+2 处理** | 两条都不通 → 继续维持 RAG-off 出货，另评估纯 Go embedding（§18-7） |
| CGO 交叉编译（arm64 上出 x64）未验证 | 中 → **首版不适用**（arm64-only） | 既有隐患，非 Wails 引入；**M+1 处理** | 修不动 → 该架构改用原生 runner 构建 |
| **首版 RAG-off / arm64-only 的用户预期落差** | 低-中 | Intel Mac 与 RAG 用户 cutover 前留在 Electron 通道；**且 Electron 通道今天本就没有 RAG**（资源从未打包）——对用户是"仍未到达"而非"退化" | EA 发布说明写明架构与 RAG 状态 |
| 多窗口/modal OAuth（未来） | 低 | v3 多窗口已原生支持，此风险较 v2 已消除 | 届时再议 |
| keychain 仅 macOS（Win/Linux 缺） | 既有债 | 与 shell 无关；跨平台时补 Credential Manager/libsecret | 不阻塞 mac 迁移 |

**终止条件**：P0 的 SSE kill 检查或导航收束验证失败 → 立即停止，保持 Electron 出货。

## §16 切换与回滚

- **rev.3 姿态**：Wails 是主线，Electron 是**回滚热备**——但"主线"指开发投入方向（§0.5.4 的三条纪律），**出货通道仍是 Electron**，直到下面的切换门槛全绿。这两件事不要混。
- 切换门槛（首版）：契约对等 ✓、性能基准落数字 ✓、安全对等清单评审通过（含 D4.1）✓、至少 1 个 **arm64** EA 构建签名公证 ✓。**RAG 与 x64 不是切换门槛**——它们在 Electron 通道上今天也不具备（RAG）或不受影响（x64 需在 M+1 补齐后才能对 Intel 用户 cutover）。
  > 注意这条推论：**Intel Mac 用户的 cutover 必须等 M+1**。首版 EA 只对 Apple Silicon 用户切换，Intel 用户继续走 Electron 通道。发布说明要写清楚。
- 回滚：Electron release 通道保留 N 周（建议 ≥2 个迭代）热备；Wails 出现回归即切回。
- **退役收尾**：Wails 稳定运行 ≥2 迭代后，smoke 切到 `wails --serve-only`，删除 `server/cmd/workagent-desktop/`（及 Electron 构建脚本）——达成"单入口双模式"终局；删除前 grep 确认无引用。

## §17 工时分解（单专注工程师）

**首版关键路径（rev.3 主线，对应 §0.5.3）**

| 周 | 内容 | 估时 | 占比主因 |
|---|---|---|---|
| W1 | Bootstrap 抽取（双文件 cgo 拆分）+ 最小 binary + `--serve-only` + 三条 kill 检查 | 1 周 | 去风险，非代码量 |
| W2 | 安全（LoginAPI/RuntimeAPI/单实例 + 导航收束 + CSP 落地） | 1 周 | 信任边界重证 |
| W3 | renderer shim（SSE parser + typed bridge 移植 + helper 重指向）· 可与 W2 并行 | 1 周 | 机械但需对等测试 |
| W4 | 打包（entitlements 纯减法 / preflight / 公证 / 许可）+ 对等测试 + 性能基准 | 1 周 | 单可执行文件 + embed.FS，公证最简形态 |
| **合计（首个可分发 arm64 EA 包）** | | **~4 周** | |

**首版之后（不阻塞出包）**

| 里程碑 | 估时 |
|---|---|
| M+1 x64 / universal（CGO 交叉编译修复） | 2–3 天 |
| M+2 RAG 收口（native 资源入包 + 逐个签名 + ONNX 退出验证 + 许可登记，§6.1 全套） | 0.5–1 周 |
| M+3 退役（smoke 切 `--serve-only`、删 `cmd/workagent-desktop` 与 Electron 脚本） | 2–3 天 |

> 若走 D2-alt（token-secrecy 硬要求），SSE 改 `EventsEmit` 额外 +0.5–1 周。**rev.3 已否决 D2-alt**（D4/A2 已定），此项仅作退路留档。
>
> **口径说明**：M+1 与 M+2 **不是 Wails 引入的成本**——不迁移也要还（今天 RAG 在 packaged build 里根本没启用、x64 的 CGO 交叉编译也从未验证过）。rev.2 把它们合并计入得出 4–7.5 周；rev.3 把它们移出首版关键路径，是**排期决策而非估时修正**——总工作量没变，变的是什么先出货。

## §18 决策台账（rev.3：全部落定）

1. **是否采纳迁移**：**采纳**（rev.3）。Wails 为主线 shell，按 §0.5 最快路径执行；Electron 降级为回滚热备并冻结 shell 层（§0.5.4）。
2. **Wails 版本**：**v3**（已定；P0 spike 验证 mac 稳定度，v2 退路）。
3. **D4 token 暴露**：**接受 A2**（暴露 X-Local-Token 给受信 webview，SSE 不变；不走 D2-alt）。（已定）
4. **入口结构**：**单入口双模式**——Wails 二进制用 `--serve-only` 吸收 sidecar 角色；`cmd/workagent-desktop` 迁移期间保留作 Electron 退路，Electron 退役后删除。（已定）
5. **Windows 目标时间**：**推迟到 M+n**，不进首版视野。届时一并还 keychain（Credential Manager/libsecret）与 L3c 在 Windows 上的 cgo 依赖。
6. **RAG native 资源打包的时机**：**rev.4 改为「首次运行下载」**——RAG 回到首版范围，资源不进包，安装包保持 ~26MB，详见 §0.5.6。（rev.3 曾选 C「先出 RAG-off 的 Wails 包」；下载方案在同样不牺牲体积的前提下把 RAG 一并交付，故取代之。）原 rev.3 记述：先出 RAG-off 的 Wails 包。依据 §0.5.1：首版 main 不 import `knowledge`，`FileIndexer`/`KnowledgeHooks` 传 nil，RAG 关闭是既有一等公民状态；§6.1 全套移到 M+2。（A/B 两案留档：若产品侧要求 RAG 与瘦身同版出货，回到 A——先在 Electron 侧还清 P-1，再迁。）
7. **L3c 的 CGO 依赖是否接受为长期约束**：**接受，降级为长期观察项**（rev.3）。理由：Wails 在 mac/linux 上**自身就用 cgo**，"纯 Go 单文件随便交叉编译"这个属性迁移后本来就保不住（§0.5.1），所以 L3c 并没有额外拿走什么。仅在做 Windows 时重新评估——那里 Wails 是纯 Go 而 L3c 不是，账才算得清。

8. **（rev.4）Electron 何时删除**：**W1 三条 kill 检查全绿之后**（不是现在，也不是等 M+3）。目标是删除而非长期冻结；卡在验证之后，是为了不在 SSE 验证通过前失去唯一的出货通道。
9. **（rev.4，未决）模型与分词器托管在哪**：见 §0.5.8。

**首版唯一还开着的技术未知**：W1 的三条 kill 检查（SSE 字节级 / v3 mac 稳定度 / 导航收束）。三条全绿即进入执行，任一不过按 §15 的触发动作处理。

---

## 附录 A：桥接面测绘（renderer↔backend）

- 两个 contextBridge 全局：`window.workmaxLocal`（legacy 兼容）+ `window.desktopBridge`（typed facade，`version "1.0.0-alpha.7"`），见 `preload.ts` 的两处 `contextBridge.exposeInMainWorld`。
- 声明面：~23 typed 方法（5 命名空间 auth/history/agent/system/settings）+ 6 legacy 成员。**renderer 实际只用 ~13 个**。
- 传输分类（**实际使用**）：**4 个 IPC**（登录交易 begin/status/password/cancel；reveal 未用）vs **~11 个 HTTP**（3 raw fetch：auth-status/threads/messages；6 typed agent；2 typed settings），其中 **2 个 SSE 流式**（agent startTurn/resumeTurn）。
- SSE 全部封装在 preload：POST `text/event-stream` → `response.body.getReader()` → `AgentSSEParser` → 归一为 `AgentTurnEvent`（`text_delta`/`done`/`proxy_error`/`canceled`/`protocol_error`/`unknown`）→ 同步回调进 renderer。**renderer.js 零流式代码**。
- 死面（声明但未用，可丢）：整个 `system` 命名空间、`auth.status/userInfo/logout`、`history.listThreads/listMessages`、`revealDataDir`、`capabilities()`。
- 结论：迁移真正要碰的 renderer 代码集中在几个读桥接全局的 helper + `renderer.js` 自身的 `sidecarFetch`，面很小。

## 附录 B：Go sidecar 嵌入可行性

- HTTP server：`desktop.NewServer`，`net.Listen("tcp","127.0.0.1:0")` + gin，标准 `Serve`；SSE 因分钟级长连接**故意不设 WriteTimeout**（`NewServer` 内有注释）。进程内外无差别。
- **路由清单（25 条**，权威源 `route_policy.go` 的 `currentSidecarRoutePolicies`，由 `check-desktop-boundaries.mjs` 与 `desktop/contracts/desktop-boundaries.v0.json` 双向校验**）**：

  | 组 | 条数 | 备注 |
  |---|---|---|
  | health | 1 | `GET /health` |
  | auth | 8 | `status` / `userinfo` / `start`(OAuth) / `logout` + **login-transaction×4**（D3 要移出 HTTP 表） |
  | agent | 9 | `chat` · `turns/recoverable` · `turns/:uuid/replay` · `turns/:uuid/cancel` · `threads`(GET) · `threads/:uuid`(PUT) · `threads/:uuid/messages` · `threads/:uuid/files`(POST, **50MiB multipart**) · `skills/catalog` |
  | system | 5 | `network-state` · `log` · `diagnostics` · `server-version` · `trigger-sync` |
  | settings | 2 | `model-route` GET/PUT |

  **SSE 共 3 条**：`POST /agent/chat`、`POST /agent/turns/:uuid/replay`（共用 `server_agent_turn.go` 的流式 writer）、`GET /system/network-state`。
  > 旧版本此处写"27 条 / health×3 / agent SSE×4"——为 rev.1 的口径错误，已按 rev.2 校正。

- 强制独立进程点（全在 `server/cmd/workagent-desktop/main.go`，皆可跳过）：PID 锁（`acquireSidecarLock`，保留无害）、stdout 握手（`desktop.WriteHandshake`，**删**）、stdin 守护（`watchStdinShutdown`，EOF=父进程消失→关，**删**，换 Wails `OnBeforeClose`）、信号处理（保留）、token 经 env（换进程内变量）。**无 ppid/args[0] 检查**。
- 运行时依赖：SQLite（`glebarez/sqlite` + `modernc.org/sqlite`，含 sqlite-vec `vec0` 虚表，**纯 Go**）、纯 env 配置（不碰 cloud 的 viper/GraConf）、keychain（仅 macOS shell）、cloud HTTP client（纯 net/http）、sync/network watcher（goroutine）、local inference（net/http 调用户自配端点，**不 shell claude CLI**——契合 L2 SDK blocker 现状）。**无阻塞**。
- **CGO（rev.2 更正）**：rev.1 写的"无 CGO"**已失效**。`server/desktop/knowledge/*` 全部 `//go:build desktop && cgo`（ONNX Runtime，dlopen `libonnxruntime`），且被 `cmd/workagent-desktop/main.go` 无条件 import。实测：
  ```
  CGO_ENABLED=0 go build -tags desktop ./cmd/workagent-desktop
  → imports server/desktop/knowledge: build constraints exclude all Go files
  ```
  注意 `server/desktop` 包**本身**仍无 cgo——它用 `FileIndexer` 接口结构化解耦（`server.go` 有明确注释）。cgo 只在 `knowledge` 包与最终 `package main` 汇合。**抽 Bootstrap 时要保住这层解耦**（见 §10）。
- Build tag 分离干净：`server/main.go`（云）与 `server/cmd/workagent-desktop/main.go`（sidecar）是两个独立 `package main`；sidecar 只 import `server/desktop/*`，零 import cloud 包。

## 附录 C：安全/登录/打包要点

- **导航收束**（`main.ts` 的 window hardening + `security-helpers.ts`）：permission blanket-deny；`will-navigate`/`will-redirect` 经 `isURLWithinRendererRoute` 收束到 bundled route（`file:` 要求精确匹配 `.../renderer/en/desktop/index.html`，无 host/query/hash）；`setWindowOpenHandler` 全 deny + 走 SSRF-hardened `normalizeExternalHTTPURL`（拒一切 local/private/loopback）→ `shell.openExternal`。
- **IPC sender 校验**（`main.ts` 的 sender 断言）：`sender===mainWindow.webContents` + `senderFrame===mainFrame` + 可信 URL 三连；+ `requestSingleInstanceLock` 关掉"第二窗口第二 webContents"绕过。
- **token**：`randomBytes(32).base64url`，每次 spawn 新生；env→preload 闭包，renderer 触不到；loopback 端口本身可被本地其他进程连，**token 才是真保护**。
- **登录交易 Main-only 的威胁模型**：token 是认证任意 sidecar 操作的主密钥；若泄露给 renderer，XSS/被 compromis 的依赖可伪造任意 sidecar 请求。故把特权 auth 命名空间强制走 Main-process gate（`login-transaction.ts`），renderer 只能经一条 typed password 命令提交凭据、只观察 `{state,error}`；flow ID Main 侧生成、**不回传 IPC 结果**。凭据卫生激进（fresh copy + `finally` 置空 + 手写 JSON 解析拒重复键 + 4KiB 上限）。
  > **rev.2 补充（L3d）**：这个威胁模型原本隐含"没登录 ⇒ 没东西可偷"。L3d 之后不再成立——未登录用户走 `localSingleUserUID (1<<62)` 也能建 thread / chat / 传附件，所以 token 泄漏在**任何登录状态下**都意味着全部本地数据面可读写。见 §9 D4.1。
- **OAuth**：`dist/oauth-window.js` 是僵尸文件（`src/oauth-window.ts` 已删，但 **`electron-builder.yml` 的 `files:` 仍列着它**、`dist/` 里也仍有一份旧编译产物在被打包）；`validateOpenOAuthArgs` 孤儿。**现出货登录只有密码**。迁移时自然消失；若近期不迁，也值得单独清掉这两处残留。
- **打包**：仅 macOS（arm64/x64）；`hardenedRuntime:true`、`publish:null`；entitlements 仅 allow-jit/unsigned-exec-mem/dyld-env + network.client（**故意无 network.server**，loopback 由 sidecar 持）；`appId ai.workmax.desktop` 绑 Keychain。**无任何自动更新机制**。**`extraResources` 目前含 sidecar 二进制 / renderer / LICENSE / 三方许可，但不含任何 ONNX native 资源**——这是 §6.1 那一整节的由来。

---

## 附录 D：修订记录

| 版本 | 日期 | 变更 |
|---|---|---|
| rev.1 | 2026-08-08 | 首版评估 + Part B 落地方案；D1/D4/入口结构三项决策落定（v3 / A2 / 单入口双模式） |
| **rev.18** | **2026-08-08** | **代码/文档对照复审（§0.5.23）。** 修一个真实缺口：外链无人拦截，点击会把 app 导航到远程页且回不来——renderer 捕获阶段拦截 + Go 侧 SSRF 校验后交系统浏览器，15 条拒绝有测试，契约增至 **14 条保证**。删死代码 `RuntimeAPI`。对齐四处文档失真（D3 从未实现、路由 21→25、保证 11→14、"首版 RAG-off"已过时）。§0.5 小节按编号重排并新增 §0.5.0 状态索引 |
| rev.17 | 2026-08-08 | **新增 `--verify-app`**（§0.5.22）：第一次运行用户实际走的路径——未经修改的 `index.html` + 出货 shim + 真 sidecar + 真 webview。断言从反代侧做，因为为观测而改页面就等于不再测出货物。唯一必需请求 `/auth/status` 覆盖整条链；其余启动请求是会话依赖的可选项（首次跑因把它们列为必需而误红，已修正） |
| rev.16 | 2026-08-08 | **补齐 RAG on/off 基准，抓出两个缺陷。** 新增 §0.5.21：实测 RAG 增量 **+223 MB**（§5 写的是"+数十 MB"，差一个数量级）。① embedder 改为**懒加载**，资源在场但未使用的成本回到 32 MB/48 ms，与 RAG-off 相同；② 修复**关闭竞态**——后台索引 goroutine 与 ONNX 环境销毁并发导致 SIGSEGV（"回答刚出来就关窗即崩溃"），`Close` 改为停单→等待→销毁，超时则不销毁。另验证 `smoke-local.sh` 核心集对新二进制通过（含 `--check-pid-lock`），两个可选检查依赖云端会话 |
| rev.15 | 2026-08-08 | **首个打包产物 + 性能实测。** 新增 §0.5.20：`build-mac.sh` 按 Wails 布局重写并强制过检查器，arm64 `.app` 端到端可用。修复 `resolveResourcesDir()` 与下载目的地错位（打包后才显形）。实测替换 §5 估算：安装包 22 MB ✅、冷启动 ~48ms/~100ms ✅、**稳态内存 ~109–126 MB（§5 估的 40–80 MB 偏低约 50%）** ❌。记录一个流程错误：同机对照基线未在删除 Electron 前采集，比值暂无可引用实测 |
| rev.14 | 2026-08-08 | **W4 起步。** 新增 §0.5.19：entitlements 从 4 条减到 1 条（去掉 V8/dyld 三条，`disable-library-validation` 待 RAG 里程碑再加，缺席理由写进 plist）；`inspect-mac-package.sh` 按 Wails 布局重写并配 16 项负向测试，补上 `notarize-mac.sh` 的失败关闭，两者已接回 CI。修掉自己写的一条静默失效检查（`grep -E` 不支持负向先行断言 + stderr 被丢），并把两处重叠检查收敛到一处 |
| rev.13 | 2026-08-08 | **W2 安全收尾。** 新增 §0.5.18：CSP 去掉 `'unsafe-inline'`（出货 renderer 实测不需要，harness 的 `<style>` 一并外置）、DevTools 改为仅 env 显式开启且应用内无法开启、单实例两层实测。契约增至 12 条保证（新增 `devtools-off-by-default`，`containment-headers` 加缺席断言），`uiserver_test.go` 独立复核 |
| rev.12 | 2026-08-08 | **文档与许可审计链收口。** `desktop/README.md` 逐节重写；顶层 README / RELEASING / THIRD_PARTY_NOTICES 同步；`license-audit.sh` 改指 renderer 的构建期 npm 树，并新增 `desktop/wails` 模块的 go-licenses 审计（此前无任何一遍覆盖真正出货的那个模块）。`make bootstrap` / `make test-shell` / `make build-desktop` 取代原 Electron 目标 |
| rev.11 | 2026-08-08 | **修好行为闸门，并因此抓到一个真实缺陷。** 新增 §0.5.17。`check-bundled-renderer-behavior.mjs` 的假 DOM 改为从 `index.html` 派生（元素集合、tag 名、初始 hidden 全部读真实标记），结构上杜绝再次漂移；补齐 22 个 agent mock 的 `uploadThreadFile`。修复 L3b 缺陷：`state.pendingFiles` 在 `startTurn` 读取前被清空，导致附件 `fileIDs` 恒为空、文件静默丢失；已加回归测试并做负向验证 |
| rev.10 | 2026-08-08 | **Electron 已退役。** 新增 §0.5.16。删 `desktop/electron/` + `cmd/workagent-desktop/` + electron-builder 专用打包脚本；`desktop-bridge.ts` 迁入 `desktop/renderer/src/` 并与 Node 类型解耦；契约 checker 的每条 Electron 断言换成同职能的 Wails 断言（而非删除），边界扫描加扫 `.go`；契约 `ipc` 段更名 `privilegedGate`；`notarize-mac.sh` 去耦 electron-builder 布局并对缺失 bundle 检查器**失败关闭**。`dev.sh` 重写为构建并运行单一二进制 |
| rev.9 | 2026-08-08 | **Electron 退役的前置被找出来了：契约 checker 迁移。** 新增 §0.5.15。checker 断言的是 Electron 源码字符串，直删会一并删掉 §14 的 parity 闸门；且 `preload.ts` import `desktop-bridge.ts`，所以"先挪源文件"也不安全——删除是原子操作。做法改为增量：契约新增 `wailsShell` 段（11 条保证 + 4 个登录动词，每条写明"为什么重要 + 哪个文件必须携带它"），checker 数据驱动地同时校验两套 shell，退役时只删 Electron 那一半。闸门经三次负向测试验证 |
| rev.8 | 2026-08-08 | **W3 renderer shim 落地并通过验收。** 新增 §0.5.14。`desktop-bridge.ts` 生成为经典脚本供 renderer 复用（非手抄），`shim.js` 提供同源 request + 移植的 SSE 消费循环 + `/login/` 网关四动词，Electron 下自动退场。`--verify-shim` 加载真实 renderer 目录在 WKWebView 内验收：13 个方法齐备、token 未入页、完整 turn 逐字节一致，连续三次通过。发现 `check-bundled-renderer-behavior.mjs` 在 main 上已是红的（既有问题） |
| rev.7 | 2026-08-08 | **更正 rev.6 的一处错误结论 + 修一个我引入的安全回归。** ① §0.5.11 反转：Wails 绑定在 loopback origin 上**不可用**（runtime.js 把调用 POST 到 `location.origin + "/wails/runtime"`，那是我们的 UI server）——rev.6 只看了对象存在就下结论，补测 `Call.ByName` 为 `undefined`。D3 改倾向"UI server 上的能力受保护端点 + 进程内调 `loginCoordinator`"。② 新增 §0.5.13：同源反代替调用方注入 token，导致 UI 端口无门禁 ⇒ 任何本地进程扫到即可全权驱动 sidecar；已用**能力路径**修复并加回归测试。③ 反代封禁 login-transaction 路由 + 拒绝非规范路径（§0.5.12） |
| rev.6 | 2026-08-08 | **W1 三条 kill 检查全绿。** 新增 §0.5.10（kill ③ 导航收束）与 §0.5.11（W2 绑定可用性）。核心发现：mac 上 Wails v3 **没有可取消的导航钩子**（`decidePolicyForNavigationAction` 仅 iOS 实现），Electron 的阻止式收束无法复刻——§11 标注的"最大残余风险"就此有了答案。补偿控制实测全部生效（CSP 拦异源/404/路径穿越/`window.open` 返回 null），且 CSP 由 Go 以响应头下发，强于 Electron 的 meta 标签。现有 renderer 已无内联脚本/样式，可直接收紧。另确认 loopback HTTP 加载下 Wails 运行时仍注入 ⇒ W2 的 `LoginAPI` 绑定可用。生产 `runDesktop` 已切到 `uiServer`；`RuntimeAPI` 不再携带 token（D4 反转落地） |
| rev.5 | 2026-08-08 | **kill ① PASS，迁移继续。** 新增 §0.5.9 实测报告。两项实测推翻既有设计：① sidecar 对任何 `Origin` 403 且无 CORS 预检 ⇒ D2 的跨源直连不可行（Electron 能用只因 Chromium 对 `file://` 省略 Origin，是隐式依赖）；② `wails://` 自定义 scheme 丢 POST body（WebKit `WKURLSchemeHandler`）⇒ `embed.FS`+自定义 scheme 方案作废。采纳同源形态：renderer 经 loopback HTTP 加载、API 由 Go 在同源下反代。**D4 因此反转**——token 不再下发 JS，机密性恢复到 Electron 强度。kill ② 一并通过；kill ③ 导航收束改为"UI listener 只服 embed.FS" |
| rev.4 | 2026-08-08 | **W1 开工。** RAG 由「首版排除」改为**首次运行下载**（§0.5.6：`<DataDir>/resources` + SHA-256 钉死 + `.part` 续传；连带 entitlements 要**加** `disable-library-validation`，推翻 rev.3 的「纯减法」）；Electron 由「冻结」改为**删除**，执行点仍在 W1 kill 检查之后（§18-8）。新增 §0.5.7 已落地清单与 §0.5.8 剩余项。记录一个真实缺陷：`Cancel`/`Shutdown` 共用 `sync.Once` 导致 teardown 不执行、RAG-on 退出 SIGABRT，已修并加回归测试 |
| rev.3 | 2026-08-08 | **Wails 转正为主线，决策全部落定。** 新增 §0.5 最快路径（核心杠杆：首版 main 不 import `knowledge` ⇒ 我们这边零 cgo，§6.1 整节移出关键路径；且 Wails 自身在 mac/linux 就用 cgo，故"构建带 CGO"是零增量）；首版范围 In/Out + 四周排期 + §0.5.4 三条工作纪律（Electron shell 冻结 / renderer 共享真源 / RAG 在 `--serve-only` 继续开发）。§13 重排为 W1–W4 关键路径 + M+1/M+2/M+3 后续里程碑；§17 首版 **~4 周**（总量不变，是排期决策非估时修正）；§16 明确"主线≠出货通道"、Intel Mac cutover 须等 M+1；§15 三条 rev.2 风险标注"首版不适用"并新增预期落差风险；§18 由待决策改为决策台账（1=采纳、5=推迟、6=C 方案、7=接受并降级） |
| rev.2 | 2026-08-08 | 对齐 L3b/L3c/L3d 后的代码基线。**事实更正**：① "纯 Go、无 CGO" 失效（§1、附录 B，附实测命令）；② 路由 27→**25**，分组口径重写（附录 B）；③ 全文行号引用改为**函数名锚点**（rev.1 的行号已整体漂移，`main.go` 最甚）。**新增**：§6.1 CGO/ONNX 打包增量、§9 D4.1 残余面、§10 Bootstrap 的 cgo 依赖方向与 ONNX 析构生命周期、§13 P-1 阶段 + cgo/ONNX kill 检查 + P2-native、§15 三条新风险、§17 工时 3–6 周 → **4–7.5 周**、§18 新增第 6/7 项待决策、附录 D 本表 |

> **维护约定**：本文的代码引用一律用函数/常量名，不用行号。若必须指向大段代码，给"约 N 行"量级而非精确区间。路由条数以 `route_policy.go` 的 `currentSidecarRoutePolicies` 为准，改动时同步本文附录 B 的表。

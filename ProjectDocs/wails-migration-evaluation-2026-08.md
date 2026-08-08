# WorkMax Desktop → Wails 迁移评估

| Field | Value |
|---|---|
| **Document** | Electron → Wails shell 迁移可行性与方案评估 |
| **Date** | 2026-08-08 |
| **Status** | Evaluation / recommendation（待决策，尚未实施） |
| **Related** | `oss-local-desktop-runtime-mode-2026-08.md`（本地 Desktop 运行时模式）· `desktop/README.md`（当前架构）· `desktop/electron/src/*`（现 shell）· `server/desktop/*`、`server/cmd/workagent-desktop/main.go`（Go sidecar） |
| **调查方法** | 三路并行代码测绘：① renderer↔backend 桥接面 ② Go sidecar 进程内嵌入可行性 ③ 安全/登录/打包链 |

---

## 0. TL;DR（结论先行）

**技术上强烈可行，且比绝大多数 Electron→X 迁移都干净**：核心 Go 后端可直接进程内嵌入，renderer 契约几乎不动。真实成本不在"搬代码"，而在"重做安全信任边界 + 重建打包链"。

**前置标定（最重要的一句）**：Wails 只解决 **shell 层性能**（内存/体积/冷启动），**不解决旗舰脑图画布的性能**——画布卡顿是 renderer 渲染层问题（DOM/SVG vs Canvas/WebGL），换 shell 一点都不改变它。

- 痛点是 **app 太重（内存/安装包/启动慢）** → Wails 是对的、干净的根治方案，现在 `0.1.0-p1-ea` 阶段是迁移最便宜的窗口。
- 痛点是 **画布/交互卡顿** → 别迁 Wails，先把画布搬到 `<canvas>`/WebGL，那跟 shell 无关。（具体画布库选型与长历史虚拟化见 §1.5.2，无论迁不迁都适用。）

**推荐架构**：Option A（保留嵌入式 loopback HTTP server），不是 Option B（路由全改 Wails 绑定）。理由见 §3。

---

## 1. 为什么这次迁移"异常干净"

| 维度 | 发现 | 对迁移的意义 |
|---|---|---|
| **Go sidecar 可进程内嵌入** | `//go:build desktop` 与云服务干净分离（147 文件带 tag，sidecar 零依赖 cloud 包）；纯 Go、**无 CGO**；HTTP server 就是标准 `net.Listen("tcp","127.0.0.1:0")`+gin（`server/desktop/server.go:144`） | 没有架构性障碍，Wails binary 直接 import `server/desktop` 跑同样代码 |
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

- Agent 流式输出已是 **SSE**：`Accept: text/event-stream` + `ReadableStream.getReader()`，全在 preload 层（`desktop/electron/src/preload.ts:295-306, 460-479, 593-714`），renderer 侧零流式代码。
- IPC（`ipcMain.handle`）只用于**登录交易**（begin/status/password/cancel）+ reveal-data-dir，刻意保持 Main-only 以护住 token 与凭据。
- 安全周界：loopback + `X-Local-Token`（每次 spawn 新生 32 字节 CSPRNG）+ Origin 拒绝 + 每 route 的 body/content-type/size 策略（`server/desktop/route_policy.go:333-387`）。

---

## 3. 架构选择：Option A vs Option B

| | **Option A：保留嵌入式 loopback HTTP server**（推荐） | Option B：路由全部改 Wails Go 绑定 |
|---|---|---|
| Renderer 改动 | **零**（仍 `fetch('http://127.0.0.1:port/...')`） | 重写 27 路由的调用层 |
| 27 路由 + route_policy 安全周界 | **全保留** | 重写为绑定，周界失效 |
| **SSE 流式**（`/agent/chat`、`/agent/turns/:uuid/replay`、`/system/network-state`） | **字节级不变**，preload 解析器照常工作 | 改成 Go `EventsEmit` + renderer `EventsOn`，**重铺流式管线 = 最大风险** |
| Go 侧工作量 | **纯减法**：删 `WriteHandshake`/`watchStdinShutdown`，其余复用 | 重写 ~27 handler 的传输 + 流式 |
| 安全周界 | token + Origin + body 策略**保留** | 进程内调用，周界失效（损失纵深防御） |

**Option A 明显更优**：减法、renderer 不动、旗舰 SSE 路径零风险、安全周界存活，且是 Option B 的严格子集——以后想逐命名空间迁绑定也行。唯一例外：若硬性要求"完全不开 loopback socket"（例如怕 Windows 防火墙弹窗），才被迫走 B。

> Option A 下 Go 侧要做的，本质是：新建薄 Wails boot 路径——`desktop.NewServer(cfg)` 后读 `srv.Port()`，跳过 `WriteHandshake` 与 `watchStdinShutdown`，token 走进程内变量；`cmd/workagent-desktop/main.go:95-316` 的 wiring（OpenLocalDB、keychain、proxy、syncers、network watcher、model settings、local inference、ServerConfig）**逐字复用**。

---

## 4. 真正的成本：安全信任边界（要重设计的部分）

Electron 给了三样 Wails 没有的东西：**硬件级 IPC sender 身份**、**隔离的 preload 世界**、**统一的导航 hook**。但关键标定——**在 Option A 下，这个成本被显著缩小**：

| 安全件 | 现状（Electron） | Option A 下 | 实成本 |
|---|---|---|---|
| **token 隔离**（renderer 读不到 X-Local-Token） | preload 闭包持有（`preload.ts:55-57,74`） | loopback + token 周界**原样存活**；token 放 Go 内存 | **低**——财产保住 |
| **登录交易 Main-only** | `ipcMain.handle` + sender 校验（`main.ts:408-455, 509-529`） | 变成 Go-only 函数，flow ID 留在 Go | **低且更好** |
| **导航/弹窗/权限收束** | `will-navigate`/`setWindowOpenHandler`/`setPermissionRequestHandler`（`main.ts:312-348`） | Wails 的 `OnBeforeRequest`/`OnNavigation` 更薄且**按 webview 引擎分平台**；SSRF-hardened 外链逻辑（`security-helpers.ts:176-222`）可作纯 Go 搬过去 | **中**——要重写 + 重测 |
| **"只有我的 bundled 页面能调特权"** | `sender===mainWindow.webContents` + mainFrame 校验 | 无 sender 概念；靠"只加载 bundled 资产 + 单实例"近似 | **中**——单窗口 bundled app 下威胁面小，但要重新论证 |

### 4.1 必须在早期决定的设计岔路：token 隔离 ↔ SSE 流式

Option A 下两者有张力：

- 要 **token 不暴露给 JS** → 流式得走 Wails 事件（Option B 的成本渗进来）；
- 要 **SSE 直连不变** → 得把 token 给受信的 bundled webview。

**推荐后者（详见 Part B §9 决策 D2/D3/D4）**：把 webview 当作可信（只加载 bundled 资产、单源单窗口），token 暴露给 JS，直接 fetch loopback 含 SSE。这把"renderer 物理上读不到 token"弱化为"renderer 能读 token 但 webview 受信"。token 的本职——挡住**其他本地进程**连 loopback 端口——依然成立。是**有界削弱**而非崩溃。

**但暴露 token 会连带把 4 条 login-transaction HTTP 路由重新暴露给 renderer**（今天靠 preload 屏蔽 + IPC gate 让它够不到；服务端本身照常接受，见 `route_policy.go:91-94`）。为保住"凭据不经 renderer 可直达"的属性，Part B 采 **D3**：这 4 条路由在嵌入式 server 里**不注册**，登录改走 Wails Go 绑定直接调 `loginCoordinator`（进程内，凭据不过 HTTP）——这其实比今天的 Main-only 更严。token-secrecy 若为硬要求，则退到 D2-alt（token 留 Go、经绑定代理 fetch），代价是 SSE 要改走 Wails 事件。

---

## 5. 性能收益（具体，且标定边界）

| 指标 | Electron（现） | Wails Option A | 来源 |
|---|---|---|---|
| 基础内存 | ~150–250MB | ~40–80MB | 不再捆绑 Chromium |
| 安装包 | ~100MB+ | ~15–25MB | 系统 webview（WKWebView/WebView2） |
| 冷启动 | 拉 Chromium + spawn sidecar + 5s 握手 | 单进程直起（删掉 spawn+握手） | 进程内嵌入的直接收益 |
| **脑图画布帧率** | 不变 | **不变** | ⚠️ 两者都是 webview，画布性能是 renderer 层问题 |

> 数值为工程估算范围，非实测；P0 spike 应顺便落一个 cold-start/RAM 基准对照。

---

## 6. 分发与打包

- **可复用**：`notarize-mac.sh` 里的 `codesign`/`notarytool`/`stapler`/`spctl` 是 shell 管道，换路径输入即可；**`appId: ai.workmax.desktop` 必须保留**（绑 Keychain service，`server/desktop/cloud_proxy/keychain.go`）。
- **要改**：entitlements（去掉 Electron 专属的 JIT/unsigned-exec-mem，换更小的 webview entitlements）；`build-mac.sh` 的 preflight 校验器按新 bundle 布局重写；bundle 布局变（无 asar）。
- **净新增**：Windows/Linux 打包——今天本来就没有，Wails 的 Win/Linux 比 Electron 更省事。
- **自动更新**：打平——现在就没有（`publish:null`，无 electron-updater）；Wails 不更差；将来要做得自己写（GitHub releases 校验 + 签名），两边都一样。
- **已知跨平台债（既有，非 Wails 引入）**：keychain 只实现了 macOS（shell `security` CLI，`keychain_darwin.go`；`keychain_other.go` 是报错 stub）。Win/Linux 上要做 Credential Manager/libsecret——无论 Electron 还是 Wails 都得做。

---

## 7. 成本量级 + 阶段化

**约 3–6 周（一个专注工程师）**，主要被"安全重证 + 打包重建 + WKWebView 怪癖"吃掉，**不是被代码量吃掉**。

| 阶段 | 内容 | 目的 |
|---|---|---|
| **P0 Spike（几天）** | 最小 Wails binary：import `server/desktop`、起嵌入式 server、加载 bundled renderer、fetch 跑通 **+ WKWebView 下 SSE 字节级验证** | 砍掉最大未知（WKWebView fetch loopback / SSE / 资产收束） |
| **P1 安全** | token 代理绑定（或决策暴露 token）、登录交易 Go-only、导航收束、单实例 | 重建信任边界 |
| **P2 打包** | 新 entitlements + preflight 重写 + 公证流跑通；保留 `appId` | 出可分发 macOS 包 |
| 切换 | Electron 作为出货通道直到 P2 全绿，再 cut over | 不中断现网 |

**关键去风险点 = P0 spike 里的「WKWebView 下 SSE 是否字节级跑通」**——这是整个迁移唯一可能让你回头的技术未知。先花几天验证它，再做最终决策。

---

## 8. 最终建议 + 决策条件

**推荐：迁移到 Wails Option A**——后端可嵌入已被代码证实，迁移是一次罕见的干净窗口。

**满足任一条件，建议先不迁**：

1. 真实性能痛是**画布/交互** → 先做 Canvas/WebGL，那与 shell 无关。
2. 现阶段**出货速度 / 赚钱功能**优先级高于瘦身 → 迁移是纯基建，不推进旗舰；内存/体积现在没用户投诉就先不动。
3. **需要 modal 内嵌 OAuth webview** → 那段已死代码若要复活，Electron 的 BrowserWindow 比 Wails（只能开系统浏览器 + loopback 回调）省事。

**满足下面条件则趁早迁**：

- 痛点是 app 太重/启动太慢、或有 Windows/Linux 计划（Wails 让跨平台更便宜）、且现在处于 p1-ea（用户少、renderer 还没膨胀）。**越晚迁移，要重证的安全面和 renderer 契约越大。**

---

---

# Part B — 落地方案（Option A，假定采纳迁移）

> 以下是把 §8 推荐落成可执行计划的细化。状态仍为"待决策"；本部分默认"决定迁移到 Wails Option A"为前提。

## §9 目标拓扑与关键决策

```
┌──────────────────────── 单一 Wails 二进制 (workmax-desktop) ─────────────────────────┐
│                                                                                        │
│   Go 主进程                                                                            │
│   ┌────────────────────────────────────────────────────────┐                           │
│   │ desktop.Bootstrap(cfg)  ← 抽自 cmd/workagent-desktop     │  SQLite (WAL, 纯 Go)     │
│   │   OpenLocalDB · keychain · tokenStore · cloudClient     │                           │
│   │   proxy · loginCoordinator · syncers · networkWatcher   │                           │
│   │   modelSettings · localInference · localFiles           │                           │
│   │   → desktop.NewServer(ServerConfig{...})                │                           │
│   │   → 绑 127.0.0.1:0 （含 23 条路由，不含 4 条 login HTTP）│                           │
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
| **D1 shell 版本** | **Wails v2**（stable）起步，跟踪 v3 | v3 仍 beta，Windows webview 有崩溃（issue #4559）；WorkMax 单窗口，v2 够用且稳。v3 转正后再评估 |
| **D2 传输** | token 暴露给受信 bundled webview，renderer 直接 fetch loopback（含 SSE） | SSE 字节级不变；契约零改动。代价见 D4 |
| **D2-alt**（退路） | token 留 Go，renderer 经 Wails 绑定 `Fetch()` 代理 | 保住 token-secrecy；但 3 条 SSE 流要改走 `EventsEmit`（Option B 成本渗入）。仅当 secrecy 为硬要求 |
| **D3 登录交易** | 4 条 `/auth/login-transaction*` **不在嵌入式 server 注册**；登录走 Wails Go 绑定直调 `loginCoordinator` | 忠实移植"Main-only"且更严（凭据不过 HTTP）；D2 下必须，否则 token 暴露=login 路由暴露 |
| **D4 token secrecy** | 由"renderer 物理读不到"降级为"webview 受信（bundle-only / 单源 / 单实例）" | 有界削弱；缓解：严格 CSP、只服 bundled 资产、禁远端内容 |
| **D5 死面** | 丢弃 `system.*`/`history.*` typed/`revealDataDir`/`capabilities`，保留 raw fetch | renderer 本就没用；少移植、少攻击面 |

## §10 Wails 应用结构与启动装配

**目录**：新建 `desktop/wails/`（Go package main，`-tags desktop`）。保留 `server/cmd/workagent-desktop/` 作独立/smoke 用——**两个入口共享同一 Bootstrap**。

**关键重构：抽出共享 Bootstrap**。把 `server/cmd/workagent-desktop/main.go:108-316`（dataDir 锁 → OpenLocalDB → token → OAuth/keychain/tokenStore/cloudClient/proxy/loginCoordinator → networkWatcher/syncers/rendererLogger/modelSettings/localInference/localFiles → `desktop.NewServer(ServerConfig{...})`）抽成：

```go
// server/desktop/bootstrap.go  (新，-tags desktop)
package desktop

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
    cancel context.CancelFunc
}

// Bootstrap 跑通所有 sidecar wiring 并起好嵌入式 HTTP server（不含 login-transaction HTTP 路由）。
// 调用方决定如何暴露 port/token、何时 Shutdown。
func Bootstrap(cfg BootstrapConfig) (*Boot, error) { /* = main.go:108-316 */ }
func (b *Boot) Shutdown(ctx context.Context) error { /* = main.go:368-413 有序关闭 */ }
```

两个入口都调它，wiring 永不漂移：
- `cmd/workagent-desktop/main.go`：保留 stdout 握手 + stdin 守护 + PID 锁（独立跑/smoke 需要）。
- `desktop/wails/main.go`：**删** `WriteHandshake`/`watchStdinShutdown`；**保留** `acquireSidecarLock`（无害）+ 信号处理（或换 Wails shutdown hook）。

**Wails main 骨架**：

```go
//go:build desktop
package main

func main() {
    boot, err := desktop.Bootstrap(desktop.BootstrapConfig{
        DropLoginTransactionHTTP: true,          // D3：login 走绑定，不进 HTTP 表
    })
    if err != nil { log.Fatalf("bootstrap: %v", err) }
    defer boot.Shutdown(context.Background())

    app := app.New(
        app.Bind(&LoginAPI{coord: boot.LoginCoord}),           // D3：直调 coordinator
        app.Bind(&RuntimeAPI{port: boot.Server.Port(), token: boot.LocalToken}), // 给 webview 坐标
        // 资产：embed.FS 只暴露 renderer/en/desktop（bundle-only → 无别处可导航）
    )
    app.OnBeforeClose(func() { boot.Shutdown(ctx) })
    app.Run()
}
```

**生命周期**：Wails `OnBeforeClose`/shutdown → `boot.cancel()` → `srv.Shutdown(ctx)` → syncers Drain → DB close，复刻 `main.go:368-413` 的有序关闭（messages/threads Drain 与 SQLite-WAL 竞态的兜底不变）。

## §11 安全重证（逐件）

| Electron 件 | Wails 重证实现 | 残余风险 |
|---|---|---|
| 导航/弹窗/权限收束 | ① 只服 `embed.FS` bundled 资产 → webview **无别处可导航**；② 外链拦截→`browser.OpenURL`，前置 SSRF-hardened `normalizeExternalHTTPURL`（`security-helpers.ts:176-222` 移植成纯 Go）；③ bundled 资产加严格 CSP | Wails 导航 hook 比 Electron 薄且按 webview 引擎分平台——**P0 必须验证所选 Wails 版本+平台的 hook 能力**（最大残余风险） |
| token 隔离 | D2：token 经 `RuntimeAPI` 绑定下发 webview；loopback 仍由 `RequireLocalToken` 中间件保护，挡住**其他本地进程** | D4：renderer 可读 token（webview 受信兜底） |
| 登录交易 Main-only | D3：login-transaction 不进 HTTP 表；`LoginAPI` 绑定直调 `loginCoordinator`，flow ID 留 Go；凭据卫生（fresh copy + finally 置空）原样移植 | 低——比现状更严 |
| "只有我的页能调特权" | bundle-only 资产 + 单实例 + token 周界共同近似 | 无 `mainFrame` 模拟；单窗口 bundled app 下威胁面小，但需论证并存档 |
| 单实例 | Wails 单实例锁；或保留 `acquireSidecarLock` + 平台 named mutex | 低 |

> **token-secrecy 硬要求场景**：退到 D2-alt（token 留 Go、`Fetch()` 绑定代理），login 路由也可继续注册（代理屏蔽）。代价：3 条 SSE 改 `EventsEmit`——属 Option B 成本，需单列工时。

## §12 Renderer 改动（具体）

Wails **无 preload 世界**，故 `desktop/electron/src/preload.ts`（SSE 解析器 `593-714`、fetch 包装、typed bridge、login IPC）需整体重新落地为 **renderer 侧 JS shim**，随 bundled 资产下发，暴露同样的 `window.workmaxLocal` / `window.desktopBridge` 形状。契约不变 → `renderer.js` 几乎不动。

| 现状（preload） | Wails 下落点 | 改动 |
|---|---|---|
| `sidecarFetch`（注入 token） | renderer shim：从 `RuntimeAPI` 拿 port/token 后直接 `fetch(loopback)` | 小——逻辑不变，token 来源换 |
| SSE parser + `getReader` | renderer shim 内（plain JS fetch + ReadableStream） | 移植，**WKWebView 下 ReadableStream 字节级验证=P0 kill 检查** |
| typed `desktopBridge`（auth/agent/...） | renderer shim 同形重建；底层从 fetch 改直连 loopback | 中——机械移植，契约不变 |
| login IPC（begin/status/password/cancel） | 改调 Wails `LoginAPI` 绑定 | 小 |
| 死面（system/history/reveal/capabilities） | 不重建（D5） | 减负 |

`renderer.js` 实际改动集中在 6 个 helper（`769-843`）+ `sidecarFetch`（`993`）的**来源重指向**，业务逻辑零改。

## §13 任务分解

| 阶段 | 任务 | 关键文件 | 完成标准 |
|---|---|---|---|
| **P0 Spike** | 抽 `Bootstrap`；最小 Wails binary 起嵌入式 server；WKWebView 加载 bundled renderer；fetch `/health` 跑通 | `server/desktop/bootstrap.go`(新)、`desktop/wails/main.go`(新) | 单进程起，`/health` 200 |
| **P0 kill 检查** | WKWebView 下 `/agent/chat` SSE 字节级跑通（ReadableStream + parser 移植） | renderer shim SSE | 完整一轮 text_delta→done 到达；**不过则终止迁移** |
| **P0** | 导航收束可行性：embed.FS bundle-only + 外链 OpenURL + CSP | `desktop/wails/` | 外链走系统浏览器，无任意导航 |
| **P1 安全** | `LoginAPI` 绑定 + `DropLoginTransactionHTTP`；`RuntimeAPI` token 下发；单实例 | `bootstrap.go`、`desktop/wails/` | login 全流程过；token 不入日志 |
| **P1 renderer shim** | 移植 SSE parser + typed bridge + fetch 包装；renderer helper 重指向 | `renderer/en/desktop/shim.js`(新) | 与 Electron 版行为对等（见 §14） |
| **P2 打包** | entitlements 重写（去 JIT/unsigned-exec-mem）；preflight 校验器按新 bundle 重写；公证流跑通；**保留 `appId ai.workmax.desktop`** | `desktop/wails/build/`、`scripts/` | Developer ID 签名 + notarytool + stapler + spctl 过 |
| **P2 资产** | `go-licenses` 重跑；THIRD_PARTY 更新（去 Electron/Chromium，加 Wails/webview 依赖） | `scripts/` | 许可清单完整 |

## §14 验证与对等（parity）

- **契约对等**：`desktop/contracts/desktop-boundaries.v0.json` 是 renderer↔后端合同的真源——Wails 版必须满足同一边界校验。把 `scripts/check-desktop-boundaries.mjs` 复用到 Wails 构建。
- **smoke 复用**：`desktop/scripts/smoke-local.sh`（`--check-pid-lock`/`--sidecar-binary`）和 packaged smoke renderer reporter（`main.ts:180-310` 的逻辑）重定向到 Wails 包，验 cached thread/message 可见、local-token 拒绝、diagnostics ok。
- **性能基准**（同机对照）：冷启动时间、稳态 RSS、安装包大小——**落数字**，验证 §5 的估算区间。
- **安全对等清单**：逐条列 Electron 保证（§附录 C），标 Wails 如何满足；残余项显式标红供评审。

## §15 风险登记 + 终止条件

| 风险 | 等级 | 缓解 | 触发动作 |
|---|---|---|---|
| WKWebView 下 SSE 不稳/不支持所需 ReadableStream 行为 | **高** | P0 第一周验证 | **不过→终止迁移，留 Electron** |
| 导航收束在 WKWebView/WebView2/webkitgtk 上不可达 Electron 等强度 | 中-高 | embed.FS bundle-only + CSP + 外链拦截；P0 验证 | 强度不足→评估 D2-alt 或保留 Electron |
| macOS 公证对非 Electron bundle 的差异（无 asar、WKWebView 框架） | 中 | 复用 notarize-mac.sh 的 codesign/notarytool/stapler； entitlements 重写 | 打通即解 |
| renderer shim 移植 bug（SSE parser/typed bridge 行为偏移） | 中 | 契约对等测试 + smoke | 回归测试兜底 |
| Wails v2 单窗口限制（未来要多窗口/modal OAuth） | 低 | 现在单窗口够用；多窗口需求出现时评估 v3 | 届时再议 |
| keychain 仅 macOS（Win/Linux 缺） | 既有债 | 与 shell 无关；跨平台时补 Credential Manager/libsecret | 不阻塞 mac 迁移 |

**终止条件**：P0 的 SSE kill 检查或导航收束验证失败 → 立即停止，保持 Electron 出货。

## §16 切换与回滚

- Electron 作为**唯一出货通道**直到 Wails 达 §14 全部对等 + §15 高风险清零 + 签名公证通过。
- 切换门槛：契约对等 ✓、性能基准达标 ✓、安全对等清单评审通过 ✓、至少 1 个完整 EA 构建签名公证 ✓。
- 回滚：Electron release 通道保留 N 周（建议 ≥2 个迭代）热备；Wails 出现回归即切回。

## §17 工时分解（单专注工程师）

| 阶段 | 估时 | 占比主因 |
|---|---|---|
| P0 Spike（Bootstrap 抽取 + 最小 binary + SSE kill 检查 + 导航验证） | 3–5 天 | 去风险，非代码量 |
| P1 安全（LoginAPI/RuntimeAPI/单实例 + 导航收束落地） | 1–1.5 周 | 信任边界重证 |
| P1 renderer shim（SSE parser + typed bridge 移植 + helper 重指向） | 1 周 | 机械但需对等测试 |
| P2 打包（entitlements/preflight/公证/许可） | 1 周 | bundle 布局 + 签名 |
| 对等测试 + 性能基准 + 缓冲 | 0.5–1 周 | parity |
| **合计** | **~3–6 周** | 被安全+打包+WKWebView 怪癖主导，非代码量 |

> 若走 D2-alt（token-secrecy 硬要求），SSE 改 `EventsEmit` 额外 +0.5–1 周。

## §18 待决策问题（需你拍板）

1. **是否采纳迁移**？（前提：痛点是 app 重，不是画布卡——见 §0 标定）
2. **Wails 版本**：v2（推荐，稳）起步 vs 直接 v3（beta）？
3. **D4 token 暴露**是否可接受？（不可接受→走 D2-alt，+工时）
4. **`cmd/workagent-desktop` 是否保留**为独立/smoke 二进制？（推荐保留，零额外成本）
5. **Windows 目标时间**？决定 keychain 跨平台债何时还（与迁移正交，但影响排序）。

---

## 附录 A：桥接面测绘（renderer↔backend）

- 两个 contextBridge 全局：`window.workmaxLocal`（legacy 兼容）+ `window.desktopBridge`（typed facade，`version "1.0.0-alpha.7"`），`preload.ts:204-205`。
- 声明面：~23 typed 方法（5 命名空间 auth/history/agent/system/settings）+ 6 legacy 成员。**renderer 实际只用 ~13 个**。
- 传输分类（**实际使用**）：**4 个 IPC**（登录交易 begin/status/password/cancel；reveal 未用）vs **~11 个 HTTP**（3 raw fetch：auth-status/threads/messages；6 typed agent；2 typed settings），其中 **2 个 SSE 流式**（agent startTurn/resumeTurn）。
- SSE 全部封装在 preload：POST `text/event-stream` → `response.body.getReader()` → `AgentSSEParser`（`preload.ts:593-714`）→ 归一为 `AgentTurnEvent`（`text_delta`/`done`/`proxy_error`/`canceled`/`protocol_error`/`unknown`）→ 同步回调进 renderer。**renderer.js 零流式代码**。
- 死面（声明但未用，可丢）：整个 `system` 命名空间、`auth.status/userInfo/logout`、`history.listThreads/listMessages`、`revealDataDir`、`capabilities()`。
- 结论：迁移真正要碰的 renderer 代码集中在 6 个 helper（`renderer.js:769-843`）+ `sidecarFetch`（`renderer.js:993`），面很小。

## 附录 B：Go sidecar 嵌入可行性

- HTTP server：`server/desktop/server.go:136-201`，`net.Listen("tcp","127.0.0.1:0")` + gin，标准 `Serve`；SSE 因分钟级长连接**故意不设 WriteTimeout**（`server.go:182-184`）。进程内外无差别。
- 路由清单（27，权威源 `server/desktop/route_policy.go:86-112`，镜像于 `desktop/contracts/desktop-boundaries.v0.json`）：health×3、auth×8（含 OAuth `/auth/start` 与密码登录交易×4）、**agent SSE×4**（`/agent/chat`、`/agent/turns/:uuid/replay`、cancel、recoverable）、threads/files×4（含 50MiB multipart 上传）、skills/settings×3、system×5（含 `/system/network-state` SSE）。
- 强制独立进程点（全在 `server/cmd/workagent-desktop/main.go`，皆可跳过）：PID 锁（`acquireSidecarLock:570-608`，保留无害）、stdout 握手（`desktop/handshake.go:35-57`，**删**）、stdin 守护（`watchStdinShutdown:667-679`，EOF=父进程消失→关，**删**，换 Wails `OnBeforeClose`）、信号处理（保留）、token 经 env（换进程内变量）。**无 ppid/args[0] 检查**。
- 运行时依赖：SQLite（`db.go:89-91`，`glebarez/sqlite`，纯 Go）、纯 env 配置（不碰 cloud 的 viper/GraConf）、keychain（仅 macOS shell）、cloud HTTP client（纯 net/http）、sync/network watcher（goroutine）、local inference（net/http 调用户自配端点，**不 shell claude CLI**——契合 L2 SDK blocker 现状）。**无 CGO，无阻塞**。
- Build tag 分离干净：`server/main.go`（云）与 `server/cmd/workagent-desktop/main.go`（sidecar）是两个独立 `package main`；sidecar 只 import `server/desktop/*`，零 import cloud 包。

## 附录 C：安全/登录/打包要点

- **导航收束**（`main.ts:312-348` + `security-helpers.ts:16-222`）：permission blanket-deny；`will-navigate`/`will-redirect` 经 `isURLWithinRendererRoute` 收束到 bundled route（`file:` 要求精确匹配 `.../renderer/en/desktop/index.html`，无 host/query/hash）；`setWindowOpenHandler` 全 deny + 走 SSRF-hardened `normalizeExternalHTTPURL`（拒一切 local/private/loopback）→ `shell.openExternal`。
- **IPC sender 校验**（`main.ts:509-529`）：`sender===mainWindow.webContents` + `senderFrame===mainFrame` + 可信 URL 三连；+ `requestSingleInstanceLock` 关掉"第二窗口第二 webContents"绕过。
- **token**：`randomBytes(32).base64url`，每次 spawn 新生；env→preload 闭包，renderer 触不到；loopback 端口本身可被本地其他进程连，**token 才是真保护**。
- **登录交易 Main-only 的威胁模型**：token 是认证任意 sidecar 操作的主密钥；若泄露给 renderer，XSS/被 compromis 的依赖可伪造任意 sidecar 请求。故把特权 auth 命名空间强制走 Main-process gate（`login-transaction.ts`），renderer 只能经一条 typed password 命令提交凭据、只观察 `{state,error}`；flow ID Main 侧生成、**不回传 IPC 结果**。凭据卫生激进（fresh copy + `finally` 置空 + 手写 JSON 解析拒重复键 + 4KiB 上限）。
- **OAuth**：`dist/oauth-window.js` 是僵尸文件（源已删，引用的 helper 已不存在，加载即崩）；`validateOpenOAuthArgs`（`security-helpers.ts:108-143`）孤儿。**现出货登录只有密码**。
- **打包**：仅 macOS（arm64/x64）；`hardenedRuntime:true`、`publish:null`；entitlements 仅 allow-jit/unsigned-exec-mem/dyld-env + network.client（**故意无 network.server**，loopback 由 sidecar 持）；`appId ai.workmax.desktop` 绑 Keychain。**无任何自动更新机制**。

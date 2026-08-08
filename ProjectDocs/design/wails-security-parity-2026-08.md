# WorkMax Desktop — 安全对等清单（Electron → Wails）

| Field | Value |
|---|---|
| **Document** | 切换门槛所需的逐条安全对等评审 |
| **Date** | 2026-08-08 |
| **Status** | 待评审。`ProjectDocs/wails-migration-evaluation-2026-08.md` §14/§16 把本清单列为 cutover 门槛之一 |
| **对照基准** | 迁移文档附录 C（Electron shell 退役前的保证清单） |
| **证据口径** | 每条只写**实际验证过的**：文件位置 + 自动化断言。凡是"设计如此但没验证"的，标为残余项 |

---

## 怎么读这份清单

三种结论，不含糊：

- **保持** —— Wails 侧有等效或更强的机制，且有自动化断言钉住。
- **加强** —— 比 Electron 强，写明强在哪。
- **降级** —— 弱于 Electron。**每条都必须有补偿控制和残余风险描述**，由评审人决定是否接受。

"没有对应实现"不是一种结论。这份清单里不存在只写了计划的行。

---

## 1. 导航收束

| | |
|---|---|
| **Electron 的保证** | permission blanket-deny；`will-navigate`/`will-redirect` 经 `isURLWithinRendererRoute` 收束到 bundled route；`setWindowOpenHandler` 全 deny；外链经 SSRF-hardened `normalizeExternalHTTPURL` → `shell.openExternal` |
| **结论** | ⚠️ **降级（阻止式钩子不存在）+ 加强（CSP 由响应头下发）** |

**降级的事实**：macOS 上 Wails v3.0.0-beta.5 **没有实现 `decidePolicyForNavigationAction`**（只有 iOS 有），`pkg/events` 里 mac 的导航事件全是不可取消的通知。Electron `will-navigate` 那种**阻止式**收束无法复刻。这是迁移文档 §11 标注的"最大残余风险"，答案是：钩子不存在。

**补偿控制（全部实测生效）**：

| 控制 | 实现 | 断言 |
|---|---|---|
| CSP `script-src 'self'`、`connect-src 'self'`、无任何 inline 豁免 | `desktop/wails/uiserver.go` 响应头下发 | 契约保证 `containment-headers`（含 `'unsafe-inline'` 缺席断言）+ `uiserver_test.go` |
| 只服 asset FS，未知路径 404 | `UIHandler` | `uiserver_test.go`、kill③ 实测 |
| 路径穿越 404 | `rejectNonCanonicalPaths` | 同上 |
| `window.open` 被拒（无 `WKUIDelegate`） | WebKit 默认行为 | kill③ 实测返回 `null` |
| **外链交系统浏览器** | renderer 捕获阶段拦截 → Go 侧 SSRF 校验 → `browser.OpenURL` | 契约保证 ×2 + `external.go` 15 条拒绝测试 + 行为套件 3 条断言 |

**比 Electron 强的地方**：CSP 现在由**我们自己的 Go listener 以响应头下发**。Electron 把 renderer 放在 `file://` 上，只能用 meta 标签——meta 标签对已经拿到执行权的脚本没有约束力，响应头则由服务端强制。

**残余风险（须评审接受）**：顶层导航（`location.href = '...'`）**无法被阻止**。唯一可用的是 `WebViewDidStartProvisionalNavigation` 事后检测 + `SetURL` 回退，存在一个极短的"已开始导航"窗口，且**目前未实现**。

前提条件是攻击者已能在页面里执行脚本——而这被 `script-src 'self'` 挡在前面。判断依据：现实注入面是**模型输出渲染进 DOM**，CSP 阻断其执行；导航收束在本仓是 CSP 之后的纵深防御，不是第一道闸。

> 已知的一次真实失效：外链拦截在 rev.18 之前**根本不存在**，尽管迁移文档四处声称有。点击 `index.html` 里的 GitHub 链接会把 app 导航到远程页且回不来。当时 kill③ 只测了 `window.open`，没有枚举"同标签页 `<a>` 导航"这种形态。**评审时应追问：还有哪种触发导航的形态没被枚举？**

---

## 2. 调用方身份（Electron 的 IPC sender 校验）

| | |
|---|---|
| **Electron 的保证** | `sender===mainWindow.webContents` + `senderFrame===mainFrame` + 可信 URL 三连；配 `requestSingleInstanceLock` |
| **结论** | ⚠️ **机制不同，强度相当** |

Electron 能问操作系统"这个 IPC 是谁发的"。loopback origin 上的 webview 没有这种身份，**等价物是"必须已持有能力才能寻址"**：整个 UI 面（资产 + `/api/*` + `/login/*` + `/open-external`）挂在每次启动新生的 32 字节随机路径段下，其余一律 404 且不提示"换个路径就行"。

- 实现：`mintCapability` / `UIHandler`（`desktop/wails/uiserver.go`）
- 断言：契约保证 `capability-path`；`uiserver_test.go` 的 `TestUIOriginRequiresItsOwnCapability`
- 单实例两层：Wails `SingleInstanceOptions` + `<DataDir>/sidecar.pid`；实测第二个实例被拒、第一个仍干净退出

**为什么选路径段而非请求头**：第一个请求是顶层导航，带不了头。renderer 全用相对 URL，所以"知道能力"等同于"知道自己的 URL"。

**这条是补回来的，不是天生的**：同源反代**替调用方注入 token**，所以在能力路径落地之前，任何本地进程扫到 UI 端口就能全权驱动 sidecar——token 周界被完全绕过。**评审时应确认回归测试仍在**（`TestUIOriginRequiresItsOwnCapability`）。

---

## 3. Local token 的机密性

| | |
|---|---|
| **Electron 的保证** | `randomBytes(32).base64url`，每次 spawn 新生；env → preload 闭包；renderer 触不到 |
| **结论** | ✅ **保持**（一度计划降级，最终未降级） |

决策 D4/A2 曾接受"把 token 下发给受信 webview"，理由是跨源 fetch 需要这个头。同源形态使这个理由消失：**反代在 Go 侧注入 token，页面读不到它**。

- 断言：契约保证 `token-never-in-renderer`（按**带引号的字符串字面量**匹配，让规则约束代码而非注释措辞）
- 实测：`--verify-shim` 用真实长度随机 token 搜遍整个桥接对象，确认未泄漏

**残余（D4.1，须逐字写入而非略过）**：token 泄漏的后果比迁移前**更大**。L3d 之后未登录用户也能用 `localSingleUserUID (1<<62)` 建 thread、聊天、传附件，所以**即使从未云登录**，一把 token 也意味着全部本地 thread/message/附件（及 RAG 索引）可读写。

这仍在"本机本地"边界内——能拿到 token 的攻击者已在本机，本来就能读 SQLite 文件。但清单不能只写"够不到云凭据"。

---

## 4. 凭据路径（登录交易）

| | |
|---|---|
| **Electron 的保证** | 特权 auth 命名空间强制走 Main-process gate；renderer 只能提交一条 typed password 命令、只观察 `{state,error}`；flow ID 不回传；凭据卫生（fresh copy + `finally` 置空 + 手写 JSON 拒重复键 + 4KiB 上限） |
| **结论** | ✅ **保持 + 加强** |

renderer 只能说出四个动词之一（begin/status/password/cancel），**Go 决定它变成哪条 sidecar 路由、用什么方法**。这是 `ipcMain` 网关的直接对应物。

**加强在于执行位置**：preload 的守卫和它约束的代码住在同一个进程里；这里凭据路由由**反代在服务端拒绝**，renderer 够不到那个地方。

- 实现：`uiLoginPrefix` / `loginGateRoutes` / `privilegedSidecarPaths`（`desktop/wails/uiserver.go`）
- 断言：契约保证 `privileged-routes-refused-server-side`、`login-through-gate-only`；网关动词集**双向**钉死（多一个动词即红）；`uiserver_test.go` 覆盖非规范路径不得被重定向放行
- 服务端凭据卫生**原样保留**：网关转发到同样的 sidecar 路由，那些保护全部照常生效

**与原设计的偏差（须评审知晓）**：原 D3 计划"4 条 login 路由不在 sidecar 注册 + 走 Wails 绑定"。**未实现**，因为 (a) D4 反转后其前提消失，(b) 实测 Wails 绑定在 loopback origin 上不可用。当前形态达成同一目标。"不注册"仍是可选的严格化。

---

## 5. Renderer 请求卫生

| | |
|---|---|
| **Electron 的保证** | `credentials: "omit"`、`redirect: "error"` |
| **结论** | ✅ **保持** |

原样继承到 `shim.js`——请求在哪儿构造，卫生就在哪儿。`redirect: "error"` 与服务端的"拒绝非规范路径、不发清理重定向"是一对：**只有在客户端跟随重定向时才成立的特权检查，不算检查**。

- 断言：契约保证 `request-hygiene-preserved`、`no-redirect-based-checks`

---

## 6. 打包与分发

| | |
|---|---|
| **Electron 的保证** | `hardenedRuntime:true`；entitlements 仅 4 条；`appId ai.workmax.desktop` 绑 Keychain；无自动更新 |
| **结论** | ✅ **加强（授权面缩小）**，⏸ **签名公证未实跑** |

entitlements 从 4 条减到 **1 条**（只留 `network.client`）。去掉的三条都是 Electron V8 / build-from-source 的需要，Wails 用系统 WebView，二进制既不生成也不执行代码。

`disable-library-validation` **刻意暂不添加**——它要到 RAG 里程碑（ONNX 库从数据目录 dlopen）才有正当理由。plist 里把每条缺席的理由也写下来了。

- 断言：`inspect-mac-package.sh` 把 entitlement 集合与 plist **完全一致**地钉死；16 项负向测试
- bundle 结构穷举检查：bundle id、版本标记、不得有打包的 sidecar 二进制、renderer 白名单、CSP 逐源校验

**未完成（阻塞 cutover）**：Developer ID 签名 + `notarytool` 公证**从未实跑**（需要证书）。`notarize-mac.sh` 的签名门槛有测试覆盖，但走通一次真实提交是 §16 的门槛之一。

---

## 7. 已消失的攻击面

| 项 | 状态 |
|---|---|
| OAuth 内嵌窗口（`oauth-window.js` 僵尸文件 + 仍被打包） | ✅ 随 Electron 删除而消失 |
| preload / contextBridge / IPC 表面 | ✅ 不再存在 |
| 独立 sidecar 进程 + stdout 握手 + stdin 守护 | ✅ 单进程，握手代码已删 |
| Electron 40.x + Chromium 依赖树 | ✅ 移除；`desktop/wails` 模块**首次**被纳入 go-licenses 审计 |

---

## 评审结论栏

| 条目 | 结论 | 评审人 | 日期 |
|---|---|---|---|
| 1 导航收束（含顶层导航残余） | ☐ 接受 ☐ 需处理 | | |
| 2 调用方身份（能力路径） | ☐ 接受 ☐ 需处理 | | |
| 3 token 机密性（含 **D4.1** 残余） | ☐ 接受 ☐ 需处理 | | |
| 4 凭据路径（含 D3 偏差） | ☐ 接受 ☐ 需处理 | | |
| 5 请求卫生 | ☐ 接受 ☐ 需处理 | | |
| 6 打包（**签名公证未实跑**） | ☐ 接受 ☐ 需处理 | | |

**建议评审时追问的三个问题**：

1. 还有哪种能触发导航的形态没被枚举？（外链那次就是漏了一种形态。）
2. 每条"补偿控制"是否都有**会失败的**断言？（本文引用的断言均做过负向验证；新增的应同样对待。）
3. 哪些结论依赖"攻击者已在本机"这个前提？该前提在本产品的威胁模型里是否成立？

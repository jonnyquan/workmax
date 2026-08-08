# ADR: WorkMax ↔ WorkMax Plus 仓库边界

| Field | Value |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-08 |
| **Context** | OSS local Desktop + sibling commercial repo |

## Context

WorkMax（本仓库）是开源、本地优先的 Agent 产品：`server/` + `desktop/`，AGPL。  
WorkMax Plus（sibling：`../workmaxplus`）承载基于 WorkMax 的**商业化**：托管 SaaS、Web/Admin、支付包装、Team/Enterprise、运营能力。

需要冻结依赖方向与交付边界，避免：

- 开源仓重新引入必选 `web/` / `admin/`；
- Plus 私有商业逻辑回灌污染 OSS 默认路径；
- Desktop 用户被强制安装 MySQL / 云端 Worker。

## Decision

### D1 — 双仓角色

| 仓库 | 角色 |
|---|---|
| **workmax** | 开源底座：Desktop 本地运行时、Sidecar、Agent/Identity **协议与内核合同**、可自建的 Go Server core |
| **workmaxplus** | 商业化：营销/账户 Web、Admin、Stripe/商品包装、Team/Enterprise、托管运营、`desktopplus` 等增值壳 |

### D2 — 依赖方向（单向）

```text
workmax  ──协议 / 内核 / Desktop core──►  workmaxplus（可 vendoring / 子模块 / 发布制品消费）
workmaxplus  ──✗ 禁止──►  把商业私有码作为 workmax 运行时硬依赖
```

- Plus **可以**依赖 WorkMax 已发布的协议、OpenAPI/合同、Desktop Sidecar 行为。
- WorkMax **不得** import Plus 私有包、不得在默认构建中要求 Plus 存在。
- 共享能力上提到 WorkMax（协议、Kernel、Local model route）；商业包装留在 Plus。

### D3 — 交付面

| 表面 | WorkMax | WorkMax Plus |
|---|---|---|
| Desktop + Sidecar + SQLite | **是**（唯一用户客户端） | 可选 `desktopplus` 叠加权益/品牌 |
| Go Server core / Agent 合同 | **是** | 托管部署 + 商业扩展 |
| 顶层 `web/` 营销/账户 SPA | **否**（禁止恢复为 OSS 必选） | **是** |
| 顶层 `admin/` 运营台 | **否** | **是** |
| Stripe / 定价页 / 增长 | 可选协议客户端 | **是**（商品权威包装） |
| MySQL / Worker / Commerce 账本 | 自建 Server 可选 | 托管 **必需** |

### D4 — 运行时路径（OSS 用户）

1. 启动 Desktop → 本地 SQLite（无本机 MySQL）。
2. 登录默认 `https://workmax.app`（Plus 托管控制面；可配置自建 Origin）。
3. 模型：**Local**（用户自配 endpoint/key）∥ **Official**（账户额度 + 官方配置）。
4. 一 Turn 一路由；禁止静默跨路由扣费回退。

详见 `ProjectDocs/oss-local-desktop-runtime-mode-2026-08.md`。

### D5 — BYOK / 本地模型策略分叉（不矛盾）

| 路径 | WorkMax OSS | WorkMax Plus 商业策略 |
|---|---|---|
| Local route | **支持**用户本地/自建模型与 API Key（Keychain） | 不否定；可选择不主推 |
| Official route | 客户端调用托管；无供应商 Key | 可 **拒绝**「官方路径 BYOK 掏空 Credits」包装 |

Plus 文档中的「驳回 BYOK」约束 **Official/托管毛利**，不删除 OSS Local route。

### D6 — 稳定合同 vs 可变实现

**WorkMax 对外稳定（Plus 与第三方可依赖）：**

- Desktop Resource / OAuth / Login Transaction 合同（版本化）；
- Agent v1 目标合同（挂载前仍 default-off）；
- Sidecar loopback RoutePolicy + `desktop-boundaries` 清单；
- Local model settings 公开 DTO（无密钥字段）。

**Plus 可变（可不回灌 OSS）：**

- 定价文案、SKU、营销页、Admin 工作流、召回运营、企业合同条款；
- 非协议必要的 Web-only 功能。

### D7 — 密钥与配置

- OSS 仓库：仅 `*.example`；无 live `config.yaml`、无用户 Key。
- Desktop：OAuth token 与 Local API Key 仅 Keychain（或等效 OS store）。
- Plus 生产：Secret Manager / 托管配置；与 OSS 开发者本机隔离。

### D8 — 云端工程切片归属

P0-049（migration runner、Worker wiring、Commerce Outbox 等）服务 **托管/自建 Server**，默认 **不是** Desktop 用户启动清单。实现可落在 WorkMax `server/`（内核）或 Plus 部署编排；**启用与运营**属 Plus/托管。

## Consequences

### 正面

- 开源叙事清晰：装 Desktop 即用，可接本地模型。
- 商业能力不绑架 AGPL 默认路径。
- 协议集中在 WorkMax，减少双写分叉。

### 代价 / 约束

- 两仓需显式同步合同版本（boundary manifest、API version）。
- Plus 不得长期 fork 内核不回馈；共享 bugfix 优先上提 WorkMax。
- 贡献者需识别改动属于 OSS core 还是 Plus 商业包装。

## Non-goals

- 本 ADR 不定价数字、不定义 Team SKU 细则（见 Plus `commercial-edition-design.md`）。
- 不强制 monorepo 合并。
- 不要求立刻删除 Plus 内历史 `ProjectDocs` 与 WorkMax 的重复设计语料（可渐进收敛）。

## References

- `ProjectDocs/oss-local-desktop-runtime-mode-2026-08.md`
- `ProjectDocs/p0-049-production-wiring-evidence-gates-design-2026-08.md`
- `ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md`
- Sibling: `workmaxplus/ProjectDocs/design/commercial-edition-design.md`

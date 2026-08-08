# Desktop Local / Official 模型路由设置（v0）

| Field | Value |
|---|---|
| **Date** | 2026-08-08 |
| **Status** | Partial implementation (Sidecar + typed bridge alpha.7 + minimal UI) |
| **Product mode** | `oss-local-desktop-runtime-mode-2026-08.md` |

## Goal

让开源 Desktop 用户在登录后配置：

1. **preferred_route**: `local` | `official`
2. **Local profile**: protocol、base_url、model_id；API Key 进 Keychain，永不回传 Renderer

Official catalog / 额度仍来自云端；本切片不实现完整 Local 推理执行，只冻结**配置合同与安全边界**，供后续 Turn 路由读取。

## Wire contract (Sidecar loopback)

### `GET /settings/model-route`

Response JSON（无密钥）：

```json
{
  "preferred_route": "official",
  "local": {
    "protocol": "openai_compatible",
    "base_url": "http://127.0.0.1:11434/v1",
    "model_id": "llama3.2",
    "api_key_configured": false
  },
  "updated_at": "2026-08-08T12:00:00Z"
}
```

空配置时 `local` 字段可为空串，`api_key_configured=false`。

### `PUT /settings/model-route`

Request JSON（≤ 8 KiB）：

```json
{
  "preferred_route": "local",
  "local": {
    "protocol": "openai_compatible",
    "base_url": "http://127.0.0.1:11434/v1",
    "model_id": "llama3.2",
    "api_key": "optional-new-secret",
    "clear_api_key": false
  }
}
```

规则：

- `preferred_route` 必填：`local` | `official`
- 选 `local` 时：`protocol`、`base_url`、`model_id` 必须合法；Key 可已存在或本次提供
- `api_key` 省略或 `null`：保留 Keychain 原值
- `clear_api_key: true`：删除 Keychain 条目
- 响应与 GET 同形，**永不**包含 `api_key`

### Validation

| Field | Rule |
|---|---|
| protocol | `openai_compatible` \| `anthropic_compatible` |
| base_url | 绝对 URL；https **或** http 且 host 为 `127.0.0.1` / `localhost` / `::1`；无 userinfo；无 fragment；长度 ≤ 512 |
| model_id | 1..128 可打印 ASCII（字母数字 `._-:`） |
| api_key | 若提供：1..4096 字节，无控制字符 |

## Storage

| 数据 | 位置 |
|---|---|
| preferred_route + local 非密钥字段 | SQLite `w_desktop_model_settings`（singleton id=1） |
| API Key | Keychain service `ai.workmax.desktop` account `local-model-api-key` |

## Typed bridge (alpha.7)

| Method | Route |
|---|---|
| `desktopBridge.settings.getModelRoute()` | `GET /settings/model-route` |
| `desktopBridge.settings.putModelRoute(input)` | `PUT /settings/model-route` |

- `workmaxLocal.fetch` **禁止**访问 `/settings/model-route`（防密钥绕过 typed 校验）
- Bridge 校验 put 输入；get/put 响应拒绝含 `api_key` 字段
- Bundled Renderer：**Models** 侧栏表单（最小 UI）

## Non-goals (remaining)

- 实际 Local LLM 调用与 Agent Turn 切换（读取本设置的后续 PR）
- Official model catalog UI / 额度明细
- 多 profile / 每线程覆盖

## Evidence

- Hermetic tests：fake Keychain + temp SQLite；preload typed bridge tests
- Route policy + typedBridge.routeMethods 进入 `desktop-boundaries.v0.json`
- `api_key` 不得出现在 GET 响应与 diagnostics

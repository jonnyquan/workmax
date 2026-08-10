package model

import (
	"time"

	"server/globals"
)

// DesktopModelGatewayUsage is one row per model-gateway request: what the
// Desktop asked for, which platform credential answered it, and what it cost
// us in tokens.
//
// Why this table exists rather than a credit reservation: the gateway is
// called by a LOCAL tool loop, many times per user-visible turn, and the
// upstream reports tokens — not the total_cost_usd figure the cloud agent
// path settles on (api/pro/tools/workagent/agent_api.go). Reserving credits
// per HTTP call would both invent a pricing model the catalog does not carry
// and re-shape billing from per-turn to per-hop. So this release meters
// honestly and does not charge; see ProjectDocs and the package doc on
// service/desktop/modelgateway for the gap.
//
// Nothing here is ever serialized to a client. ProviderAccountID in
// particular is an internal join key onto w_workagent_account — a row is
// evidence for us, not a receipt for the user.
type DesktopModelGatewayUsage struct {
	globals.GraMODEL
	// UID is the caller (w_user.id), from the Desktop OAuth bearer.
	UID uint `json:"uid" gorm:"column:uid;not null;index:idx_dmg_usage_uid_created,priority:1;comment:调用者用户ID"`
	// RequestID is our own per-request identifier, also echoed in logs so an
	// operator can join a support report to a row without a token.
	RequestID string `json:"requestId" gorm:"column:request_id;type:varchar(64);not null;uniqueIndex:uk_dmg_usage_request;comment:网关请求ID"`
	// Protocol is the wire shape the caller used: anthropic | openai.
	Protocol string `json:"protocol" gorm:"column:protocol;type:varchar(16);not null;comment:anthropic/openai"`
	// Operation is WHICH endpoint of that protocol: messages | count_tokens.
	// It exists because count_tokens is not billed by the provider and its
	// response carries an input_tokens field that is an answer, not a bill —
	// without this column a spend report could not tell the two apart, and
	// the whole point of the table is that it can.
	Operation string `json:"operation" gorm:"column:operation;type:varchar(24);not null;default:'messages';comment:messages/count_tokens"`
	// ModelID is the catalog modelId the caller asked for (w_global_model).
	ModelID string `json:"modelId" gorm:"column:model_id;type:varchar(128);not null;index:idx_dmg_usage_model;comment:目录modelId"`
	// UpstreamModel is the real provider model name we sent out.
	UpstreamModel string `json:"upstreamModel" gorm:"column:upstream_model;type:varchar(128);not null;default:'';comment:实际供应商模型名"`
	// ProviderAccountID is w_workagent_account.id — which platform credential
	// paid for this call. Never leaves the server.
	ProviderAccountID uint64 `json:"providerAccountId" gorm:"column:provider_account_id;not null;default:0;index:idx_dmg_usage_account;comment:供应商账号ID"`
	// Stream records whether the caller asked for SSE.
	Stream bool `json:"stream" gorm:"column:stream;type:tinyint(1);not null;default:0;comment:是否流式"`
	// Status is completed | failed. A failed row still carries whatever
	// tokens the upstream managed to bill us for before it broke.
	Status string `json:"status" gorm:"column:status;type:varchar(16);not null;default:'completed';comment:completed/failed"`
	// HTTPStatus is what WE returned to the Desktop, not what upstream said.
	HTTPStatus int `json:"httpStatus" gorm:"column:http_status;not null;default:0;comment:网关返回状态码"`
	// ErrorClass is our classification (see modelgateway errors.go), empty on
	// success. Deliberately a small closed vocabulary, never upstream prose.
	ErrorClass string `json:"errorClass" gorm:"column:error_class;type:varchar(32);not null;default:'';comment:错误归类"`

	InputTokens         int `json:"inputTokens" gorm:"column:input_tokens;not null;default:0"`
	OutputTokens        int `json:"outputTokens" gorm:"column:output_tokens;not null;default:0"`
	CacheReadTokens     int `json:"cacheReadTokens" gorm:"column:cache_read_tokens;not null;default:0"`
	CacheCreationTokens int `json:"cacheCreationTokens" gorm:"column:cache_creation_tokens;not null;default:0"`
	// TotalTokens is stored rather than derived so reporting queries do not
	// have to know which of the four columns a given protocol populates.
	TotalTokens int `json:"totalTokens" gorm:"column:total_tokens;not null;default:0"`

	// DurationMS is wall-clock time for the whole upstream exchange.
	DurationMS int `json:"durationMs" gorm:"column:duration_ms;not null;default:0"`
	// StartedAt is when we dialled upstream; CreatedAt is when we wrote the row.
	StartedAt time.Time `json:"startedAt" gorm:"column:started_at;index:idx_dmg_usage_uid_created,priority:2"`
}

func (DesktopModelGatewayUsage) TableName() string {
	return "w_desktop_model_gateway_usage"
}

const (
	DesktopModelGatewayUsageStatusCompleted = "completed"
	DesktopModelGatewayUsageStatusFailed    = "failed"
)

// GlobalModelMetadataUpstreamModel is the metadata key holding the real
// provider model name a catalog modelId maps to. It lives in metadata rather
// than in its own column because the mapping is operator-tunable routing
// data, not catalog identity — an ops change, not a schema change.
//
// The gateway fails closed when it is missing: sending the catalog id
// ("work-pro") upstream would produce a confusing provider-side 404 instead
// of a clear "this model is not configured for the gateway" answer.
const GlobalModelMetadataUpstreamModel = "upstreamModel"

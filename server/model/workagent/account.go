package workagent

import (
	"encoding/json"
	"time"
)

// AgentAccount Agent账号配置模型
type AgentAccount struct {
	ID        uint64 `json:"id" gorm:"primaryKey"`
	Name      string `json:"name" gorm:"type:varchar(100);not null;uniqueIndex:uk_name_deleted"`
	Provider  string `json:"provider" gorm:"type:varchar(50);not null"`
	BaseURL   string `json:"baseUrl" gorm:"column:base_url;type:varchar(255);not null"`
	APIKey    string `json:"apiKey" gorm:"column:api_key;type:varchar(500);not null"`
	IsPremium bool   `json:"isPremium" gorm:"column:is_premium;type:tinyint(1);default:0;index:idx_is_premium"`
	Priority  int    `json:"priority" gorm:"default:5;index:idx_status_priority"`
	Status    int    `json:"status" gorm:"default:1;index:idx_status_priority"`
	IsActive  bool   `json:"isActive" gorm:"column:is_active;default:false;index"`

	// 实时统计（由实际请求更新）
	ConsecutiveFailures int        `json:"consecutiveFailures" gorm:"column:consecutive_failures;default:0"`
	TotalRequests       int64      `json:"totalRequests" gorm:"column:total_requests;default:0"`
	FailedRequests      int64      `json:"failedRequests" gorm:"column:failed_requests;default:0"`
	SuccessRate         float64    `json:"successRate" gorm:"-"` // 计算字段，不存储
	LastUsedAt          *time.Time `json:"lastUsedAt" gorm:"column:last_used_at"`
	LastError           string     `json:"lastError" gorm:"column:last_error;type:text"`
	LastErrorAt         *time.Time `json:"lastErrorAt" gorm:"column:last_error_at"`

	// Monthly token-spend budget. The SDK currently reports upstream
	// token cost as total_cost_usd, which Work Agent settles into
	// credits; these fields track that token-spend budget in credits
	// rather than inventing an unavailable raw-token count.
	MonthlyTokenBudgetCredits int    `json:"monthlyTokenBudgetCredits" gorm:"column:monthly_token_budget_credits;default:0;comment:月度token成本预算credits，0表示不限制"`
	MonthlyTokenCreditsUsed   int    `json:"monthlyTokenCreditsUsed" gorm:"column:monthly_token_credits_used;default:0;comment:当前月份已消耗token成本credits"`
	MonthlyTokenBudgetMonth   string `json:"monthlyTokenBudgetMonth" gorm:"column:monthly_token_budget_month;type:varchar(7);comment:token预算统计月份YYYY-MM"`

	Remark string `json:"remark" gorm:"type:varchar(500)"`

	// 系统字段
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	DeletedAt *time.Time `json:"-" gorm:"index;uniqueIndex:uk_name_deleted"`
}

// TableName 指定表名
func (AgentAccount) TableName() string {
	return "w_workagent_account"
}

// CalculateSuccessRate 计算成功率
func (a *AgentAccount) CalculateSuccessRate() {
	if a.TotalRequests == 0 {
		a.SuccessRate = 100.0
		return
	}
	a.SuccessRate = float64(a.TotalRequests-a.FailedRequests) / float64(a.TotalRequests) * 100.0
}

// PrepareForDisplay 准备用于API响应的数据（计算字段）
func (a *AgentAccount) PrepareForDisplay() {
	a.CalculateSuccessRate()
}

// maskAPIKey collapses the full API key down to a fingerprint suitable
// for display ("sk-a...bcde", "<unset>" for empty). Keeping the first
// few and last few characters lets an admin distinguish two configured
// keys without surfacing enough material to actually USE the key from
// a dev-tools network log. Keys shorter than 12 chars (toy / test
// values) collapse to a constant placeholder so the display can't be
// reverse-engineered into the original.
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) < 12 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// MarshalJSON redacts the API key on JSON output. The struct is
// returned in plain form to admin endpoints (list / detail / create /
// update); without this mask the full key landed in every browser
// dev-tools network panel that opened those responses, making
// exfiltration trivial for anyone with admin-page access (or a
// compromised admin browser, or a screen-sharing session).
//
// DB reads and SDK usage are unaffected — those go through the field
// directly, not through json.Marshal. APIKey only gets redacted on
// the encoding path. Inbound JSON (admin form submission) still
// populates the field via the standard tag — Marshal/Unmarshal are
// independent.
func (a *AgentAccount) MarshalJSON() ([]byte, error) {
	type alias AgentAccount
	return json.Marshal(&struct {
		APIKey string `json:"apiKey"`
		*alias
	}{
		APIKey: maskAPIKey(a.APIKey),
		alias:  (*alias)(a),
	})
}

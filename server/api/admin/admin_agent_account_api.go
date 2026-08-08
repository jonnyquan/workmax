package admin

import (
	"fmt"
	"server/globals"
	"server/model/common/response"
	workagentModel "server/model/workagent"
	"server/service/tools/workagent"
	"strconv"

	"github.com/gin-gonic/gin"
)

type AdminAgentAccountApi struct{}

// GetAgentAccountListResponse 账号列表响应
type GetAgentAccountListResponse struct {
	Accounts        []*workagentModel.AgentAccount `json:"accounts"`
	ActiveAccountID uint64                         `json:"activeAccountId"`
	Total           int                            `json:"total"`
}

// @Tags Admin AgentAccount
// @Summary Get agent account list
// @Success 200 {object} response.Response{data=GetAgentAccountListResponse}
// @Router /api/admin/agent-accounts [get]
func (a *AdminAgentAccountApi) GetAgentAccountList(c *gin.Context) {
	accountPool := workagent.GetAgentAccountPool()

	// 获取所有账号
	accounts := accountPool.GetAllAccounts()

	// 获取当前活跃账号ID
	activeAccountID := accountPool.GetActiveAccountID()

	response.OkWithData(GetAgentAccountListResponse{
		Accounts:        accounts,
		ActiveAccountID: activeAccountID,
		Total:           len(accounts),
	}, c)
}

// @Tags Admin AgentAccount
// @Summary Get agent account detail
// @Param id path int true "Account ID"
// @Success 200 {object} response.Response{data=workagentModel.AgentAccount}
// @Router /api/admin/agent-accounts/{id} [get]
func (a *AdminAgentAccountApi) GetAgentAccountDetail(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.FailWithMessage("Invalid account ID", c)
		return
	}

	accountPool := workagent.GetAgentAccountPool()
	account, err := accountPool.GetAccountByID(id)
	if err != nil {
		response.FailWithMessage("Account not found", c)
		return
	}

	response.OkWithData(account, c)
}

// CreateAgentAccountRequest 创建账号请求
type CreateAgentAccountRequest struct {
	Name                      string `json:"name" binding:"required"`
	Provider                  string `json:"provider" binding:"required"`
	BaseURL                   string `json:"baseUrl" binding:"required"`
	APIKey                    string `json:"apiKey" binding:"required"`
	IsPremium                 bool   `json:"isPremium"`
	Priority                  int    `json:"priority"`
	Status                    int    `json:"status"`
	MonthlyTokenBudgetCredits int    `json:"monthlyTokenBudgetCredits"`
	Remark                    string `json:"remark"`
}

// @Tags Admin AgentAccount
// @Summary Create agent account
// @Param account body CreateAgentAccountRequest true "Account info"
// @Success 200 {object} response.Response{data=workagentModel.AgentAccount}
// @Router /api/admin/agent-accounts [post]
func (a *AdminAgentAccountApi) CreateAgentAccount(c *gin.Context) {
	var req CreateAgentAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	// BaseURL has to be on the provider allowlist — every Agent invocation
	// pushes it into ANTHROPIC_BASE_URL, so an arbitrary value here would
	// hijack every prompt and tool call. See baseurl_validator.go.
	if err := workagent.ValidateBaseURL(req.BaseURL); err != nil {
		response.FailWithMessage("Invalid BaseURL: "+err.Error(), c)
		return
	}

	// 创建账号
	account := &workagentModel.AgentAccount{
		Name:                      req.Name,
		Provider:                  req.Provider,
		BaseURL:                   req.BaseURL,
		APIKey:                    req.APIKey,
		IsPremium:                 req.IsPremium,
		Priority:                  req.Priority,
		Status:                    req.Status,
		Remark:                    req.Remark,
		IsActive:                  false, // 新建账号默认不激活
		MonthlyTokenBudgetCredits: req.MonthlyTokenBudgetCredits,
	}

	if account.Priority == 0 {
		account.Priority = 5
	}
	if account.Status == 0 {
		account.Status = 1
	}

	accountPool := workagent.GetAgentAccountPool()
	if err := accountPool.CreateAccount(account); err != nil {
		response.FailWithMessage("Failed to create account: "+err.Error(), c)
		return
	}

	globals.Info(fmt.Sprintf("[AdminAPI] Created agent account: %d (%s)", account.ID, account.Name))
	// Same call Update + GetByID + Stats make. Without it the freshly
	// created account ships back with successRate=0 (zero requests
	// makes the calc skip), and the admin UI's just-created card
	// renders "0%" until the next refresh — confusing for an account
	// that has never been called.
	account.PrepareForDisplay()
	response.OkWithData(account, c)
}

// UpdateAgentAccountRequest 更新账号请求
type UpdateAgentAccountRequest struct {
	Name                      string `json:"name"`
	Provider                  string `json:"provider"`
	BaseURL                   string `json:"baseUrl"`
	APIKey                    string `json:"apiKey"`
	IsPremium                 *bool  `json:"isPremium"`
	Priority                  int    `json:"priority"`
	Status                    int    `json:"status"`
	MonthlyTokenBudgetCredits *int   `json:"monthlyTokenBudgetCredits"`
	Remark                    string `json:"remark"`
}

// @Tags Admin AgentAccount
// @Summary Update agent account
// @Param id path int true "Account ID"
// @Param account body UpdateAgentAccountRequest true "Account info"
// @Success 200 {object} response.Response{data=workagentModel.AgentAccount}
// @Router /api/admin/agent-accounts/{id} [put]
func (a *AdminAgentAccountApi) UpdateAgentAccount(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.FailWithMessage("Invalid account ID", c)
		return
	}

	var req UpdateAgentAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	db := globals.GraDBs["system"]

	// 检查账号是否存在
	var account workagentModel.AgentAccount
	if err := db.First(&account, id).Error; err != nil {
		response.FailWithMessage("Account not found", c)
		return
	}

	// 更新字段
	updates := make(map[string]interface{})
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Provider != "" {
		updates["provider"] = req.Provider
	}
	if req.BaseURL != "" {
		if err := workagent.ValidateBaseURL(req.BaseURL); err != nil {
			response.FailWithMessage("Invalid BaseURL: "+err.Error(), c)
			return
		}
		updates["base_url"] = req.BaseURL
	}
	if req.APIKey != "" {
		updates["api_key"] = req.APIKey
	}
	if req.IsPremium != nil {
		updates["is_premium"] = *req.IsPremium
	}
	if req.Priority > 0 {
		updates["priority"] = req.Priority
	}
	if req.Status > 0 {
		updates["status"] = req.Status
	}
	if req.MonthlyTokenBudgetCredits != nil && *req.MonthlyTokenBudgetCredits >= 0 {
		updates["monthly_token_budget_credits"] = *req.MonthlyTokenBudgetCredits
	}
	if req.Remark != "" {
		updates["remark"] = req.Remark
	}

	if len(updates) > 0 {
		if err := db.Model(&account).Updates(updates).Error; err != nil {
			response.FailWithMessage("Failed to update account: "+err.Error(), c)
			return
		}
	}

	// 重新获取更新后的数据
	if err := db.First(&account, id).Error; err != nil {
		response.FailWithMessage("Failed to get updated account", c)
		return
	}

	globals.Info(fmt.Sprintf("[AdminAPI] Updated agent account: %d (%s)", account.ID, account.Name))
	account.PrepareForDisplay()
	response.OkWithData(account, c)
}

// @Tags Admin AgentAccount
// @Summary Delete agent account
// @Param id path int true "Account ID"
// @Success 200 {object} response.Response
// @Router /api/admin/agent-accounts/{id} [delete]
func (a *AdminAgentAccountApi) DeleteAgentAccount(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.FailWithMessage("Invalid account ID", c)
		return
	}

	accountPool := workagent.GetAgentAccountPool()
	if err := accountPool.DeleteAccount(id); err != nil {
		response.FailWithMessage("Failed to delete account: "+err.Error(), c)
		return
	}

	globals.Info(fmt.Sprintf("[AdminAPI] Deleted agent account: %d", id))
	response.OkWithMessage("Account deleted successfully", c)
}

// @Tags Admin AgentAccount
// @Summary Switch active agent account
// @Param id path int true "Account ID"
// @Success 200 {object} response.Response{data=workagentModel.AgentAccount}
// @Router /api/admin/agent-accounts/{id}/switch [post]
func (a *AdminAgentAccountApi) SwitchAgentAccount(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.FailWithMessage("Invalid account ID", c)
		return
	}

	accountPool := workagent.GetAgentAccountPool()

	// 切换账号
	if err := accountPool.SwitchToAccount(id, "Switched by admin"); err != nil {
		response.FailWithMessage("Failed to switch account: "+err.Error(), c)
		return
	}

	// 获取切换后的账号信息
	newAccount, err := accountPool.GetAccountByID(id)
	if err != nil {
		response.FailWithMessage("Failed to get new account: "+err.Error(), c)
		return
	}

	globals.Info(fmt.Sprintf("[AdminAPI] Switched to agent account: %d (%s)", newAccount.ID, newAccount.Name))
	response.OkWithData(newAccount, c)
}

// @Tags Admin AgentAccount
// @Summary Test agent account connection
// @Param id path int true "Account ID"
// @Success 200 {object} response.Response
// @Router /api/admin/agent-accounts/{id}/test [post]
func (a *AdminAgentAccountApi) TestAgentAccount(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.FailWithMessage("Invalid account ID", c)
		return
	}

	accountPool := workagent.GetAgentAccountPool()
	result, err := accountPool.TestAccount(id)
	if err != nil {
		response.FailWithMessage("Test failed: "+err.Error(), c)
		return
	}

	globals.Info(fmt.Sprintf("[AdminAPI] Tested agent account: %d, result: %v", id, result["success"]))
	response.OkWithData(result, c)
}

// PoolHealthResponse rolls every signal an operator needs to triage a
// pool issue into one payload: per-account breaker + counters, the
// SSE manager's live connection stats, and the thread cache's size /
// capacity / TTL. Issuing one call instead of N+2 also means the
// admin UI sees a consistent snapshot rather than partial state from
// concurrent updates.
type PoolHealthResponse struct {
	Accounts    []workagent.AccountHealth `json:"accounts"`
	SSE         map[string]interface{}    `json:"sse"`
	ThreadCache map[string]interface{}    `json:"thread_cache"`
}

// @Tags Admin AgentAccount
// @Summary Get pool-wide health snapshot
// @Description Per-account breaker state + counters, SSE connection stats, ThreadCache stats — in one shot
// @Success 200 {object} response.Response{data=PoolHealthResponse}
// @Router /api/admin/agent-accounts/health [get]
func (a *AdminAgentAccountApi) GetAgentPoolHealth(c *gin.Context) {
	accountPool := workagent.GetAgentAccountPool()
	response.OkWithData(PoolHealthResponse{
		Accounts:    accountPool.GetPoolHealth(),
		SSE:         workagent.GetGlobalSSEManager().GetStats(),
		ThreadCache: workagent.GetThreadCache().Stats(),
	}, c)
}

// @Tags Admin AgentAccount
// @Summary Get agent account statistics
// @Param id path int true "Account ID"
// @Success 200 {object} response.Response
// @Router /api/admin/agent-accounts/{id}/stats [get]
func (a *AdminAgentAccountApi) GetAgentAccountStats(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.FailWithMessage("Invalid account ID", c)
		return
	}

	accountPool := workagent.GetAgentAccountPool()
	stats := accountPool.GetAccountStats(id)

	if errMsg, ok := stats["error"]; ok {
		response.FailWithMessage(errMsg.(string), c)
		return
	}

	response.OkWithData(stats, c)
}

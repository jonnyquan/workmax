package admin

import (
	"fmt"
	"html"
	"net/http"
	"server/globals"
	"server/model"
	accountsvc "server/service/account"
	"server/utils"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AdminUserApi struct{}

const adminCreditsPackGrantedTemplateCode = "admin_credits_pack_granted"

type adminCreditsPackEmailTemplate struct {
	ID      uint
	Subject string
	Content string
}

type adminCreditsPackEmailResult struct {
	Recipient string
	Sent      bool
	Skipped   bool
	Error     string
}

func resolveCreditsPackNotificationEmail(user *model.User) string {
	if user == nil {
		return ""
	}
	return strings.TrimSpace(user.Email)
}

func loadAdminCreditsPackEmailTemplate() (*adminCreditsPackEmailTemplate, error) {
	var template adminCreditsPackEmailTemplate
	err := globals.GraDBs["system"].
		Table("w_email_template").
		Select("id, subject, content").
		Where("code = ? AND status = ?", adminCreditsPackGrantedTemplateCode, model.EMAIL_TEMPLATE_STATUS_ENABLED).
		First(&template).Error
	if err != nil {
		return nil, err
	}
	return &template, nil
}

func replaceMailVariables(content string, variables map[string]string) string {
	replaced := content
	for key, value := range variables {
		replaced = strings.ReplaceAll(replaced, "{{"+key+"}}", value)
	}
	return replaced
}

func buildAdminCreditsPackEmailFallback(user *model.User, credits int, sourceID string, expiresAt *time.Time, remark string, creditsRemaining int) (string, string) {
	siteName := "WorkMax"
	dashboardURL := strings.TrimSpace(globals.GraConf.System.FrontendURL)
	if dashboardURL == "" {
		dashboardURL = "https://workmax.app"
	}
	dashboardURL = strings.TrimRight(dashboardURL, "/")
	dashboardLink := dashboardURL + "/dashboard"

	displayName := strings.TrimSpace(user.Nickname)
	if displayName == "" {
		displayName = resolveCreditsPackNotificationEmail(user)
	}
	expiresText := "Never expires"
	if expiresAt != nil {
		expiresText = expiresAt.In(time.Local).Format("2006-01-02 15:04")
	}
	remarkBlock := ""
	if cleanedRemark := strings.TrimSpace(remark); cleanedRemark != "" {
		remarkBlock = fmt.Sprintf(
			`<tr><td style="padding:12px 16px;color:#6b7280;">Remark</td><td style="padding:12px 16px;color:#111827;text-align:right;">%s</td></tr>`,
			html.EscapeString(cleanedRemark),
		)
	}

	subject := fmt.Sprintf("%s credits added to your %s account", strconv.Itoa(credits), siteName)
	body := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8" />
  <title>Credits Added</title>
</head>
<body style="margin:0;padding:0;background:#f4f7fb;font-family:Arial,sans-serif;color:#111827;">
  <div style="max-width:640px;margin:0 auto;padding:32px 16px;">
    <div style="background:#ffffff;border-radius:18px;overflow:hidden;box-shadow:0 10px 30px rgba(15,23,42,0.08);">
      <div style="background:linear-gradient(135deg,#111827,#334155);padding:36px 32px;">
        <p style="margin:0;color:#cbd5e1;font-size:13px;letter-spacing:0.08em;text-transform:uppercase;">%s</p>
        <h1 style="margin:10px 0 0;color:#ffffff;font-size:28px;line-height:1.2;">Credits Added To Your Account</h1>
      </div>
      <div style="padding:32px;">
        <p style="margin:0 0 14px;font-size:16px;line-height:1.7;">Hi %s,</p>
        <p style="margin:0 0 24px;font-size:15px;line-height:1.8;color:#4b5563;">We've added <strong>%d credits</strong> to your account. You can use them immediately in your video and creative workflows.</p>
        <table style="width:100%%;border-collapse:collapse;background:#f8fafc;border:1px solid #e5e7eb;border-radius:14px;overflow:hidden;">
          <tr><td style="padding:12px 16px;color:#6b7280;">Credits added</td><td style="padding:12px 16px;color:#111827;text-align:right;font-weight:700;">%d</td></tr>
          <tr><td style="padding:12px 16px;color:#6b7280;">Credits remaining</td><td style="padding:12px 16px;color:#111827;text-align:right;font-weight:700;">%d</td></tr>
          <tr><td style="padding:12px 16px;color:#6b7280;">Grant reference</td><td style="padding:12px 16px;color:#111827;text-align:right;">%s</td></tr>
          <tr><td style="padding:12px 16px;color:#6b7280;">Expires at</td><td style="padding:12px 16px;color:#111827;text-align:right;">%s</td></tr>
          %s
        </table>
        <div style="margin-top:28px;">
          <a href="%s" style="display:inline-block;background:#111827;color:#ffffff;text-decoration:none;padding:14px 22px;border-radius:12px;font-weight:700;">Open Dashboard</a>
        </div>
      </div>
    </div>
  </div>
</body>
</html>`,
		siteName,
		html.EscapeString(displayName),
		credits,
		credits,
		creditsRemaining,
		html.EscapeString(sourceID),
		html.EscapeString(expiresText),
		remarkBlock,
		html.EscapeString(dashboardLink),
	)
	return subject, body
}

func sendAdminCreditsPackGrantedEmail(user *model.User, credits int, sourceID string, expiresAt *time.Time, remark string, creditsRemaining int) adminCreditsPackEmailResult {
	recipient := resolveCreditsPackNotificationEmail(user)
	if recipient == "" {
		return adminCreditsPackEmailResult{
			Skipped: true,
			Error:   "user has no email address",
		}
	}

	variables := map[string]string{
		"nickname":         strings.TrimSpace(user.Nickname),
		"email":            recipient,
		"creditsGranted":   strconv.Itoa(credits),
		"creditsRemaining": strconv.Itoa(creditsRemaining),
		"sourceId":         sourceID,
		"remark":           strings.TrimSpace(remark),
		"dashboardUrl":     strings.TrimRight(strings.TrimSpace(globals.GraConf.System.FrontendURL), "/"),
	}
	if variables["nickname"] == "" {
		variables["nickname"] = recipient
	}
	if variables["dashboardUrl"] == "" {
		variables["dashboardUrl"] = "https://workmax.app"
	}
	if expiresAt != nil {
		variables["expiresAt"] = expiresAt.In(time.Local).Format("2006-01-02 15:04")
	} else {
		variables["expiresAt"] = "Never expires"
	}

	template, templateErr := loadAdminCreditsPackEmailTemplate()
	subject, body := buildAdminCreditsPackEmailFallback(user, credits, sourceID, expiresAt, remark, creditsRemaining)
	templateID := 0
	if templateErr == nil && template != nil {
		templateID = int(template.ID)
		subject = replaceMailVariables(template.Subject, variables)
		body = replaceMailVariables(template.Content, variables)
	}

	result := adminCreditsPackEmailResult{
		Recipient: recipient,
	}
	sendErr := utils.SendEmail(recipient, subject, body)
	if sendErr != nil {
		result.Error = sendErr.Error()
	} else {
		result.Sent = true
	}

	if templateID > 0 {
		sendRecord := model.EmailSendRecord{
			TemplateID: templateID,
			UID:        int(user.Id),
			Email:      recipient,
			Subject:    subject,
			Status:     model.EMAIL_SEND_STATUS_SENT,
			SentAt:     time.Now(),
		}
		if sendErr != nil {
			sendRecord.Status = model.EMAIL_SEND_STATUS_FAILED
			sendRecord.ErrorMsg = sendErr.Error()
		}
		if err := globals.GraDBs["system"].Create(&sendRecord).Error; err != nil {
			globals.Warn("failed to save credits grant email send record: " + err.Error())
		}
	}

	return result
}

// @Tags 管理员用户管理
// @Summary 获取用户列表
// @Description 获取用户列表
// @Success 200 {object} response.Response{data=[]response.UserListResponse} "用户列表"
// @Router /api/admin/user/getUserList [get]
func (a *AdminUserApi) GetUserList(c *gin.Context) {

	pageIndex := c.DefaultQuery("pageIndex", "1")
	pageSize := c.DefaultQuery("pageSize", "10")
	sortOrder := c.DefaultQuery("sort[order]", "desc")
	sortKey := c.DefaultQuery("sort[key]", "created_at")
	query := c.DefaultQuery("query", "")
	filterStatus := c.DefaultQuery("filterData[status]", "all")

	globals.Info(pageIndex, pageSize, sortOrder, sortKey, query, filterStatus)

	userList := []model.User{}
	pageIndexInt, _ := strconv.Atoi(pageIndex)
	pageSizeInt, _ := strconv.Atoi(pageSize)
	queryObj := globals.GraDBs["system"].Model(&model.User{})
	if sortKey != "" && sortOrder != "" {
		queryObj = queryObj.Order(sortKey + " " + sortOrder)
	} else {
		queryObj = queryObj.Order("created_at desc")
	}
	if filterStatus != "all" && filterStatus != "" {
		queryObj = queryObj.Where("ban = ?", func() int {
			if filterStatus == "active" {
				return 0
			}
			return 1
		}())
	}
	if query != "" {
		queryObj = queryObj.Where("nickname LIKE ? OR email LIKE ?", "%"+query+"%", "%"+query+"%")
	}

	total := int64(0)
	queryObj.Count(&total)

	queryObj.Limit(pageSizeInt).Offset((pageIndexInt - 1) * pageSizeInt).Find(&userList)

	data := []map[string]interface{}{}
	for _, user := range userList {
		loginAddress := ""
		if user.LoginAddress != "" {
			loginAddress = strings.Split(user.LoginAddress, "/")[0]
		}
		data = append(data, map[string]interface{}{
			"id":           user.Id,
			"name":         user.Nickname,
			"email":        user.Email,
			"authEmail":    user.AuthEmail,
			"role":         user.Role,
			"created_at":   user.CreatedAt,
			"avatar":       user.Avatar,
			"loginAddress": loginAddress,
			"loginIP":      user.LoginIP,
			"loginTime":    user.LoginTime.Unix(),
			"status": func() string {
				if user.Ban {
					return "blocked"
				} else {
					return "active"
				}
			}(),
		})
	}

	c.JSON(http.StatusOK, gin.H{"data": data, "total": total})
}

// @Tags 管理员用户管理
// @Summary 获取用户统计
// @Description 获取用户统计
// @Success 200 {object} response.Response{data=[]response.UserStatisticResponse} "用户统计"
// @Router /api/admin/user/getUserStatistic [get]
func (a *AdminUserApi) GetUserStatistic(c *gin.Context) {
	totalUsers := int64(0)
	globals.GraDBs["system"].Model(&model.User{}).Count(&totalUsers)
	activeUsers := int64(0)
	globals.GraDBs["system"].Model(&model.User{}).Where("login_time >= ?", time.Now().AddDate(0, 0, -1)).Count(&activeUsers)
	newUsers := int64(0)
	globals.GraDBs["system"].Model(&model.User{}).Where("created_at >= ?", time.Now().AddDate(0, 0, -1)).Count(&newUsers)

	totalUsersGrowShrink := 0
	totalUsersGrowShrink = int(totalUsers-newUsers) / int(totalUsers) * 100
	activeUsersGrowShrink := 0
	newUsersGrowShrink := 0
	data := make(map[string]interface{})
	data["totalUsers"] = map[string]interface{}{
		"value":      totalUsers,
		"growShrink": totalUsersGrowShrink,
	}
	data["activeUsers"] = map[string]interface{}{
		"value":      activeUsers,
		"growShrink": activeUsersGrowShrink,
	}
	data["newUsers"] = map[string]interface{}{
		"value":      newUsers,
		"growShrink": newUsersGrowShrink,
	}
	c.JSON(http.StatusOK, gin.H{"data": data})
}

// @Tags 管理员用户管理
// @Summary 修改用户
// @Description 修改用户
// @Success 200 {object} response.Response{data=[]response.UserResponse} "用户"
// @Router /api/admin/user/putUser [post]
func (a *AdminUserApi) PutUser(c *gin.Context) {

}

// @Tags 管理员用户管理
// @Summary 手动赠送积分包
// @Description 管理员为指定用户手动赠送积分, 记入 source_type=admin 的积分包
// @Success 200 {object} response.Response{data=map[string]interface{}} "赠送结果"
// @Router /api/admin/user/grantCreditsPack [post]
func (a *AdminUserApi) GrantCreditsPack(c *gin.Context) {
	var req struct {
		UID       int    `json:"uid"`
		Credits   int    `json:"credits"`
		SourceID  string `json:"sourceId"`
		ExpiresAt string `json:"expiresAt"`
		Remark    string `json:"remark"`
		SendEmail *bool  `json:"sendEmail"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.UID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "uid is required"})
		return
	}
	if req.Credits <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "credits must be positive"})
		return
	}

	var user model.User
	if err := globals.GraDBs["system"].Select("id", "nickname", "email", "auth_email").Where("id = ?", req.UID).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	var expiresAt *time.Time
	if raw := strings.TrimSpace(req.ExpiresAt); raw != "" {
		layouts := []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"}
		parsed := time.Time{}
		parsedOk := false
		for _, layout := range layouts {
			if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
				parsed = t
				parsedOk = true
				break
			}
		}
		if !parsedOk {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid expiresAt format"})
			return
		}
		if !parsed.After(time.Now()) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expiresAt must be in the future"})
			return
		}
		expiresAt = &parsed
	}

	sourceID := strings.TrimSpace(req.SourceID)
	if sourceID == "" {
		sourceID = "manual-" + time.Now().Format("20060102150405")
	}

	remark := strings.TrimSpace(req.Remark)
	if adminID := utils.GetUserID(c); adminID > 0 {
		tag := "grant_by_admin=" + strconv.Itoa(int(adminID))
		if remark == "" {
			remark = tag
		} else {
			remark = remark + ";" + tag
		}
	}

	packService := accountsvc.NewCreditsPackService()
	err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		return packService.CreatePackTx(tx, req.UID, model.CreditsSourceAdmin, sourceID, req.Credits, expiresAt, remark)
	})
	if err != nil {
		globals.Warn("grantCreditsPack failed: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// PR3a: credits 现场 SUM，不再读 w_user_quota 缓存。
	creditsTotal, creditsUsed, creditsRemaining, _ := accountsvc.NewCreditsPackService().GetBalance(req.UID)

	shouldSendEmail := req.SendEmail == nil || *req.SendEmail
	emailResult := adminCreditsPackEmailResult{Skipped: !shouldSendEmail}
	if shouldSendEmail {
		emailResult = sendAdminCreditsPackGrantedEmail(&user, req.Credits, sourceID, expiresAt, remark, creditsRemaining)
	}

	c.JSON(http.StatusOK, gin.H{
		"data": map[string]interface{}{
			"uid":              req.UID,
			"sourceId":         sourceID,
			"creditsGranted":   req.Credits,
			"creditsTotal":     creditsTotal,
			"creditsUsed":      creditsUsed,
			"creditsRemaining": creditsRemaining,
			"expiresAt":        expiresAt,
			"emailSent":        emailResult.Sent,
			"emailSkipped":     emailResult.Skipped,
			"emailRecipient":   emailResult.Recipient,
			"emailError":       emailResult.Error,
		},
	})
}

// @Tags 管理员用户管理
// @Summary 删除用户
// @Description 删除用户 (支持单个和批量删除)
// @Success 200 {object} response.Response{data=bool} "是否成功"
// @Router /api/admin/user/deleteUsers [post]
func (a *AdminUserApi) DeleteUsers(c *gin.Context) {
	var req struct {
		ID interface{} `json:"id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 根据不同的 id 类型进行处理
	switch v := req.ID.(type) {
	case string:
		// 单个删除
		result := globals.GraDBs["system"].Delete(&model.User{}, "id = ?", v)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
			return
		}
	case []interface{}:
		// 批量删除
		ids := make([]string, 0)
		for _, id := range v {
			if idStr, ok := id.(string); ok {
				ids = append(ids, idStr)
			}
		}
		result := globals.GraDBs["system"].Delete(&model.User{}, "id IN ?", ids)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid id format"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": true})
}

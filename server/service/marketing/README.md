# Email Marketing Service

WorkMax 邮件营销系统服务层实现文档

## 概述

邮件营销服务提供完整的邮件活动管理、用户分组、模板管理、自动化规则和发送追踪功能。

## 核心组件

### 1. EmailTemplateService - 邮件模板服务
负责邮件模板的CRUD操作和状态管理。

**主要方法:**
- `GetTemplateList(page, pageSize, category, status, keyword)` - 获取模板列表
- `GetTemplateByID(id)` - 根据ID获取模板
- `GetTemplateByCode(code)` - 根据代码获取启用的模板
- `CreateTemplate(template)` - 创建新模板
- `UpdateTemplate(template)` - 更新模板
- `DeleteTemplate(id)` - 删除模板
- `UpdateTemplateStatus(id, status)` - 更新模板状态

### 2. EmailSegmentService - 用户分组服务
提供基于规则的用户分组和筛选功能。

**主要方法:**
- `GetSegmentList(page, pageSize, status, keyword)` - 获取分组列表
- `GetSegmentByID(id)` - 获取分组详情
- `CreateSegment(segment)` - 创建分组
- `UpdateSegment(segment)` - 更新分组
- `DeleteSegment(id)` - 删除分组
- `GetSegmentUsers(segmentID)` - 获取分组用户列表
- `FilterUsersByRules(rules)` - 根据规则筛选用户
- `SyncSegmentUserCount(segmentID)` - 同步分组用户数量

**分组规则结构:**
```go
type SegmentRule struct {
    Field    string      // 字段名：member/created_at/login_time等
    Operator string      // 操作符：eq/neq/gt/lt/gte/lte/in/nin/contains
    Value    interface{} // 值
}

type SegmentRules struct {
    Logic string        // and/or
    Rules []SegmentRule
}
```

### 3. EmailCampaignService - 邮件活动服务
管理邮件活动的全生命周期。

**主要方法:**
- `GetCampaignList(page, pageSize, status, keyword)` - 获取活动列表
- `GetCampaignByID(id)` - 获取活动详情
- `CreateCampaign(campaign)` - 创建活动
- `UpdateCampaign(campaign)` - 更新活动
- `DeleteCampaign(id)` - 删除活动
- `UpdateCampaignStatus(id, status)` - 更新活动状态
- `IncrementCampaignStats(campaignID, field)` - 增加统计数据
- `GetScheduledCampaigns()` - 获取待执行的定时活动
- `GetCampaignStats(campaignID)` - 获取活动统计

**活动状态:**
- `draft` - 草稿
- `scheduled` - 已安排
- `running` - 运行中
- `completed` - 已完成
- `paused` - 已暂停
- `cancelled` - 已取消

### 4. EmailSendService - 邮件发送服务
处理邮件发送、追踪和退订。

**主要方法:**
- `SendEmail(templateID, uid, variables)` - 发送单封邮件
- `SendCampaignEmails(campaignID)` - 批量发送活动邮件
- `SendEmailWithCampaign(templateID, uid, campaignID, variables)` - 发送带活动ID的邮件
- `RenderTemplate(template, variables)` - 渲染模板变量
- `AddTrackingPixel(content, uid, campaignID)` - 添加打开追踪像素
- `AddUnsubscribeLink(content, uid)` - 添加退订链接
- `GetSendRecordList(...)` - 获取发送记录列表
- `RecordEmailOpen(uid, campaignID, ip, userAgent)` - 记录邮件打开
- `RecordEmailClick(uid, campaignID, ip, userAgent)` - 记录邮件点击
- `IsUnsubscribed(uid)` - 检查用户是否已退订
- `Unsubscribe(uid, reason, campaignID, ip)` - 退订

**发送限流:**
- 批量发送时每封邮件间隔100ms（每秒最多10封）
- 失败时支持3次重试

### 5. EmailAutomationService - 自动化规则服务
管理基于事件触发的自动化邮件。

**主要方法:**
- `GetAutomationRuleList(page, pageSize, status, triggerType)` - 获取规则列表
- `GetAutomationRuleByID(id)` - 获取规则详情
- `CreateAutomationRule(rule)` - 创建规则
- `UpdateAutomationRule(rule)` - 更新规则
- `DeleteAutomationRule(id)` - 删除规则
- `UpdateAutomationRuleStatus(id, status)` - 更新规则状态
- `TriggerAutomationByType(triggerType, uid, data)` - 根据类型触发自动化
- `TriggerUserRegister(uid, nickname, email)` - 触发用户注册自动化
- `TriggerSubscriptionExpire(uid, expiryDate)` - 触发订阅即将过期自动化
- `TriggerUsageLimit(uid, quotaType, usagePercent)` - 触发使用限制自动化

**支持的触发类型:**
- `user_register` - 用户注册
- `subscription_expire` - 订阅即将到期
- `usage_limit` - 使用限制
- `inactivity` - 用户不活跃
- `first_creation` - 首次创作
- `payment_success` - 支付成功
- `payment_failed` - 支付失败

## 使用示例

### 1. 创建并发送邮件模板

```go
// 创建模板
template := &model.EmailTemplate{
    Name:        "欢迎邮件",
    Code:        "welcome_email",
    Subject:     "欢迎加入 WorkMax - {{nickname}}",
    Content:     "<h1>您好，{{nickname}}！</h1><p>欢迎使用WorkMax...</p>",
    Category:    "transactional",
    Status:      1,
}
templateService.CreateTemplate(template)

// 发送邮件
sendService.SendEmail(template.Id, userID, map[string]string{
    "nickname": "张三",
    "email":    "user@example.com",
})
```

### 2. 创建用户分组

```go
// 定义分组规则
rules := SegmentRules{
    Logic: "and",
    Rules: []SegmentRule{
        {
            Field:    "member",
            Operator: "eq",
            Value:    1, // 付费用户
        },
        {
            Field:    "created_at",
            Operator: "gte",
            Value:    "2024-01-01", // 2024年后注册
        },
    },
}

rulesJSON, _ := json.Marshal(rules)
segment := &model.EmailSegment{
    Name:        "活跃付费用户",
    Description: "2024年后注册的付费用户",
    Rules:       string(rulesJSON),
    Status:      1,
}
segmentService.CreateSegment(segment)

// 同步用户数量
segmentService.SyncSegmentUserCount(segment.Id)
```

### 3. 创建并启动邮件活动

```go
// 创建活动
campaign := &model.EmailCampaign{
    Name:         "新功能推广",
    TemplateID:   templateID,
    SegmentID:    segmentID,
    ScheduleType: "immediate", // 立即发送
    Status:       "draft",
}
campaignService.CreateCampaign(campaign)

// 启动活动（异步发送）
go func() {
    sendService.SendCampaignEmails(campaign.Id)
}()
```

### 4. 设置自动化规则

```go
// 创建用户注册欢迎邮件规则
rule := &model.EmailAutomationRule{
    Name:         "新用户欢迎邮件",
    TriggerType:  "user_register",
    TemplateID:   welcomeTemplateID,
    DelayMinutes: 0, // 立即发送
    Status:       1,
    Priority:     10,
}
automationService.CreateAutomationRule(rule)

// 在用户注册时触发
automationService.TriggerUserRegister(newUserID, nickname, email)
```

## 数据流程

### 邮件发送流程
```
1. 创建/选择模板
2. 创建活动（可选用户分组）
3. 设置发送计划（立即/定时/循环）
4. 启动活动
5. 批量发送邮件（含追踪像素和退订链接）
6. 记录发送状态
7. 追踪打开和点击
8. 统计分析
```

### 自动化流程
```
1. 创建自动化规则
2. 关联模板和触发条件
3. 事件发生时触发规则
4. 根据延迟时间发送邮件
5. 记录触发次数
```

## 注意事项

1. **发送限流**: 批量发送时会自动限流，避免被SMTP服务商封禁
2. **退订检查**: 发送前会自动检查用户是否已退订
3. **错误重试**: 发送失败会记录错误信息，需要手动重试
4. **追踪链接**: 所有营销邮件会自动添加追踪像素和退订链接
5. **变量替换**: 模板变量使用 `{{variable}}` 格式
6. **数据库**: 所有服务使用 `globals.GraDBs["system"]` 数据库连接

## API端点

所有API端点都在 `/api/admin/email/` 下：

- Templates: `/templates`, `/templates/:id`
- Campaigns: `/campaigns`, `/campaigns/:id`, `/campaigns/:id/start`, `/campaigns/:id/pause`
- Segments: `/segments`, `/segments/:id`, `/segments/:id/users`, `/segments/:id/sync`
- Records: `/records`
- Automation: `/automation`, `/automation/:id`
- Tracking: `/track/open`, `/track/click`, `/unsubscribe` (公开路由)

详细API文档请参考 `server/api/admin/marketing/` 目录下的API文件。

# 不活跃用户召回功能

## 功能概述

不活跃用户召回功能允许系统自动检测长时间未登录的用户，并向他们发送召回邮件，提醒他们回来使用平台。

## 特性

✅ **智能检测**: 基于用户最后登录时间自动识别不活跃用户
✅ **灵活配置**: 支持自定义不活跃天数阈值（如7天、30天、90天）
✅ **防骚扰机制**: 7天内不会向同一用户重复发送召回邮件
✅ **退订支持**: 自动跳过已退订的用户
✅ **邮箱验证**: 仅向已验证邮箱的用户发送
✅ **批量处理**: 支持一次性检测多个不活跃级别的用户

## 核心功能

### 1. 单次检测 - CheckInactiveUsers

检测指定不活跃天数的用户并发送召回邮件。

**API端点**: `POST /api/admin/email/automation/check-inactive`

**请求示例**:
```json
{
  "inactiveDays": 30
}
```

**响应示例**:
```json
{
  "code": 200,
  "message": "Inactive users check completed",
  "data": {
    "inactiveDays": 30,
    "emailsSent": 42
  }
}
```

**使用场景**: 
- 手动触发30天不活跃用户召回
- 定期检测特定天数的不活跃用户

---

### 2. 批量检测 - BatchCheckInactiveUsers

一次性检测多个不活跃级别的用户。

**API端点**: `POST /api/admin/email/automation/batch-check-inactive`

**请求示例**:
```json
{
  "inactiveDaysList": [7, 30, 90]
}
```

**响应示例**:
```json
{
  "code": 200,
  "message": "Batch inactive users check completed",
  "data": {
    "results": {
      "7": 12,
      "30": 45,
      "90": 23
    },
    "totalEmailsSent": 80
  }
}
```

**使用场景**:
- 每周定期批量检测所有级别的不活跃用户
- 全面的用户召回活动

---

## 工作流程

### 检测逻辑

```
1. 查询条件:
   - login_time < (当前时间 - 不活跃天数)
   - login_time IS NOT NULL (至少登录过一次)
   - auth_email = 1 (已验证邮箱)

2. 过滤条件:
   ✓ 跳过已退订的用户
   ✓ 跳过7天内已发送过召回邮件的用户

3. 触发召回:
   ✓ 根据自动化规则匹配对应的邮件模板
   ✓ 发送个性化召回邮件
   ✓ 记录发送日志
```

### 防骚扰机制

为了避免频繁打扰用户，系统内置了多重保护：

1. **冷却期**: 同一用户7天内只能收到1封召回邮件
2. **退订支持**: 已退订的用户永久不会收到召回邮件
3. **邮箱验证**: 只向已验证邮箱的用户发送
4. **主题识别**: 通过邮件主题包含 "comeback" 关键词识别召回邮件

---

## 配置自动化规则

在发送召回邮件之前，需要在后台创建对应的自动化规则：

### 1. 创建邮件模板

访问: **营销管理 > 邮件模板**

创建召回邮件模板，支持以下变量：
- `{{nickname}}` - 用户昵称
- `{{lastLoginDate}}` - 最后登录日期
- `{{inactiveDays}}` - 不活跃天数

**示例模板**:
```html
<h2>Hi {{nickname}}, we miss you!</h2>
<p>We noticed you haven't visited us since {{lastLoginDate}} ({{inactiveDays}} days ago).</p>
<p>Come back and see what's new!</p>
<a href="https://workmax.app">Return to WorkMax</a>
```

### 2. 创建自动化规则

访问: **营销管理 > 自动化规则**

配置规则：
- **触发类型**: `inactivity` (不活跃)
- **模板**: 选择步骤1创建的召回模板
- **延迟时间**: 0分钟（立即发送）
- **状态**: 启用

---

## 定时任务配置

### 使用系统 Cron 定时任务

创建定时任务脚本 `/etc/cron.d/inactive-users-recall`:

```bash
# 每周一上午10点检测7天、30天、90天不活跃用户
0 10 * * 1 curl -X POST http://localhost:9200/api/admin/email/automation/batch-check-inactive \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  -d '{"inactiveDaysList": [7, 30, 90]}'
```

### 推荐定时策略

| 不活跃天数 | 召回时机 | 频率 | 说明 |
|-----------|---------|------|------|
| 7天 | 每周一 10:00 | 每周 | 短期不活跃，挽回及时 |
| 30天 | 每月1日 10:00 | 每月 | 中期不活跃，重点召回 |
| 90天 | 每季度1日 10:00 | 每季度 | 长期不活跃，最后尝试 |

---

## 代码示例

### Go 代码调用

```go
package main

import (
    "server/service"
)

func main() {
    automationService := &marketing.EmailAutomationService{}
    
    // 单次检测30天不活跃用户
    count, err := automationService.CheckAndTriggerInactiveUsers(30)
    if err != nil {
        log.Printf("Error: %v", err)
        return
    }
    log.Printf("Sent %d recall emails", count)
    
    // 批量检测多个级别
    results, err := automationService.BatchCheckInactiveUsers([]int{7, 30, 90})
    if err != nil {
        log.Printf("Error: %v", err)
        return
    }
    log.Printf("Batch results: %+v", results)
}
```

### cURL 示例

```bash
# 单次检测
curl -X POST http://localhost:9200/api/admin/email/automation/check-inactive \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{"inactiveDays": 30}'

# 批量检测
curl -X POST http://localhost:9200/api/admin/email/automation/batch-check-inactive \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{"inactiveDaysList": [7, 30, 90]}'
```

---

## 监控和日志

### 日志位置

所有不活跃用户检测的日志都会记录在系统日志中：

```bash
# 查看最近的召回日志
tail -f /path/to/logs/app.log | grep "Inactive users"
```

### 日志示例

```
2025-11-09 10:00:01 INFO  Inactive users check completed 
  inactiveDays=30 emailsSent=42

2025-11-09 10:05:12 INFO  Batch inactive users check completed 
  results={"7":12,"30":45,"90":23} totalEmailsSent=80

2025-11-09 10:05:13 ERROR Failed to check inactive users 
  inactiveDays=30 error="database connection failed"
```

### 监控指标

建议监控以下指标：
- **召回邮件发送数**: 每日/每周/每月发送的召回邮件总数
- **成功率**: 成功发送的邮件占比
- **召回率**: 收到召回邮件后重新登录的用户占比
- **退订率**: 因召回邮件而退订的用户占比

---

## 最佳实践

### 1. 分级召回策略

针对不同不活跃程度的用户，采用不同的召回策略：

| 不活跃级别 | 天数 | 邮件风格 | 优惠力度 |
|-----------|------|---------|---------|
| 轻度 | 7天 | 友好提醒 | 无 |
| 中度 | 30天 | 突出新功能 | 小额优惠 |
| 重度 | 90天 | 强力优惠 | 大额优惠/会员赠送 |

### 2. A/B 测试

为不同用户群体创建多个召回模板，测试哪种更有效：
- **版本A**: 强调新功能
- **版本B**: 提供优惠券
- **版本C**: 情感化文案

### 3. 个性化内容

在邮件模板中加入更多个性化元素：
- 用户最后使用的工具
- 用户创建的内容数量
- 推荐的新功能（基于用户历史行为）

### 4. 合规性

确保召回邮件符合邮件营销法规：
- ✅ 提供明显的退订链接
- ✅ 包含公司联系信息
- ✅ 尊重用户的退订选择
- ✅ 遵守GDPR/CAN-SPAM等法规

---

## 故障排查

### 问题1: 没有发送任何邮件

**可能原因**:
- 没有创建对应的自动化规则
- 自动化规则状态为"禁用"
- 没有符合条件的不活跃用户

**解决方案**:
```bash
# 检查自动化规则
curl http://localhost:9200/api/admin/email/automation?triggerType=inactivity

# 检查不活跃用户数量
SELECT COUNT(*) FROM w_user 
WHERE login_time < DATE_SUB(NOW(), INTERVAL 30 DAY)
AND auth_email = 1;
```

### 问题2: 邮件发送失败

**可能原因**:
- SMTP配置错误
- 邮件模板格式错误
- 用户邮箱无效

**解决方案**:
- 检查邮件发送记录表 `w_email_send_record`
- 查看 `error_msg` 字段获取详细错误信息

### 问题3: 用户频繁收到召回邮件

**可能原因**:
- 防骚扰机制失效
- 主题识别逻辑问题

**解决方案**:
- 确保邮件主题包含 "comeback" 关键词
- 检查发送记录表，确认冷却期逻辑正常工作

---

## 数据统计

### 查询召回效果

```sql
-- 查看最近30天的召回邮件发送情况
SELECT 
    DATE(sent_at) as send_date,
    COUNT(*) as total_sent,
    SUM(CASE WHEN status = 'sent' THEN 1 ELSE 0 END) as success_count,
    SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) as failed_count,
    SUM(CASE WHEN open_count > 0 THEN 1 ELSE 0 END) as opened_count
FROM w_email_send_record
WHERE subject LIKE '%comeback%'
AND sent_at >= DATE_SUB(NOW(), INTERVAL 30 DAY)
GROUP BY DATE(sent_at)
ORDER BY send_date DESC;

-- 查询召回成功率（用户重新登录）
SELECT 
    COUNT(DISTINCT r.uid) as total_recalled,
    COUNT(DISTINCT CASE WHEN u.login_time > r.sent_at THEN r.uid END) as returned_users,
    ROUND(COUNT(DISTINCT CASE WHEN u.login_time > r.sent_at THEN r.uid END) * 100.0 / COUNT(DISTINCT r.uid), 2) as return_rate
FROM w_email_send_record r
JOIN w_user u ON r.uid = u.id
WHERE r.subject LIKE '%comeback%'
AND r.sent_at >= DATE_SUB(NOW(), INTERVAL 30 DAY);
```

---

## 版本历史

- **v1.0** (2025-11-09): 初始版本
  - 实现基础不活跃用户检测功能
  - 支持单次和批量检测
  - 内置防骚扰机制
  - API端点完整

---

## 相关文档

- [邮件营销系统总览](./README.md)
- [自动化规则配置](../email_automation_service.go)
- [邮件模板管理](../email_template_service.go)
- [邮件发送服务](../email_send_service.go)

---

## 联系支持

如有问题或建议，请联系技术支持团队。

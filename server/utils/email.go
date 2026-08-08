package utils

import (
	"os"
	"server/globals"
	"time"

	"gopkg.in/gomail.v2"
)

// ========================================
// 以下方法为交易类邮件，直接发送
// 营销类邮件请使用 EmailAutomationService
// ========================================

// getSMTPConfig 获取SMTP配置
// 优先从环境变量读取密码，如果没有则使用配置文件中的密码
func getSMTPConfig() (host string, port int, username, password, from string, ssl bool) {
	host = globals.GraConf.System.SMTP.Host
	port = globals.GraConf.System.SMTP.Port
	username = globals.GraConf.System.SMTP.Username
	from = globals.GraConf.System.SMTP.From
	ssl = globals.GraConf.System.SMTP.SSL

	// 优先使用环境变量中的密码（生产环境推荐）
	if envPassword := os.Getenv("SMTP_PASSWORD"); envPassword != "" {
		password = envPassword
	} else {
		// 如果环境变量没有，使用配置文件中的密码
		password = globals.GraConf.System.SMTP.Password
	}

	return
}

// SendEmail 发送邮件 (保留通用接口 - 用于交易类邮件)
func SendEmail(toEmail string, subject string, body string) error {
	host, port, username, password, from, ssl := getSMTPConfig()

	m := gomail.NewMessage()
	m.SetHeader("From", from)
	m.SetHeader("To", toEmail)
	m.SetHeader("Subject", subject)
	m.SetBody("text/html", body)

	d := gomail.NewDialer(host, port, username, password)
	d.SSL = ssl
	if err := d.DialAndSend(m); err != nil {
		return err
	}

	return nil
}

// SendVerificationEmail 发送验证码邮件 - 交易类邮件（保留）
func SendVerificationEmail(to string) error {
	return EmailService.SendVerificationEmail(to)
}

// SendResetPasswordEmail 发送重置密码邮件 - 交易类邮件（保留）
func SendResetPasswordEmail(id uint, to string) error {
	return EmailService.SendResetPasswordEmail(id, to)
}

// SendWelcomeEmail 发送欢迎邮件
// Deprecated: 已废弃，请使用 EmailAutomationService.TriggerUserRegister() 替代
// 该方法将在未来版本中移除
func SendWelcomeEmail(to string, nickname string) error {
	return EmailService.SendWelcomeEmail(to, nickname)
}

// SendOrderEmail 发送订单确认邮件 - 交易类邮件（保留）。
// `amount` is the already-formatted display string (e.g. "$19.99"); see the
// underlying EmailTemplateService.SendOrderEmail for why callers own the
// formatting. Use FormatStripeAmount for Stripe-sourced minor-units values.
func SendOrderEmail(to string, customerName string, packageName string, orderNo string, orderDate time.Time, amount string) error {
	return EmailService.SendOrderEmail(to, customerName, packageName, orderNo, orderDate, amount)
}

// SendSubscriptionCancellationEmail 发送订阅取消确认邮件
func SendSubscriptionCancellationEmail(to string, nickname string, expiryDate string) error {
	return EmailService.SendSubscriptionCancellationEmail(to, nickname, expiryDate)
}

// SendSubscriptionRenewalEmail 发送订阅续费确认邮件（含取消订阅与管理后台入口）
func SendSubscriptionRenewalEmail(to string, nickname string, planName string, amount string, renewalDate string, paymentMethod string) error {
	return EmailService.SendSubscriptionRenewalEmail(to, nickname, planName, amount, renewalDate, paymentMethod)
}

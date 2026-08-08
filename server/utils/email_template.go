package utils

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"server/globals"
	"strings"
	"time"

	"gopkg.in/gomail.v2"
)

// generateVerificationCode returns a 6-digit numeric code drawn from a CSPRNG.
// Six digits keeps the keyspace at 1M (vs. the old 10K) which combined with
// the per-email attempt cap and the 5-minute TTL pushes brute-force out of
// reach without making the code painful to type.
func generateVerificationCode() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// CSPRNG failure is exotic — fall back to time-based so a deploy
		// in a degraded entropy environment still issues *some* code, but
		// log loudly. Calling code already treats failures as send-failed.
		globals.GraLog.Warn(fmt.Sprintf("crypto/rand failed in verification code, falling back: %v", err))
		return fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
	}
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(b[:])%1000000)
}

// generateResetToken returns a 32-byte hex token from CSPRNG. Collisions are
// negligible (2^128 keyspace) and the token is unguessable even with full
// knowledge of the request timing — the previous md5(time.Now().String())
// was tractable for an attacker who could observe roughly when the user
// requested a reset.
func generateResetToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// EmailTemplateService 邮件模板服务
type EmailTemplateService struct{}

// NewEmailTemplateService 创建邮件模板服务
func NewEmailTemplateService() *EmailTemplateService {
	return &EmailTemplateService{}
}

// getTemplateByCode 从数据库获取模板
func (s *EmailTemplateService) getTemplateByCode(code string) (string, string, error) {
	var template struct {
		Subject string
		Content string
		Status  int
	}

	err := globals.GraDBs["system"].Table("w_email_template").
		Select("subject, content, status").
		Where("code = ? AND status = 1", code).
		First(&template).Error

	if err != nil {
		return "", "", fmt.Errorf("template not found: %s", code)
	}

	return template.Subject, template.Content, nil
}

// replaceVariables 替换模板变量
func (s *EmailTemplateService) replaceVariables(content string, variables map[string]string) string {
	result := content
	for key, value := range variables {
		placeholder := fmt.Sprintf("{{%s}}", key)
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result
}

// ReplaceVariables 公开的替换变量方法（用于测试邮件）
func (s *EmailTemplateService) ReplaceVariables(content string, variables map[string]string) string {
	return s.replaceVariables(content, variables)
}

// sendEmail 发送邮件
func (s *EmailTemplateService) sendEmail(to string, subject string, body string) error {
	host, port, username, password, from, ssl := getSMTPConfig()

	m := gomail.NewMessage()
	m.SetHeader("From", from)
	m.SetHeader("To", to)
	m.SetHeader("Subject", subject)
	m.SetBody("text/html", body)

	d := gomail.NewDialer(host, port, username, password)
	d.SSL = ssl
	if err := d.DialAndSend(m); err != nil {
		return err
	}

	return nil
}

// verifyCodeTTL is how long a freshly-issued code stays valid. Matches the
// "10 minutes" copy in the production mail_code template body so the user
// isn't told 10 min then locked out at 5.
const verifyCodeTTL = 10 * time.Minute

// SendVerificationEmail 发送验证码邮件。
//
// Cache contract: stores the plaintext code under `verifyCode-{lowercased
// email}` for verifyCodeTTL; the verify-email handler reads + deletes it.
// Email is lowercased so verify accepts whatever case the user types.
//
// The mail_code template in production references {{nickname}} and
// {{dashboardUrl}} alongside {{code}} — we populate all three so users
// don't see literal "{{nickname}}" in their inbox. Nickname is derived
// from the user row when available, falling back to the email prefix
// (the same fallback SendResetPasswordEmail uses).
func (s *EmailTemplateService) SendVerificationEmail(to string) error {
	subject, content, err := s.getTemplateByCode("mail_code")
	if err != nil {
		return err
	}

	verificationCode := generateVerificationCode()

	body := s.replaceVariables(content, map[string]string{
		"code":         verificationCode,
		"nickname":     resolveNicknameForEmail(to),
		"dashboardUrl": dashboardURL(),
	})

	if err := s.sendEmail(to, subject, body); err != nil {
		return err
	}

	globals.GraCache.Set(verifyCodeCacheKey(to), verificationCode, verifyCodeTTL)
	return nil
}

// resolveNicknameForEmail looks up the display name for an outgoing email
// recipient. Failures (user not found, DB hiccup) degrade to the email
// prefix so transactional sends never block on cosmetic enrichment.
func resolveNicknameForEmail(email string) string {
	var nickname string
	if err := globals.GraDBs["system"].Table("w_user").
		Select("nickname").
		Where("email = ?", email).
		Limit(1).
		Scan(&nickname).Error; err == nil && nickname != "" {
		return nickname
	}
	if at := strings.Index(email, "@"); at > 0 {
		return email[:at]
	}
	return email
}

// dashboardURL is the public dashboard URL used in transactional email
// footers. Falls back to https://workmax.app when System.FrontendURL is unset.
//
// Use this when the template has `<a href="{{dashboardUrl}}">` — i.e. when
// the placeholder IS the dashboard URL. For templates that pattern-build
// their own paths (`{{dashboardUrl}}/dashboard/order`, `{{dashboardUrl}}/dashboard`)
// use siteBaseURL() instead — those templates assume `dashboardUrl` is the
// site root, an unfortunate naming inconsistency that pre-dates this code.
func dashboardURL() string {
	base := siteBaseURL()
	if !strings.HasSuffix(base, "/") {
		base = base + "/"
	}
	return base + "dashboard"
}

// siteBaseURL returns the site root (no trailing slash, no path). Use this
// for templates that own their path layout — see the dashboardURL doc for
// the per-template breakdown.
func siteBaseURL() string {
	base := strings.TrimSpace(globals.GraConf.System.FrontendURL)
	if base == "" {
		base = "https://workmax.app"
	}
	return strings.TrimRight(base, "/")
}

// verifyCodeCacheKey is the canonical cache key for a pending email
// verification code. Lowercased so case differences in user input don't
// produce a miss; matches the lookup in the verify-email handler.
func verifyCodeCacheKey(email string) string {
	return fmt.Sprintf("verifyCode-%s", strings.ToLower(strings.TrimSpace(email)))
}

// VerifyCodeCacheKey is the exported lookup helper for callers outside this
// package (the account API consumes the code after the email round-trip).
func VerifyCodeCacheKey(email string) string {
	return verifyCodeCacheKey(email)
}

// SendResetPasswordEmail 发送重置密码邮件
func (s *EmailTemplateService) SendResetPasswordEmail(id uint, to string) error {
	subject, content, err := s.getTemplateByCode("password_reset")
	if err != nil {
		return err
	}

	// 获取用户信息
	var user struct {
		Nickname string
		Email    string
	}
	err = globals.GraDBs["system"].Table("w_user").
		Select("nickname, email").
		Where("id = ?", id).
		First(&user).Error
	if err != nil {
		// 如果获取失败，使用邮箱作为昵称
		user.Nickname = to
		user.Email = to
	}

	// CSPRNG-backed reset token. Old impl used md5(time.Now().String()) —
	// tractable for an attacker who could observe roughly when the user
	// requested the reset, since nanoseconds + monotonic only add ~10^9
	// of effective entropy.
	resetToken, err := generateResetToken()
	if err != nil {
		return err
	}
	resetLink := fmt.Sprintf("%s/reset-password/%s", globals.GraConf.System.FrontendURL, resetToken)

	// The production password_reset template body references {{resetUrl}}
	// (not {{resetLink}}) and {{dashboardUrl}}. Both `resetLink` and
	// `resetUrl` keys are populated so the substitution works regardless
	// of which placeholder a future template author chose.
	variables := map[string]string{
		"nickname":     user.Nickname,
		"email":        to,
		"resetLink":    resetLink,
		"resetUrl":     resetLink,
		"dashboardUrl": dashboardURL(),
		"expiryHours":  "24",
	}
	body := s.replaceVariables(content, variables)
	subject = s.replaceVariables(subject, variables)

	// 发送邮件
	if err := s.sendEmail(to, subject, body); err != nil {
		return err
	}

	// 缓存重置令牌
	globals.GraCache.Set(fmt.Sprintf("resetToken-%s", resetToken), id, 5*time.Minute)
	return nil
}

// SendWelcomeEmail 发送欢迎邮件
func (s *EmailTemplateService) SendWelcomeEmail(to string, nickname string) error {
	subject, content, err := s.getTemplateByCode("welcome_email")
	if err != nil {
		return err
	}

	// 准备Dashboard URL
	dashboardURL := globals.GraConf.System.FrontendURL
	if dashboardURL == "" {
		dashboardURL = "https://workmax.app"
	}
	if !strings.HasSuffix(dashboardURL, "/") {
		dashboardURL = dashboardURL + "/"
	}
	dashboardURL = dashboardURL + "dashboard"

	helpURL := globals.GraConf.System.FrontendURL + "/help"
	unsubscribeURL := globals.GraConf.System.FrontendURL + "/unsubscribe"
	privacyURL := globals.GraConf.System.FrontendURL + "/privacy"

	// 替换变量
	variables := map[string]string{
		"nickname":       nickname,
		"email":          to,
		"dashboardUrl":   dashboardURL,
		"helpUrl":        helpURL,
		"unsubscribeUrl": unsubscribeURL,
		"privacyUrl":     privacyURL,
	}
	body := s.replaceVariables(content, variables)
	subject = s.replaceVariables(subject, variables) // 替换主题中的变量

	// 发送邮件
	return s.sendEmail(to, subject, body)
}

// SendOrderEmail 发送订单确认邮件
// SendOrderEmail 发送订单确认邮件。`amount` is the already-formatted display
// string (e.g. "$19.99", "¥199.00") — callers own the currency formatting so
// this layer doesn't need a currency parameter and can't accidentally mis-fmt.
//
// Historical bug: this function used to hardcode "19.99" as the amount,
// meaning every receipt showed $19.99 regardless of what the user actually
// paid. The signature now requires the caller to pass the real amount.
func (s *EmailTemplateService) SendOrderEmail(to string, nickname string, packageName string, orderNo string, orderDate time.Time, amount string) error {
	subject, content, err := s.getTemplateByCode("payment_receipt")
	if err != nil {
		return err
	}

	// `dashboardUrl` is the site root here, not the dashboard URL — the
	// payment_receipt template builds the link itself with `{{dashboardUrl}}/dashboard/order`.
	// Using dashboardURL() here would render `https://workmax.app/dashboard/dashboard/order`.
	variables := map[string]string{
		"nickname":      nickname,
		"email":         to,
		"receiptNumber": orderNo,
		"paymentDate":   orderDate.Format("2006-01-02 15:04:05"),
		"description":   packageName,
		"paymentMethod": "Credit Card",
		"amount":        amount,
		"dashboardUrl":  siteBaseURL(),
	}
	body := s.replaceVariables(content, variables)
	subject = s.replaceVariables(subject, variables)

	// 发送邮件
	return s.sendEmail(to, subject, body)
}

// SendEmailVerificationLink 发送邮箱验证链接.
//
// The production email_verification template references {{verificationUrl}}
// (not {{verificationLink}}) and {{dashboardUrl}}. Both `verificationLink`
// and `verificationUrl` keys are populated so the substitution works
// regardless of which placeholder the template uses — same defense the
// password_reset sender applies for {{resetUrl}}/{{resetLink}}.
func (s *EmailTemplateService) SendEmailVerificationLink(to string, nickname string, verificationLink string) error {
	subject, content, err := s.getTemplateByCode("email_verification")
	if err != nil {
		return err
	}

	variables := map[string]string{
		"nickname":         nickname,
		"email":            to,
		"verificationLink": verificationLink,
		"verificationUrl":  verificationLink,
		"dashboardUrl":     dashboardURL(),
	}
	body := s.replaceVariables(content, variables)
	subject = s.replaceVariables(subject, variables)

	return s.sendEmail(to, subject, body)
}

// SendSubscriptionRenewalEmail 发送订阅续费提醒。
//
// Variables include `email` even though the original signature didn't surface
// it: the production template often references {{email}} in the footer; if
// the key isn't supplied it renders literally. Same reasoning for the
// shared dashboardURL() helper.
func (s *EmailTemplateService) SendSubscriptionRenewalEmail(to string, nickname string, planName string, amount string, renewalDate string, paymentMethod string) error {
	subject, content, err := s.getTemplateByCode("subscription_renewal")
	if err != nil {
		return err
	}

	// Same caveat as SendOrderEmail — subscription_renewal builds its own
	// path (`{{dashboardUrl}}/dashboard/order`) so dashboardUrl must be the
	// site root, not the full dashboard URL.
	variables := map[string]string{
		"nickname":      nickname,
		"email":         to,
		"planName":      planName,
		"amount":        amount,
		"renewalDate":   renewalDate,
		"paymentMethod": paymentMethod,
		"dashboardUrl":  siteBaseURL(),
	}
	body := s.replaceVariables(content, variables)
	subject = s.replaceVariables(subject, variables)

	return s.sendEmail(to, subject, body)
}

// SendSubscriptionCancellationEmail 发送订阅取消确认邮件.
//
// Pads `email` and `dashboardUrl` defensively for the same reason as
// SendSubscriptionRenewalEmail — production templates reference these
// in headers/footers and miss-keys render as literal `{{...}}`.
func (s *EmailTemplateService) SendSubscriptionCancellationEmail(to string, nickname string, expiryDate string) error {
	subject, content, err := s.getTemplateByCode("subscription_cancellation")
	if err != nil {
		return err
	}

	variables := map[string]string{
		"nickname":     nickname,
		"email":        to,
		"expiryDate":   expiryDate,
		"dashboardUrl": dashboardURL(),
	}
	body := s.replaceVariables(content, variables)
	subject = s.replaceVariables(subject, variables)

	return s.sendEmail(to, subject, body)
}

// Global instance
var EmailService = NewEmailTemplateService()

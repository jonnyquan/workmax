-- Seed the transactional email templates the auth/account flows depend on.
-- Idempotent: each INSERT is gated on `code` not already existing, so reruns
-- are safe and operators who hand-edited a template won't get clobbered.
--
-- Templates seeded:
--   mail_code         — 6-digit verification code (vars: {{code}})
--   password_reset    — reset-password link (vars: {{nickname}}, {{email}}, {{resetLink}}, {{expiryHours}})
--   email_verification — click-to-verify link variant (vars: {{nickname}}, {{verificationLink}})
--
-- Without these rows the corresponding goroutines in server/utils/email_template.go
-- silently fail with "template not found" and the user never receives an email.

INSERT INTO w_email_template
  (name, code, subject, content, category, variables, preview_text, description, status, created_at, updated_at)
SELECT
  'Email verification code',
  'mail_code',
  'Your workmax.app verification code',
  '<!doctype html><html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;max-width:560px;margin:0 auto;padding:32px 24px;color:#0f172a"><h1 style="font-size:20px;margin:0 0 16px">Verify your email</h1><p style="font-size:14px;line-height:1.6;margin:0 0 24px">Use the code below to confirm your email address. It expires in 5 minutes.</p><p style="font-size:32px;font-weight:700;letter-spacing:8px;text-align:center;background:#f1f5f9;border-radius:12px;padding:20px;margin:0 0 24px">{{code}}</p><p style="font-size:12px;color:#64748b;line-height:1.6;margin:0">Didn''t request this? You can safely ignore this email.</p></body></html>',
  'transactional',
  '{"code":"6-digit verification code"}',
  'Your workmax.app verification code',
  'Sent on signup and on resend; consumed by /api/account/verify-email.',
  1,
  NOW(),
  NOW()
WHERE NOT EXISTS (SELECT 1 FROM w_email_template WHERE code = 'mail_code');

INSERT INTO w_email_template
  (name, code, subject, content, category, variables, preview_text, description, status, created_at, updated_at)
SELECT
  'Password reset',
  'password_reset',
  'Reset your workmax.app password',
  '<!doctype html><html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;max-width:560px;margin:0 auto;padding:32px 24px;color:#0f172a"><h1 style="font-size:20px;margin:0 0 16px">Reset your password</h1><p style="font-size:14px;line-height:1.6;margin:0 0 16px">Hi {{nickname}},</p><p style="font-size:14px;line-height:1.6;margin:0 0 24px">We received a request to reset the password for <strong>{{email}}</strong>. Click the button below to set a new one. The link expires in {{expiryHours}} hours.</p><p style="text-align:center;margin:0 0 24px"><a href="{{resetLink}}" style="display:inline-block;background:#0f172a;color:#ffffff;text-decoration:none;padding:12px 24px;border-radius:9999px;font-weight:600">Reset password</a></p><p style="font-size:12px;color:#64748b;line-height:1.6;margin:0">If you didn''t request a password reset you can safely ignore this email — your password won''t change.</p></body></html>',
  'transactional',
  '{"nickname":"display name","email":"account email","resetLink":"reset URL","expiryHours":"link TTL in hours"}',
  'Reset your workmax.app password',
  'Sent by /api/auth/forgot-password; consumed by /api/auth/change-password.',
  1,
  NOW(),
  NOW()
WHERE NOT EXISTS (SELECT 1 FROM w_email_template WHERE code = 'password_reset');

INSERT INTO w_email_template
  (name, code, subject, content, category, variables, preview_text, description, status, created_at, updated_at)
SELECT
  'Email verification link',
  'email_verification',
  'Confirm your workmax.app email',
  '<!doctype html><html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;max-width:560px;margin:0 auto;padding:32px 24px;color:#0f172a"><h1 style="font-size:20px;margin:0 0 16px">Confirm your email</h1><p style="font-size:14px;line-height:1.6;margin:0 0 16px">Hi {{nickname}},</p><p style="font-size:14px;line-height:1.6;margin:0 0 24px">Click the button below to confirm this is your email address.</p><p style="text-align:center;margin:0 0 24px"><a href="{{verificationLink}}" style="display:inline-block;background:#0f172a;color:#ffffff;text-decoration:none;padding:12px 24px;border-radius:9999px;font-weight:600">Confirm email</a></p><p style="font-size:12px;color:#64748b;line-height:1.6;margin:0">If you didn''t create a workmax.app account, ignore this email.</p></body></html>',
  'transactional',
  '{"nickname":"display name","verificationLink":"confirm URL"}',
  'Confirm your workmax.app email',
  'Reserved for the link-based verification variant; not used by the current 6-digit code flow.',
  1,
  NOW(),
  NOW()
WHERE NOT EXISTS (SELECT 1 FROM w_email_template WHERE code = 'email_verification');

-- Add an email template for admin-granted credits packs so the admin console
-- can notify users when credits are manually issued.

INSERT INTO `w_email_template` (
    `name`,
    `code`,
    `subject`,
    `content`,
    `category`,
    `variables`,
    `preview_text`,
    `description`,
    `status`,
    `created_at`,
    `updated_at`
)
VALUES (
    'Admin Credits Pack Granted',
    'admin_credits_pack_granted',
    '{{creditsGranted}} credits were added to your WorkMax account',
    '<!DOCTYPE html>\n<html>\n<head>\n    <meta charset="UTF-8">\n    <title>Credits Added</title>\n</head>\n<body style="margin:0;padding:0;background:#f4f7fb;font-family:Arial,sans-serif;color:#111827;">\n    <div style="max-width:640px;margin:0 auto;padding:32px 16px;">\n        <div style="background:#ffffff;border-radius:18px;overflow:hidden;box-shadow:0 10px 30px rgba(15,23,42,0.08);">\n            <div style="background:linear-gradient(135deg,#111827,#334155);padding:36px 32px;">\n                <p style="margin:0;color:#cbd5e1;font-size:13px;letter-spacing:0.08em;text-transform:uppercase;">WorkMax</p>\n                <h1 style="margin:10px 0 0;color:#ffffff;font-size:28px;line-height:1.2;">Credits Added To Your Account</h1>\n            </div>\n            <div style="padding:32px;">\n                <p style="margin:0 0 14px;font-size:16px;line-height:1.7;">Hi {{nickname}},</p>\n                <p style="margin:0 0 24px;font-size:15px;line-height:1.8;color:#4b5563;">We\'ve added <strong>{{creditsGranted}} credits</strong> to your account. You can use them immediately in your video and creative workflows.</p>\n                <table style="width:100%;border-collapse:collapse;background:#f8fafc;border:1px solid #e5e7eb;border-radius:14px;overflow:hidden;">\n                    <tr><td style="padding:12px 16px;color:#6b7280;">Credits added</td><td style="padding:12px 16px;color:#111827;text-align:right;font-weight:700;">{{creditsGranted}}</td></tr>\n                    <tr><td style="padding:12px 16px;color:#6b7280;">Credits remaining</td><td style="padding:12px 16px;color:#111827;text-align:right;font-weight:700;">{{creditsRemaining}}</td></tr>\n                    <tr><td style="padding:12px 16px;color:#6b7280;">Grant reference</td><td style="padding:12px 16px;color:#111827;text-align:right;">{{sourceId}}</td></tr>\n                    <tr><td style="padding:12px 16px;color:#6b7280;">Expires at</td><td style="padding:12px 16px;color:#111827;text-align:right;">{{expiresAt}}</td></tr>\n                    <tr><td style="padding:12px 16px;color:#6b7280;">Remark</td><td style="padding:12px 16px;color:#111827;text-align:right;">{{remark}}</td></tr>\n                </table>\n                <div style="margin-top:28px;">\n                    <a href="{{dashboardUrl}}/dashboard" style="display:inline-block;background:#111827;color:#ffffff;text-decoration:none;padding:14px 22px;border-radius:12px;font-weight:700;">Open Dashboard</a>\n                </div>\n            </div>\n        </div>\n    </div>\n</body>\n</html>',
    'notification',
    '{"nickname":"User nickname","email":"User email","creditsGranted":"Granted credits","creditsRemaining":"Remaining credits after the grant","sourceId":"Grant reference id","expiresAt":"Expiration time or Never expires","remark":"Admin remark","dashboardUrl":"Frontend base URL"}',
    'Admin granted a credits pack to this user',
    'Notification email sent after an admin manually grants a credits pack',
    1,
    NOW(),
    NOW()
)
ON DUPLICATE KEY UPDATE
    `name` = VALUES(`name`),
    `subject` = VALUES(`subject`),
    `content` = VALUES(`content`),
    `category` = VALUES(`category`),
    `variables` = VALUES(`variables`),
    `preview_text` = VALUES(`preview_text`),
    `description` = VALUES(`description`),
    `status` = VALUES(`status`),
    `updated_at` = NOW();

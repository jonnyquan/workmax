-- Strip the hardcoded `$` prefix from the payment_receipt template's
-- {{amount}} placeholder. The Go-side SendOrderEmail now passes a fully
-- formatted, currency-aware amount string (via formatInvoiceAmount), so the
-- template must NOT prepend its own currency symbol — otherwise USD users
-- see "$$19.99", EUR users see "$€19.99", JPY users see "$¥1999".
--
-- Idempotent: REPLACE is a no-op after the first run, and the WHERE clause
-- selects on the un-replaced literal so subsequent runs touch zero rows.

UPDATE w_email_template
SET content = REPLACE(content, '${{amount}}', '{{amount}}'),
    updated_at = NOW()
WHERE code = 'payment_receipt'
  AND content LIKE '%${{amount}}%';

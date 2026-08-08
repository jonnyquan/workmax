-- Defense-in-depth CHECK constraint on w_credits_pack.
--
-- ReserveCreditsDetailedTx already guards via
--   WHERE credits_used + ? <= credits_total
-- and RefundAllocationsTx via
--   WHERE credits_used >= ?
-- so reservations + refunds can't violate this in normal flow. The DB-level
-- constraint is a safety net for any future code path (admin tool,
-- migration, manual fix) that bypasses those guards. Without it, a buggy
-- write that pushed credits_used past credits_total would be silently
-- absorbed by the `if remaining < 0 { remaining = 0 }` clamp in
-- GetBalanceTx — the symptom hidden, the bug invisible until accounting
-- tries to reconcile.
--
-- Pre-flight verified zero violations on prod data:
--   SELECT COUNT(*) FROM w_credits_pack
--    WHERE credits_used < 0 OR credits_used > credits_total;  -- 0
--
-- MySQL 8 enforces CHECK constraints (unlike <8 which parsed and ignored).

ALTER TABLE `w_credits_pack`
  ADD CONSTRAINT `chk_credits_used_bounds`
  CHECK (`credits_used` >= 0 AND `credits_used` <= `credits_total`);

-- Retire w_user_quota — the sole schema change needed for the
-- quota/credits consolidation (w_user is untouched).
--
-- Precondition:
--   All application code in this branch is live. Go code has no remaining
--   reads or writes against w_user_quota; everything goes through
--   w_credits_pack (credits) and w_user (membership).
--
-- Two steps in one shot:
--   (A) Legacy safety net: any user who still has a positive credits balance
--       recorded only in w_user_quota (credits_total > 0 and no matching
--       w_credits_pack rows) gets a synthesized bonus pack. Keeps the
--       "w_credits_pack is the sole source of truth" invariant from
--       silently zeroing legacy accounts when the table is renamed.
--   (B) RENAME (not DROP) so any missed code path fails loudly. After ~2
--       weeks of clean production signal, run DROP TABLE w_user_quota_bak.
--
-- Rollback:
--   Within 24h — RENAME TABLE w_user_quota_bak TO w_user_quota;
--   Long-term rollback not supported (all read/write code has been removed).

-- ---- (A) Legacy credits safety net ----
INSERT INTO w_credits_pack (uid, source_type, source_id, credits_total, credits_used, expires_at, remark, created_at, updated_at)
SELECT q.uid,
       'bonus',
       'legacy-bootstrap',
       q.credits_total,
       LEAST(q.credits_used, q.credits_total),
       NULL,
       'migrated from w_user_quota',
       NOW(),
       NOW()
FROM w_user_quota q
LEFT JOIN (
    SELECT uid, COUNT(*) AS pack_count
    FROM w_credits_pack
    GROUP BY uid
) p ON p.uid = q.uid
WHERE q.credits_total > 0
  AND COALESCE(p.pack_count, 0) = 0;

-- ---- (B) Rename for observation ----
RENAME TABLE w_user_quota TO w_user_quota_bak;

-- After ~2 weeks of clean production signal:
--   DROP TABLE w_user_quota_bak;

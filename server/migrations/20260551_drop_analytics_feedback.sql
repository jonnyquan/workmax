-- Drops the M4-W13-01 effect-feedback foundation tables.
--
-- The pair (w_publication + w_performance_metric) shipped only as a
-- write-side stub: a customer-webhook endpoint
-- (POST /api/callback/feedback/conversions) and idempotent upsert
-- service. There were no readers — no platform pullers, no insight
-- surface, no dashboard — and no production data flowed through.
-- Phase 3's full design (analytics-feedback-loop-design.md) will
-- re-introduce a richer schema when it ships, so the half-built stub
-- is removed rather than carried.
--
-- Removed in the same change:
--   - server/migrations/20260425_add_analytics_feedback.sql
--   - server/model/{publication,performance_metric}.go
--   - server/service/analytics/ (whole package)
--   - server/api/callback/feedback_callback_api{,_test}.go
--   - server/api/callback/enter.go         FeedbackCallbackApi field
--   - server/router/callback/callback_router.go
--                                         /feedback/conversions route
--   - server/initialize/gorm.go           AutoMigrate registration
--   - server/utils/testutil/testdb.go     SQLite test schema
--
-- IF EXISTS keeps fresh environments (which never created the tables)
-- happy. Drop order: child (metric) before parent (publication).

DROP TABLE IF EXISTS `w_performance_metric`;
DROP TABLE IF EXISTS `w_publication`;

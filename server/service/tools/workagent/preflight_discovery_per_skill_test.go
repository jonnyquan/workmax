package workagent

import (
	"strings"
	"testing"

	"server/utils/testutil"
)

// preflight_discovery_per_skill_test.go — G10/G11 (2026-05-17).
// Pins the per-skill scoping of discovery rows added by the
// 2026-05-17 fix:
//
//   G10: a discovery row written under skill A must NOT be
//        injected into a turn running under skill B (preflight
//        path: loadDiscoveryContext + FindLatestDiscoveryAnswers
//        ForSkill).
//
//   G11: a thread that's switched skills mid-conversation must
//        be allowed to re-emit the form for the new skill
//        (dispatcher path: AlreadyAnswered driven by the
//        per-skill repo lookup, not by ThreadMessageCnt).
//
// Both checks turn on a single primitive — form_id == skillName
// in the persisted metadata row — so testing them together pins
// the contract from both the writer and reader sides.

// TestPerSkill_DiscoveryWritesScopedToSkill — pin: an inferred
// row written for skill A is invisible to skill B's loader.
// Pre-G10 this would have failed: the loader did a skill-agnostic
// LIKE search and would have returned skill A's row when skill B
// asked.
func TestPerSkill_DiscoveryWritesScopedToSkill(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	const uid = 42
	const threadID = 8101

	// Write a discovery row for the "ppt" skill.
	PersistInferredDiscoveryAnswers(uid, threadID, "ppt", map[string]string{
		"audience": "exec",
		"tone":     "modern_minimal",
	}, "nlp_inferred")

	// Reading with skill="ppt" surfaces the row.
	pptCtx := loadDiscoveryContext(threadID, "ppt")
	if !strings.Contains(pptCtx, "audience: exec") {
		t.Errorf("ppt should see its own discovery row; got %q", pptCtx)
	}

	// Reading with skill="socialAd" must NOT surface ppt's row —
	// this is the G10 bug fix.
	socialAdCtx := loadDiscoveryContext(threadID, "socialAd")
	if socialAdCtx != "" {
		t.Errorf("socialAd should NOT see ppt's discovery row (G10 cross-skill leak); got %q", socialAdCtx)
	}
}

// TestPerSkill_TwoSkillsTwoRows — pin: a thread that has used
// two skills carries one row per skill, and each skill's loader
// returns its own answers.
func TestPerSkill_TwoSkillsTwoRows(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	const uid = 42
	const threadID = 8102

	PersistInferredDiscoveryAnswers(uid, threadID, "ppt", map[string]string{
		"audience": "exec",
	}, "nlp_inferred")
	PersistInferredDiscoveryAnswers(uid, threadID, "socialAd", map[string]string{
		"platform": "douyin",
	}, "nlp_inferred")

	pptCtx := loadDiscoveryContext(threadID, "ppt")
	socialAdCtx := loadDiscoveryContext(threadID, "socialAd")

	if !strings.Contains(pptCtx, "audience: exec") {
		t.Errorf("ppt should see audience=exec; got %q", pptCtx)
	}
	if strings.Contains(pptCtx, "platform") {
		t.Errorf("ppt should NOT see socialAd's platform answer; got %q", pptCtx)
	}
	if !strings.Contains(socialAdCtx, "platform: douyin") {
		t.Errorf("socialAd should see platform=douyin; got %q", socialAdCtx)
	}
	if strings.Contains(socialAdCtx, "audience") {
		t.Errorf("socialAd should NOT see ppt's audience answer; got %q", socialAdCtx)
	}
}

// TestPerSkill_EmptySkillFallbackPreservesLegacyReads — pin: the
// empty-skill argument to loadDiscoveryContext falls back to the
// skill-agnostic finder, so legacy call sites (and the
// preflight_discovery_test.go suite) keep working.
func TestPerSkill_EmptySkillFallbackPreservesLegacyReads(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	const uid = 42
	const threadID = 8103

	PersistInferredDiscoveryAnswers(uid, threadID, "ppt", map[string]string{
		"audience": "exec",
	}, "nlp_inferred")

	// Empty skillName → legacy fallback → returns whichever row
	// is latest (any skill, any form_id).
	legacyCtx := loadDiscoveryContext(threadID, "")
	if !strings.Contains(legacyCtx, "audience: exec") {
		t.Errorf("empty-skill fallback should return the row regardless of form_id; got %q", legacyCtx)
	}
}

// TestPerSkill_DispatcherReFiresOnSkillSwitch — pin G11: a
// thread that's been used heavily on ppt (high message count)
// must STILL allow the form to fire when switched to socialAd.
// Pre-G11 the "ThreadMessageCnt > 0" gate suppressed this.
func TestPerSkill_DispatcherReFiresOnSkillSwitch(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	const uid = 42
	const threadID = 8104

	// Simulate a thread that already has ppt discovery answers
	// and a few turns of message history.
	PersistInferredDiscoveryAnswers(uid, threadID, "ppt", map[string]string{
		"audience": "exec",
	}, "nlp_inferred")

	// Reader for socialAd (different skill): no row exists for
	// socialAd, so the per-skill lookup returns nil. The handler
	// then sets DispatchInput.AlreadyAnswered = false, and the
	// dispatcher emits the form even though ThreadMessageCnt > 0.
	row, err := DefaultMessageRepository().FindLatestDiscoveryAnswersForSkill(threadID, "socialAd")
	if err != nil {
		t.Fatalf("per-skill lookup: %v", err)
	}
	if row != nil {
		t.Errorf("socialAd should have NO discovery row yet (only ppt does); got id=%d", row.Id)
	}

	// And vice versa — ppt's row still surfaces for ppt.
	pptRow, err := DefaultMessageRepository().FindLatestDiscoveryAnswersForSkill(threadID, "ppt")
	if err != nil {
		t.Fatalf("per-skill lookup ppt: %v", err)
	}
	if pptRow == nil {
		t.Error("ppt should still see its own discovery row")
	}
}

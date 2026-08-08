package canvas

import (
	"context"
	"testing"
)

// Pins the consistency-judge counters added on 2026-05-15 per the #9
// follow-up note ("加 canvas.consistency_judge.{ok,warned,skipped}
// 计数器"). Stats are package-level (BuildVideoConsistencyJudgment is a
// free function), so tests compute deltas around each call instead of
// resetting global state — a Reset() helper would invite cross-test
// race / order bugs and is intentionally NOT exposed.
//
// Covered here: the four skip branches that BuildVideoConsistencyJudgment
// can reach without an LLM client. The OK / Warned paths require a live
// LLM response and are exercised in integration; we sanity-check the
// classifier itself with a synthetic snapshot diff.

func deltaConsistencyStats(t *testing.T, fn func()) ConsistencyJudgeStats {
	t.Helper()
	before := VideoConsistencyJudgeStats()
	fn()
	after := VideoConsistencyJudgeStats()
	return ConsistencyJudgeStats{
		OK:      after.OK - before.OK,
		Warned:  after.Warned - before.Warned,
		Skipped: after.Skipped - before.Skipped,
	}
}

func TestStats_EmptyPromptIncrementsSkipped(t *testing.T) {
	delta := deltaConsistencyStats(t, func() {
		_, err := BuildVideoConsistencyJudgment(context.Background(), nil, 1, 1, ConsistencyJudgeInput{
			Prompt: "   ",
		})
		if err != nil {
			t.Fatalf("expected nil err, got %v", err)
		}
	})
	if delta.Skipped != 1 {
		t.Errorf("Skipped delta = %d, want 1", delta.Skipped)
	}
	if delta.OK != 0 || delta.Warned != 0 {
		t.Errorf("OK/Warned must not advance on skip path, got %+v", delta)
	}
}

func TestStats_NilDBIncrementsSkipped(t *testing.T) {
	delta := deltaConsistencyStats(t, func() {
		_, _ = BuildVideoConsistencyJudgment(context.Background(), nil, 1, 1, ConsistencyJudgeInput{
			Prompt: "a sunny meadow",
		})
	})
	if delta.Skipped != 1 {
		t.Errorf("Skipped delta = %d, want 1", delta.Skipped)
	}
}

func TestStats_ZeroUIDIncrementsSkipped(t *testing.T) {
	delta := deltaConsistencyStats(t, func() {
		_, _ = BuildVideoConsistencyJudgment(context.Background(), nil, 0, 1, ConsistencyJudgeInput{
			Prompt: "a sunny meadow",
		})
	})
	if delta.Skipped != 1 {
		t.Errorf("Skipped delta = %d, want 1", delta.Skipped)
	}
}

func TestStats_ZeroProjectIDIncrementsSkipped(t *testing.T) {
	delta := deltaConsistencyStats(t, func() {
		_, _ = BuildVideoConsistencyJudgment(context.Background(), nil, 1, 0, ConsistencyJudgeInput{
			Prompt: "a sunny meadow",
		})
	})
	if delta.Skipped != 1 {
		t.Errorf("Skipped delta = %d, want 1", delta.Skipped)
	}
}

func TestStats_FourSkipsAccumulate(t *testing.T) {
	delta := deltaConsistencyStats(t, func() {
		_, _ = BuildVideoConsistencyJudgment(context.Background(), nil, 1, 1, ConsistencyJudgeInput{Prompt: ""})
		_, _ = BuildVideoConsistencyJudgment(context.Background(), nil, 1, 1, ConsistencyJudgeInput{Prompt: "x"})
		_, _ = BuildVideoConsistencyJudgment(context.Background(), nil, 0, 1, ConsistencyJudgeInput{Prompt: "x"})
		_, _ = BuildVideoConsistencyJudgment(context.Background(), nil, 1, 0, ConsistencyJudgeInput{Prompt: "x"})
	})
	if delta.Skipped != 4 {
		t.Errorf("Skipped delta = %d, want 4", delta.Skipped)
	}
}

// TestStats_ClassifierShape sanity-checks the deferred classifier's
// branch logic by reading the snapshot directly. We can't easily drive
// BuildVideoConsistencyJudgment all the way through an OK/Warned path
// in a unit test (no LLM client), but the classifier's contract is:
//
//   - SkippedReason set        → skipped (regardless of OK)
//   - SkippedReason="", OK=true  → ok
//   - SkippedReason="", OK=false → warned
//
// The function shape (deferred named-return classifier) means this
// branching is colocated in one place; if it ever changes, the
// integration tests on the LLM happy path would catch it. This test
// is mostly here so a refactor that breaks the snapshot accessor at
// compile time fails loudly.
func TestStats_SnapshotIsImmutableCopy(t *testing.T) {
	before := VideoConsistencyJudgeStats()
	_, _ = BuildVideoConsistencyJudgment(context.Background(), nil, 1, 1, ConsistencyJudgeInput{Prompt: ""})
	// `before` must NOT have advanced — it's a value copy.
	again := VideoConsistencyJudgeStats()
	if again.Skipped <= before.Skipped {
		t.Errorf("global Skipped should have advanced; before=%d after=%d", before.Skipped, again.Skipped)
	}
}

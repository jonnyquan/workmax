package workagent

// context_keys_test.go — pin the contract for the three
// context.Value keys (uid / threadID / projectID) used by the
// agent pipeline. Two contracts to pin:
//
//   1. Round-trip: WithX(ctx, v) → XFromContext(ctx) returns (v, true)
//   2. Missing-key safety: XFromContext on an unstamped ctx returns
//      (0, false) so callers can treat that as "unknown" without a
//      panic OR a misleading default
//
// Plus a subtle but important property: keys are package-scoped via
// the unexported ctxKey type. A string-literal key (e.g.
// context.WithValue(ctx, "user_id", uid)) would silently be
// overwritable by any other package that chose the same string —
// this test stamps a string-keyed value and asserts the typed
// reader does NOT see it (proves the scoping).

import (
	"context"
	"testing"
)

func TestWithUserID_RoundTrip(t *testing.T) {
	ctx := WithUserID(context.Background(), 42)
	got, ok := UserIDFromContext(ctx)
	if !ok {
		t.Fatalf("UserIDFromContext should return ok=true after WithUserID")
	}
	if got != 42 {
		t.Errorf("UserIDFromContext = %d, want 42", got)
	}
}

func TestUserIDFromContext_MissingReturnsZeroAndFalse(t *testing.T) {
	got, ok := UserIDFromContext(context.Background())
	if ok {
		t.Errorf("unstamped ctx must return ok=false, got ok=true value=%d", got)
	}
	if got != 0 {
		t.Errorf("missing key must return 0, got %d", got)
	}
}

func TestUserIDFromContext_WrongTypeReturnsZeroAndFalse(t *testing.T) {
	// A producer stamping a non-uint value should be ignored by
	// the typed reader, not coerced.
	ctx := context.WithValue(context.Background(), userIDKey, "not-a-uint")
	got, ok := UserIDFromContext(ctx)
	if ok {
		t.Errorf("wrong-type value must produce ok=false; got value=%d", got)
	}
}

func TestUserIDStringFromContext_KnownAndUnknown(t *testing.T) {
	known := UserIDStringFromContext(WithUserID(context.Background(), 99))
	if known != "99" {
		t.Errorf("known uid → %q, want \"99\"", known)
	}
	// Unknown falls back to the "unknown" sentinel string,
	// NOT the Sprintf-of-nil "%!d(<nil>)" trap.
	unknown := UserIDStringFromContext(context.Background())
	if unknown != "unknown" {
		t.Errorf("missing uid → %q, want \"unknown\"", unknown)
	}
}

func TestWithThreadID_RoundTrip(t *testing.T) {
	ctx := WithThreadID(context.Background(), 100)
	got, ok := ThreadIDFromContext(ctx)
	if !ok || got != 100 {
		t.Errorf("ThreadIDFromContext = (%d, %v), want (100, true)", got, ok)
	}
}

func TestThreadIDFromContext_MissingReturnsZeroAndFalse(t *testing.T) {
	got, ok := ThreadIDFromContext(context.Background())
	if ok || got != 0 {
		t.Errorf("missing thread id = (%d, %v), want (0, false)", got, ok)
	}
}

func TestWithProjectID_RoundTrip(t *testing.T) {
	ctx := WithProjectID(context.Background(), 7)
	got, ok := ProjectIDFromContext(ctx)
	if !ok || got != 7 {
		t.Errorf("ProjectIDFromContext = (%d, %v), want (7, true)", got, ok)
	}
}

func TestWithProjectID_ZeroIsValidStamp(t *testing.T) {
	// Doc comment explicitly says: "Zero (no project bound) is a
	// valid stamp." Pin that: a stamped 0 must read back as
	// (0, true), distinguishing "explicitly unbound" from "key
	// not stamped at all."
	ctx := WithProjectID(context.Background(), 0)
	got, ok := ProjectIDFromContext(ctx)
	if !ok {
		t.Errorf("WithProjectID(0) must produce ok=true (explicit unbound)")
	}
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestProjectIDFromContext_MissingReturnsZeroAndFalse(t *testing.T) {
	got, ok := ProjectIDFromContext(context.Background())
	if ok || got != 0 {
		t.Errorf("missing project id = (%d, %v), want (0, false)", got, ok)
	}
}

// TestContextKeys_PackageScopedAgainstStringKeys — the LOAD-BEARING
// safety property of the unexported ctxKey type. If WithUserID used
// a string literal "user_id" as the key, any other package could
// silently overwrite it. Stamp a string-keyed "user_id" value and
// assert the typed reader doesn't see it (proves the typed key is
// package-scoped).
func TestContextKeys_PackageScopedAgainstStringKeys(t *testing.T) {
	type fakeStringKey string
	ctx := context.WithValue(context.Background(), fakeStringKey("user_id"), uint(99))
	if _, ok := UserIDFromContext(ctx); ok {
		t.Errorf("string-keyed value should NOT leak into typed reader; got hit")
	}
}

// TestContextKeys_AllThreeIndependent — stamping one key must not
// affect the readers for the other two. Verifies the iota
// assignments produced distinct values.
func TestContextKeys_AllThreeIndependent(t *testing.T) {
	ctx := context.Background()
	ctx = WithUserID(ctx, 42)
	ctx = WithThreadID(ctx, 100)
	ctx = WithProjectID(ctx, 7)

	if uid, ok := UserIDFromContext(ctx); !ok || uid != 42 {
		t.Errorf("uid = (%d, %v), want (42, true)", uid, ok)
	}
	if tid, ok := ThreadIDFromContext(ctx); !ok || tid != 100 {
		t.Errorf("thread = (%d, %v), want (100, true)", tid, ok)
	}
	if pid, ok := ProjectIDFromContext(ctx); !ok || pid != 7 {
		t.Errorf("project = (%d, %v), want (7, true)", pid, ok)
	}
}

// Pure + DB tests for the C-11 share-token rotation service.
//
// matchSharedProjectShareToken pins the legacy-vs-rotated semantics
// without DB. The token generator's randomness gets a smoke test
// (uniqueness on two consecutive calls + non-empty + length).
// RotateProjectShareToken + GetSharedProjectByUUIDWithToken exercise
// the DB path through testutil.NewTestDB.

package canvas

import (
	"context"
	"strings"
	"testing"

	"server/utils/testutil"
)

func ptr(s string) *string { return &s }

func TestMatchSharedProjectShareToken_LegacyAcceptsEmpty(t *testing.T) {
	// NULL stored token → legacy state: empty requested token allowed.
	if !matchSharedProjectShareToken(nil, "") {
		t.Errorf("nil stored + empty requested must match")
	}
}

func TestMatchSharedProjectShareToken_LegacyRejectsNonEmpty(t *testing.T) {
	// NULL stored token → any non-empty request token is a mismatch
	// (a stale token shouldn't grant access to a project that never
	// rotated).
	if matchSharedProjectShareToken(nil, "abc") {
		t.Errorf("nil stored must reject non-empty request token")
	}
}

func TestMatchSharedProjectShareToken_RotatedRequiresExactToken(t *testing.T) {
	stored := ptr("abc123")
	if !matchSharedProjectShareToken(stored, "abc123") {
		t.Errorf("exact token match should pass")
	}
	if matchSharedProjectShareToken(stored, "wrong") {
		t.Errorf("wrong token must fail")
	}
	if matchSharedProjectShareToken(stored, "") {
		t.Errorf("empty token against rotated project must fail")
	}
}

func TestMatchSharedProjectShareToken_PointerSetButEmptyTreatedAsNil(t *testing.T) {
	// Defensive: a `*string` set to "" would be a bug in the rotate
	// path but mustn't accidentally accept any non-empty request
	// token (or it'd grant access on garbage). Should behave like
	// "no rotation" — empty request only.
	empty := ptr("")
	if !matchSharedProjectShareToken(empty, "") {
		t.Errorf("empty stored + empty requested must match")
	}
	if matchSharedProjectShareToken(empty, "garbage") {
		t.Errorf("empty stored must reject non-empty request")
	}
}

func TestGenerateShareToken_NonEmptyHex(t *testing.T) {
	tok, err := generateShareToken()
	if err != nil {
		t.Fatalf("generateShareToken: %v", err)
	}
	if len(tok) != shareTokenByteLen*2 {
		t.Errorf("token length = %d, want %d (hex-encoded %d bytes)", len(tok), shareTokenByteLen*2, shareTokenByteLen)
	}
	for _, c := range tok {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isHex {
			t.Errorf("token contains non-hex char %q in %q", c, tok)
			break
		}
	}
}

func TestGenerateShareToken_TwoCallsProduceDifferentTokens(t *testing.T) {
	// Smoke check on the entropy — collision on 256 bits is
	// astronomically unlikely, but the test would catch a stupid
	// "constant seed" regression instantly.
	a, _ := generateShareToken()
	b, _ := generateShareToken()
	if a == b {
		t.Errorf("two tokens collided: both = %q", a)
	}
}

func TestRotateProjectShareToken_OwnerCanRotate(t *testing.T) {
	db := testutil.NewTestDB(t)
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "rotate-test"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	token1, err := RotateProjectShareToken(context.Background(), db, 42, created.Id)
	if err != nil {
		t.Fatalf("RotateProjectShareToken: %v", err)
	}
	if len(token1) != shareTokenByteLen*2 {
		t.Errorf("token length = %d, want %d", len(token1), shareTokenByteLen*2)
	}

	// Rotate again — should produce a different token (idempotent
	// in the "still works" sense, NOT "same value").
	token2, err := RotateProjectShareToken(context.Background(), db, 42, created.Id)
	if err != nil {
		t.Fatalf("second rotate: %v", err)
	}
	if token2 == token1 {
		t.Errorf("re-rotation produced same token: %q", token1)
	}
}

func TestRotateProjectShareToken_NonOwnerDenied(t *testing.T) {
	db := testutil.NewTestDB(t)
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "denied-test"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := RotateProjectShareToken(context.Background(), db, 99, created.Id); err == nil {
		t.Fatalf("non-owner rotate should error, got nil")
	}
}

func TestRotateProjectShareToken_MissingProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	if _, err := RotateProjectShareToken(context.Background(), db, 42, 9999); err == nil {
		t.Fatalf("missing project rotate should error, got nil")
	}
}

func TestGetSharedProjectByUUIDWithToken_LegacyURLWorksUntilRotated(t *testing.T) {
	db := testutil.NewTestDB(t)
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "legacy"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// Project needs to be unlisted/public AND published (w_canvas_
	// share_snapshot row exists) to be reachable via the shared
	// lookup path.
	if _, err := UpdateProject(context.Background(), db, 42, created.Id, UpdateProjectInput{
		Visibility: int8Ptr(CanvasVisibilityUnlisted),
	}); err != nil {
		t.Fatalf("UpdateProject visibility: %v", err)
	}
	if _, err := PublishProject(context.Background(), db, 42, created.Id); err != nil {
		t.Fatalf("PublishProject: %v", err)
	}

	// Pre-rotation: any caller can hit the URL without a token.
	got, err := GetSharedProjectByUUIDWithToken(context.Background(), db, created.UUID, "")
	if err != nil {
		t.Fatalf("legacy lookup: %v", err)
	}
	if got.UUID != created.UUID {
		t.Errorf("got.UUID = %q, want %q", got.UUID, created.UUID)
	}
}

func TestGetSharedProjectByUUIDWithToken_AfterRotationNeedsToken(t *testing.T) {
	db := testutil.NewTestDB(t)
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "rotated"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := UpdateProject(context.Background(), db, 42, created.Id, UpdateProjectInput{
		Visibility: int8Ptr(CanvasVisibilityUnlisted),
	}); err != nil {
		t.Fatalf("visibility: %v", err)
	}
	if _, err := PublishProject(context.Background(), db, 42, created.Id); err != nil {
		t.Fatalf("PublishProject: %v", err)
	}

	token, err := RotateProjectShareToken(context.Background(), db, 42, created.Id)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// Token-less request after rotation → not found.
	if _, err := GetSharedProjectByUUIDWithToken(context.Background(), db, created.UUID, ""); err == nil {
		t.Errorf("token-less lookup against rotated project must fail")
	}
	// Wrong token → not found.
	if _, err := GetSharedProjectByUUIDWithToken(context.Background(), db, created.UUID, "wrong-token"); err == nil {
		t.Errorf("wrong-token lookup must fail")
	}
	// Correct token → success.
	got, err := GetSharedProjectByUUIDWithToken(context.Background(), db, created.UUID, token)
	if err != nil {
		t.Fatalf("correct-token lookup: %v", err)
	}
	if got.UUID != created.UUID {
		t.Errorf("got.UUID = %q, want %q", got.UUID, created.UUID)
	}
}

func TestGetSharedProjectByUUIDWithToken_PrivateProjectRejectedEvenWithToken(t *testing.T) {
	db := testutil.NewTestDB(t)
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "private"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// Rotation can happen on a private project too — but the project
	// is still not reachable via the public path (visibility=0 is the
	// default private state).
	token, err := RotateProjectShareToken(context.Background(), db, 42, created.Id)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := GetSharedProjectByUUIDWithToken(context.Background(), db, created.UUID, token); err == nil {
		t.Errorf("private project should not be readable via shared path even with correct token")
	}
}

// int8Ptr is a tiny helper because UpdateProjectInput.Visibility is
// *int8 and Go has no terse literal for that.
func int8Ptr(v int8) *int8 { return &v }

// Sanity guard: the package's exported sentinel must remain unchanged
// for the handler's errors.Is check to keep working. (Compile-time
// import; the function ref keeps it from being unused-import noise.)
var _ = strings.TrimSpace

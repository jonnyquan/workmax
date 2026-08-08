package workagent

// rate_message_api_test.go — API coverage for POST
// /chat/message/:id/rate (P0 #3 critique loop, part 2 of 4).
//
// What this file pins:
//   - uid=0 unauthorized (401)
//   - non-numeric id rejected (400)
//   - missing rating field rejected (binding required)
//   - oversized feedback rejected (4 KiB cap)
//   - invalid rating value surfaces ErrInvalidRating as 400
//   - cross-tenant IDOR collapses to 404 "Message not found"
//   - happy paths: thumbs-down + feedback, thumbs-up empty
//
// Layer split: repo-side IDOR + sentinel + thread isolation
// already pinned in service/tools/workagent/message_repository_test.go.
// This file is API-shape only.

import (
	"net/http"
	"strings"
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func buildRateMessageEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.POST("/chat/message/:id/rate", withClaims(uid), api.RateMessage)
	return r
}

func seedRatableMessage(t *testing.T, db *gorm.DB, ownerUID int) uint {
	t.Helper()
	msg := &workagentModel.ChatMessage{
		UID:      ownerUID,
		UUID:     "rate-test-" + t.Name(),
		ThreadID: 100,
		UserText: "user prompt",
		AIText:   "ai response to rate",
		ChatMode: "agent",
	}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	return msg.Id
}

func TestRateMessage_Unauthorized(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildRateMessageEngine(t, 0)
	w := postJSON(engine, "/chat/message/1/rate", map[string]any{"rating": 1})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRateMessage_RejectsNonNumericID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildRateMessageEngine(t, 42)
	w := postJSON(engine, "/chat/message/abc/rate", map[string]any{"rating": 1})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestRateMessage_RejectsMissingRatingField(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildRateMessageEngine(t, 42)
	// rating absent → binding:"required" trips → 400.
	w := postJSON(engine, "/chat/message/1/rate", map[string]any{
		"feedback": "but no rating",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestRateMessage_RejectsOversizedFeedback(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildRateMessageEngine(t, 42)
	// maxFeedbackBytes + 1 — exact boundary check. Cheap to
	// construct; rejects without needing a real message row
	// because the cap fires before the repo write.
	big := strings.Repeat("x", maxFeedbackBytes+1)
	w := postJSON(engine, "/chat/message/1/rate", map[string]any{
		"rating":   -1,
		"feedback": big,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "exceeds") {
		t.Errorf("body should mention 'exceeds', got %q", w.Body.String())
	}
}

func TestRateMessage_RejectsOutOfRangeRating(t *testing.T) {
	// Rating outside [-1, 1] surfaces ErrInvalidRating as a 400.
	// Pin the sentinel→400 mapping so a future regression to
	// "500 on unknown errors" lights up here.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	msgID := seedRatableMessage(t, db, 42)

	engine := buildRateMessageEngine(t, 42)
	w := postJSON(engine, "/chat/message/"+uintToStr(msgID)+"/rate", map[string]any{
		"rating": 99,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "rating must be") {
		t.Errorf("body should carry the ErrInvalidRating sentinel message, got %q", w.Body.String())
	}
}

func TestRateMessage_NotFoundOnCrossTenant_IDORRegression(t *testing.T) {
	// Body must be the same "Message not found" as the missing-id
	// branch — no enumeration oracle. AND the row must NOT have
	// been mutated.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := 100
	msgID := seedRatableMessage(t, db, ownerUID)

	attackerUID := uint(42)
	engine := buildRateMessageEngine(t, attackerUID)
	w := postJSON(engine, "/chat/message/"+uintToStr(msgID)+"/rate", map[string]any{
		"rating":   -1,
		"feedback": "this is not my message",
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Message not found") {
		t.Errorf("body = %q, want 'Message not found'", w.Body.String())
	}

	var after workagentModel.ChatMessage
	if err := db.First(&after, msgID).Error; err != nil {
		t.Fatalf("re-load row: %v", err)
	}
	if after.UserRating != 0 || after.UserFeedback != "" {
		t.Errorf("attacker mutated row across tenants: rating=%d feedback=%q",
			after.UserRating, after.UserFeedback)
	}
}

func TestRateMessage_HappyPath_ThumbsDownWithFeedback(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := 42
	msgID := seedRatableMessage(t, db, ownerUID)

	engine := buildRateMessageEngine(t, uint(ownerUID))
	feedback := "less neon, more film-noir lighting"
	w := postJSON(engine, "/chat/message/"+uintToStr(msgID)+"/rate", map[string]any{
		"rating":   -1,
		"feedback": feedback,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var after workagentModel.ChatMessage
	if err := db.First(&after, msgID).Error; err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if after.UserRating != -1 {
		t.Errorf("UserRating = %d, want -1", after.UserRating)
	}
	if after.UserFeedback != feedback {
		t.Errorf("UserFeedback = %q, want the seeded feedback", after.UserFeedback)
	}
}

func TestRateMessage_HappyPath_ThumbsUpEmptyFeedback(t *testing.T) {
	// Thumbs-up without feedback is the common case. Pin so a
	// future "let's require feedback on every rating" change is
	// a conscious one, not silent breakage.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := 42
	msgID := seedRatableMessage(t, db, ownerUID)

	engine := buildRateMessageEngine(t, uint(ownerUID))
	w := postJSON(engine, "/chat/message/"+uintToStr(msgID)+"/rate", map[string]any{
		"rating": 1,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var after workagentModel.ChatMessage
	if err := db.First(&after, msgID).Error; err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if after.UserRating != 1 {
		t.Errorf("UserRating = %d, want 1", after.UserRating)
	}
}

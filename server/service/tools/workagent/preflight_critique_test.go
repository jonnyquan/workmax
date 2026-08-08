package workagent

// preflight_critique_test.go — coverage for the loadPreviousCritique
// preflight loader (P0 #3 critique loop, part 3 of 4). Pins:
//
//   1. No rating in thread → empty injection
//   2. Thumbs-down + feedback → <previous-critique> block with the
//      verbatim user text
//   3. Stale critique (newer message exists in thread) → empty
//      injection (LoadActiveUserCritique's staleness gate)
//   4. End-to-end via BuildPreflightAdditionsForThread — the
//      critique block lands in the assembled SystemAdditions
//      output, not just the loader's return value
//
// Same in-mem-SQLite pattern as preflight_brand_test.go.

import (
	"strings"
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"
)

func TestLoadPreviousCritique_EmptyWhenNoRating(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	repo := DefaultMessageRepository()
	msg := &workagentModel.ChatMessage{
		UID: 42, UUID: "no-rating-" + t.Name(),
		ThreadID: 100, UserText: "u", AIText: "a", ChatMode: "agent",
	}
	if err := repo.CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}

	if got := loadPreviousCritique(100); got != "" {
		t.Errorf("expected empty critique injection, got %q", got)
	}
}

func TestLoadPreviousCritique_RendersFeedback(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	repo := DefaultMessageRepository()
	msg := &workagentModel.ChatMessage{
		UID: 42, UUID: "rated-" + t.Name(),
		ThreadID: 100, UserText: "u", AIText: "a", ChatMode: "agent",
	}
	if err := repo.CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}
	feedback := "less neon, more film-noir lighting"
	if err := repo.SetUserRatingForOwner(msg.Id, 42, -1, feedback); err != nil {
		t.Fatal(err)
	}

	got := loadPreviousCritique(100)
	if got == "" {
		t.Fatal("expected non-empty critique injection")
	}
	if !strings.Contains(got, "<previous-critique>") {
		t.Errorf("missing opening tag: %q", got)
	}
	if !strings.Contains(got, "</previous-critique>") {
		t.Errorf("missing closing tag: %q", got)
	}
	if !strings.Contains(got, feedback) {
		t.Errorf("feedback text not in output: %q", got)
	}
}

func TestLoadPreviousCritique_StaleAfterNewerMessage(t *testing.T) {
	// User rated message N with thumbs-down, then sent message N+1.
	// The critique is stale — user has moved on — and must NOT be
	// injected into the new turn's preflight.
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	repo := DefaultMessageRepository()
	rated := &workagentModel.ChatMessage{
		UID: 42, UUID: "rated-" + t.Name(),
		ThreadID: 100, UserText: "u", AIText: "a1", ChatMode: "agent",
	}
	if err := repo.CreateAgentMessage(rated); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetUserRatingForOwner(rated.Id, 42, -1, "old"); err != nil {
		t.Fatal(err)
	}
	newer := &workagentModel.ChatMessage{
		UID: 42, UUID: "newer-" + t.Name(),
		ThreadID: 100, UserText: "moved on", AIText: "a2", ChatMode: "agent",
	}
	if err := repo.CreateAgentMessage(newer); err != nil {
		t.Fatal(err)
	}

	if got := loadPreviousCritique(100); got != "" {
		t.Errorf("stale critique must not surface; got %q", got)
	}
}

func TestLoadPreviousCritique_ThumbsUpDoesNotSurface(t *testing.T) {
	// The loader gates on rating=-1 via FindLatestUserCritique's SQL
	// WHERE; a thumbs-up row must produce empty injection regardless
	// of its feedback content. Pins the contract at the loader
	// boundary in case the repo gate ever relaxes — only critique
	// (negative feedback) is meant to ride into the next-turn
	// preflight; "good response" is not a steering signal.
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	repo := DefaultMessageRepository()
	msg := &workagentModel.ChatMessage{
		UID: 42, UUID: "thumbs-up-" + t.Name(),
		ThreadID: 100, UserText: "u", AIText: "a", ChatMode: "agent",
	}
	if err := repo.CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetUserRatingForOwner(msg.Id, 42, 1, "great response, keep it up"); err != nil {
		t.Fatal(err)
	}

	if got := loadPreviousCritique(100); got != "" {
		t.Errorf("thumbs-up must not surface as critique injection; got %q", got)
	}
}

func TestLoadPreviousCritique_ThumbsDownWithoutFeedbackEmpty(t *testing.T) {
	// Frontend sends `{rating: -1}` (feedback field omitted) when the
	// user clicks thumbs-down without typing a critique — the rating
	// itself persists immediately on click, the textarea is optional.
	// The loader must return "" for this row so the preflight doesn't
	// inject an empty <previous-critique> block (a bare "the user
	// disliked your previous response and asked you to change the
	// following:\n" with no text would actively mislead the model).
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	repo := DefaultMessageRepository()
	msg := &workagentModel.ChatMessage{
		UID: 42, UUID: "down-no-fb-" + t.Name(),
		ThreadID: 100, UserText: "u", AIText: "a", ChatMode: "agent",
	}
	if err := repo.CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}
	// Empty feedback: mirrors the frontend's empty-string path
	// (rateMessage omits the field; backend's Feedback field
	// defaults to "" via json zero-value).
	if err := repo.SetUserRatingForOwner(msg.Id, 42, -1, ""); err != nil {
		t.Fatal(err)
	}

	if got := loadPreviousCritique(100); got != "" {
		t.Errorf("thumbs-down with empty feedback must not inject; got %q", got)
	}
}

func TestBuildPreflightAdditionsForThread_IncludesCritique(t *testing.T) {
	// End-to-end: BuildPreflightAdditionsForThread walks the
	// composer, the loader fires, and the resulting SystemAdditions
	// string contains the <previous-critique> block.
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	repo := DefaultMessageRepository()
	msg := &workagentModel.ChatMessage{
		UID: 42, UUID: "e2e-" + t.Name(),
		ThreadID: 200, UserText: "u", AIText: "a", ChatMode: "agent",
	}
	if err := repo.CreateAgentMessage(msg); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetUserRatingForOwner(msg.Id, 42, -1, "make it cinematic"); err != nil {
		t.Fatal(err)
	}

	// Pass a real skillName so the composer runs (empty skill name
	// short-circuits the whole function). "ppt" exists across the
	// agent-mode catalog and goes through the composer normally.
	got := BuildPreflightAdditionsForThread(42, "ppt", 200)
	if !strings.Contains(got, "<previous-critique>") {
		t.Errorf("composed additions missing previous-critique block: %q", got)
	}
	if !strings.Contains(got, "make it cinematic") {
		t.Errorf("composed additions missing feedback verbatim: %q", got)
	}
}

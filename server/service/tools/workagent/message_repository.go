package workagent

import (
	"errors"
	"fmt"

	"server/globals"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

// MessageRepository owns chat_message row writes — saveAgentConversation
// inserts, the partial-clear / full-delete paths in DeleteMessage, and
// the ownership-check load that those paths gate on. Pulled out of
// agent_api.go so the handler stops reaching directly into
// globals.GraDBs["system"] for these mutations.
//
// As of B3 the repo also owns the chat-history paginated read path
// (ListByThread / CountByThread). Share-link views and plan-rehydration
// message-walks still live where they are — those carry endpoint-
// specific column narrows and joins that don't generalise cleanly.
type MessageRepository struct {
	db *gorm.DB
}

// ErrInvalidRating is the sentinel returned by SetUserRatingForOwner
// when the caller passes a rating outside {-1, 0, 1}. Handler code
// distinguishes "4xx-shaped bad input" from "5xx-shaped DB failure"
// via errors.Is(err, ErrInvalidRating) — matches the
// mcp_connector.ErrValidation pattern we landed in the connector
// refactor so the codebase has one idiom, not two.
var ErrInvalidRating = errors.New("workagent: rating must be -1, 0, or 1")

// NewMessageRepository wraps a *gorm.DB. Tests construct a repo
// against a fixture DB; production calls DefaultMessageRepository to
// pick up the system-tenant connection.
func NewMessageRepository(db *gorm.DB) *MessageRepository {
	return &MessageRepository{db: db}
}

// DefaultMessageRepository returns a repo bound to the system DB.
func DefaultMessageRepository() *MessageRepository {
	return NewMessageRepository(globals.GraDBs["system"])
}

// CreateAgentMessage inserts a new chat_message row. The caller's
// pointer is mutated in place — GORM writes the autoincrement Id back
// onto the struct, and saveAgentConversation reads msg.Id straight
// after the call to use as the message_id in the SSE payload.
func (r *MessageRepository) CreateAgentMessage(msg *workagentModel.ChatMessage) error {
	if msg == nil {
		return fmt.Errorf("create message: nil pointer")
	}
	if err := r.db.Create(msg).Error; err != nil {
		return fmt.Errorf("create message: %w", err)
	}
	return nil
}

// LoadByID returns the message row for the given id. Used by
// DeleteMessage to gate the actual mutation on an ownership check.
// Surfaces gorm.ErrRecordNotFound on miss so callers can errors.Is
// against it (the previous handler shape did `err.Error() == "record
// not found"`, which broke on every wrapped error).
func (r *MessageRepository) LoadByID(messageID uint) (*workagentModel.ChatMessage, error) {
	var msg workagentModel.ChatMessage
	if err := r.db.Where("id = ?", messageID).First(&msg).Error; err != nil {
		return nil, err
	}
	return &msg, nil
}

// DeleteByID hard-deletes a chat_message row. Used by DeleteMessage
// when both user_text and ai_text would be empty after the requested
// clear — clearing the last text on an existing row is the user's
// signal to drop the row entirely.
func (r *MessageRepository) DeleteByID(messageID uint) error {
	if err := r.db.Where("id = ?", messageID).Delete(&workagentModel.ChatMessage{}).Error; err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	return nil
}

// ClearAIText blanks the ai_text column. Used by DeleteMessage with
// type=assistant when the user_text on the same row should survive.
// Explicit single-column writer (rather than a generic Updates(map))
// so a future caller can't pass a typo'd column name.
func (r *MessageRepository) ClearAIText(messageID uint) error {
	if err := r.db.Model(&workagentModel.ChatMessage{}).
		Where("id = ?", messageID).
		Update("ai_text", "").Error; err != nil {
		return fmt.Errorf("clear ai_text: %w", err)
	}
	return nil
}

// ClearUserText blanks the user_text column. Sibling of ClearAIText
// for type=user deletes.
func (r *MessageRepository) ClearUserText(messageID uint) error {
	if err := r.db.Model(&workagentModel.ChatMessage{}).
		Where("id = ?", messageID).
		Update("user_text", "").Error; err != nil {
		return fmt.Errorf("clear user_text: %w", err)
	}
	return nil
}

// SetUserRatingForOwner writes the user_rating + user_feedback
// columns for one message, scoped to the caller's uid. Used by the
// 👍/👎 buttons on the message bubble — the rating IS the user's
// signal on the assistant reply, and the optional feedback feeds
// the next turn's <previous-critique> system-prompt injection so
// the agent reads the correction directly instead of having to
// re-derive it from the next user message.
//
// rating must be one of: -1 (thumbs-down), 0 (clear / unrated), 1
// (thumbs-up). Other values are rejected with ErrInvalidRating —
// a typed sentinel so the handler can branch via errors.Is without
// the string-prefix matching that bit us in the MCP connector
// validation refactor.
//
// uid + messageID zero short-circuit to ErrRecordNotFound (same
// IDOR posture as every other *ForOwner method). Cross-tenant
// access collapses to the same sentinel so a brute-force scan
// of /messages/:id/rate can't enumerate other users' message ids.
//
// Empty feedback is a legitimate input — thumbs-up doesn't need a
// reason. The caller decides what to write; the repo just
// persists.
func (r *MessageRepository) SetUserRatingForOwner(messageID uint, uid uint, rating int8, feedback string) error {
	if uid == 0 || messageID == 0 {
		return gorm.ErrRecordNotFound
	}
	if rating < -1 || rating > 1 {
		return ErrInvalidRating
	}
	res := r.db.Model(&workagentModel.ChatMessage{}).
		Where("id = ? AND uid = ?", messageID, uid).
		Updates(map[string]interface{}{
			"user_rating":   rating,
			"user_feedback": feedback,
		})
	if res.Error != nil {
		return fmt.Errorf("set user rating: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// LoadActiveUserCritique is FindLatestUserCritique gated on
// "is this critique still relevant?" — returns the critique only
// when the critiqued message is still the most recent in the
// thread. As soon as the user sends another turn (whether the
// agent's next reply addressed the critique or not), the critique
// becomes stale and this method returns (nil, nil).
//
// The staleness gate matters because injecting an old critique
// into a turn-15 preflight where the user already moved on at
// turn 6 wastes budget AND can re-trigger fixes the user is no
// longer asking for ("revert to film-noir lighting" after the
// user has since asked for sunset palette).
//
// Implementation: one extra COUNT after the FindLatestUserCritique
// hit. Cheap — the per-thread message index walks the same
// (thread_id, id) prefix.
func (r *MessageRepository) LoadActiveUserCritique(threadID uint) (*workagentModel.ChatMessage, error) {
	critique, err := r.FindLatestUserCritique(threadID)
	if err != nil || critique == nil {
		return critique, err
	}
	var newerCount int64
	if err := r.db.Model(&workagentModel.ChatMessage{}).
		Where("thread_id = ? AND id > ?", threadID, critique.Id).
		Count(&newerCount).Error; err != nil {
		return nil, fmt.Errorf("count newer messages: %w", err)
	}
	if newerCount > 0 {
		return nil, nil
	}
	return critique, nil
}

// FindLatestUserCritique returns the most recent message in the
// thread carrying rating=-1 AND non-empty user_feedback. Used by
// the next-turn preflight composer to detect "user thumbs-down'd
// my last reply and told me what to fix" and inject that into
// the system prompt as <previous-critique>.
//
// Returns (nil, nil) when no qualifying row exists — the most
// common state (most threads never get a thumbs-down). Walks the
// idx_thread_rating_id index in reverse so LIMIT 1 doesn't sort.
//
// NOTE on staleness: this returns the LATEST thumbs-down regardless
// of whether the user has since re-engaged with the thread (newer
// thumbs-up, newer chat turn that implicitly accepted the output).
// The handler that consumes this result is responsible for the
// "is this critique still relevant" check — typically: only inject
// if the immediately-previous assistant message is the rated one.
// Pushing that gate into the repo would couple the two too tightly
// (regenerate paths and explicit-replay paths want different
// semantics).
func (r *MessageRepository) FindLatestUserCritique(threadID uint) (*workagentModel.ChatMessage, error) {
	if threadID == 0 {
		return nil, nil
	}
	var msg workagentModel.ChatMessage
	err := r.db.
		Where("thread_id = ? AND user_rating = ? AND user_feedback <> ?", threadID, int8(-1), "").
		Order("id DESC").
		Limit(1).
		Take(&msg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find latest user critique: %w", err)
	}
	return &msg, nil
}

// SetMetadata writes the metadata text column for an existing
// message row. Used by the M1 NLP pre-scan path (PR-6b) to record
// inferred discovery_form_result answers on the user message — the
// next-turn prompt assembly then reads it back via
// FindLatestDiscoveryAnswers and surfaces it as a <discovery-context>
// block in SystemAdditions.
//
// metadata is the raw JSON string the caller already serialized.
// Single-column write (Updates(map)) so callers can't accidentally
// stomp other fields on the row.
func (r *MessageRepository) SetMetadata(messageID uint, metadata string) error {
	if messageID == 0 {
		return fmt.Errorf("set metadata: empty message id")
	}
	if err := r.db.Model(&workagentModel.ChatMessage{}).
		Where("id = ?", messageID).
		Update("metadata", metadata).Error; err != nil {
		return fmt.Errorf("set metadata: %w", err)
	}
	return nil
}

// FindLatestDiscoveryAnswers returns the most recent message in the
// thread carrying a discovery_form_result metadata blob, or
// (nil, nil) when no such row exists. Convenience wrapper over
// FindLatestByMetadataKind that fixes the kind argument to
// "discovery_form_result".
//
// Per-skill scoping callers should use FindLatestDiscoveryAnswersForSkill
// instead — this skill-agnostic helper is kept for the rare
// caller that genuinely wants "any discovery row" (e.g. ops
// inspection tooling) and for back-compat with one-off paths that
// haven't migrated yet.
func (r *MessageRepository) FindLatestDiscoveryAnswers(threadID uint) (*workagentModel.ChatMessage, error) {
	return r.FindLatestByMetadataKind(threadID, "discovery_form_result")
}

// FindLatestDiscoveryAnswersForSkill returns the latest
// discovery_form_result row whose form_id matches the supplied
// skill name. Pre-G10 (2026-05-17) every row carried the
// constant form_id="discovery"; after G10 form_id == agentMode
// so a thread that's been used across multiple skills has one
// row per skill and each skill's preflight only sees its own.
//
// Returns (nil, nil) for "no row for this skill" so the caller's
// existing nil-handling logic stays the same as
// FindLatestDiscoveryAnswers — empty result is not an error.
//
// The query is the same LIKE-substring pattern other metadata
// finders use; both substrings ("kind" + "form_id") must match
// in the SAME row. Practical scope: discovery rows always land at
// thread head (first-turn semantics), so the limit-10 scan in the
// underlying scan loop covers every realistic shape.
func (r *MessageRepository) FindLatestDiscoveryAnswersForSkill(threadID uint, skillName string) (*workagentModel.ChatMessage, error) {
	if threadID == 0 {
		return nil, fmt.Errorf("find discovery by skill: empty thread id")
	}
	if skillName == "" {
		// Fall back to skill-agnostic finder rather than returning
		// an error. Callers that truly need a skill scope should
		// never pass an empty name; the fallback is for legacy
		// call-sites that haven't migrated yet.
		return r.FindLatestDiscoveryAnswers(threadID)
	}
	kindPattern := `%"kind":"discovery_form_result"%`
	formPattern := fmt.Sprintf(`%%"form_id":"%s"%%`, skillName)
	var msgs []workagentModel.ChatMessage
	if err := r.db.
		Where("thread_id = ? AND metadata LIKE ? AND metadata LIKE ?", threadID, kindPattern, formPattern).
		Order("id DESC").
		Limit(1).
		Find(&msgs).Error; err != nil {
		return nil, fmt.Errorf("find discovery for skill %q: %w", skillName, err)
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	return &msgs[0], nil
}

// FindLatestByMetadataKind locates the latest message in the thread
// whose metadata JSON contains the supplied "kind" value. Used by
// the preflight composer to surface synthetic marker rows
// (discovery_form_result, visual_direction_selected, future
// kinds) without each caller hand-rolling its own LIKE query.
//
// Scope-limited to the first 10 thread messages (the markers
// always land at thread head — first-turn semantics for discovery,
// directly-after-pick semantics for direction). A wider scan would
// waste rows for the rare case where someone re-opens the form
// mid-thread.
//
// The "metadata LIKE" predicate is intentionally substring-based —
// metadata is opaque JSON to the DB, so the kind discriminator must
// be unique enough to not collide with other field values. Current
// kinds (discovery_form_result, visual_direction_selected) clear
// that bar.
func (r *MessageRepository) FindLatestByMetadataKind(threadID uint, kind string) (*workagentModel.ChatMessage, error) {
	if threadID == 0 {
		return nil, fmt.Errorf("find by metadata kind: empty thread id")
	}
	if kind == "" {
		return nil, fmt.Errorf("find by metadata kind: empty kind")
	}
	pattern := fmt.Sprintf(`%%"kind":"%s"%%`, kind)
	var msgs []workagentModel.ChatMessage
	if err := r.db.
		Where("thread_id = ? AND metadata LIKE ?", threadID, pattern).
		Order("id ASC").
		Limit(10).
		Find(&msgs).Error; err != nil {
		return nil, fmt.Errorf("find by metadata kind %q: %w", kind, err)
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	return &msgs[len(msgs)-1], nil
}

// FindMostRecentByMetadataKind returns the most recent message in
// the thread whose metadata.kind matches — id DESC, limit 1.
// Distinct from FindLatestByMetadataKind, which scans the first
// 10 rows ASC (thread-head posture for discovery / direction
// markers). The production-tool auto-chain
// (script_generator → shotboard_generator → shot_prompt_generator,
// Phase 4) needs the head of a deep thread, not the head of the
// thread — a script written turn 7 should still serve as parent
// for a shotboard call in the same turn.
func (r *MessageRepository) FindMostRecentByMetadataKind(threadID uint, kind string) (*workagentModel.ChatMessage, error) {
	if threadID == 0 {
		return nil, fmt.Errorf("find most-recent by metadata kind: empty thread id")
	}
	if kind == "" {
		return nil, fmt.Errorf("find most-recent by metadata kind: empty kind")
	}
	pattern := fmt.Sprintf(`%%"kind":"%s"%%`, kind)
	var msg workagentModel.ChatMessage
	err := r.db.
		Where("thread_id = ? AND metadata LIKE ?", threadID, pattern).
		Order("id DESC").
		Limit(1).
		Take(&msg).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("find most-recent by metadata kind %q: %w", kind, err)
	}
	return &msg, nil
}

// chatHistoryColumns is the canonical narrow column set for the
// paginated history view. Skips three longtext / token-counter
// columns (total_prompt, use_files, use_images, ai_audio, user_audio,
// ip, task_id, integral/token counters) that the chat-history UI
// doesn't render. Concrete shape: a 50-message page over the full
// schema can ship multi-MB; this narrow keeps it ~1MB or less. The
// audit/replay path that needs total_prompt fetches single rows by
// id (LoadByID returns the full row).
const chatHistoryColumns = "id, uuid, thread_id, user_text, ai_text, " +
	"model, chat_mode, content_type, structured_content, actions, metadata, " +
	"user_rating, user_feedback, " +
	"created_at, updated_at"

// ListByThread returns a paginated slice of chat_message rows for the
// given thread, newest first. Used by GetChatHistory; the column-
// narrow stays here so a future caller can't accidentally pull the
// full row over the wire by writing a list query inline.
//
// page is 1-indexed. Caller is responsible for bounds-checking limit
// and the maxOffsetGuard before delegating — same posture as
// ListByOwner. We don't fail-fast on page<1 or limit<=0 so the
// behaviour stays predictable for callers that want "best-effort
// page 1" semantics on degenerate inputs.
func (r *MessageRepository) ListByThread(threadID uint, page, limit int) ([]workagentModel.ChatMessage, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	var messages []workagentModel.ChatMessage
	if err := r.db.
		Select(chatHistoryColumns).
		Where("thread_id = ?", threadID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&messages).Error; err != nil {
		return nil, fmt.Errorf("list messages by thread: %w", err)
	}
	return messages, nil
}

// CountByThread returns the row count for one thread. Used by the
// chat-history pagination response so clients can render "page X of
// Y". Skipped on the cursor-mode path elsewhere — the count is
// expensive on large threads.
func (r *MessageRepository) CountByThread(threadID uint) (int64, error) {
	var n int64
	if err := r.db.Model(&workagentModel.ChatMessage{}).
		Where("thread_id = ?", threadID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count messages by thread: %w", err)
	}
	return n, nil
}

// LoadByUUID returns one chat_message by its UUID. Used by the public
// share endpoint (GetMessageDetail in conversation_share.go) where
// the v4-random UUID is the auth gate. No uid scope by design — the
// caller layers an IsPublic check on the parent thread.
func (r *MessageRepository) LoadByUUID(messageUUID string) (*workagentModel.ChatMessage, error) {
	if messageUUID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	msg := &workagentModel.ChatMessage{}
	if err := r.db.Where("uuid = ?", messageUUID).First(msg).Error; err != nil {
		return nil, err
	}
	return msg, nil
}

// shareViewColumns is the column-narrow for the public share-link
// list. Differs from chatHistoryColumns: includes use_files (the
// share UI renders attachment indicators) but the ChatHistory
// authenticated view doesn't need it; both skip the long-text
// total_prompt and audio columns. Wider divergence than I'd like;
// the shapes co-evolved with the two endpoints. Future cleanup:
// converge if the share UI gets the same chip set as the auth view.
const shareViewColumns = "id, uuid, thread_id, user_text, ai_text, " +
	"chat_mode, content_type, structured_content, actions, metadata, use_files, " +
	"created_at, updated_at"

// ListByThreadForShare returns the public share-link view of the
// thread's latest messages: DESC order (newest first) and a peek-one-extra
// row so the caller can detect truncation precisely (vs. the prior
// `len(messages) >= cap` heuristic that false-positived at exactly cap).
// The handler reverses the trimmed result before rendering so readers still
// scan the shared transcript top-to-bottom while seeing the latest turn.
//
// Returns up to (cap+1) rows; caller trims and uses the extra-row
// presence as the "truncated" signal. cap<=0 falls back to a small
// default rather than returning everything — defence against an
// accidental zero parameter that would ship multi-MB on an
// unauthenticated endpoint.
func (r *MessageRepository) ListByThreadForShare(threadID uint, cap int) ([]workagentModel.ChatMessage, error) {
	if cap <= 0 {
		cap = 50
	}
	var messages []workagentModel.ChatMessage
	if err := r.db.
		Select(shareViewColumns).
		Where("thread_id = ?", threadID).
		Order("created_at DESC").
		Limit(cap + 1).
		Find(&messages).Error; err != nil {
		return nil, fmt.Errorf("list messages for share: %w", err)
	}
	return messages, nil
}

// LoadLatestAIPreviewByThread returns the AIText of the most recent
// non-empty assistant message in the thread. Used by per-turn
// statistics refresh to populate ChatThread.MsgPreview, which the
// thread-list UI renders as the preview row.
//
// Returns the empty string when no rows match — the caller (base.go's
// updateThreadStatistics) treats that as "no preview yet" and stores
// "" in the column. Distinct from gorm.ErrRecordNotFound on purpose:
// the preview path is best-effort, not gating.
func (r *MessageRepository) LoadLatestAIPreviewByThread(threadID uint) (string, error) {
	var row workagentModel.ChatMessage
	err := r.db.
		Select("ai_text").
		Where("thread_id = ? AND ai_text IS NOT NULL AND ai_text != ''", threadID).
		Order("created_at DESC").
		First(&row).Error
	if err != nil {
		// "Not found" is a valid no-preview state — surface as empty
		// string with nil error so callers don't need to branch on
		// gorm.ErrRecordNotFound here.
		if err == gorm.ErrRecordNotFound {
			return "", nil
		}
		return "", fmt.Errorf("load latest ai preview: %w", err)
	}
	return row.AIText, nil
}

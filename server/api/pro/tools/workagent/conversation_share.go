package workagent

// IDOR-EXEMPT-FILE: public share-link endpoints — auth model is UUID v4
// non-enumerability + is_public flag, NOT uid scoping. See the
// long-form rationale in the header comment below. The IDOR contract
// test at idor_contract_test.go skips this whole file because the
// exemption is structural (every handler in here is intentionally
// public-by-design); piecemeal function-level markers would just
// duplicate the file-header rationale 4 times.
//
// Public share-link handlers for WorkAgent conversations and messages.
//
// Auth model differs sharply from conversation_api.go (private CRUD):
// these endpoints accept a request and grant access *purely* on the
// strength of the supplied UUID v4 plus the thread's is_public flag.
// There is no JWT scope, no uid match, no per-user authorization
// step. The defense is twofold:
//   - v4 UUIDs are non-enumerable; if the user shared the link,
//     anyone with the link gets read access until is_public flips.
//   - is_public lets the owner revoke access at any time without
//     rotating the UUID (see SetConversationVisibility in
//     conversation_api.go).
//
// Splitting this into its own file is a fence: future edits to the
// private CRUD path can't accidentally inherit the public auth model,
// and reviewers see at a glance which handlers are "public link,
// anyone with the URL can read" vs. "JWT-scoped, owner only".
// Public share returns a display-only DTO. Raw Agent structuredContent
// stays owner-only because it can contain tool-call inputs/results,
// upstream session ids, provider hints, and execution logs that do not
// belong on an unauthenticated URL even after workspace-path scrubbing.
//
// Future share UI note: when a Next.js share page is built on top of
// these handlers, do NOT pipe the thread's Name into <meta og:title>.
// Crawlers (Slack, Twitter, search engines) cache OG metadata on
// first fetch and the cached preview survives is_public revocation.
// Build OG preview from a generic title or a user-controlled
// "share title" field, never from the private thread.Name.

import (
	"encoding/json"
	"errors"
	"fmt"

	"server/globals"
	"server/model/common/response"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// sharedConversationMessageCap caps the messages returned by the
// unauthenticated share endpoint. See GetConversationDetail for
// rationale.
const sharedConversationMessageCap = 500

// GetConversationDetail gets detailed information about a specific conversation for sharing.
// Access is granted only via the thread UUID (v4 random, non-enumerable). Numeric-ID
// lookup is intentionally disabled here to prevent enumeration of other users' threads.
func (api *AIChatApiNew) GetConversationDetail(c *gin.Context) {
	threadId := c.Param("threadId")

	if threadId == "" {
		response.FailWithMessage("Thread ID is required", c)
		return
	}

	// Reject anything that isn't a UUID to avoid numeric/ID enumeration.
	if _, parseErr := uuid.Parse(threadId); parseErr != nil {
		response.FailWithMessage("Conversation not found", c)
		return
	}

	threadPtr, err := workagentService.DefaultThreadRepository().LoadByUUID(threadId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Conversation not found", c)
		} else {
			globals.Error(fmt.Sprintf("Database error: %v", err))
			response.FailWithMessage("Database error", c)
		}
		return
	}
	thread := *threadPtr

	// Owner has revoked sharing — return the same generic "not found"
	// the unknown-UUID branch returns so the response doesn't tell a
	// crawler "this URL used to be shared." See the matching check in
	// GetMessageDetail.
	if !thread.IsPublic {
		response.FailWithMessage("Conversation not found", c)
		return
	}

	// Get owner display name. The previous shape did a direct
	// `globals.GraDBs["system"].Table("w_user")` from the api
	// package — coupling the chat module to the user-table schema
	// with no fence. LoadOwnerNickname keeps that coupling in the
	// service layer where a future schema refactor has one place to
	// update.
	user := struct {
		ID     uint   `json:"id"`
		Name   string `json:"name"`
		Avatar string `json:"avatar"`
	}{
		ID:     uint(thread.UID),
		Name:   workagentService.LoadOwnerNickname(uint(thread.UID)),
		Avatar: workagentService.LoadOwnerAvatar(uint(thread.UID)),
	}

	// Get up to sharedConversationMessageCap messages for this
	// conversation. Same Select-narrow as GetChatHistory: skip
	// total_prompt and the audio / token counters the share-link UI
	// doesn't render (public path → also a minor privacy concern).
	//
	// The hard cap matters: this is an UNAUTHENTICATED endpoint
	// reachable to anyone with the UUID. Without a LIMIT a thread
	// with 5000 messages would ship multi-MB to any caller, and a
	// crawler indexing a leaked share URL could repeatedly re-fetch
	// the whole transcript. 500 is well above any human-readable
	// share view (a typical share-link flow renders 5–50 messages),
	// but bounded enough that worst-case payload stays within a
	// reasonable bound.
	// Peek-one-extra: ask for cap+1 rows so we can tell apart
	// "thread has exactly cap messages, we got all of them" from
	// "thread has > cap, we hit the cap". The previous
	// `len(messages) >= cap` heuristic was a false positive at
	// exactly cap — a 500-message thread always rendered with the
	// "truncated" indicator on the share page. Trim the extra row
	// before formatting.
	messages, err := workagentService.DefaultMessageRepository().
		ListByThreadForShare(thread.Id, sharedConversationMessageCap)
	if err != nil {
		globals.Error(fmt.Sprintf("Failed to fetch messages: %v", err))
		response.FailWithMessage("Failed to fetch messages", c)
		return
	}
	truncated := len(messages) > sharedConversationMessageCap
	if truncated {
		messages = messages[:sharedConversationMessageCap]
	}
	reverseChatMessages(messages)

	// A thread with no messages yet has nothing to share. Return the
	// same generic "not found" the unknown-UUID and revoked-share
	// branches return, so a future share-page consumer can treat all
	// three as one empty-state instead of trying to render a timeline
	// with zero messages. Mirrors the silence-by-default principle of
	// the rest of this endpoint.
	if len(messages) == 0 {
		response.FailWithMessage("Conversation not found", c)
		return
	}

	// This is a public (share-link) endpoint; only display text leaves
	// the process. Rich Agent structuredContent remains owner-only.
	// Absolute host paths inside displayed AI text are still rewritten to
	// the safe workspace-prefixed form.
	workspaceRoot := workagentService.ResolveWorkspaceRoot()

	// Format messages for frontend - Split each ChatMessage into user and assistant messages
	formattedMessages := make([]map[string]interface{}, 0, len(messages)*2)
	for _, msg := range messages {
		// Files placeholder. The previous shape parsed `msg.UseFiles`
		// into a structured array, but the share endpoint never
		// surfaced parsed file metadata to the client — only used the
		// presence flag, which AIText and StructuredContent already
		// reveal. Kept as an empty array so the wire shape stays
		// stable for any frontend that reads the `files` key.
		files := make([]map[string]interface{}, 0)

		// Add user message if UserText exists
		if msg.UserText != "" {
			formattedMessages = append(formattedMessages, map[string]interface{}{
				"id":        fmt.Sprintf("%s-user", msg.UUID), // Use UUID for sharing
				"role":      "user",
				"content":   msg.UserText,
				"timestamp": msg.CreatedAt,
				"files":     files,
				"chatMode":  msg.ChatMode, // Chat mode: normal or agent
			})
		}

		// Add assistant message if AIText exists
		if msg.AIText != "" {
			assistantMsg := map[string]interface{}{
				"id":        fmt.Sprintf("%s-assistant", msg.UUID), // Use UUID for sharing
				"role":      "assistant",
				"content":   workagentService.ReplaceWorkspacePaths(msg.AIText, workspaceRoot),
				"timestamp": msg.CreatedAt,
				"files":     []map[string]interface{}{}, // AI messages typically don't have files
				"chatMode":  msg.ChatMode,               // Chat mode: normal or agent
			}

			// Only expose display fields on the public share path.
			// Actions, Metadata and StructuredContent are deliberately
			// omitted. They hold opaque agent-internal JSON (tool-call
			// params/results, internal IDs, model trace bits, upstream
			// session/provider hints, sometimes raw workspace paths).
			// Workspace-path scrubbing alone is not a privacy boundary.
			// The authenticated /chat/history path (conversation_api.go)
			// still serves the rich transcript to the owner.
			if msg.ContentType != "" {
				assistantMsg["contentType"] = msg.ContentType
			}
			// Generated deliverables need explicit public file ACLs / expiring
			// tokens before they can leave the authenticated WorkAgent surface.
			// Do not expose metadata-only placeholders on public shares: they
			// look like broken files and imply a capability the link cannot
			// safely provide yet.

			formattedMessages = append(formattedMessages, assistantMsg)
		}
	}

	// `truncated` is computed above from the peek-one-extra row count,
	// not from the trimmed slice length, so a thread with exactly cap
	// messages no longer false-positives. Owner sees the full thread
	// via the authenticated GetChatHistory.

	// Build response matching frontend expectations
	conversationData := map[string]interface{}{
		"id":          thread.UUID, // Use UUID for sharing instead of internal ID
		"title":       thread.Name,
		"description": fmt.Sprintf("AIChat conversation with %d messages", len(messages)),
		"isPublic":    true, // For shared conversations, treat as public
		"createdAt":   thread.CreatedAt,
		"updatedAt":   thread.UpdatedAt,
		"owner": map[string]interface{}{
			"name":   user.Name,
			"avatar": nullableShareString(user.Avatar),
		},
		"messages":  formattedMessages,
		"truncated": truncated,
	}

	// Shared conversations are live and can be revoked by the owner.
	// Do not let browsers/CDNs replay a stale public response after
	// IsPublic flips back to false.
	c.Header("Cache-Control", "no-store")
	// Audit log so a future "view count" UI can attribute reads, and so
	// ops can see scrape patterns without parsing access logs. Stays at
	// INFO because share traffic is low-volume by design.
	globals.Info(fmt.Sprintf(
		"[Share] conversation read uuid=%s ip=%s ua=%q messages=%d truncated=%v",
		threadId, c.ClientIP(), c.Request.UserAgent(), len(messages), truncated,
	))
	response.OkWithData(conversationData, c)
}

// GetMessageDetail gets detailed information about a specific message for sharing.
// Access is granted only via the message UUID (v4 random, non-enumerable). Numeric-ID
// lookup is intentionally disabled here to prevent enumeration of other users' messages.
func (api *AIChatApiNew) GetMessageDetail(c *gin.Context) {
	msgId := c.Param("msgId")

	if msgId == "" {
		response.FailWithMessage("Message ID is required", c)
		return
	}

	if _, parseErr := uuid.Parse(msgId); parseErr != nil {
		response.FailWithMessage("Message not found", c)
		return
	}

	messagePtr, err := workagentService.DefaultMessageRepository().LoadByUUID(msgId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Message not found", c)
		} else {
			globals.Error(fmt.Sprintf("Database error: %v", err))
			response.FailWithMessage("Database error", c)
		}
		return
	}
	message := *messagePtr

	// Get thread information for context. The thread record also tells
	// us whether the owner has revoked the share — refuse if so.
	//
	// Auth bypass closed: the previous shape used a `else if` so a thread
	// lookup ERROR fell through to the "Unknown Conversation" warn-and-
	// continue branch, completely skipping the IsPublic gate and exposing
	// message data on the public endpoint regardless of share state. A
	// transient DB blip during the thread fetch is then enough to leak a
	// private message body. Refuse explicitly on lookup failure with the
	// same generic "not found" the rest of this file uses, so an attacker
	// (or a crawler) can't tell apart "this UUID never existed" from
	// "this UUID exists but the gate query failed".
	threadPtr, err := workagentService.DefaultThreadRepository().LoadByID(uint(message.ThreadID))
	if err != nil {
		globals.Warn(fmt.Sprintf("Failed to get thread info for message %s: %v", msgId, err))
		response.FailWithMessage("Message not found", c)
		return
	}
	thread := *threadPtr
	if !thread.IsPublic {
		// Same generic "not found" as the unknown-UUID branch —
		// see the matching check in GetConversationDetail.
		response.FailWithMessage("Message not found", c)
		return
	}

	// Get owner display name via the service-layer helper — keeps the
	// w_user schema reference fenced behind one named function (see
	// LoadOwnerNickname) instead of open-coded in the api package.
	user := struct {
		ID     uint   `json:"id"`
		Name   string `json:"name"`
		Avatar string `json:"avatar"`
	}{
		ID:     uint(message.UID),
		Name:   workagentService.LoadOwnerNickname(uint(message.UID)),
		Avatar: workagentService.LoadOwnerAvatar(uint(message.UID)),
	}

	// Parse files if any
	files := make([]map[string]interface{}, 0)
	if message.UseFiles != "" {
		// Add file parsing logic here if needed
	}

	// Public share endpoint — only display text leaves the process,
	// and absolute host paths in that text must be rewritten to the
	// safe workspace-prefixed form. Raw structuredContent remains
	// owner-only because tool-call/result JSON is not a public DTO.
	workspaceRoot := workagentService.ResolveWorkspaceRoot()

	// Build response matching frontend expectations
	// Return complete message with both user and AI text (like in conversation detail)
	messageData := map[string]interface{}{
		"id":        message.UUID,
		"userText":  message.UserText,
		"aiText":    workagentService.ReplaceWorkspacePaths(message.AIText, workspaceRoot),
		"timestamp": message.CreatedAt,
		"isPublic":  true,
		"chatContext": map[string]interface{}{
			"title":       thread.Name,
			"description": "A AIChat conversation",
		},
		"owner": map[string]interface{}{
			"name":   user.Name,
			"avatar": nullableShareString(user.Avatar),
		},
		"files":    files,
		"chatMode": message.ChatMode, // Add chat mode
	}

	// Add display-only Agent Message System fields if present.
	if message.ContentType != "" {
		messageData["contentType"] = message.ContentType
	}
	// message.StructuredContent / message.Actions / message.Metadata
	// intentionally omitted — see the matching note in
	// GetConversationDetail. They can leak tool-call params/results and
	// other agent internals on an unauthenticated endpoint. Generated
	// deliverables also stay owner-only until public file tokens/ACLs
	// exist; otherwise the share DTO exposes stable file metadata without
	// a real public access contract.

	// Same revocation semantics as GetConversationDetail: public
	// message reads must not survive owner-side unshare.
	c.Header("Cache-Control", "no-store")
	globals.Info(fmt.Sprintf(
		"[Share] message read uuid=%s thread_uuid=%s ip=%s ua=%q",
		msgId, thread.UUID, c.ClientIP(), c.Request.UserAgent(),
	))
	response.OkWithData(messageData, c)
}

func nullableShareString(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}

func extractPublicGeneratedFiles(structuredContent string, workspaceRoot string) []map[string]interface{} {
	if structuredContent == "" {
		return nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(structuredContent), &payload); err != nil {
		return nil
	}
	raw := payload["generatedFiles"]
	if raw == nil {
		raw = payload["generated_files"]
	}
	if raw == nil {
		if result, ok := payload["result"].(map[string]interface{}); ok {
			raw = result["generatedFiles"]
			if raw == nil {
				raw = result["generated_files"]
			}
		}
	}

	files, ok := raw.([]interface{})
	if !ok {
		return nil
	}

	out := make([]map[string]interface{}, 0, len(files))
	for _, item := range files {
		file, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		publicFile := map[string]interface{}{}
		for _, key := range []string{"name", "originalName", "mimeType", "type", "size"} {
			value, ok := file[key]
			if !ok {
				continue
			}
			if s, ok := value.(string); ok {
				publicFile[key] = workagentService.ReplaceWorkspacePaths(s, workspaceRoot)
				continue
			}
			publicFile[key] = value
		}
		if len(publicFile) > 0 {
			out = append(out, publicFile)
		}
	}
	return out
}

func reverseChatMessages(messages []workagentModel.ChatMessage) {
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
}

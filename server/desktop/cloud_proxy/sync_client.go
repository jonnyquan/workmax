//go:build desktop

package cloud_proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// ErrSyncAuthExpired is returned by sync client calls when the cloud
// responds with 401. The SyncWorker treats this as "trigger token
// refresh; retry on next tick" rather than a hard failure — the
// access token might have expired between when the worker decided
// to sync and when the HTTP call hit the wire.
//
// Distinct from cloud_proxy.ErrNoSession (Keychain empty, no
// refresh possible). The worker handles each via different paths;
// callers should errors.Is-check.
var ErrSyncAuthExpired = errors.New("sync client: auth expired (HTTP 401)")

// ThreadDeltaItem is the per-row shape returned by the cloud's
// GET /api/desktop/sync/threads endpoint (cloud-side type lives at
// server/service/desktop/sync/thread_repo.go::ThreadDeltaRow, mirrored
// in api/desktop/sync/sync_api.go::listThreadsItem with an Action
// field).
//
// Action is "upsert" or "delete"; delete items come from the cloud
// tombstone sweep and remove local cache rows idempotently.
//
// UpdatedAt + CreatedAt are kept as raw strings (not time.Time) so
// the sidecar's local SQLite cache can write them through verbatim
// without an encode/decode round-trip. The worker only needs to
// COMPARE timestamps via the cursor; it doesn't render them.
type ThreadDeltaItem struct {
	Action        string `json:"action"`
	CloudThreadID string `json:"cloud_thread_id"`
	UUID          string `json:"uuid"`
	Name          string `json:"name"`
	AgentMode     string `json:"agent_mode"`
	AgentType     string `json:"agent_type"`
	Model         string `json:"model"`
	MessageCount  int    `json:"message_count"`
	MsgPreview    string `json:"msg_preview"`
	FileCount     int    `json:"file_count"`
	IsPublic      bool   `json:"is_public"`
	UpdatedAt     string `json:"updated_at"`
	CreatedAt     string `json:"created_at"`
}

// ThreadsDeltaPage is the decoded form of the cloud's response
// envelope (api/desktop/sync/sync_api.go::listThreadsResponse).
// Fields match wire shape 1:1.
type ThreadsDeltaPage struct {
	Items      []ThreadDeltaItem `json:"items"`
	NextCursor string            `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
	ServerTime string            `json:"server_time"`
}

// MessageDeltaItem is the per-row shape returned by the cloud's
// GET /api/desktop/sync/messages endpoint (see
// server/service/desktop/sync/message_repo.go::MessageDeltaRow).
//
// Same Action enum + UpdatedAt-as-string contract as
// ThreadDeltaItem (the worker compares via cursor, never renders).
//
// JSON-blob fields (StructuredContent, Actions, Metadata,
// UseImages, UseFiles) are forwarded verbatim as strings — the
// sidecar doesn't parse them; the renderer's WorkAgent component
// already knows their shapes.
type MessageDeltaItem struct {
	Action            string `json:"action"`
	CloudMessageID    string `json:"cloud_message_id"`
	UUID              string `json:"uuid"`
	ThreadUUID        string `json:"thread_uuid"`
	UserText          string `json:"user_text"`
	AIText            string `json:"ai_text"`
	ChatMode          string `json:"chat_mode"`
	ContentType       string `json:"content_type,omitempty"`
	StructuredContent string `json:"structured_content,omitempty"`
	Actions           string `json:"actions,omitempty"`
	Metadata          string `json:"metadata,omitempty"`
	UseImages         string `json:"use_images,omitempty"`
	UseFiles          string `json:"use_files,omitempty"`
	UserRating        int    `json:"user_rating"`
	UserFeedback      string `json:"user_feedback,omitempty"`
	UpdatedAt         string `json:"updated_at"`
	CreatedAt         string `json:"created_at"`
}

// MessagesDeltaPage is the decoded form of the cloud's response
// envelope. Field shape matches api/desktop/sync/sync_api.go's
// listMessagesResponse.
type MessagesDeltaPage struct {
	Items      []MessageDeltaItem `json:"items"`
	NextCursor string             `json:"next_cursor"`
	HasMore    bool               `json:"has_more"`
	ServerTime string             `json:"server_time"`
}

// ListMessagesDelta fetches a page of message upserts for the given
// thread (scoped server-side to the JWT's uid — IDOR-safe). Mirrors
// the shape of ListThreadsDelta with an extra cloudThreadID
// parameter; the cloud handler returns 404 if the thread isn't
// owned by the caller (see api/desktop/sync/sync_api.go's
// ListMessages handler).
//
// Errors:
//   - ErrSyncAuthExpired on 401 (same recovery story as threads)
//   - 404 surfaces as a wrapped error containing "HTTP 404" —
//     callers can errors.Is-check against ErrThreadNotOwnedOrMissing
//     when they care about the difference; treating 404 the same
//     as 5xx (retryable via backoff) is also fine since the cause
//     is usually "local cache hasn't received the thread yet".
//   - Other non-2xx wrapped with HTTP status + body prefix
func (c *Client) ListMessagesDelta(ctx context.Context, accessToken string, cloudThreadID uint64, cursor string, limit int) (MessagesDeltaPage, error) {
	if cloudThreadID == 0 {
		return MessagesDeltaPage{}, fmt.Errorf("sync list messages: cloud_thread_id required")
	}
	if c == nil {
		return MessagesDeltaPage{}, fmt.Errorf("sync list messages: HTTP: client is missing")
	}
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return MessagesDeltaPage{}, fmt.Errorf("sync list messages: HTTP: cloud base URL is invalid")
	}
	q := url.Values{}
	q.Set("thread_id", strconv.FormatUint(cloudThreadID, 10))
	if cursor != "" {
		q.Set("since", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	endpoint := baseURL + CloudRouteSyncMessages + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return MessagesDeltaPage{}, fmt.Errorf("sync list messages: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	SetClientHeaders(req.Header)

	resp, err := c.credentialHTTPClient().Do(req)
	if err != nil {
		return MessagesDeltaPage{}, fmt.Errorf("sync list messages: HTTP: %w", err)
	}
	defer resp.Body.Close()

	body, err := readBoundedCloudResponseBody(resp, 8<<20) // messages can carry structured_content blobs
	if err != nil {
		return MessagesDeltaPage{}, fmt.Errorf("sync list messages: invalid response body")
	}
	defer clear(body)

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return MessagesDeltaPage{}, ErrSyncAuthExpired
	case resp.StatusCode == http.StatusNotFound:
		return MessagesDeltaPage{}, fmt.Errorf("%w: HTTP 404", ErrThreadNotOwnedOrMissing)
	case resp.StatusCode != http.StatusOK:
		return MessagesDeltaPage{}, fmt.Errorf("sync list messages: HTTP %d", resp.StatusCode)
	}

	var page MessagesDeltaPage
	if err := json.Unmarshal(body, &page); err != nil {
		return MessagesDeltaPage{}, fmt.Errorf("sync list messages: invalid JSON response")
	}
	if page.Items == nil {
		page.Items = []MessageDeltaItem{}
	}
	return page, nil
}

// ErrThreadNotOwnedOrMissing wraps the HTTP 404 from /sync/messages.
// Callers that want to distinguish "thread sync hasn't landed yet"
// from a hard failure can errors.Is-check this sentinel.
var ErrThreadNotOwnedOrMissing = errors.New("sync messages: thread not owned by uid or doesn't exist")

// ListThreadsDelta fetches a page of thread upserts/deletes from
// the cloud. Cursor and limit are optional — empty cursor = full
// sync from beginning; limit=0 = let cloud pick default (100).
//
// Returns ErrSyncAuthExpired on HTTP 401 so the worker can route
// to a token refresh path without parsing error text. Other non-2xx
// responses surface as fmt-wrapped errors with the HTTP status +
// body prefix; the worker logs them and falls into its retry
// backoff loop.
//
// Uses the snug-timeout httpClient (10s) inherited from Client —
// sync calls are JSON, not SSE, so the chat-relay's no-timeout
// client would be wrong here.
func (c *Client) ListThreadsDelta(ctx context.Context, accessToken, cursor string, limit int) (ThreadsDeltaPage, error) {
	if c == nil {
		return ThreadsDeltaPage{}, fmt.Errorf("sync list threads: HTTP: client is missing")
	}
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return ThreadsDeltaPage{}, fmt.Errorf("sync list threads: HTTP: cloud base URL is invalid")
	}
	q := url.Values{}
	if cursor != "" {
		q.Set("since", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	endpoint := baseURL + CloudRouteSyncThreads
	if encoded := q.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ThreadsDeltaPage{}, fmt.Errorf("sync list threads: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	SetClientHeaders(req.Header)

	resp, err := c.credentialHTTPClient().Do(req)
	if err != nil {
		return ThreadsDeltaPage{}, fmt.Errorf("sync list threads: HTTP: %w", err)
	}
	defer resp.Body.Close()

	body, err := readBoundedCloudResponseBody(resp, 4<<20) // generous for sync pages
	if err != nil {
		return ThreadsDeltaPage{}, fmt.Errorf("sync list threads: invalid response body")
	}
	defer clear(body)

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ThreadsDeltaPage{}, ErrSyncAuthExpired
	case resp.StatusCode != http.StatusOK:
		return ThreadsDeltaPage{}, fmt.Errorf("sync list threads: HTTP %d", resp.StatusCode)
	}

	var page ThreadsDeltaPage
	if err := json.Unmarshal(body, &page); err != nil {
		return ThreadsDeltaPage{}, fmt.Errorf("sync list threads: invalid JSON response")
	}
	// Defensive: cloud should never return null items per the
	// envelope's nil-coerce policy (api/desktop/sync/response.go),
	// but normalize here too so callers always get a non-nil slice.
	if page.Items == nil {
		page.Items = []ThreadDeltaItem{}
	}
	return page, nil
}

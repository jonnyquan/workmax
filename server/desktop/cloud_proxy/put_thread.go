//go:build desktop

package cloud_proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	putThreadMaxResponseBodyBytes = 64 << 10
	putThreadMaxAccessTokenBytes  = 32 << 10
	putThreadMaxNameBytes         = 200
	putThreadMaxAgentModeBytes    = 50
	putThreadMaxAgentTypeBytes    = 50
	putThreadMaxModelBytes        = 255
)

var (
	// ErrPutThreadAuthExpired permits exactly one session-fenced refresh and
	// replay. PUT is idempotent by UUID, so this retry cannot duplicate a cloud
	// conversation.
	ErrPutThreadAuthExpired = errors.New("put thread: auth expired (HTTP 401)")
	ErrPutThreadConflict    = errors.New("put thread: uuid conflict")
)

type PutThreadInput struct {
	UUID      string
	Name      string
	AgentMode string
}

type PutThreadResult struct {
	Thread  ThreadDeltaItem
	Created bool
}

type putThreadRequest struct {
	Name      string `json:"name"`
	AgentMode string `json:"agent_mode"`
}

type putThreadResponse struct {
	Thread  ThreadDeltaItem `json:"thread"`
	Created bool            `json:"created"`
}

// PutThread creates-or-returns one Desktop Agent thread at a stable UUID.
// The Server route is a Desktop OAuth resource, not the legacy browser-era
// conversation POST, so 401 is a real HTTP status and safe refresh behavior is
// explicit at the Sidecar call site.
func (c *Client) PutThread(ctx context.Context, accessToken string, input PutThreadInput) (PutThreadResult, error) {
	if err := sessionChangedContextError(ctx); err != nil {
		return PutThreadResult{}, err
	}
	if c == nil {
		return PutThreadResult{}, fmt.Errorf("put thread: HTTP: client is missing")
	}
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return PutThreadResult{}, fmt.Errorf("put thread: HTTP: cloud base URL is invalid")
	}
	if err := validatePutThreadInput(accessToken, input); err != nil {
		return PutThreadResult{}, err
	}
	body, err := json.Marshal(putThreadRequest{Name: input.Name, AgentMode: input.AgentMode})
	if err != nil {
		return PutThreadResult{}, fmt.Errorf("put thread: encode request")
	}
	defer clear(body)
	endpoint := baseURL + strings.Replace(
		CloudRouteAgentThread,
		":uuid",
		url.PathEscape(input.UUID),
		1,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return PutThreadResult{}, fmt.Errorf("put thread: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	SetClientHeaders(req.Header)

	response, err := c.credentialHTTPClient().Do(req)
	if err != nil {
		if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
			return PutThreadResult{}, sessionErr
		}
		return PutThreadResult{}, fmt.Errorf("put thread: HTTP: %w", err)
	}
	defer response.Body.Close()

	// Authentication and conflict status are authoritative protocol signals;
	// their bodies are neither trusted nor part of the success contract. Drain
	// only a bounded prefix for connection hygiene, then let the Sidecar perform
	// its once-only UUID-stable refresh/retry. In particular, an oversized or
	// malformed 401 body must not suppress legitimate token recovery.
	switch response.StatusCode {
	case http.StatusUnauthorized:
		drainPutThreadResponseBody(response)
		if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
			return PutThreadResult{}, sessionErr
		}
		return PutThreadResult{}, ErrPutThreadAuthExpired
	case http.StatusConflict:
		drainPutThreadResponseBody(response)
		if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
			return PutThreadResult{}, sessionErr
		}
		return PutThreadResult{}, ErrPutThreadConflict
	case http.StatusOK, http.StatusCreated:
		// Strictly decode the bounded success resource below.
	default:
		drainPutThreadResponseBody(response)
		if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
			return PutThreadResult{}, sessionErr
		}
		return PutThreadResult{}, fmt.Errorf("put thread: HTTP %d", response.StatusCode)
	}

	responseBody, err := readBoundedCloudResponseBody(response, putThreadMaxResponseBodyBytes)
	if err != nil {
		if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
			return PutThreadResult{}, sessionErr
		}
		return PutThreadResult{}, fmt.Errorf("put thread: invalid response body")
	}
	defer clear(responseBody)
	if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
		return PutThreadResult{}, sessionErr
	}

	if err := requireJSONResponse(response.Header); err != nil {
		return PutThreadResult{}, err
	}

	decoded, err := decodePutThreadResponse(responseBody)
	if err != nil {
		return PutThreadResult{}, fmt.Errorf("put thread: invalid JSON response")
	}
	decoded.Thread.Action = "upsert"
	if err := validatePutThreadResponse(input, response.StatusCode, decoded); err != nil {
		return PutThreadResult{}, err
	}
	if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
		return PutThreadResult{}, sessionErr
	}
	return PutThreadResult{Thread: decoded.Thread, Created: decoded.Created}, nil
}

func drainPutThreadResponseBody(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, putThreadMaxResponseBodyBytes+1))
}

func requireJSONResponse(header http.Header) error {
	values := header.Values("Content-Type")
	if len(values) != 1 {
		return fmt.Errorf("put thread: invalid response content type")
	}
	value := values[0]
	if value == "" || len(value) > 256 || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value || containsPutThreadControl(value) {
		return fmt.Errorf("put thread: invalid response content type")
	}
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" ||
		(len(params) != 0 && (len(params) != 1 || !strings.EqualFold(params["charset"], "utf-8"))) {
		return fmt.Errorf("put thread: invalid response content type")
	}
	return nil
}

func validatePutThreadResponse(input PutThreadInput, status int, response putThreadResponse) error {
	thread := response.Thread
	if thread.UUID != input.UUID || canonicalPutThreadUUID(thread.UUID) != nil ||
		!validPutThreadText(thread.Name, putThreadMaxNameBytes) ||
		!validPutThreadText(thread.AgentMode, putThreadMaxAgentModeBytes) ||
		thread.AgentType != "general_agent" ||
		!validPutThreadText(thread.AgentType, putThreadMaxAgentTypeBytes) ||
		!validPutThreadText(thread.Model, putThreadMaxModelBytes) ||
		thread.MessageCount < 0 || thread.FileCount < 0 || !utf8.ValidString(thread.MsgPreview) {
		return fmt.Errorf("put thread: malformed resource response")
	}
	cloudID, err := strconv.ParseUint(thread.CloudThreadID, 10, 64)
	if err != nil || cloudID == 0 || strconv.FormatUint(cloudID, 10) != thread.CloudThreadID {
		return fmt.Errorf("put thread: malformed resource response")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, thread.CreatedAt)
	if err != nil || createdAt.IsZero() {
		return fmt.Errorf("put thread: malformed resource response")
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, thread.UpdatedAt)
	if err != nil || updatedAt.IsZero() || updatedAt.Before(createdAt) {
		return fmt.Errorf("put thread: malformed resource response")
	}
	if (status == http.StatusCreated) != response.Created {
		return fmt.Errorf("put thread: inconsistent create status")
	}
	return nil
}

func validatePutThreadInput(accessToken string, input PutThreadInput) error {
	if accessToken == "" || len(accessToken) > putThreadMaxAccessTokenBytes ||
		!utf8.ValidString(accessToken) || strings.TrimSpace(accessToken) != accessToken ||
		containsPutThreadControl(accessToken) {
		return fmt.Errorf("put thread: access token is required")
	}
	if err := canonicalPutThreadUUID(input.UUID); err != nil {
		return fmt.Errorf("put thread: uuid must be canonical v4")
	}
	if !validPutThreadText(input.Name, putThreadMaxNameBytes) {
		return fmt.Errorf("put thread: name is invalid")
	}
	if !validPutThreadText(input.AgentMode, putThreadMaxAgentModeBytes) {
		return fmt.Errorf("put thread: agent_mode is invalid")
	}
	return nil
}

func canonicalPutThreadUUID(value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return errors.New("uuid is required")
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 || parsed.String() != value {
		return errors.New("uuid must be canonical v4")
	}
	return nil
}

func validPutThreadText(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !containsPutThreadControl(value)
}

func containsPutThreadControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func decodePutThreadResponse(body []byte) (putThreadResponse, error) {
	var response putThreadResponse
	fields, err := decodeExactPutThreadObject(body, []string{"thread", "created"})
	if err != nil {
		return response, err
	}
	if err := decodeRequiredPutThreadField(fields, "created", &response.Created); err != nil {
		return response, err
	}
	threadFields, err := decodeExactPutThreadObject(fields["thread"], []string{
		"cloud_thread_id",
		"uuid",
		"name",
		"agent_mode",
		"agent_type",
		"model",
		"message_count",
		"msg_preview",
		"file_count",
		"is_public",
		"updated_at",
		"created_at",
	})
	if err != nil {
		return response, err
	}
	thread := &response.Thread
	for _, field := range []struct {
		name   string
		target any
	}{
		{name: "cloud_thread_id", target: &thread.CloudThreadID},
		{name: "uuid", target: &thread.UUID},
		{name: "name", target: &thread.Name},
		{name: "agent_mode", target: &thread.AgentMode},
		{name: "agent_type", target: &thread.AgentType},
		{name: "model", target: &thread.Model},
		{name: "message_count", target: &thread.MessageCount},
		{name: "msg_preview", target: &thread.MsgPreview},
		{name: "file_count", target: &thread.FileCount},
		{name: "is_public", target: &thread.IsPublic},
		{name: "updated_at", target: &thread.UpdatedAt},
		{name: "created_at", target: &thread.CreatedAt},
	} {
		if err := decodeRequiredPutThreadField(threadFields, field.name, field.target); err != nil {
			return putThreadResponse{}, err
		}
	}
	return response, nil
}

// decodeRequiredPutThreadField closes encoding/json's null-to-zero-value gap.
// Every field in the wire contract is required and non-null, including fields
// whose legitimate zero value is otherwise indistinguishable after Unmarshal
// (false, 0, and the empty message preview).
func decodeRequiredPutThreadField(fields map[string]json.RawMessage, key string, target any) error {
	raw, ok := fields[key]
	if !ok || len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("response field %q is missing or null", key)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("response field %q has the wrong type: %w", key, err)
	}
	return nil
}

// decodeExactPutThreadObject rejects encoding/json's otherwise-permissive
// duplicate-key, case-folded-field, and unknown-field behavior. Both the
// envelope and nested thread resource are closed protocol objects.
func decodeExactPutThreadObject(body []byte, required []string) (map[string]json.RawMessage, error) {
	if len(body) == 0 || !utf8.Valid(body) {
		return nil, errors.New("JSON object is missing or invalid UTF-8")
	}
	allowed := make(map[string]struct{}, len(required))
	for _, key := range required {
		allowed[key] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("response must be one JSON object")
	}
	fields := make(map[string]json.RawMessage, len(required))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("response object key is malformed")
		}
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("unexpected response field %q", key)
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("duplicate response field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, errors.New("response object is not closed")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("response has trailing JSON")
	}
	if len(fields) != len(required) {
		return nil, errors.New("response object is missing a required field")
	}
	return fields, nil
}

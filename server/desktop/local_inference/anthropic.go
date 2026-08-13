//go:build desktop

package local_inference

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// anthropicAPIVersion 是 Anthropic Messages API 的必选版本头。
const anthropicAPIVersion = "2023-06-01"

// AnthropicBaseURL canonicalizes a user-typed Anthropic endpoint to the ONE
// spelling every consumer in this repo agrees on: no trailing slash, and no
// trailing `/v1`.
//
// It exists because there are two consumers of the same stored base_url and
// they used to disagree. This adapter (L1) appended `/messages`; the claude
// CLI that L2 drives appends `/v1/messages` to ANTHROPIC_BASE_URL and there is
// no way to talk it out of that. So whichever spelling the user typed, exactly
// one of the two engines 404'd — and which one depended on a trailing path
// segment nobody told them about.
//
// The fix is to make the stored value mean one thing. `/v1` is the API
// version, which belongs to the endpoint path, not to the host the user
// configured: strip it here, and let each caller append the full versioned
// path it needs. Both spellings the user might type collapse to the same base,
// so both engines reach the same URL.
//
// Normalizing on read rather than on write is deliberate: settings saved
// before this change carry either spelling, and a read-side fix repairs them
// without a migration or a re-save.
func AnthropicBaseURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.HasSuffix(trimmed, "/v1") {
		trimmed = strings.TrimSuffix(trimmed, "/v1")
	}
	return trimmed
}

// anthropicAdapter 适配 Anthropic 兼容 endpoint（/v1/messages 流式）。
// 文本增量在 event:content_block_delta 的 data.delta.text；终态是
// event:message_stop。
type anthropicAdapter struct{}

func (anthropicAdapter) endpoint(baseURL string) string {
	return AnthropicBaseURL(baseURL) + "/v1/messages"
}

func (anthropicAdapter) requestBody(modelID string, history []Message, userText string, atts []Attachment, systemMessage string) (io.Reader, error) {
	// History arrives as strictly alternating user/assistant pairs (the
	// loader only emits completed exchanges), which is what this wire
	// protocol requires — two user messages in a row are a 400.
	messages := make([]map[string]any, 0, len(history)+1)
	for _, m := range history {
		messages = append(messages, map[string]any{"role": m.Role, "content": m.Text})
	}
	messages = append(messages, map[string]any{"role": "user", "content": anthropicContent(userText, atts)})
	body := map[string]any{
		"model":      modelID,
		"messages":   messages,
		"max_tokens": 4096,
		"stream":     true,
	}
	// A mind's role hint rides the system prompt — the strongest slot this
	// protocol offers for "how to work" rather than "what to work on".
	if strings.TrimSpace(systemMessage) != "" {
		body["system"] = systemMessage
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(string(raw)), nil
}

// anthropicContent: 无附件 → 纯字符串；有附件 → 多模态片段数组
// （提取文本拼进 text 片段，图片用 image source base64）。
func anthropicContent(userText string, atts []Attachment) any {
	if len(atts) == 0 {
		return userText
	}
	parts := []any{}
	textParts := []string{userText}
	for _, a := range atts {
		if a.Kind == "text" && a.Text != "" {
			textParts = append(textParts, a.Text)
		}
	}
	parts = append(parts, map[string]any{"type": "text", "text": strings.Join(textParts, "\n\n")})
	for _, a := range atts {
		if a.Kind == "image" && a.Base64 != "" {
			parts = append(parts, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": a.MimeType,
					"data":       a.Base64,
				},
			})
		}
	}
	return parts
}

func (anthropicAdapter) setAuth(req *http.Request, apiKey string) {
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	req.Header.Set("anthropic-version", anthropicAPIVersion)
}

func (anthropicAdapter) extractText(frameEvent, frameData string) (string, bool) {
	switch frameEvent {
	case "message_stop":
		return "", true
	case "content_block_delta":
		var chunk struct {
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(frameData), &chunk); err != nil {
			return "", false
		}
		// tool_use 等非 text_delta 的 content_block_delta，text 为空 → 跳过。
		// L1 只做纯对话流，工具循环是 L2。
		return chunk.Delta.Text, false
	default:
		// message_start / content_block_start / content_block_stop / message_delta
		// 等元数据帧跳过。
		return "", false
	}
}

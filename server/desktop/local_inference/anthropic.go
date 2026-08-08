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

// anthropicAdapter 适配 Anthropic 兼容 endpoint（/messages 流式）。
// 文本增量在 event:content_block_delta 的 data.delta.text；终态是
// event:message_stop。
type anthropicAdapter struct{}

func (anthropicAdapter) endpoint(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/messages"
}

func (anthropicAdapter) requestBody(modelID, userText string, atts []Attachment) (io.Reader, error) {
	body := map[string]any{
		"model":      modelID,
		"messages":   []map[string]any{{"role": "user", "content": anthropicContent(userText, atts)}},
		"max_tokens": 4096,
		"stream":     true,
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

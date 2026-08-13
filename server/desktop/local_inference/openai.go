//go:build desktop

package local_inference

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// openaiAdapter 适配 OpenAI 兼容 endpoint（如 Ollama /v1/chat/completions、
// vLLM、OpenRouter 等）。流式增量在 choices[0].delta.content；终态是字面量
// [DONE]（无 event: 字段）。
type openaiAdapter struct{}

func (openaiAdapter) endpoint(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/chat/completions"
}

func (openaiAdapter) requestBody(modelID string, history []Message, userText string, atts []Attachment, systemMessage string) (io.Reader, error) {
	// Prior exchanges ride as plain text — attachments and retrieval context
	// belong to the turn that carried them, not to every turn after it.
	messages := make([]map[string]any, 0, len(history)+2)
	// A mind's role hint rides the system message — the strongest slot this
	// protocol offers for "how to work" rather than "what to work on".
	if strings.TrimSpace(systemMessage) != "" {
		messages = append(messages, map[string]any{"role": "system", "content": systemMessage})
	}
	for _, m := range history {
		messages = append(messages, map[string]any{"role": m.Role, "content": m.Text})
	}
	messages = append(messages, map[string]any{"role": "user", "content": openaiContent(userText, atts)})
	body := map[string]any{
		"model":    modelID,
		"messages": messages,
		"stream":   true,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(string(raw)), nil
}

// openaiContent: 无附件 → 纯字符串（兼容所有模型）；有附件 → 多模态片段数组
// （提取文本拼进 text 片段，图片用 image_url data: base64 —— Ollama vision 系模型支持）。
func openaiContent(userText string, atts []Attachment) any {
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
				"type":      "image_url",
				"image_url": map[string]string{"url": "data:" + a.MimeType + ";base64," + a.Base64},
			})
		}
	}
	return parts
}

func (openaiAdapter) setAuth(req *http.Request, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func (openaiAdapter) extractText(frameEvent, frameData string) (string, bool) {
	// OpenAI 流不使用 event: 字段；终态是字面量 [DONE]。
	if strings.TrimSpace(frameData) == "[DONE]" {
		return "", true
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(frameData), &chunk); err != nil {
		return "", false // 跳过无法解析的帧（如上游 ping / keepalive 注释已在上层过滤）
	}
	if len(chunk.Choices) == 0 {
		return "", false
	}
	return chunk.Choices[0].Delta.Content, false
}

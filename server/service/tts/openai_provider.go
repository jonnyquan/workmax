package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIProvider calls the OpenAI /v1/audio/speech endpoint — the
// tts-1 family. Kept intentionally simple: one POST, no streaming,
// no caching. Response body is the raw audio bytes (typed by Format).
//
// Construct via NewOpenAIProvider so the endpoint + HTTP client can
// be injected for tests. Production callers wire config from
// globals.GraConf.
type OpenAIProvider struct {
	apiKey     string
	endpoint   string // defaults to https://api.openai.com/v1/audio/speech
	model      string // "tts-1" or "tts-1-hd"
	httpClient *http.Client
}

// NewOpenAIProvider constructs an OpenAI TTS provider. endpoint
// falls back to the public OpenAI URL when empty; model falls back
// to tts-1. httpClient defaults to one with a 30s timeout.
func NewOpenAIProvider(apiKey, endpoint, model string, httpClient *http.Client) *OpenAIProvider {
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/audio/speech"
	}
	if model == "" {
		model = "tts-1"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout * time.Second}
	}
	return &OpenAIProvider{
		apiKey:     apiKey,
		endpoint:   endpoint,
		model:      model,
		httpClient: httpClient,
	}
}

func (p *OpenAIProvider) Name() string {
	return "openai-" + p.model
}

// openaiRequestBody mirrors https://platform.openai.com/docs/api-reference/audio/createSpeech.
type openaiRequestBody struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format,omitempty"`
	Speed          float64 `json:"speed,omitempty"`
}

// Synthesize implements Provider. Returns the audio bytes on success
// or a descriptive error on HTTP / API failure. The error message
// preserves the remote status code + body prefix so callers can tell
// "invalid voice name" apart from "rate limited" when deciding
// whether to retry or abort.
func (p *OpenAIProvider) Synthesize(ctx context.Context, req *SynthesizeRequest) (*SynthesizeResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return nil, fmt.Errorf("empty text")
	}
	if len(text) > MaxTextChars {
		return nil, fmt.Errorf("text exceeds %d-char limit (%d)", MaxTextChars, len(text))
	}
	if strings.TrimSpace(req.Voice) == "" {
		return nil, fmt.Errorf("voice is required")
	}

	format := req.Format
	if format == "" {
		format = DefaultFormat
	}
	speed := req.Speed
	if speed == 0 {
		speed = DefaultSpeed
	}

	body := openaiRequestBody{
		Model:          p.model,
		Input:          text,
		Voice:          req.Voice,
		ResponseFormat: format,
		Speed:          speed,
	}
	payload, err := json.Marshal(&body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	start := time.Now()
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Preserve a short error-body prefix so callers can grep for
		// known failure modes (invalid_voice / rate_limit_exceeded
		// etc.) without us having to parse the whole error envelope.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("openai tts status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read audio body: %w", err)
	}

	return &SynthesizeResponse{
		AudioBytes:   audio,
		MIMEType:     mimeForFormat(format),
		ResponseTime: time.Since(start),
		Provider:     "openai",
		Model:        p.model,
	}, nil
}

// mimeForFormat maps the request's Format field to the MIME type
// OpenAI actually returns. Kept local so callers don't need to pull
// in a generic mime lookup table for three formats.
func mimeForFormat(format string) string {
	switch strings.ToLower(format) {
	case "wav":
		return "audio/wav"
	case "opus":
		return "audio/opus"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	default:
		return "audio/mpeg"
	}
}

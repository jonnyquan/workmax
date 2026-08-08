package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"server/globals"
	"server/model"
	"strings"
	"time"
)

// GPTImage2Provider drives the gptsapi.net async predictions API for the
// gpt-image-2 model family. The flow is create-then-poll:
//
//  1. POST /api/v3/openai/<modelCode>/text-to-image  (or /image-edit when
//     reference images are present) with prompt + aspect_ratio +
//     output_format. Server returns {data:{id, urls:{get}}, status:"created"}.
//  2. GET data.urls.get on a fixed cadence until status=="completed" (or
//     "failed"). Successful response carries data.outputs[] — one or more
//     hosted image URLs.
//
// This is a fundamentally different protocol from OpenAI's official
// /v1/images/generations endpoint (which is sync, takes `size` instead of
// `aspect_ratio`, and accepts multipart binary on /v1/images/edits). The
// gptsapi.net gateway is OpenAI-style only in spirit; in practice it
// brokers an async backend, so the provider polls.
type GPTImage2Provider struct {
	name         string
	providerType string
	endpoint     string
	apiKey       string
	model        string
	enabled      bool
	timeout      time.Duration
	pollInterval time.Duration
	maxWait      time.Duration
}

const (
	defaultGPTImage2Endpoint     = "https://api.gptsapi.net"
	defaultGPTImage2Model        = "gpt-image-2-plus"
	defaultGPTImage2PollInterval = 3 * time.Second
	defaultGPTImage2MaxWait      = 5 * time.Minute
)

func NewGPTImage2Provider(cfg *ProviderConfig) *GPTImage2Provider {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = defaultGPTImage2Endpoint
	}

	timeout := 30 * time.Second
	pollInterval := defaultGPTImage2PollInterval
	maxWait := defaultGPTImage2MaxWait
	if cfg.ExtraConfig != nil {
		if cfg.ExtraConfig.Timeout > 0 {
			timeout = time.Duration(cfg.ExtraConfig.Timeout) * time.Second
			// Cap maxWait to the configured timeout so a stuck job can't
			// outlive the operator-set ceiling.
			maxWait = timeout
		}
		if cfg.ExtraConfig.PollInterval > 0 {
			pollInterval = time.Duration(cfg.ExtraConfig.PollInterval) * time.Second
		}
		if cfg.ExtraConfig.MaxWaitSeconds > 0 {
			maxWait = time.Duration(cfg.ExtraConfig.MaxWaitSeconds) * time.Second
		}
	}

	return &GPTImage2Provider{
		name:         cfg.Name,
		providerType: model.ProviderTypeOpenAI,
		endpoint:     strings.TrimRight(endpoint, "/"),
		apiKey:       cfg.APIKey,
		model:        cfg.Model,
		enabled:      cfg.Enabled,
		timeout:      timeout,
		pollInterval: pollInterval,
		maxWait:      maxWait,
	}
}

func (p *GPTImage2Provider) Name() string { return p.name }

func (p *GPTImage2Provider) Type() string {
	if p.providerType != "" {
		return p.providerType
	}
	return model.ProviderTypeOpenAI
}

func (p *GPTImage2Provider) IsEnabled() bool {
	return p.enabled && p.apiKey != "" && p.endpoint != ""
}

// gptImage2CreateRequest is the create-job body for both text-to-image and
// image-edit. Fields the operator hasn't set get omitted so the upstream
// can pick its own defaults.
type gptImage2CreateRequest struct {
	Prompt       string   `json:"prompt"`
	AspectRatio  string   `json:"aspect_ratio,omitempty"`
	OutputFormat string   `json:"output_format,omitempty"`
	Quality      string   `json:"quality,omitempty"`
	Images       []string `json:"images,omitempty"` // image-edit only
}

// gptImage2Envelope mirrors the wire shape for both submit and poll
// responses. Status transitions: created → running/processing → completed
// (terminal) or failed (terminal). Outputs is populated on completed.
type gptImage2Envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
	Data    struct {
		ID            string   `json:"id,omitempty"`
		Model         string   `json:"model,omitempty"`
		Status        string   `json:"status,omitempty"`
		Outputs       []string `json:"outputs,omitempty"`
		Error         *string  `json:"error,omitempty"`
		ExecutionTime float64  `json:"executionTime,omitempty"`
		URLs          struct {
			Get string `json:"get,omitempty"`
		} `json:"urls,omitempty"`
	} `json:"data"`
}

func (p *GPTImage2Provider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()

	if !p.IsEnabled() {
		return &GenerationResult{
			Success:  false,
			Error:    "GPT Image 2 provider is not enabled or missing endpoint/API key",
			Provider: p.Name(),
		}, nil
	}

	prompt := req.Prompt
	if req.StylePreset != "" {
		prompt += StylePresetToPromptSuffix(req.StylePreset)
	}
	if neg := strings.TrimSpace(req.NegativePrompt); neg != "" {
		prompt += "\n\nAvoid: " + neg
	}

	modelCode := strings.TrimSpace(p.model)
	if modelCode == "" {
		modelCode = defaultGPTImage2Model
	}

	body := gptImage2CreateRequest{
		Prompt:       prompt,
		AspectRatio:  normalizeGPTImage2AspectRatio(req.AspectRatio),
		OutputFormat: normalizeGPTImage2OutputFormat(req.OutputFormat),
		Quality:      normalizeGPTImage2Quality(req.Quality),
	}

	endpointPath := fmt.Sprintf("/api/v3/openai/%s/text-to-image", modelCode)
	if urls := collectReferenceImageURLs(req.ReferenceImages); len(urls) > 0 {
		body.Images = urls
		endpointPath = fmt.Sprintf("/api/v3/openai/%s/image-edit", modelCode)
	}

	jobID, pollURL, err := p.submitJob(ctx, endpointPath, body)
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    err.Error(),
			Provider: p.Name(),
		}, nil
	}
	globals.Info(fmt.Sprintf("[GPT Image 2] submitted job=%s poll=%s", jobID, pollURL))
	if req.OnProviderJob != nil {
		req.OnProviderJob(jobID, nil)
	}

	outputs, pollErr := p.pollUntilTerminal(ctx, pollURL, req.OnProgress)
	if pollErr != nil {
		// Log job id alongside the upstream error so log-grep can join
		// the submit + failure events. Previous behavior dropped the
		// job id between the submit log and the WARNING from
		// generator_service, leaving only the human error string.
		globals.Warn(fmt.Sprintf("[GPT Image 2] job=%s failed: %s", jobID, pollErr.Error()))
		return &GenerationResult{
			Success:       false,
			Error:         pollErr.Error(),
			Provider:      p.Name(),
			ProviderJobID: jobID,
		}, nil
	}

	if len(outputs) == 0 {
		return &GenerationResult{
			Success:       false,
			Error:         "GPT Image 2 returned no image",
			Provider:      p.Name(),
			ProviderJobID: jobID,
		}, nil
	}

	// outputs[] from this gateway is sometimes a hosted https:// URL and
	// sometimes the raw base64 of the image (no data: prefix) — observed
	// in production: a single 2.6 MB base64 string came back where we
	// expected a URL. Splitting them is essential because:
	//   - URLs flow to saveRemoteFiles → download → R2 → public URL.
	//   - Bytes flow to saveToObjectStore → R2 → public URL.
	// Stuffing base64 into ImageURLs (the previous behavior) wrote the
	// raw blob into w_generation_task.result_data, blowing the JSON
	// column past 2 MB and breaking the next sort_buffer-bounded list
	// query downstream.
	imageURLs, imageData, decodeErrs := splitGPTImage2Outputs(outputs)
	if len(imageURLs) == 0 && len(imageData) == 0 {
		joined := strings.Join(decodeErrs, "; ")
		return &GenerationResult{
			Success:       false,
			Error:         fmt.Sprintf("GPT Image 2 returned outputs but none were decodable: %s", joined),
			Provider:      p.Name(),
			ProviderJobID: jobID,
		}, nil
	}
	for _, msg := range decodeErrs {
		globals.Warn(fmt.Sprintf("[GPT Image 2] job=%s output decode warning: %s", jobID, msg))
	}

	duration := time.Since(startTime).Milliseconds()
	globals.Info(fmt.Sprintf("[GPT Image 2] generated %d image(s) (urls=%d, bytes=%d) in %dms (job %s)",
		len(imageURLs)+len(imageData), len(imageURLs), len(imageData), duration, jobID))

	// Match nanobanana_official.go's return shape: never leave the
	// "other" slice nil — saveImages handles len()==0 either way, but
	// downstream code (logging, history reconstruction) is happier with
	// an explicit empty slice than a nil that requires defensive nil
	// checks at every read site.
	if imageURLs == nil {
		imageURLs = []string{}
	}
	if imageData == nil {
		imageData = [][]byte{}
	}

	return &GenerationResult{
		Success:       true,
		ImageURLs:     imageURLs,
		ImageData:     imageData,
		Seed:          req.Seed,
		Duration:      duration,
		Provider:      p.Name(),
		ProviderJobID: jobID,
	}, nil
}

// splitGPTImage2Outputs partitions the gateway's outputs[] strings into
// hosted URLs vs decoded image bytes. Accepts:
//   - http:// or https:// → forwarded as-is via ImageURLs
//   - data:image/<type>;base64,<payload> → decoded → ImageData
//   - bare base64 (no data: prefix) → decoded → ImageData
//
// Anything that fails to classify or decode produces an entry in errs so
// the caller can surface it. Empty / whitespace-only entries silently
// skipped.
func splitGPTImage2Outputs(outputs []string) (urls []string, data [][]byte, errs []string) {
	for i, raw := range outputs {
		out := strings.TrimSpace(raw)
		if out == "" {
			continue
		}
		lower := strings.ToLower(out)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			urls = append(urls, out)
			continue
		}
		// data: URL — strip the metadata prefix before decoding.
		if strings.HasPrefix(lower, "data:") {
			if comma := strings.Index(out, ","); comma >= 0 {
				payload := out[comma+1:]
				decoded, err := base64.StdEncoding.DecodeString(payload)
				if err != nil {
					errs = append(errs, fmt.Sprintf("output[%d] data-url decode: %v", i, err))
					continue
				}
				data = append(data, decoded)
				continue
			}
			errs = append(errs, fmt.Sprintf("output[%d] data-url missing payload", i))
			continue
		}
		// Bare base64 string (the gptsapi.net case observed in prod).
		decoded, err := base64.StdEncoding.DecodeString(out)
		if err != nil {
			errs = append(errs, fmt.Sprintf("output[%d] base64 decode: %v", i, err))
			continue
		}
		data = append(data, decoded)
	}
	return
}

// submitJob POSTs the create body and returns (jobID, pollURL). Both must
// be non-empty for the pipeline to make sense; missing fields are treated
// as a server-side error.
func (p *GPTImage2Provider) submitJob(ctx context.Context, path string, body gptImage2CreateRequest) (string, string, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", "", fmt.Errorf("marshal request: %w", err)
	}

	apiURL := p.endpoint + path
	globals.Info(fmt.Sprintf("[GPT Image 2] POST %s", apiURL))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return "", "", fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	client := &http.Client{Timeout: p.timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", "", fmt.Errorf("submit failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		preview := string(respBytes[:min(len(respBytes), 500)])
		globals.Error(fmt.Sprintf("[GPT Image 2] submit HTTP %d: %s", resp.StatusCode, preview))
		return "", "", fmt.Errorf("submit returned HTTP %d: %s", resp.StatusCode, preview)
	}

	var env gptImage2Envelope
	if err := json.Unmarshal(respBytes, &env); err != nil {
		return "", "", fmt.Errorf("parse response: %w", err)
	}
	if env.Code != 0 && env.Code != 200 {
		msg := env.Message
		if msg == "" {
			msg = "unknown gateway error"
		}
		return "", "", fmt.Errorf("gateway error (%d): %s", env.Code, msg)
	}
	if env.Data.ID == "" || env.Data.URLs.Get == "" {
		return "", "", fmt.Errorf("submit response missing job id or poll url")
	}
	return env.Data.ID, env.Data.URLs.Get, nil
}

// pollUntilTerminal polls the get URL on the provider's pollInterval. Caps
// the wait at p.maxWait via context derivation. Reports rough progress on
// each tick so the UI doesn't sit at 0% for the whole job.
func (p *GPTImage2Provider) pollUntilTerminal(ctx context.Context, getURL string, onProgress func(int, map[string]interface{})) ([]string, error) {
	pollCtx := ctx
	var cancel context.CancelFunc
	if p.maxWait > 0 {
		pollCtx, cancel = context.WithTimeout(ctx, p.maxWait)
		defer cancel()
	}

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	// Synthetic progress: server doesn't expose a numeric progress field,
	// so we ramp 5 → 90% across the expected wait so the user sees motion.
	// Ceiling at 90 leaves headroom for the post-completed download step.
	tickCount := 0
	maxTicks := int(p.maxWait/p.pollInterval) + 1
	if maxTicks <= 0 {
		maxTicks = 60
	}

	// First poll immediately — most jobs finish well before maxWait, so a
	// pre-tick check avoids burning the first interval for free.
	for {
		// pollWithInnerRetry re-queries the SAME job on transient HTTP /
		// network failures. This is the "auto retry == 重新查询" rule:
		// a flaky GET on /predictions/{id}/result must not abandon the
		// already-paid job. Resubmits live one layer up (the wrapper)
		// and only fire when ProviderJobID is empty (= no charge yet).
		env, err := p.pollWithInnerRetry(pollCtx, getURL)
		if err != nil {
			return nil, err
		}

		switch strings.ToLower(env.Data.Status) {
		case "completed", "succeeded":
			if onProgress != nil {
				onProgress(99, nil)
			}
			return env.Data.Outputs, nil
		case "failed", "error", "cancelled", "canceled":
			msg := "job failed"
			if env.Data.Error != nil && *env.Data.Error != "" {
				msg = *env.Data.Error
			}
			return nil, fmt.Errorf("%s", msg)
		}

		tickCount++
		if onProgress != nil {
			pct := 5 + (90-5)*tickCount/maxTicks
			if pct > 90 {
				pct = 90
			}
			onProgress(pct, nil)
		}

		select {
		case <-pollCtx.Done():
			return nil, fmt.Errorf("poll timed out after %s", p.maxWait)
		case <-ticker.C:
			// next iteration
		}
	}
}

// pollWithInnerRetry delegates to the shared helper so every async
// provider inherits the same auto-retry-equals-requery contract.
func (p *GPTImage2Provider) pollWithInnerRetry(ctx context.Context, getURL string) (*gptImage2Envelope, error) {
	return PollWithInnerRetry(ctx, "GPT Image 2", func(c context.Context) (*gptImage2Envelope, error) {
		return p.pollOnce(c, getURL)
	})
}

func (p *GPTImage2Provider) pollOnce(ctx context.Context, getURL string) (*gptImage2Envelope, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build poll request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: p.timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("poll request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read poll response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		preview := string(respBytes[:min(len(respBytes), 500)])
		return nil, fmt.Errorf("poll returned HTTP %d: %s", resp.StatusCode, preview)
	}
	var env gptImage2Envelope
	if err := json.Unmarshal(respBytes, &env); err != nil {
		return nil, fmt.Errorf("parse poll response: %w", err)
	}
	return &env, nil
}

// collectReferenceImageURLs prefers the public URL stamped in
// buildProviderReferenceImages. Skips anything without a URL since this
// API takes URLs only — no base64 path.
func collectReferenceImageURLs(refs []ReferenceImageData) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if u := strings.TrimSpace(ref.URL); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// normalizeGPTImage2AspectRatio canonicalizes the aspect ratio for the
// gateway. The API accepts canonical "W:H" strings; we trim whitespace
// and pass through values that look ratio-shaped, otherwise drop empty so
// the gateway picks its default.
func normalizeGPTImage2AspectRatio(raw string) string {
	r := strings.TrimSpace(raw)
	if r == "" {
		return ""
	}
	// Cheap shape check: looks like "<digits>:<digits>" (no other chars).
	parts := strings.Split(r, ":")
	if len(parts) != 2 {
		return ""
	}
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return ""
		}
	}
	return r
}

func normalizeGPTImage2Quality(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "low", "medium", "high", "auto":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func normalizeGPTImage2OutputFormat(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "png", "jpeg", "webp":
		return strings.ToLower(strings.TrimSpace(raw))
	case "jpg":
		return "jpeg"
	default:
		return ""
	}
}

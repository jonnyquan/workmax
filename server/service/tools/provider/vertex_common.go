package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"server/model"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	defaultVertexLocation = "us-central1"
	vertexCloudScope      = "https://www.googleapis.com/auth/cloud-platform"
)

type vertexConfig struct {
	projectID       string
	location        string
	endpoint        string
	model           string
	credentialsJSON string
	token           string
	headers         map[string]string
}

func isVertexProviderConfig(cfg *ProviderConfig) bool {
	if cfg == nil {
		return false
	}
	if model.NormalizeProviderType(cfg.Type) == model.ProviderTypeVertex {
		return true
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(cfg.Endpoint)), "aiplatform.googleapis.com") {
		return true
	}
	if cfg.ExtraConfig != nil && strings.TrimSpace(cfg.ExtraConfig.VertexProjectID) != "" {
		return true
	}
	return false
}

func newVertexConfig(cfg *ProviderConfig) vertexConfig {
	location := defaultVertexLocation
	projectID := ""
	credentialsJSON := ""
	headers := map[string]string(nil)
	if cfg != nil && cfg.ExtraConfig != nil {
		if v := strings.TrimSpace(cfg.ExtraConfig.VertexLocation); v != "" {
			location = v
		}
		projectID = strings.TrimSpace(cfg.ExtraConfig.VertexProjectID)
		credentialsJSON = strings.TrimSpace(cfg.ExtraConfig.VertexCredentialsJSON)
		if len(cfg.ExtraConfig.Headers) > 0 {
			headers = cfg.ExtraConfig.Headers
		}
	}
	endpoint := ""
	token := ""
	modelCode := ""
	if cfg != nil {
		endpoint = strings.TrimSpace(cfg.Endpoint)
		token = strings.TrimSpace(cfg.APIKey)
		modelCode = strings.TrimSpace(cfg.Model)
	}
	if endpoint == "" {
		if strings.EqualFold(location, "global") {
			endpoint = "https://aiplatform.googleapis.com"
		} else {
			endpoint = fmt.Sprintf("https://%s-aiplatform.googleapis.com", location)
		}
	}
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[len("Bearer "):])
	}
	return vertexConfig{
		projectID:       projectID,
		location:        location,
		endpoint:        strings.TrimRight(endpoint, "/"),
		model:           modelCode,
		credentialsJSON: credentialsJSON,
		token:           token,
		headers:         headers,
	}
}

func (c vertexConfig) modelPath(modelCode string) string {
	return fmt.Sprintf("/v1/projects/%s/locations/%s/publishers/google/models/%s", c.projectID, c.location, modelCode)
}

func (c vertexConfig) publisherModelPath(modelCode string) string {
	return fmt.Sprintf("/v1/publishers/google/models/%s", modelCode)
}

func (c vertexConfig) apiKeyMode() bool {
	token := strings.TrimSpace(c.token)
	if token == "" || strings.HasPrefix(token, "{") {
		return false
	}
	lower := strings.ToLower(token)
	return !strings.HasPrefix(lower, "ya29.") &&
		!strings.HasPrefix(lower, "y29.") &&
		!strings.HasPrefix(lower, "bearer ")
}

func (c vertexConfig) bearerToken(ctx context.Context) (string, error) {
	if c.token != "" && !strings.HasPrefix(strings.TrimSpace(c.token), "{") {
		return c.token, nil
	}
	if c.credentialsJSON != "" || strings.HasPrefix(strings.TrimSpace(c.token), "{") {
		raw := c.credentialsJSON
		if raw == "" {
			raw = c.token
		}
		creds, err := google.CredentialsFromJSON(ctx, []byte(raw), vertexCloudScope)
		if err != nil {
			return "", fmt.Errorf("invalid Vertex credentials JSON: %w", err)
		}
		tok, err := creds.TokenSource.Token()
		if err != nil {
			return "", fmt.Errorf("failed to get Vertex access token: %w", err)
		}
		return tok.AccessToken, nil
	}
	creds, err := google.FindDefaultCredentials(ctx, vertexCloudScope)
	if err != nil {
		return "", fmt.Errorf("Vertex credentials not configured: set API key bearer token, vertexCredentialsJson, or GOOGLE_APPLICATION_CREDENTIALS: %w", err)
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("failed to get Vertex ADC token: %w", err)
	}
	return tok.AccessToken, nil
}

func (c vertexConfig) doJSONRequest(ctx context.Context, method, path string, payload []byte, out interface{}) error {
	if c.projectID == "" {
		return fmt.Errorf("Vertex project id is required (extra_config.vertexProjectId)")
	}
	token, err := c.bearerToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create Vertex request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("Vertex request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := readLimitedProviderResponse(resp.Body, providerMaxJSONResponseLen)
	if err != nil {
		return fmt.Errorf("failed to read Vertex response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("Vertex http %d: %s", resp.StatusCode, strings.TrimSpace(truncateResponse(body, 300)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("failed to parse Vertex response: %w", err)
	}
	return nil
}

func (c vertexConfig) doAPIKeyJSONRequest(ctx context.Context, method, path string, payload []byte, out interface{}) error {
	apiKey := strings.TrimSpace(c.token)
	if apiKey == "" || strings.HasPrefix(apiKey, "{") {
		return fmt.Errorf("Vertex API key is required")
	}
	u, err := url.Parse(c.endpoint + path)
	if err != nil {
		return fmt.Errorf("failed to build Vertex API key request URL: %w", err)
	}
	q := u.Query()
	q.Set("key", apiKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create Vertex API key request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("Vertex API key request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := readLimitedProviderResponse(resp.Body, providerMaxJSONResponseLen)
	if err != nil {
		return fmt.Errorf("failed to read Vertex API key response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("Vertex http %d: %s", resp.StatusCode, strings.TrimSpace(truncateResponse(body, 300)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("failed to parse Vertex API key response: %w", err)
	}
	return nil
}

func (c vertexConfig) httpClient(ctx context.Context) (*http.Client, error) {
	if c.token != "" && !strings.HasPrefix(strings.TrimSpace(c.token), "{") {
		return oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: c.token, TokenType: "Bearer"})), nil
	}
	token, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	return oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"})), nil
}

//go:build desktop

package cloud_proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ErrListSkillsAuthExpired lets the Sidecar force one revision-fenced refresh
// after the Server rejects an access token that is still locally unexpired.
var ErrListSkillsAuthExpired = errors.New("list skills: auth expired (HTTP 401)")

// CloudSkillItem mirrors the per-skill JSON row returned by the
// cloud's GET /api/work-agent/skills endpoint. Field tags match the
// cloud's `skillCatalogItem` (server/api/pro/tools/workagent/
// skill_catalog_api.go) so the desktop sidecar can re-emit the same
// shape without translation.
//
// We deliberately don't import the cloud handler's type — pulling it
// in would drag the GORM + service stack into the desktop build for
// 7 fields of JSON.
type CloudSkillItem struct {
	AgentMode             string               `json:"agentMode"`
	Name                  string               `json:"name"`
	Description           string               `json:"description"`
	Version               string               `json:"version"`
	HasQuestionForm       bool                 `json:"hasQuestionForm"`
	HasDirectionsFallback bool                 `json:"hasDirectionsFallback"`
	HasPostScripts        bool                 `json:"hasPostScripts"`
	Artifacts             *CloudSkillArtifacts `json:"artifacts,omitempty"`
	LabelKey              string               `json:"labelKey"`
	DescriptionKey        string               `json:"descriptionKey"`
}

// CloudSkillArtifacts mirrors skills.ArtifactMetadata without
// importing the cloud skills package into the desktop build.
type CloudSkillArtifacts struct {
	PrimaryType     string   `json:"primaryType"`
	OutputTypes     []string `json:"outputTypes"`
	PreviewTypes    []string `json:"previewTypes"`
	ExportTargets   []string `json:"exportTargets"`
	CritiqueAnchors []string `json:"critiqueAnchors"`
}

// cloudListSkillsResponse is the gin-vue-admin response envelope the
// cloud handler wraps everything in.
type cloudListSkillsResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Items []CloudSkillItem `json:"items"`
		Count int              `json:"count"`
	} `json:"data"`
}

// ListSkills calls the cloud's GET /api/work-agent/skills endpoint
// with Bearer auth and returns the unwrapped item list. The caller
// (the sidecar handler) applies the desktop allowlist filter; this client
// stays "dumb proxy" so a future build that wants the full list (e.g.
// browse-mode) can reuse it untouched.
//
// Returns an error if the upstream is unreachable, non-2xx, or
// returns code != response.SUCCESS (1). A lease-bound context canceled by a
// login/logout transition returns ErrSessionChanged instead of a generic
// transport cancellation.
func (c *Client) ListSkills(ctx context.Context, accessToken string) ([]CloudSkillItem, error) {
	if err := sessionChangedContextError(ctx); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("list skills: HTTP: client is missing")
	}
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("list skills: HTTP: cloud base URL is invalid")
	}
	url := baseURL + CloudRouteSkillsList
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("list skills: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	SetClientHeaders(req.Header)

	resp, err := c.credentialHTTPClient().Do(req)
	if err != nil {
		if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
			return nil, sessionErr
		}
		return nil, fmt.Errorf("list skills: HTTP: %w", err)
	}
	defer resp.Body.Close()

	body, err := readBoundedCloudResponseBody(resp, 1<<20)
	if err != nil {
		if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
			return nil, sessionErr
		}
		return nil, fmt.Errorf("list skills: invalid response body")
	}
	defer clear(body)
	if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
		return nil, sessionErr
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrListSkillsAuthExpired
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list skills: HTTP %d", resp.StatusCode)
	}

	var env cloudListSkillsResponse
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("list skills: invalid JSON response")
	}
	// gin-vue-admin convention: code=1 means SUCCESS.
	const successCode = 1
	if env.Code != successCode {
		return nil, fmt.Errorf("list skills: Server rejected request (code %d)", env.Code)
	}
	if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
		return nil, sessionErr
	}
	return env.Data.Items, nil
}

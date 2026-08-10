//go:build desktop

package cloud_proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ErrListModelsAuthExpired lets the Sidecar force one revision-fenced refresh
// after the Server rejects an access token that is still locally unexpired.
var ErrListModelsAuthExpired = errors.New("list models: auth expired (HTTP 401)")

// CloudModelItem is one entry of the official model catalog.
//
// Metadata ONLY. There is deliberately no api key and no endpoint on this
// shape, and there must never be one: official inference runs through the
// cloud's own chat route, so the desktop never needs a credential for it, and
// a credential the desktop never needs is a credential it must never hold.
//
// Permissions is the entitlement answer for THIS account right now. An empty
// slice means "this model exists and you can see it, but your tier cannot use
// it" — visible-but-locked is a product decision (people should be able to see
// what an upgrade buys), not an error state.
type CloudModelItem struct {
	ModelID      string   `json:"modelId"`
	DisplayName  string   `json:"displayName"`
	Description  string   `json:"description"`
	RequiredTier string   `json:"requiredTier"`
	Permissions  []string `json:"permissions"`
	Default      bool     `json:"default"`
}

// CloudModelPermissionUse is the permission that makes a catalog entry
// selectable. Anything else present alongside it is additive.
const CloudModelPermissionUse = "use"

// Usable reports whether this account may currently run turns on this model.
func (i CloudModelItem) Usable() bool {
	for _, permission := range i.Permissions {
		if permission == CloudModelPermissionUse {
			return true
		}
	}
	return false
}

// CloudModelCatalog is the whole GET /api/desktop/models response.
type CloudModelCatalog struct {
	Items         []CloudModelItem `json:"items"`
	Tier          string           `json:"tier"`
	TierExpiresAt string           `json:"tierExpiresAt"`
}

// ListModels fetches the official model catalog with Bearer auth.
//
// Shaped like userinfo rather than list_skills: this is a Desktop-surface
// route, so it answers with the resource directly instead of the
// gin-vue-admin {code,message,data} envelope the legacy /api/work-agent
// routes use. A lease-bound context canceled by a login/logout transition
// returns ErrSessionChanged instead of a generic transport cancellation.
func (c *Client) ListModels(ctx context.Context, accessToken string) (CloudModelCatalog, error) {
	if err := sessionChangedContextError(ctx); err != nil {
		return CloudModelCatalog{}, err
	}
	if c == nil {
		return CloudModelCatalog{}, fmt.Errorf("list models: HTTP: client is missing")
	}
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return CloudModelCatalog{}, fmt.Errorf("list models: HTTP: cloud base URL is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+CloudRouteModelsList, nil)
	if err != nil {
		return CloudModelCatalog{}, fmt.Errorf("list models: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	SetClientHeaders(req.Header)

	resp, err := c.credentialHTTPClient().Do(req)
	if err != nil {
		if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
			return CloudModelCatalog{}, sessionErr
		}
		return CloudModelCatalog{}, fmt.Errorf("list models: HTTP: %w", err)
	}
	defer resp.Body.Close()

	body, err := readBoundedCloudResponseBody(resp, 256<<10)
	if err != nil {
		if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
			return CloudModelCatalog{}, sessionErr
		}
		return CloudModelCatalog{}, fmt.Errorf("list models: invalid response body")
	}
	defer clear(body)
	if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
		return CloudModelCatalog{}, sessionErr
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return CloudModelCatalog{}, ErrListModelsAuthExpired
	}
	if resp.StatusCode != http.StatusOK {
		return CloudModelCatalog{}, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
	}

	var out CloudModelCatalog
	if err := json.Unmarshal(body, &out); err != nil {
		return CloudModelCatalog{}, fmt.Errorf("list models: invalid JSON response")
	}
	// A catalog entry with no id cannot be selected, stored, or sent back on a
	// turn. Dropping it here keeps every consumer from having to re-ask.
	filtered := make([]CloudModelItem, 0, len(out.Items))
	for _, item := range out.Items {
		if item.ModelID == "" {
			continue
		}
		if item.Permissions == nil {
			item.Permissions = []string{}
		}
		filtered = append(filtered, item)
	}
	out.Items = filtered
	if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
		return CloudModelCatalog{}, sessionErr
	}
	return out, nil
}

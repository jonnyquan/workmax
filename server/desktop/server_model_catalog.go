//go:build desktop

package desktop

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	cloudproxy "server/desktop/cloud_proxy"
)

// GET /settings/model-catalog — which official models may this identity pick?
//
// The catalog is the cloud's answer and only the cloud's: entitlement follows
// a membership that can lapse, upgrade, or be downgraded without anything
// happening on this machine. So the sidecar caches it briefly and never
// invents it. What the sidecar DOES own is the join — which of those models
// this identity previously chose, and whether that choice still stands — so
// that the renderer never has to re-derive a verdict the sidecar already knows.
//
// Metadata only in both directions: nothing here carries an API key or an
// endpoint, because official turns run through the cloud's own chat route.

// Catalog availability, as the renderer sees it.
const (
	// ModelCatalogUnbound: no WorkMax account is connected. Not an error —
	// the local route is a complete way to use this app — so the answer is an
	// empty catalog with a reason, not a 401 the renderer has to interpret.
	ModelCatalogUnbound = "unbound"
	// ModelCatalogReady: the list below is the cloud's current answer.
	ModelCatalogReady = "ready"
	// ModelCatalogUnavailable: an account is connected but the cloud could
	// not be asked (offline, transient failure, expired credential). Distinct
	// from "ready with zero models", which would be a lie about entitlement.
	ModelCatalogUnavailable = "unavailable"
)

// How the stored choice stands against the catalog just fetched.
const (
	// ModelSelectionUnset: nothing chosen; the account default applies.
	ModelSelectionUnset = "unset"
	// ModelSelectionOK: chosen, present, and usable.
	ModelSelectionOK = "ok"
	// ModelSelectionNotAllowed: chosen and still listed, but this tier may no
	// longer use it — a lapsed membership or a downgrade. NOT silently
	// replaced with something that would work: OSS-4 forbids a quiet fallback
	// across routes, and quietly answering on a cheaper model than the user
	// picked is the same betrayal one level down.
	ModelSelectionNotAllowed = "not_allowed"
	// ModelSelectionUnknown: chosen but no longer in the catalog at all
	// (retired model, or a different account is connected now).
	ModelSelectionUnknown = "unknown"
	// ModelSelectionUnverified: chosen, but the catalog could not be read, so
	// no verdict is available. Explicitly not "ok".
	ModelSelectionUnverified = "unverified"
)

// modelCatalogTTL keeps opening Settings from being a cloud round trip every
// time. Short enough that an upgrade shows up almost immediately; long enough
// that the form can be opened and closed without hammering the endpoint.
const modelCatalogTTL = 60 * time.Second

type modelCatalogResponse struct {
	State           string                      `json:"state"`
	Items           []cloudproxy.CloudModelItem `json:"items"`
	Count           int                         `json:"count"`
	Tier            string                      `json:"tier"`
	TierExpiresAt   string                      `json:"tier_expires_at"`
	SelectedModelID string                      `json:"selected_model_id"`
	SelectionState  string                      `json:"selection_state"`
}

type modelCatalogEntry struct {
	catalog   cloudproxy.CloudModelCatalog
	fetchedAt time.Time
}

// modelCatalogCache is per-identity because the catalog is per-account: the
// same machine can hold two accounts on different tiers, and one cache line
// would hand the second account the first one's entitlement.
type modelCatalogCache struct {
	mu      sync.Mutex
	entries map[uint64]modelCatalogEntry
}

func newModelCatalogCache() *modelCatalogCache {
	return &modelCatalogCache{entries: make(map[uint64]modelCatalogEntry)}
}

func (c *modelCatalogCache) get(uid uint64, now time.Time) (cloudproxy.CloudModelCatalog, bool) {
	if c == nil {
		return cloudproxy.CloudModelCatalog{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[uid]
	if !ok || now.Sub(entry.fetchedAt) >= modelCatalogTTL {
		return cloudproxy.CloudModelCatalog{}, false
	}
	return entry.catalog, true
}

func (c *modelCatalogCache) put(uid uint64, catalog cloudproxy.CloudModelCatalog, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[uid] = modelCatalogEntry{catalog: catalog, fetchedAt: now}
}

func (s *Server) handleModelCatalog(c *gin.Context) {
	if s.cfg.ModelSettings == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "model settings unavailable"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	settings, err := s.cfg.ModelSettings.Get(identity.UID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "model settings read failed"})
		return
	}
	selected := settings.OfficialModelID

	if s.cfg.Proxy == nil || s.cfg.TokenStore == nil || !identity.IsCloud() {
		writeModelCatalog(c, modelCatalogResponse{
			State:           ModelCatalogUnbound,
			Items:           []cloudproxy.CloudModelItem{},
			SelectedModelID: selected,
			SelectionState:  unverifiedSelectionState(selected),
		})
		return
	}

	catalog, err := s.fetchModelCatalog(c, identity)
	if err != nil {
		if errors.Is(err, cloudproxy.ErrSessionChanged) {
			writeSessionChanged(c)
			return
		}
		// Every other failure is "could not ask", including an expired
		// credential. The renderer shows the last thing it knew plus a
		// reason; it must not conclude the account lost its models.
		writeModelCatalog(c, modelCatalogResponse{
			State:           ModelCatalogUnavailable,
			Items:           []cloudproxy.CloudModelItem{},
			SelectedModelID: selected,
			SelectionState:  unverifiedSelectionState(selected),
		})
		return
	}

	writeModelCatalog(c, modelCatalogResponse{
		State:           ModelCatalogReady,
		Items:           catalog.Items,
		Count:           len(catalog.Items),
		Tier:            catalog.Tier,
		TierExpiresAt:   catalog.TierExpiresAt,
		SelectedModelID: selected,
		SelectionState:  classifyModelSelection(selected, catalog.Items),
	})
}

func writeModelCatalog(c *gin.Context, body modelCatalogResponse) {
	if body.Items == nil {
		body.Items = []cloudproxy.CloudModelItem{}
	}
	body.Count = len(body.Items)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, body)
}

func unverifiedSelectionState(selected string) string {
	if selected == "" {
		return ModelSelectionUnset
	}
	return ModelSelectionUnverified
}

// classifyModelSelection is the whole "do not silently fall back" rule, in one
// place: a stored choice is either still usable or it is a question for the
// user. Nothing here picks a replacement.
func classifyModelSelection(selected string, items []cloudproxy.CloudModelItem) string {
	if selected == "" {
		return ModelSelectionUnset
	}
	for _, item := range items {
		if item.ModelID != selected {
			continue
		}
		if item.Usable() {
			return ModelSelectionOK
		}
		return ModelSelectionNotAllowed
	}
	return ModelSelectionUnknown
}

// fetchModelCatalog serves the cache when it is warm and otherwise performs
// the same lease-bound, refresh-once cloud call the skills catalog does.
func (s *Server) fetchModelCatalog(c *gin.Context, identity requestIdentity) (cloudproxy.CloudModelCatalog, error) {
	now := time.Now()
	if cached, ok := s.modelCatalog.get(identity.UID, now); ok {
		return cached, nil
	}

	cloud := s.cfg.Proxy.CloudClient()
	pair, lease, err := cloudproxy.AcquireAccessTokenWithLease(c.Request.Context(), s.cfg.TokenStore, cloud)
	if err != nil {
		return cloudproxy.CloudModelCatalog{}, err
	}
	leaseContext, releaseLease := lease.BindContext(c.Request.Context())
	defer releaseLease()

	catalog, err := cloud.ListModels(leaseContext, pair.AccessToken)
	if errors.Is(err, cloudproxy.ErrSessionChanged) || errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
		return cloudproxy.CloudModelCatalog{}, cloudproxy.ErrSessionChanged
	}
	if errors.Is(err, cloudproxy.ErrListModelsAuthExpired) {
		freshPair, refreshErr := cloudproxy.RefreshAccessTokenAfterUnauthorizedWithLease(
			leaseContext, s.cfg.TokenStore, cloud, pair, lease,
		)
		if refreshErr != nil {
			if errors.Is(refreshErr, cloudproxy.ErrSessionChanged) {
				return cloudproxy.CloudModelCatalog{}, cloudproxy.ErrSessionChanged
			}
			return cloudproxy.CloudModelCatalog{}, refreshErr
		}
		// Exactly one retry; a second 401 stays a failure.
		catalog, err = cloud.ListModels(leaseContext, freshPair.AccessToken)
		if errors.Is(err, cloudproxy.ErrSessionChanged) {
			return cloudproxy.CloudModelCatalog{}, cloudproxy.ErrSessionChanged
		}
	}
	if err != nil {
		return cloudproxy.CloudModelCatalog{}, err
	}
	if errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
		return cloudproxy.CloudModelCatalog{}, cloudproxy.ErrSessionChanged
	}
	s.modelCatalog.put(identity.UID, catalog, now)
	return catalog, nil
}

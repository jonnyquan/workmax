// Package models serves the Desktop conversation-model catalog at
// GET /api/desktop/models.
//
// What this endpoint is for: after the Desktop client binds an account, the
// user should be able to ask "which conversation models does my membership
// let me use", pick one by name, and never type an endpoint or an API key.
//
// What it deliberately is NOT: a credential channel. The response carries
// display metadata and an entitlement verdict only — no provider identity, no
// base URL, no key, no routing hint. Inference still goes through
// POST /api/work-agent/chat/agent, which keeps owning provider selection and
// secrets. A client that got a key here could bypass metering entirely.
//
// Shape follows the skill catalog (api/pro/tools/workagent/skill_catalog_api.go):
// return EVERY item, each with its requiredTier and a permissions array, and
// let the client grey out what it cannot use. Hiding locked models would make
// the paid tier invisible — the user can't want what they can't see.
package models

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"server/middleware"
	"server/model"
	"server/service/globalmodel"
)

// ModelCatalogApi holds the DB handle. Constructed once in
// initialize/router.go rather than reading globals per request, matching
// OauthApi / SyncApi.
type ModelCatalogApi struct {
	DB *gorm.DB
}

func NewModelCatalogApi(db *gorm.DB) *ModelCatalogApi {
	return &ModelCatalogApi{DB: db}
}

// catalogResponse is the wire body. Field names are lowerCamelCase to match
// the skill catalog the Desktop already consumes.
type catalogResponse struct {
	Items []catalogItem `json:"items"`
	// Tier is the CALLER's effective tier (expiry already applied), so the
	// client can render "your plan: pro" without a second round-trip and
	// without re-deriving expiry logic of its own.
	Tier string `json:"tier"`
	// TierExpiresAt is RFC3339 UTC, omitted for callers with no paid window.
	TierExpiresAt string `json:"tierExpiresAt,omitempty"`
}

type catalogItem struct {
	// ModelID is a value POST /api/work-agent/chat/agent accepts as
	// metadata.modelTier. Anything else here would let a user pick a name that
	// then fails at send time.
	ModelID     string `json:"modelId"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	// RequiredTier is the minimum membership that unlocks the row, so the
	// client can say WHY something is locked and what upgrade fixes it.
	RequiredTier string `json:"requiredTier"`
	// Permissions is ["use"] when the caller may select the model right now,
	// and an empty array when the row is visible but locked. Never null —
	// clients index it directly.
	Permissions []string `json:"permissions"`
	// Default marks the model to preselect on a fresh install.
	Default bool `json:"default"`
}

// ListModels handles GET /api/desktop/models. Auth is the Desktop OAuth
// Bearer middleware, same as /api/desktop/sync/*.
//
// Always 200 once auth clears: an empty catalog is a legitimate state (a
// deployment that has not run the seed migration yet), not an error.
func (a *ModelCatalogApi) ListModels(c *gin.Context) {
	claims, ok := middleware.OAuthClaims(c)
	if !ok || claims.BaseClaims.Id == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if a.DB == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database is not configured"})
		return
	}

	db := a.DB.WithContext(c.Request.Context())

	var user model.User
	if err := db.Where("id = ?", claims.BaseClaims.Id).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	now := time.Now()
	callerTier := model.EffectiveMemberTier(user.Member, user.MemberEndTime, now)

	rows, err := globalmodel.NewRepository(db).ListEnabled(model.MediaTypeText)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	resp := catalogResponse{
		Items: buildCatalogItems(rows, callerTier),
		Tier:  callerTier,
	}
	// Only a caller with a live paid window gets an expiry. A free user's
	// member_end_time (the free-plan window, or a lapsed subscription) is not
	// a "your tier expires then" fact and would misread as one.
	if model.IsActivePaidMember(user.Member, user.MemberEndTime, now) && !user.MemberEndTime.IsZero() {
		resp.TierExpiresAt = user.MemberEndTime.UTC().Format(time.RFC3339)
	}

	c.JSON(http.StatusOK, resp)
}

// buildCatalogItems is separated from the handler so the entitlement mapping
// can be tested without a gin context.
func buildCatalogItems(rows []model.GlobalModel, callerTier string) []catalogItem {
	type rankedItem struct {
		item      catalogItem
		sortOrder int
	}

	ranked := make([]rankedItem, 0, len(rows))
	for _, row := range rows {
		requiredTier := globalmodel.NormalizeRequiredTier(row.RequiredTier)

		// Empty array, not nil: `permissions: []` is the wire signal for
		// "visible but locked", and a nil slice would marshal as null.
		permissions := make([]string, 0, 1)
		if globalmodel.TierSatisfiesRequirement(callerTier, requiredTier) {
			permissions = append(permissions, "use")
		}

		ranked = append(ranked, rankedItem{
			item: catalogItem{
				ModelID:      row.ModelID,
				DisplayName:  displayNameOrID(row),
				Description:  stringFromJSONMap(row.Metadata, model.GlobalModelMetadataDescription),
				RequiredTier: requiredTier,
				Permissions:  permissions,
				Default:      boolFromJSONMap(row.Capabilities, "default"),
			},
			sortOrder: row.SortOrder,
		})
	}

	// ListEnabled already orders by sort_order DESC, id ASC. Re-sorting here
	// keeps the wire order deterministic even if a caller hands in rows from
	// somewhere else, and breaks ties by modelId so the picker never reshuffles
	// between two requests.
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].sortOrder != ranked[j].sortOrder {
			return ranked[i].sortOrder > ranked[j].sortOrder
		}
		return ranked[i].item.ModelID < ranked[j].item.ModelID
	})

	items := make([]catalogItem, 0, len(ranked))
	for _, entry := range ranked {
		items = append(items, entry.item)
	}
	return items
}

// displayNameOrID keeps a row usable when operations forgot the display name:
// showing the raw model_id beats showing an empty button.
func displayNameOrID(row model.GlobalModel) string {
	if row.DisplayName != "" {
		return row.DisplayName
	}
	return row.ModelID
}

func stringFromJSONMap(source model.JSONMap, key string) string {
	if source == nil {
		return ""
	}
	value, ok := source[key].(string)
	if !ok {
		return ""
	}
	return value
}

func boolFromJSONMap(source model.JSONMap, key string) bool {
	if source == nil {
		return false
	}
	value, ok := source[key].(bool)
	return ok && value
}

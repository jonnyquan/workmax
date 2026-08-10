package modelgateway

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"server/model"
	"server/service/globalmodel"
)

// ResolvedModel is the outcome of admitting one request's `model` field: a
// catalog row the caller is entitled to right now, plus the real provider
// model name to put on the wire.
type ResolvedModel struct {
	// ModelID is the catalog id the caller sent ("work-pro").
	ModelID string
	// UpstreamModel is what actually goes upstream ("claude-sonnet-5").
	UpstreamModel string
	// RequiredTier is the row's normalized entitlement bar.
	RequiredTier string
	// CallerTier is the caller's effective tier at admission time.
	CallerTier string
}

// ModelResolver admits a requested model for a specific caller.
//
// Entitlement is evaluated on EVERY request against the live user row, not
// captured once when the Desktop bound its account. A membership that lapses
// mid-session must stop working on the next call; a token minted while the
// user was paid must not keep buying inference for the rest of its lifetime.
type ModelResolver struct {
	db *gorm.DB
}

func NewModelResolver(db *gorm.DB) *ModelResolver {
	return &ModelResolver{db: db}
}

// Resolve validates that modelID names an enabled conversation model, that
// the caller's CURRENT membership clears its required_tier, and that ops has
// mapped it to a provider model name.
//
// The three refusals are deliberately distinguishable to the caller —
// not-found, locked, not-configured are three different things to do about it
// — but none of them names another model. A caller who wants the menu asks
// GET /api/desktop/models, which is authenticated and rate-limited on its own
// terms; this endpoint must not become a second, cheaper enumeration oracle.
func (r *ModelResolver) Resolve(db *gorm.DB, uid uint, modelID string) (*ResolvedModel, *GatewayError) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil, newError(http.StatusBadRequest, ErrClassInvalidRequest,
			"the `model` field is required and must be a model id from GET /api/desktop/models")
	}
	if db == nil {
		return nil, newError(http.StatusInternalServerError, ErrClassInternal, "database is not configured")
	}

	var user model.User
	if err := db.Where("id = ?", uid).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, newError(http.StatusUnauthorized, ErrClassUnauthorized, "account not found")
		}
		return nil, newError(http.StatusInternalServerError, ErrClassInternal, "failed to load account")
	}

	var row model.GlobalModel
	err := db.Where("model_id = ? AND media_type = ? AND status = ? AND deleted_at IS NULL",
		modelID, model.MediaTypeText, model.GlobalModelStatusEnabled).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Disabled rows land here too, and that is correct: an ops
			// kill-switch should read as "this model does not exist" rather
			// than as a temporary hiccup the client retries against.
			return nil, newError(http.StatusNotFound, ErrClassModelNotFound,
				"unknown model; use a model id from GET /api/desktop/models")
		}
		return nil, newError(http.StatusInternalServerError, ErrClassInternal, "failed to load model catalog")
	}

	requiredTier := globalmodel.NormalizeRequiredTier(row.RequiredTier)
	callerTier := model.EffectiveMemberTier(user.Member, user.MemberEndTime, time.Now())
	if !globalmodel.TierSatisfiesRequirement(callerTier, requiredTier) {
		return nil, newError(http.StatusForbidden, ErrClassInsufficientTier,
			"this model requires the "+requiredTier+" plan")
	}

	upstreamModel := stringFromJSONMap(row.Metadata, model.GlobalModelMetadataUpstreamModel)
	if upstreamModel == "" {
		// Fail closed. Forwarding the catalog id would produce a provider-side
		// 404 that looks like an outage instead of the configuration gap it is.
		return nil, newError(http.StatusServiceUnavailable, ErrClassModelNotConfigured,
			"this model is not available through the gateway yet")
	}

	return &ResolvedModel{
		ModelID:       modelID,
		UpstreamModel: upstreamModel,
		RequiredTier:  requiredTier,
		CallerTier:    callerTier,
	}, nil
}

func stringFromJSONMap(source model.JSONMap, key string) string {
	if source == nil {
		return ""
	}
	value, ok := source[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

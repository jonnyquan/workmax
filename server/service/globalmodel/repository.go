package globalmodel

import (
	"strings"

	"server/globals"
	"server/model"

	"gorm.io/gorm"
)

// Repository owns the platform model catalog. It intentionally does not make
// provider routing decisions; w_generator_provider remains the routing source.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func Default() *Repository {
	return NewRepository(globals.GraDBs["system"])
}

type UpsertInput struct {
	ModelID       string
	MediaType     string
	ProviderType  string
	DisplayName   string
	Status        *int8
	PricingStatus string
	SortOrder     int
	RequiredTier  string
	Capabilities  model.JSONMap
	Metadata      model.JSONMap
}

func (r *Repository) Upsert(in UpsertInput) (*model.GlobalModel, error) {
	row := model.GlobalModel{
		ModelID:       strings.TrimSpace(in.ModelID),
		MediaType:     strings.TrimSpace(in.MediaType),
		ProviderType:  strings.TrimSpace(in.ProviderType),
		DisplayName:   strings.TrimSpace(in.DisplayName),
		Status:        normalizeStatus(in.Status),
		PricingStatus: strings.TrimSpace(in.PricingStatus),
		SortOrder:     in.SortOrder,
		RequiredTier:  NormalizeRequiredTier(in.RequiredTier),
		Capabilities:  normalizeJSONMap(in.Capabilities),
		Metadata:      normalizeJSONMap(in.Metadata),
	}
	if row.ModelID == "" || row.MediaType == "" {
		return nil, gorm.ErrRecordNotFound
	}
	err := r.db.
		Where("model_id = ? AND media_type = ?", row.ModelID, row.MediaType).
		Assign(map[string]interface{}{
			"provider_type":  row.ProviderType,
			"display_name":   row.DisplayName,
			"status":         row.Status,
			"pricing_status": row.PricingStatus,
			"sort_order":     row.SortOrder,
			"required_tier":  row.RequiredTier,
			"capabilities":   row.Capabilities,
			"metadata":       row.Metadata,
		}).
		FirstOrCreate(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) LoadEnabledByModelID(modelID, mediaType string) (*model.GlobalModel, error) {
	var row model.GlobalModel
	err := r.db.
		Where("model_id = ? AND media_type = ? AND status = ? AND deleted_at IS NULL",
			strings.TrimSpace(modelID),
			strings.TrimSpace(mediaType),
			model.GlobalModelStatusEnabled,
		).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) ListEnabled(mediaType string) ([]model.GlobalModel, error) {
	var rows []model.GlobalModel
	err := r.db.
		Where("media_type = ? AND status = ? AND deleted_at IS NULL", strings.TrimSpace(mediaType), model.GlobalModelStatusEnabled).
		Order("sort_order DESC, id ASC").
		Find(&rows).Error
	return rows, err
}

// NormalizeRequiredTier maps a stored required_tier onto the tier vocabulary
// in model/user.go. An empty or unrecognized value reads as free rather than
// as "deny everyone": a typo in an ops config must not silently take a model
// away from every user, and the runtime gate (IsPremiumMember before a
// work-plus turn) is the real enforcement either way.
func NormalizeRequiredTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case model.MemberTierEnterprise:
		return model.MemberTierEnterprise
	case model.MemberTierPro, "premium", "paid":
		return model.MemberTierPro
	default:
		return model.MemberTierFree
	}
}

// TierSatisfiesRequirement reports whether a caller's effective tier clears a
// row's required_tier. Ordering is free < pro < enterprise.
func TierSatisfiesRequirement(callerTier, requiredTier string) bool {
	return tierRank(callerTier) >= tierRank(NormalizeRequiredTier(requiredTier))
}

func tierRank(tier string) int {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case model.MemberTierEnterprise:
		return 2
	case model.MemberTierPro, "premium", "paid":
		return 1
	default:
		return 0
	}
}

func normalizeStatus(status *int8) int8 {
	if status != nil && *status == model.GlobalModelStatusDisabled {
		return model.GlobalModelStatusDisabled
	}
	return model.GlobalModelStatusEnabled
}

func normalizeJSONMap(v model.JSONMap) model.JSONMap {
	if v == nil {
		return model.JSONMap{}
	}
	return v
}

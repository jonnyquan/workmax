package workagent

// character_asset_api.go — Backlog #11 11/n + Sprint-D 4/7. Five
// thin wrappers over the generic asset_library handlers; same
// shape as brand_asset_api.go's wrappers. Phase 6 deletes these
// once tests migrate to the generic handler entry points.
//
// characterAssetSummary stays as the test-side unmarshal target.
// Summary's MarshalJSON produces a JSON shape matching this
// struct's tags exactly.

import (
	"server/service/asset_library"

	"github.com/gin-gonic/gin"
)

type characterAssetSummary struct {
	ID                 uint                                    `json:"id"`
	UID                int                                     `json:"uid"`
	Name               string                                  `json:"name"`
	Slug               string                                  `json:"slug"`
	Role               string                                  `json:"role"`
	Gender             string                                  `json:"gender,omitempty"`
	AgeRange           string                                  `json:"ageRange,omitempty"`
	Status             asset_library.Status                    `json:"status"`
	SourceKind         asset_library.SourceKind                `json:"sourceKind"`
	Confirmed          bool                                    `json:"confirmed"`
	HasReference       bool                                    `json:"hasReference"`
	CanonicalImagePath string                                  `json:"canonicalImagePath,omitempty"`
	CreatedAt          string                                  `json:"createdAt"`
	UpdatedAt          string                                  `json:"updatedAt"`
}

func characterDescriptorForAPI() asset_library.Descriptor {
	d, _ := asset_library.Default().Get(asset_library.AssetKindCharacter)
	return d
}

func (api *AIChatApiNew) ListCharacterAssets(c *gin.Context) {
	handleListAssets(characterDescriptorForAPI())(c)
}

func (api *AIChatApiNew) GetCharacterAsset(c *gin.Context) {
	handleGetAsset(characterDescriptorForAPI())(c)
}

func (api *AIChatApiNew) ConfirmCharacterAsset(c *gin.Context) {
	handleConfirmAsset(characterDescriptorForAPI())(c)
}

func (api *AIChatApiNew) RestoreCharacterAsset(c *gin.Context) {
	handleRestoreAsset(characterDescriptorForAPI())(c)
}

func (api *AIChatApiNew) DeleteCharacterAsset(c *gin.Context) {
	handleDeleteAsset(characterDescriptorForAPI())(c)
}

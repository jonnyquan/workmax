package workagent

// brand_asset_api.go — Backlog #11 4/n + Sprint-D 4/7. The five
// brand-library REST handlers (List / Get / Confirm / Restore /
// Delete) are now thin wrappers around the generic asset_library
// handlers in asset_library_api.go. Kept by name because
// brand_asset_api_test.go references them directly; Phase 6
// migrates tests to the generic handler entry points + deletes
// these wrappers.
//
// brandAssetSummary stays as the test-side unmarshal target — the
// JSON shape is preserved byte-for-byte by Summary's MarshalJSON
// (Extras flattened to top level), so existing test assertions
// against brandAssetSummary fields keep working unchanged.
//
// IDOR posture matches the rest of the work-agent module: every
// uid'd read puts uid in the WHERE clause via the descriptor's
// *ForOwner methods so a forgotten ownership check at the call
// site can't serve another user's brand.

import (
	"strconv"

	"server/service/asset_library"

	"github.com/gin-gonic/gin"
)

// brandAssetSummary — kept as the JSON unmarshal target for
// brand_asset_api_test.go. The wire shape Summary.MarshalJSON
// produces matches this struct's tags exactly (id, uid, name,
// slug, status, sourceKind, confirmed, hasColors, hasFonts,
// paletteSwatches, createdAt, updatedAt).
type brandAssetSummary struct {
	ID              uint                                `json:"id"`
	UID             int                                 `json:"uid"`
	Name            string                              `json:"name"`
	Slug            string                              `json:"slug"`
	Status          asset_library.Status     `json:"status"`
	SourceKind      asset_library.SourceKind `json:"sourceKind"`
	Confirmed       bool                                `json:"confirmed"`
	HasColors       bool                                `json:"hasColors"`
	HasFonts        bool                                `json:"hasFonts"`
	PaletteSwatches []string                            `json:"paletteSwatches,omitempty"`
	CreatedAt       string                              `json:"createdAt"`
	UpdatedAt       string                              `json:"updatedAt"`
}

func brandDescriptorForAPI() asset_library.Descriptor {
	d, _ := asset_library.Default().Get(asset_library.AssetKindBrand)
	return d
}

func (api *AIChatApiNew) ListBrandAssets(c *gin.Context) {
	handleListAssets(brandDescriptorForAPI())(c)
}

func (api *AIChatApiNew) GetBrandAsset(c *gin.Context) {
	handleGetAsset(brandDescriptorForAPI())(c)
}

func (api *AIChatApiNew) ConfirmBrandAsset(c *gin.Context) {
	handleConfirmAsset(brandDescriptorForAPI())(c)
}

func (api *AIChatApiNew) RestoreBrandAsset(c *gin.Context) {
	handleRestoreAsset(brandDescriptorForAPI())(c)
}

func (api *AIChatApiNew) DeleteBrandAsset(c *gin.Context) {
	handleDeleteAsset(brandDescriptorForAPI())(c)
}

// parseQueryIntDefault — shared helper for ?limit= / ?offset=
// parsing. Lives here so brand_asset_api remains the de-facto
// "first" file in the asset-library trio (alphabetical) — the
// other per-type files reuse it. Negative / non-numeric values
// fall back to the default rather than erroring; the repo layer
// also clamps so we don't double-validate here.
func parseQueryIntDefault(c *gin.Context, key string, def int) int {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

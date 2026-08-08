package workagent

// product_asset_api.go — P1 #5 slice 3. Five product-library
// REST handlers (List / Get / Confirm / Restore / Delete) that
// follow the same thin-wrapper pattern as brand_asset_api.go.
// Each delegates to the generic asset_library handler in
// asset_library_api.go, passing the product descriptor for
// kind-aware dispatch.
//
// productAssetSummary is the test-side JSON unmarshal target;
// the wire shape produced by Summary.MarshalJSON (Extras
// flattened to top level) matches its tags exactly.

import (
	"server/service/asset_library"

	"github.com/gin-gonic/gin"
)

// productAssetSummary — JSON shape returned by the list /
// get endpoints. SKU + category live at the top level (they
// came in via the descriptor's Summarise Extras and get
// flattened by MarshalJSON). hasSpecs / hasVisualGuidance are
// boolean hints for the picker UI's "this product has
// detailed spec data" badge.
type productAssetSummary struct {
	ID                uint                     `json:"id"`
	UID               int                      `json:"uid"`
	Name              string                   `json:"name"`
	Slug              string                   `json:"slug"`
	Status            asset_library.Status     `json:"status"`
	SourceKind        asset_library.SourceKind `json:"sourceKind"`
	Confirmed         bool                     `json:"confirmed"`
	SKU               string                   `json:"sku,omitempty"`
	Category          string                   `json:"category,omitempty"`
	HasSpecs          bool                     `json:"hasSpecs"`
	HasVisualGuidance bool                     `json:"hasVisualGuidance"`
	CreatedAt         string                   `json:"createdAt"`
	UpdatedAt         string                   `json:"updatedAt"`
}

func productDescriptorForAPI() asset_library.Descriptor {
	d, _ := asset_library.Default().Get(asset_library.AssetKindProduct)
	return d
}

func (api *AIChatApiNew) ListProductAssets(c *gin.Context) {
	handleListAssets(productDescriptorForAPI())(c)
}

func (api *AIChatApiNew) GetProductAsset(c *gin.Context) {
	handleGetAsset(productDescriptorForAPI())(c)
}

func (api *AIChatApiNew) ConfirmProductAsset(c *gin.Context) {
	handleConfirmAsset(productDescriptorForAPI())(c)
}

func (api *AIChatApiNew) RestoreProductAsset(c *gin.Context) {
	handleRestoreAsset(productDescriptorForAPI())(c)
}

func (api *AIChatApiNew) DeleteProductAsset(c *gin.Context) {
	handleDeleteAsset(productDescriptorForAPI())(c)
}

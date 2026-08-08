package workagent

// director_style_asset_api.go — Backlog #11 12/n + Sprint-D 4/7. Five
// REST handlers; four (Get / Confirm / Restore / Delete) reduce to
// thin wrappers around the generic asset_library handlers. List
// keeps a custom path because director-style is the only kind with
// the optional `?genre=` filter that hits the (genre) index from
// the migration so the library UI's genre-filter chip doesn't
// post-filter the full list client-side.
//
// directorStyleAssetSummary stays as the test-side unmarshal target.
// Summary's MarshalJSON produces a JSON shape matching this
// struct's tags exactly.

import (
	"fmt"
	"net/http"

	"server/globals"
	"server/service/asset_library"
	"server/service/director_style"
	workagentService "server/service/tools/workagent"
	"server/utils"

	"github.com/gin-gonic/gin"
)

// directorStyleAssetSummary — kept as the JSON unmarshal target for
// director_style_asset_api_test.go. Summary's MarshalJSON flattens
// Extras (era / genre / hasReferences) up to the parent so this
// struct's tags match the wire shape exactly.
type directorStyleAssetSummary struct {
	ID            uint   `json:"id"`
	UID           int    `json:"uid"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Era           string `json:"era,omitempty"`
	Genre         string `json:"genre,omitempty"`
	// Status now flows from the descriptor's projection
	// (Confirmed + DeletedAt → asset_library.Status enum string).
	Status        asset_library.Status     `json:"status"`
	SourceKind    asset_library.SourceKind `json:"sourceKind"`
	Confirmed     bool                     `json:"confirmed"`
	HasReferences bool                     `json:"hasReferences"`
	CreatedAt     string                   `json:"createdAt"`
	UpdatedAt     string                   `json:"updatedAt"`
}

func directorStyleDescriptorForAPI() asset_library.Descriptor {
	d, _ := asset_library.Default().Get(asset_library.AssetKindDirectorStyle)
	return d
}

// ListDirectorStyleAssets handles GET /api/work-agent/director-style-assets.
//
// Query params:
//   - limit  (default 50, max 100)
//   - offset (default 0; ignored when genre is set)
//   - genre  (optional; when set, hits the (genre) index)
//
// When ?genre is empty, delegates to the generic handleListAssets.
// When set, runs the per-type FilterByGenreForOwner path (one of
// the few places director-style still touches its concrete repo —
// the genre index is director-style-only and would pollute the
// Descriptor interface if generalised).
func (api *AIChatApiNew) ListDirectorStyleAssets(c *gin.Context) {
	genre := c.Query("genre")
	if genre == "" {
		handleListAssets(directorStyleDescriptorForAPI())(c)
		return
	}

	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	limit := parseQueryIntDefault(c, "limit", 50)
	offset := parseQueryIntDefault(c, "offset", 0)

	rows, err := director_style.Default().FilterByGenreForOwner(uid, genre, limit)
	if err != nil {
		globals.Error(fmt.Sprintf("[DirectorStyleAsset API] list failed uid=%d: %v", uid, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list director-style assets"})
		return
	}

	// Match the generic List path: enrich hasReferences before
	// Summarising so the genre-filter response carries the same
	// per-row hint signal as the unfiltered list.
	workagentService.EnrichDirectorStyleReferenceFlags(rows, uid)

	d := directorStyleDescriptorForAPI()
	items := make([]asset_library.Summary, 0, len(rows))
	for i := range rows {
		items = append(items, d.Summarise(workagentService.WrapDirectorStyle(&rows[i])))
	}

	pagination := gin.H{
		"limit":   limit,
		"offset":  offset,
		"hasMore": len(items) == limit,
		"genre":   genre,
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"items":      items,
		"pagination": pagination,
	}})
}

func (api *AIChatApiNew) GetDirectorStyleAsset(c *gin.Context) {
	handleGetAsset(directorStyleDescriptorForAPI())(c)
}

func (api *AIChatApiNew) ConfirmDirectorStyleAsset(c *gin.Context) {
	handleConfirmAsset(directorStyleDescriptorForAPI())(c)
}

func (api *AIChatApiNew) RestoreDirectorStyleAsset(c *gin.Context) {
	handleRestoreAsset(directorStyleDescriptorForAPI())(c)
}

func (api *AIChatApiNew) DeleteDirectorStyleAsset(c *gin.Context) {
	handleDeleteAsset(directorStyleDescriptorForAPI())(c)
}

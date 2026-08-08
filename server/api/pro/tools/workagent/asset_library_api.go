package workagent

// asset_library_api.go — Sprint-D 4/7. Generic CRUD handlers for any
// asset_library.Descriptor — collapses the five-handler trio that
// brand_asset_api.go / character_asset_api.go / director_style_asset_api.go
// each carried (List / Get / Confirm / Restore / Delete = 15 handlers,
// ~620 LOC of near-identical request shape).
//
// Handlers take a Descriptor and return a gin.HandlerFunc closure.
// The router walks asset_library.Default().All() and binds these
// under each descriptor's URLPrefix(); per-type handler files in
// this package reduce to thin wrappers in Phase 4 and disappear in
// Phase 6 once the test files migrate to call these directly.
//
// IDOR posture is identical to the per-type originals: every read /
// write goes through the descriptor's *ForOwner methods (uid
// embedded in WHERE), and cross-tenant rows return the same generic
// 404 as missing rows so there's no existence oracle.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"server/globals"
	"server/service/asset_library"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// handleListAssets — generic GET /<URLPrefix>. Mirrors the per-type
// list handlers' query-param contract (limit / offset, default 50 /
// 0; clamping at the repo layer). Returns the canonical envelope:
//
//   { data: { items: [Summary], pagination: {limit, offset, hasMore} } }
//
// Each item is a Descriptor.Summarise output; Summary's MarshalJSON
// flattens type-specific Extras up to the parent object so the
// wire shape matches the legacy per-type summaries byte-for-byte.
func handleListAssets(d asset_library.Descriptor) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := utils.GetUserID(c)
		if uid == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		limit := parseQueryIntDefault(c, "limit", 50)
		offset := parseQueryIntDefault(c, "offset", 0)

		rows, err := d.List(uid, limit, offset)
		if err != nil {
			globals.Error(fmt.Sprintf("[%s API] list failed uid=%d: %v", d.Kind(), uid, err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list " + d.Kind().String() + " assets"})
			return
		}

		items := make([]asset_library.Summary, 0, len(rows))
		for _, r := range rows {
			items = append(items, d.Summarise(r))
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"items": items,
			"pagination": gin.H{
				"limit":   limit,
				"offset":  offset,
				"hasMore": len(items) == limit,
			},
		}})
	}
}

// handleGetAsset — generic GET /<URLPrefix>/:id. Returns the full
// row (descriptor returns the underlying model wrapped in
// LibraryAsset; gin's JSON marshaller serialises the embedded model
// faithfully because LibraryAsset's accessor methods don't shadow
// the JSON tags on the model fields).
//
// Cross-tenant + missing both → 404. Same generic message keeps the
// IDOR posture consistent.
func handleGetAsset(d asset_library.Descriptor) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := utils.GetUserID(c)
		if uid == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		id, ok := parseAssetIDParam(c)
		if !ok {
			return
		}

		asset, err := d.LoadByID(id, uid)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": d.Kind().String() + " asset not found"})
				return
			}
			globals.Error(fmt.Sprintf("[%s API] load failed id=%d uid=%d: %v", d.Kind(), id, uid, err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load " + d.Kind().String() + " asset"})
			return
		}
		if asset == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": d.Kind().String() + " asset not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": asset})
	}
}

// handleConfirmAsset — generic POST /<URLPrefix>/:id/confirm. Backs
// the M4 Vocalize step UI. Idempotent: a click-twice from the UI
// returns 200 with persisted=true, not an error.
//
// Disambiguation: the underlying repos return ErrRecordNotFound for
// BOTH "already confirmed" and "row missing / cross-tenant." We
// load after to disambiguate — cross-tenant 404 stays 404; already-
// confirmed reports persisted=true with the existing row state.
func handleConfirmAsset(d asset_library.Descriptor) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := utils.GetUserID(c)
		if uid == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		id, ok := parseAssetIDParam(c)
		if !ok {
			return
		}

		err := d.MarkConfirmed(id, uid)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			globals.Error(fmt.Sprintf("[%s API] confirm failed id=%d uid=%d: %v", d.Kind(), id, uid, err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to confirm " + d.Kind().String() + " asset"})
			return
		}

		asset, loadErr := d.LoadByID(id, uid)
		if loadErr != nil || asset == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": d.Kind().String() + " asset not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"persisted":   true,
			"id":          asset.GetID(),
			"status":      asset.GetStatus(),
			"confirmed":   asset.GetConfirmed(),
			"confirmedAt": asset.GetConfirmedAt(),
		}})
	}
}

// handleRestoreAsset — generic POST /<URLPrefix>/:id/restore.
// Reverses a soft-delete. The user re-confirms via the standard
// confirm endpoint if they want the asset active in preflight again.
//
// 404 when the row isn't currently soft-deleted (already active OR
// cross-tenant).
func handleRestoreAsset(d asset_library.Descriptor) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := utils.GetUserID(c)
		if uid == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		id, ok := parseAssetIDParam(c)
		if !ok {
			return
		}

		err := d.Restore(id, uid)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": d.Kind().String() + " asset not found or not deleted"})
				return
			}
			globals.Error(fmt.Sprintf("[%s API] restore failed id=%d uid=%d: %v", d.Kind(), id, uid, err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to restore " + d.Kind().String() + " asset"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"restored": true}})
	}
}

// handleDeleteAsset — generic DELETE /<URLPrefix>/:id. Soft-delete
// (status=archived + deleted_at). Library reads filter on
// deleted_at IS NULL; the row is preserved for audit / undo.
func handleDeleteAsset(d asset_library.Descriptor) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := utils.GetUserID(c)
		if uid == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		id, ok := parseAssetIDParam(c)
		if !ok {
			return
		}

		err := d.SoftDelete(id, uid)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": d.Kind().String() + " asset not found"})
				return
			}
			globals.Error(fmt.Sprintf("[%s API] delete failed id=%d uid=%d: %v", d.Kind(), id, uid, err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete " + d.Kind().String() + " asset"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"deleted": true}})
	}
}

// parseAssetIDParam reads the :id route param as a uint and writes
// a 400 response when it's missing / unparseable / zero. Returns
// (id, ok) so the caller can early-out cleanly.
//
// IDOR-EXEMPT-FUNC: parser-only helper. The uid scoping happens in
// the wrapping handler closure (see handleGetAsset / handleConfirmAsset
// / handleListAssets / handleArchiveAsset), which guard with
// `utils.GetUserID(c)` THEN call this helper to extract the id, THEN
// feed both to `d.LoadByID(id, uid)` (the descriptor's uid-aware
// lookup). Splitting parse from auth is intentional — a single helper
// for the 400-on-bad-id branch keeps the four handler bodies thin.
func parseAssetIDParam(c *gin.Context) (uint, bool) {
	idU64, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || idU64 == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid id"})
		return 0, false
	}
	return uint(idU64), true
}

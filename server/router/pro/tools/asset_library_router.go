package tools

// asset_library_router.go — Sprint-E 5/8 platform REST surface for
// the asset_library trifecta (brand / character / director-style).
//
// Routes mount at /api/{kind} matching the existing /api/character
// pattern that pre-Sprint-E owned canvas-side character CRUD.
// /api/character keeps its rich CharacterApi handlers (Create /
// List / Get / Update / Delete / Reference operations) — Sprint-E
// only adds two new endpoints there (/:id/confirm and /:id/restore)
// for the M4 Vocalize lifecycle.
//
// /api/brand and /api/director-style are NEW routes; they mount on
// the workagent's existing generic handlers (Sprint-D 4/7). The
// workagent /api/work-agent/{kind}-assets routes stay live as
// aliases for one release cycle (Sprint-E Phase 7 retires them).
//
// Cross-binding into the workagent api group is intentional —
// the generic handlers themselves are kind-agnostic; their
// physical location in the workagent package is a Sprint-D
// historical accident, not a logical one. A future refactor
// can extract them to api/pro/tools/asset_library/ if the
// workagent module wants narrower scope.

import (
	"path"
	"strings"

	"server/api"
	"server/globals"

	"github.com/gin-gonic/gin"
)

type AssetLibraryRouter struct{}

func (r *AssetLibraryRouter) InitAssetLibraryRouter(router *gin.RouterGroup) {
	workAgentApi := api.ApiGroupApp.ToolsApiGroup.WorkAgentApi.AIChatApiNew
	characterApi := api.ApiGroupApp.ToolsApiGroup.CharacterApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	// /api/brand — NEW platform surface for brand library.
	// Mirrors the workagent /api/work-agent/brand-assets shape but
	// sits at the top-level path for agent-native parity with the
	// existing /api/character surface.
	brandGroup := router.Group(path.Join(prefix, "brand"))
	{
		brandGroup.GET("", workAgentApi.ListBrandAssets)
		brandGroup.GET("/:id", workAgentApi.GetBrandAsset)
		brandGroup.POST("/:id/confirm", workAgentApi.ConfirmBrandAsset)
		brandGroup.POST("/:id/restore", workAgentApi.RestoreBrandAsset)
		brandGroup.DELETE("/:id", workAgentApi.DeleteBrandAsset)
	}

	// /api/director-style — NEW platform surface.
	directorStyleGroup := router.Group(path.Join(prefix, "director-style"))
	{
		directorStyleGroup.GET("", workAgentApi.ListDirectorStyleAssets)
		directorStyleGroup.GET("/:id", workAgentApi.GetDirectorStyleAsset)
		directorStyleGroup.POST("/:id/confirm", workAgentApi.ConfirmDirectorStyleAsset)
		directorStyleGroup.POST("/:id/restore", workAgentApi.RestoreDirectorStyleAsset)
		directorStyleGroup.DELETE("/:id", workAgentApi.DeleteDirectorStyleAsset)
	}

	// /api/product — P1 #5 platform surface. Same shape as
	// /api/brand and /api/director-style; delegates to the
	// generic asset_library handlers via the product descriptor.
	productGroup := router.Group(path.Join(prefix, "product"))
	{
		productGroup.GET("", workAgentApi.ListProductAssets)
		productGroup.GET("/:id", workAgentApi.GetProductAsset)
		productGroup.POST("/:id/confirm", workAgentApi.ConfirmProductAsset)
		productGroup.POST("/:id/restore", workAgentApi.RestoreProductAsset)
		productGroup.DELETE("/:id", workAgentApi.DeleteProductAsset)
	}

	// /api/character — EXTEND existing CharacterApi surface with
	// the Sprint-E lifecycle endpoints. The Create/List/Get/Update/
	// Delete + Reference paths are owned by character_router.go;
	// adding /confirm + /restore here lets the asset library UI
	// reach Confirm + Restore through the platform surface without
	// going through workagent.
	//
	// Note: the workagent /api/work-agent/character-assets routes
	// also call workAgentApi.ConfirmCharacterAsset etc. The handlers
	// are stateless so dual mounting is safe — same code path,
	// different URLs.
	characterGroup := router.Group(path.Join(prefix, "character"))
	{
		characterGroup.POST("/:id/confirm", workAgentApi.ConfirmCharacterAsset)
		characterGroup.POST("/:id/restore", workAgentApi.RestoreCharacterAsset)
	}

	// Avoid an unused-variable warning when the group above is the
	// only consumer of characterApi (currently it isn't — kept for
	// future endpoints that bridge CharacterApi handlers into the
	// asset library namespace).
	_ = characterApi
}

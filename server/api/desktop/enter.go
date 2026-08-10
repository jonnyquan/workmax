// Package desktop holds the API handlers for the /api/desktop/*
// endpoint subtree (mirror of server/api/admin/). All desktop-client-
// specific routes live here:
//   - OAuth Authorization Server (P-1.4+) at /api/desktop/oauth/*
//   - Sync endpoints (P1.A.2+) at /api/desktop/sync/*
package desktop

import (
	"server/api/desktop/agent"
	"server/api/desktop/login"
	"server/api/desktop/models"
	"server/api/desktop/oauth"
	"server/api/desktop/sync"
	"server/api/desktop/version"
)

// DesktopApiGroup bundles every API surface under /api/desktop/*.
// Mirrors the (empty-)struct composition pattern used by
// server/api/admin/AdminApiGroup.
//
// OauthApi + SyncApi are *struct (not embedded value) because they
// carry configuration — DB handle + service instances — set once at
// startup rather than constructed per request. VersionApi has no
// state so it's a value, not a pointer.
type DesktopApiGroup struct {
	AgentApi *agent.ThreadApi
	LoginApi *login.LoginApi
	// ModelCatalogApi serves GET /api/desktop/models — the conversation-model
	// catalog the client uses to let a user pick a model by name.
	ModelCatalogApi *models.ModelCatalogApi
	OauthApi        *oauth.OauthApi
	SyncApi         *sync.SyncApi
	VersionApi      version.VersionApi
}

package initialize

import (
	"fmt"
	"path"
	"sort"
	"strings"

	httpv1 "server/contracts/http/v1"
	"server/globals"

	"github.com/gin-gonic/gin"
)

type routeCredentialAssignment struct {
	owner   httpv1.CredentialOwner
	current httpv1.CredentialPolicy
	target  httpv1.CredentialPolicy
}

type routeCredentialOverride struct {
	method     string
	path       string
	assignment routeCredentialAssignment
}

type httpSurfaceDeclaration struct {
	surface    httpv1.Surface
	assignment routeCredentialAssignment
	mount      func(*gin.RouterGroup)
	overrides  []routeCredentialOverride
}

// HTTPRouteSpecs derives the complete method/path catalog from the explicit
// surface mount functions and applies the declarative credential matrix below.
// It is intentionally an audit contract in Phase 0: route registration still
// happens through the existing routers, so introducing this inventory cannot
// change request behavior.
func HTTPRouteSpecs() ([]httpv1.RouteSpec, error) {
	declarations := httpSurfaceDeclarations()
	specs := make([]httpv1.RouteSpec, 0)
	seen := make(map[string]httpv1.Surface)

	for _, declaration := range declarations {
		engine := gin.New()
		declaration.mount(engine.Group(""))

		overrides := make(map[string]routeCredentialAssignment, len(declaration.overrides))
		usedOverrides := make(map[string]bool, len(declaration.overrides))
		for _, override := range declaration.overrides {
			key := routeIdentity(override.method, override.path)
			if _, duplicate := overrides[key]; duplicate {
				return nil, fmt.Errorf("duplicate credential override %s on surface %s", key, declaration.surface)
			}
			overrides[key] = override.assignment
		}

		for _, route := range engine.Routes() {
			key := routeIdentity(route.Method, route.Path)
			if previous, duplicate := seen[key]; duplicate {
				return nil, fmt.Errorf("route %s belongs to both %s and %s", key, previous, declaration.surface)
			}
			seen[key] = declaration.surface

			assignment := declaration.assignment
			if override, ok := overrides[key]; ok {
				assignment = override
				usedOverrides[key] = true
			}
			spec := httpv1.RouteSpec{
				Method:            route.Method,
				Path:              route.Path,
				Surface:           declaration.surface,
				CredentialOwner:   assignment.owner,
				CurrentCredential: assignment.current,
				TargetCredential:  assignment.target,
			}
			if err := spec.Validate(); err != nil {
				return nil, err
			}
			specs = append(specs, spec)
		}

		for key := range overrides {
			if !usedOverrides[key] {
				return nil, fmt.Errorf("credential override %s does not match a route on surface %s", key, declaration.surface)
			}
		}
	}

	sort.Slice(specs, func(i, j int) bool {
		return specs[i].Key() < specs[j].Key()
	})
	return specs, nil
}

func httpSurfaceDeclarations() []httpSurfaceDeclaration {
	publicPlatform := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerPlatform,
		current: httpv1.CredentialPublic,
		target:  httpv1.CredentialPublic,
	}
	publicPortal := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerPortal,
		current: httpv1.CredentialPublic,
		target:  httpv1.CredentialPublic,
	}
	providerSignature := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerProvider,
		current: httpv1.CredentialProviderSignature,
		target:  httpv1.CredentialProviderSignature,
	}
	publicDesktop := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerDesktop,
		current: httpv1.CredentialPublic,
		target:  httpv1.CredentialPublic,
	}
	desktopOAuth := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerDesktop,
		current: httpv1.CredentialDesktopOAuthBearer,
		target:  httpv1.CredentialDesktopOAuthBearer,
	}
	desktopLoginBootstrap := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerDesktop,
		current: httpv1.CredentialDesktopLoginBootstrap,
		target:  httpv1.CredentialDesktopLoginBootstrap,
	}
	desktopLoginTransaction := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerDesktop,
		current: httpv1.CredentialDesktopLoginTransaction,
		target:  httpv1.CredentialDesktopLoginTransaction,
	}
	publicAgentShare := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerAgent,
		current: httpv1.CredentialPublicShareRead,
		target:  httpv1.CredentialPublicShareRead,
	}
	monitorToken := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerMonitor,
		current: httpv1.CredentialMonitorToken,
		target:  httpv1.CredentialMonitorToken,
	}
	portalSessionMigration := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerPortal,
		current: httpv1.CredentialGenericJWT,
		target:  httpv1.CredentialPortalSession,
	}
	agentResourceMigration := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerAgent,
		current: httpv1.CredentialGenericJWT,
		target:  httpv1.CredentialAgentResource,
	}
	agentDesktopSharedMigration := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerAgent,
		current: httpv1.CredentialAgentDesktopShared,
		target:  httpv1.CredentialAgentResource,
	}
	adminSessionMigration := routeCredentialAssignment{
		owner:   httpv1.CredentialOwnerAdmin,
		current: httpv1.CredentialGenericJWTAdmin,
		target:  httpv1.CredentialAdminSession,
	}

	return []httpSurfaceDeclaration{
		{
			surface:    httpv1.SurfaceHealth,
			assignment: publicPlatform,
			mount:      mountHealthSurface,
		},
		{
			surface:    httpv1.SurfacePortalPublic,
			assignment: publicPortal,
			mount:      mountPortalPublicSurface,
		},
		{
			surface:    httpv1.SurfaceProviderCallback,
			assignment: providerSignature,
			mount:      mountProviderCallbackSurface,
		},
		{
			surface:    httpv1.SurfaceDesktopResource,
			assignment: publicDesktop,
			mount:      mountDesktopResourceSurface,
			overrides: []routeCredentialOverride{
				credentialOverride("PUT", "/api/desktop/agent/threads/:uuid", desktopOAuth),
				credentialOverride("POST", "/api/v1/desktop/identity/login-transactions", desktopLoginBootstrap),
				credentialOverride("GET", "/api/v1/desktop/identity/login-transactions/:id", desktopLoginTransaction),
				credentialOverride("POST", "/api/v1/desktop/identity/login-transactions/:id/password", desktopLoginTransaction),
				credentialOverride("POST", "/api/v1/desktop/identity/login-transactions/:id/exchange", desktopLoginTransaction),
				credentialOverride("GET", "/api/desktop/models", desktopOAuth),
				credentialOverride("GET", "/api/desktop/oauth/userinfo", desktopOAuth),
				credentialOverride("GET", "/api/desktop/sync/threads", desktopOAuth),
				credentialOverride("GET", "/api/desktop/sync/threads/:id", desktopOAuth),
				credentialOverride("GET", "/api/desktop/sync/messages", desktopOAuth),
			},
		},
		{
			surface:    httpv1.SurfaceLegacyAgentPublic,
			assignment: publicAgentShare,
			mount:      mountLegacyAgentPublicSurface,
		},
		{
			surface:    httpv1.SurfaceMonitor,
			assignment: monitorToken,
			mount:      mountMonitorSurface,
		},
		{
			surface:    httpv1.SurfacePortalAuthenticated,
			assignment: portalSessionMigration,
			mount:      mountPortalAuthenticatedSurface,
		},
		{
			surface:    httpv1.SurfaceLegacyAgentAuthenticated,
			assignment: agentResourceMigration,
			mount:      mountLegacyAgentAuthenticatedSurface,
			overrides:  legacyAgentAdminOverrides(adminSessionMigration),
		},
		{
			// Same product surface as above, separate declaration because the
			// admitted credential differs: these two routes also accept a
			// Desktop OAuth token. Keeping them a distinct mount is what makes
			// "which routes can a Desktop grant reach" answerable from the
			// composition root instead of from a middleware allowlist.
			surface:    httpv1.SurfaceLegacyAgentAuthenticated,
			assignment: agentDesktopSharedMigration,
			mount:      mountLegacyAgentDesktopSharedSurface,
		},
		{
			surface:    httpv1.SurfaceAdminOps,
			assignment: adminSessionMigration,
			mount:      mountAdminSurface,
		},
	}
}

func legacyAgentAdminOverrides(adminSession routeCredentialAssignment) []routeCredentialOverride {
	apiPrefix := configuredRouterPrefix()
	return []routeCredentialOverride{
		credentialOverride("GET", "/api/work-agent/skills/access-requests", adminSession),
		credentialOverride("PATCH", "/api/work-agent/skills/access-requests/:requestId/status", adminSession),
		credentialOverride("GET", "/api/work-agent/metrics/render-runners", adminSession),
		credentialOverride("POST", "/api/work-agent/metrics/render-runners/smoke", adminSession),
		credentialOverride("GET", "/api/work-agent/sse/stats", adminSession),
		credentialOverride("GET", path.Join(apiPrefix, "global/assets/audit"), adminSession),
		credentialOverride("GET", path.Join(apiPrefix, "tools/canvas/admin/stats"), adminSession),
		credentialOverride("POST", path.Join(apiPrefix, "tools/canvas/admin/prompt-guard/reload"), adminSession),
		credentialOverride("GET", path.Join(apiPrefix, "tools/canvas/admin/orphan-element-assets/preview"), adminSession),
	}
}

func configuredRouterPrefix() string {
	prefix := strings.Trim(strings.TrimSpace(globals.GraConf.System.RouterPrefix), "/")
	if prefix == "" {
		prefix = "api"
	}
	return "/" + prefix
}

func credentialOverride(method, routePath string, assignment routeCredentialAssignment) routeCredentialOverride {
	return routeCredentialOverride{
		method:     method,
		path:       routePath,
		assignment: assignment,
	}
}

func routeIdentity(method, routePath string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + " " + routePath
}

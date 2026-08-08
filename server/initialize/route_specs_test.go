package initialize

import (
	"testing"

	httpv1 "server/contracts/http/v1"

	"github.com/gin-gonic/gin"
)

func TestHTTPRouteSpecsCoverRegisteredRoutesExactlyOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)

	specs, err := HTTPRouteSpecs()
	if err != nil {
		t.Fatalf("build HTTP RouteSpecs: %v", err)
	}
	registered := make(map[string]struct{})
	for _, route := range Routers().Routes() {
		key := routeIdentity(route.Method, route.Path)
		if _, duplicate := registered[key]; duplicate {
			t.Fatalf("router contains duplicate route %s", key)
		}
		registered[key] = struct{}{}
	}

	covered := make(map[string]httpv1.RouteSpec, len(specs))
	for _, spec := range specs {
		if err := spec.Validate(); err != nil {
			t.Errorf("invalid RouteSpec: %v", err)
		}
		if previous, duplicate := covered[spec.Key()]; duplicate {
			t.Fatalf("duplicate RouteSpec %s on %s and %s", spec.Key(), previous.Surface, spec.Surface)
		}
		covered[spec.Key()] = spec
	}

	for key := range registered {
		if _, ok := covered[key]; !ok {
			t.Errorf("registered route has no RouteSpec: %s", key)
		}
	}
	for key := range covered {
		if _, ok := registered[key]; !ok {
			t.Errorf("RouteSpec does not correspond to a registered route: %s", key)
		}
	}
	if len(covered) != len(registered) {
		t.Fatalf("RouteSpec count = %d, registered route count = %d", len(covered), len(registered))
	}
}

func TestHTTPRouteSpecsCredentialMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	specs, err := HTTPRouteSpecs()
	if err != nil {
		t.Fatalf("build HTTP RouteSpecs: %v", err)
	}
	byKey := make(map[string]httpv1.RouteSpec, len(specs))
	for _, spec := range specs {
		byKey[spec.Key()] = spec
	}

	tests := []struct {
		key     string
		surface httpv1.Surface
		owner   httpv1.CredentialOwner
		current httpv1.CredentialPolicy
		target  httpv1.CredentialPolicy
	}{
		{"GET /api/health", httpv1.SurfaceHealth, httpv1.CredentialOwnerPlatform, httpv1.CredentialPublic, httpv1.CredentialPublic},
		{"POST /api/auth/sign-in", httpv1.SurfacePortalPublic, httpv1.CredentialOwnerPortal, httpv1.CredentialPublic, httpv1.CredentialPublic},
		{"POST /api/callback/subscription/stripe", httpv1.SurfaceProviderCallback, httpv1.CredentialOwnerProvider, httpv1.CredentialProviderSignature, httpv1.CredentialProviderSignature},
		{"POST /api/v1/desktop/identity/login-transactions", httpv1.SurfaceDesktopResource, httpv1.CredentialOwnerDesktop, httpv1.CredentialDesktopLoginBootstrap, httpv1.CredentialDesktopLoginBootstrap},
		{"POST /api/v1/desktop/identity/login-transactions/:id/password", httpv1.SurfaceDesktopResource, httpv1.CredentialOwnerDesktop, httpv1.CredentialDesktopLoginTransaction, httpv1.CredentialDesktopLoginTransaction},
		{"PUT /api/desktop/agent/threads/:uuid", httpv1.SurfaceDesktopResource, httpv1.CredentialOwnerDesktop, httpv1.CredentialDesktopOAuthBearer, httpv1.CredentialDesktopOAuthBearer},
		{"GET /api/desktop/version", httpv1.SurfaceDesktopResource, httpv1.CredentialOwnerDesktop, httpv1.CredentialPublic, httpv1.CredentialPublic},
		{"GET /api/desktop/oauth/userinfo", httpv1.SurfaceDesktopResource, httpv1.CredentialOwnerDesktop, httpv1.CredentialDesktopOAuthBearer, httpv1.CredentialDesktopOAuthBearer},
		{"GET /api/desktop/sync/threads", httpv1.SurfaceDesktopResource, httpv1.CredentialOwnerDesktop, httpv1.CredentialDesktopOAuthBearer, httpv1.CredentialDesktopOAuthBearer},
		{"GET /api/work-agent/conversations/:threadId", httpv1.SurfaceLegacyAgentPublic, httpv1.CredentialOwnerAgent, httpv1.CredentialPublicShareRead, httpv1.CredentialPublicShareRead},
		{"GET /api/internal/monitor/summary", httpv1.SurfaceMonitor, httpv1.CredentialOwnerMonitor, httpv1.CredentialMonitorToken, httpv1.CredentialMonitorToken},
		{"GET /api/account/quota", httpv1.SurfacePortalAuthenticated, httpv1.CredentialOwnerPortal, httpv1.CredentialGenericJWT, httpv1.CredentialPortalSession},
		{"POST /api/work-agent/chat/agent", httpv1.SurfaceLegacyAgentAuthenticated, httpv1.CredentialOwnerAgent, httpv1.CredentialGenericJWT, httpv1.CredentialAgentResource},
		{"GET /api/work-agent/metrics/render-runners", httpv1.SurfaceLegacyAgentAuthenticated, httpv1.CredentialOwnerAdmin, httpv1.CredentialGenericJWTAdmin, httpv1.CredentialAdminSession},
		{"GET /api/admin/dashboard/getBasicStatistics", httpv1.SurfaceAdminOps, httpv1.CredentialOwnerAdmin, httpv1.CredentialGenericJWTAdmin, httpv1.CredentialAdminSession},
	}

	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			spec, ok := byKey[test.key]
			if !ok {
				t.Fatalf("missing RouteSpec %s", test.key)
			}
			if spec.Surface != test.surface ||
				spec.CredentialOwner != test.owner ||
				spec.CurrentCredential != test.current ||
				spec.TargetCredential != test.target {
				t.Fatalf("RouteSpec %s = %+v", test.key, spec)
			}
		})
	}
}

func TestHTTPRouteSpecsExposeCredentialMigrations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	specs, err := HTTPRouteSpecs()
	if err != nil {
		t.Fatalf("build HTTP RouteSpecs: %v", err)
	}

	var portalMigrations, agentMigrations, adminMigrations int
	for _, spec := range specs {
		if spec.CurrentCredential == spec.TargetCredential {
			continue
		}
		switch {
		case spec.CredentialOwner == httpv1.CredentialOwnerAdmin:
			adminMigrations++
			if spec.CurrentCredential != httpv1.CredentialGenericJWTAdmin || spec.TargetCredential != httpv1.CredentialAdminSession {
				t.Errorf("unexpected Admin credential migration: %+v", spec)
			}
		case spec.Surface == httpv1.SurfacePortalAuthenticated:
			portalMigrations++
			if spec.CurrentCredential != httpv1.CredentialGenericJWT || spec.TargetCredential != httpv1.CredentialPortalSession {
				t.Errorf("unexpected Portal credential migration: %+v", spec)
			}
		case spec.Surface == httpv1.SurfaceLegacyAgentAuthenticated:
			agentMigrations++
			if spec.CurrentCredential != httpv1.CredentialGenericJWT || spec.TargetCredential != httpv1.CredentialAgentResource {
				t.Errorf("unexpected Agent credential migration: %+v", spec)
			}
		default:
			t.Errorf("unexpected credential migration outside Portal/Agent/Admin policies: %+v", spec)
		}
	}

	if portalMigrations == 0 {
		t.Fatal("Portal generic-JWT migration is not represented")
	}
	if agentMigrations == 0 {
		t.Fatal("Agent generic-JWT migration is not represented")
	}
	if adminMigrations == 0 {
		t.Fatal("Admin generic-JWT role migration is not represented")
	}
}

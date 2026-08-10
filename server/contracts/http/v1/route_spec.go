// Package v1 contains dependency-free HTTP ownership contracts shared by
// composition, audit and future route-discovery code.
//
// Keep this package free of Gin, GORM, service and middleware imports. Product
// routers may depend on it, but the contract must not depend on a router
// implementation.
package v1

import (
	"fmt"
	"strings"
)

// RouteSpecVersion identifies the current RouteSpec wire shape. It does not
// imply that every route has already migrated to its target credential policy.
const RouteSpecVersion = "1.0.0-draft"

// Surface identifies the product boundary that owns a route. Surface is kept
// separate from CredentialOwner because an Agent surface can legitimately
// contain an Admin/Ops-only control while it is being migrated.
type Surface string

const (
	SurfaceHealth                   Surface = "health"
	SurfacePortalPublic             Surface = "portal-public"
	SurfaceProviderCallback         Surface = "provider-callback"
	SurfaceDesktopResource          Surface = "desktop-resource"
	SurfaceLegacyAgentPublic        Surface = "legacy-agent-public"
	SurfaceMonitor                  Surface = "monitor"
	SurfacePortalAuthenticated      Surface = "portal-authenticated"
	SurfaceLegacyAgentAuthenticated Surface = "legacy-agent-authenticated"
	SurfaceAdminOps                 Surface = "admin-ops"
)

// CredentialOwner identifies the authority responsible for deciding whether
// a credential is valid for a route. It is an ownership boundary, not the
// current middleware implementation.
type CredentialOwner string

const (
	CredentialOwnerPlatform CredentialOwner = "platform"
	CredentialOwnerPortal   CredentialOwner = "portal"
	CredentialOwnerProvider CredentialOwner = "provider"
	CredentialOwnerDesktop  CredentialOwner = "desktop"
	CredentialOwnerAgent    CredentialOwner = "agent"
	CredentialOwnerMonitor  CredentialOwner = "monitor"
	CredentialOwnerAdmin    CredentialOwner = "admin"
)

// CredentialPolicy names an admission policy. CurrentCredential documents
// observed compatibility behavior; TargetCredential records the policy owner
// the route must migrate to without silently claiming the migration is done.
type CredentialPolicy string

const (
	CredentialPublic                  CredentialPolicy = "public"
	CredentialPublicShareRead         CredentialPolicy = "public-share-read"
	CredentialGenericJWT              CredentialPolicy = "generic-jwt"
	CredentialGenericJWTAdmin         CredentialPolicy = "generic-jwt-admin-role"
	CredentialPortalSession           CredentialPolicy = "portal-session"
	CredentialDesktopOAuthBearer      CredentialPolicy = "desktop-oauth-bearer"
	CredentialDesktopLoginBootstrap   CredentialPolicy = "desktop-login-bootstrap"
	CredentialDesktopLoginTransaction CredentialPolicy = "desktop-login-transaction"
	CredentialAgentResource           CredentialPolicy = "agent-resource"
	// CredentialAgentDesktopShared marks an Agent route that admits BOTH the
	// generic Portal JWT and a Desktop OAuth access token (audience
	// workmax.desktop + the Desktop client_id). It is deliberately a distinct
	// policy from CredentialGenericJWT so the audit shows exactly which routes
	// a Desktop grant can reach; the target remains CredentialAgentResource.
	CredentialAgentDesktopShared CredentialPolicy = "agent-desktop-shared"
	CredentialProviderSignature  CredentialPolicy = "provider-signature"
	CredentialMonitorToken       CredentialPolicy = "monitor-token"
	CredentialAdminSession       CredentialPolicy = "admin-session"
)

// RouteSpec is the auditable identity, ownership and credential migration
// contract for one HTTP method/path pair.
type RouteSpec struct {
	Method            string           `json:"method"`
	Path              string           `json:"path"`
	Surface           Surface          `json:"surface"`
	CredentialOwner   CredentialOwner  `json:"credentialOwner"`
	CurrentCredential CredentialPolicy `json:"currentCredential"`
	TargetCredential  CredentialPolicy `json:"targetCredential"`
}

// Key returns the same stable identity Gin exposes for a registered route.
func (s RouteSpec) Key() string {
	return strings.ToUpper(strings.TrimSpace(s.Method)) + " " + s.Path
}

// Validate rejects incomplete or unknown ownership metadata before it can be
// used for route discovery or a generated authorization gate.
func (s RouteSpec) Validate() error {
	trimmedMethod := strings.TrimSpace(s.Method)
	if trimmedMethod == "" || s.Method != trimmedMethod || s.Method != strings.ToUpper(s.Method) {
		return fmt.Errorf("route method %q must be non-empty uppercase", s.Method)
	}
	if s.Path != strings.TrimSpace(s.Path) || !strings.HasPrefix(s.Path, "/") {
		return fmt.Errorf("route path %q must start with /", s.Path)
	}
	if !s.Surface.Valid() {
		return fmt.Errorf("route %s has unknown surface %q", s.Key(), s.Surface)
	}
	if !s.CredentialOwner.Valid() {
		return fmt.Errorf("route %s has unknown credential owner %q", s.Key(), s.CredentialOwner)
	}
	if !s.CurrentCredential.Valid() {
		return fmt.Errorf("route %s has unknown current credential %q", s.Key(), s.CurrentCredential)
	}
	if !s.TargetCredential.Valid() {
		return fmt.Errorf("route %s has unknown target credential %q", s.Key(), s.TargetCredential)
	}
	return nil
}

// Valid reports whether the value is part of the v1 surface vocabulary.
func (s Surface) Valid() bool {
	switch s {
	case SurfaceHealth,
		SurfacePortalPublic,
		SurfaceProviderCallback,
		SurfaceDesktopResource,
		SurfaceLegacyAgentPublic,
		SurfaceMonitor,
		SurfacePortalAuthenticated,
		SurfaceLegacyAgentAuthenticated,
		SurfaceAdminOps:
		return true
	default:
		return false
	}
}

// Valid reports whether the value is part of the v1 credential-owner
// vocabulary.
func (o CredentialOwner) Valid() bool {
	switch o {
	case CredentialOwnerPlatform,
		CredentialOwnerPortal,
		CredentialOwnerProvider,
		CredentialOwnerDesktop,
		CredentialOwnerAgent,
		CredentialOwnerMonitor,
		CredentialOwnerAdmin:
		return true
	default:
		return false
	}
}

// Valid reports whether the value is part of the v1 credential-policy
// vocabulary.
func (p CredentialPolicy) Valid() bool {
	switch p {
	case CredentialPublic,
		CredentialPublicShareRead,
		CredentialGenericJWT,
		CredentialGenericJWTAdmin,
		CredentialPortalSession,
		CredentialDesktopOAuthBearer,
		CredentialDesktopLoginBootstrap,
		CredentialDesktopLoginTransaction,
		CredentialAgentResource,
		CredentialAgentDesktopShared,
		CredentialProviderSignature,
		CredentialMonitorToken,
		CredentialAdminSession:
		return true
	default:
		return false
	}
}

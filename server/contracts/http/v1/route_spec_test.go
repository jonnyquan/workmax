package v1

import "testing"

func TestRouteSpecValidate(t *testing.T) {
	valid := RouteSpec{
		Method:            "GET",
		Path:              "/api/desktop/sync/threads",
		Surface:           SurfaceDesktopResource,
		CredentialOwner:   CredentialOwnerDesktop,
		CurrentCredential: CredentialDesktopOAuthBearer,
		TargetCredential:  CredentialDesktopOAuthBearer,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid RouteSpec rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RouteSpec)
	}{
		{name: "method", mutate: func(spec *RouteSpec) { spec.Method = "get" }},
		{name: "method whitespace", mutate: func(spec *RouteSpec) { spec.Method = " GET " }},
		{name: "path", mutate: func(spec *RouteSpec) { spec.Path = "api/desktop" }},
		{name: "path whitespace", mutate: func(spec *RouteSpec) { spec.Path = "/api/desktop " }},
		{name: "surface", mutate: func(spec *RouteSpec) { spec.Surface = "unknown" }},
		{name: "owner", mutate: func(spec *RouteSpec) { spec.CredentialOwner = "unknown" }},
		{name: "current credential", mutate: func(spec *RouteSpec) { spec.CurrentCredential = "unknown" }},
		{name: "target credential", mutate: func(spec *RouteSpec) { spec.TargetCredential = "unknown" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("invalid RouteSpec was accepted: %+v", candidate)
			}
		})
	}
}

func TestRouteSpecKeyNormalizesMethodWhitespace(t *testing.T) {
	spec := RouteSpec{Method: " get ", Path: "/api/health"}
	if got, want := spec.Key(), "GET /api/health"; got != want {
		t.Fatalf("Key() = %q, want %q", got, want)
	}
}

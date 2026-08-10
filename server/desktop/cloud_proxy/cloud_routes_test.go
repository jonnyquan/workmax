//go:build desktop

package cloud_proxy

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestAllCloudRoutes_Invariants pins basic shape rules so a future
// addition can't introduce a malformed route by accident.
func TestAllCloudRoutes_Invariants(t *testing.T) {
	seenIDs := map[string]bool{}
	seenRoutes := map[string]bool{}
	for _, spec := range CurrentCloudRouteSpecs() {
		if spec.ID == "" || spec.Method == "" || spec.Path == "" {
			t.Errorf("empty identity in cloud route spec: %+v", spec)
			continue
		}
		if spec.Method != http.MethodGet && spec.Method != http.MethodPost && spec.Method != http.MethodPut {
			t.Errorf("route %q uses unsupported method %q", spec.ID, spec.Method)
		}
		if !strings.HasPrefix(spec.Path, "/api/") {
			t.Errorf("route %q must start with /api/ (sidecar only talks to workmax.app's /api surface)", spec.Path)
		}
		if strings.Contains(spec.Path, "?") {
			t.Errorf("route %q must not include query string (callers append their own)", spec.Path)
		}
		if strings.HasSuffix(spec.Path, "/") {
			t.Errorf("route %q must not have trailing slash", spec.Path)
		}
		if seenIDs[spec.ID] {
			t.Errorf("route id %q listed twice", spec.ID)
		}
		seenIDs[spec.ID] = true
		routeKey := spec.Method + " " + spec.Path
		if seenRoutes[routeKey] {
			t.Errorf("route %q listed twice", routeKey)
		}
		seenRoutes[routeKey] = true
	}
}

// TestAllCloudRoutes_CoversConstants pins that every CloudRoute*
// constant in this package is also present in AllCloudRoutes — so
// a new endpoint can't be added without showing up in the list a
// future URL-contract test will iterate.
func TestAllCloudRoutes_CoversConstants(t *testing.T) {
	want := []string{
		CloudRouteOAuthAuthorize,
		CloudRouteOAuthToken,
		CloudRouteOAuthRevoke,
		CloudRouteOAuthUserInfo,
		CloudRouteSyncThreads,
		CloudRouteSyncMessages,
		CloudRouteModelsList,
		CloudRouteModelGatewayAnthropic,
		CloudRouteModelGatewayOpenAI,
		CloudRouteSkillsList,
		CloudRouteChatAgent,
		CloudRouteAgentThread,
		CloudRouteVersion,
		CloudRouteLoginTransactionCreate,
		CloudRouteLoginTransactionStatus,
		CloudRouteLoginTransactionPassword,
		CloudRouteLoginTransactionExchange,
	}
	if len(AllCloudRoutes) != len(want) {
		t.Fatalf("AllCloudRoutes len=%d, want %d — add new const to the list", len(AllCloudRoutes), len(want))
	}
	for _, w := range want {
		found := false
		for _, r := range AllCloudRoutes {
			if r == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("constant %q missing from AllCloudRoutes", w)
		}
	}
}

func TestCloudRouteSpecsMatchDesktopBoundaryManifest(t *testing.T) {
	data, err := os.ReadFile("../../../desktop/contracts/desktop-boundaries.v0.json")
	if err != nil {
		t.Fatalf("read Desktop boundary manifest: %v", err)
	}
	var manifest struct {
		CloudRoutes []CloudRouteSpec `json:"cloudRoutes"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode Desktop boundary manifest: %v", err)
	}

	got := CurrentCloudRouteSpecs()
	if len(got) != len(manifest.CloudRoutes) {
		t.Fatalf("cloud route count: got %d, manifest has %d", len(got), len(manifest.CloudRoutes))
	}
	byID := make(map[string]CloudRouteSpec, len(got))
	for _, spec := range got {
		byID[spec.ID] = spec
	}
	for _, want := range manifest.CloudRoutes {
		spec, ok := byID[want.ID]
		if !ok {
			t.Errorf("manifest cloud route %q is missing from runtime specs", want.ID)
			continue
		}
		if spec.Method != want.Method || spec.Path != want.Path {
			t.Errorf("cloud route %q: got %s %s, want %s %s", want.ID, spec.Method, spec.Path, want.Method, want.Path)
		}
		delete(byID, want.ID)
	}
	for id := range byID {
		t.Errorf("runtime cloud route %q is missing from the manifest", id)
	}
}

func TestCurrentCloudRouteSpecsReturnsDefensiveCopy(t *testing.T) {
	first := CurrentCloudRouteSpecs()
	first[0].ID = "mutated"
	first[0].Path = "/mutated"

	second := CurrentCloudRouteSpecs()
	if second[0].ID == "mutated" || second[0].Path == "/mutated" {
		t.Fatal("cloud route specs were not defensively copied")
	}
	if !slices.Equal(AllCloudRoutes, cloudRoutePaths(second)) {
		t.Fatal("legacy AllCloudRoutes drifted from method-aware specs")
	}
}

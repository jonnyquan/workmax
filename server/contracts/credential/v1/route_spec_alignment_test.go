package v1_test

import (
	"testing"

	credentialv1 "server/contracts/credential/v1"
	httpv1 "server/contracts/http/v1"
)

// This pins the typed policy vocabulary to RouteSpec target credentials
// without making either production contract depend on the other package.
func TestTypedPoliciesAlignWithHTTPRouteSpecTargets(t *testing.T) {
	tests := []struct {
		policy credentialv1.Policy
		target httpv1.CredentialPolicy
	}{
		{credentialv1.PolicyPortalSession, httpv1.CredentialPortalSession},
		{credentialv1.PolicyAdminSession, httpv1.CredentialAdminSession},
		{credentialv1.PolicyAgentResource, httpv1.CredentialAgentResource},
	}
	for _, test := range tests {
		if string(test.policy) != string(test.target) {
			t.Errorf("typed policy %q drifted from RouteSpec target %q", test.policy, test.target)
		}
	}
}

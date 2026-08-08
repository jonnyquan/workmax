package workagent

import (
	"testing"

	workagentModel "server/model/workagent"
)

// TestDefaultAgentMode_IsValid pins the const against the allow-list.
// If someone retypes DefaultAgentMode to "" or a typo, normalizeAgentMode
// would reject it on every fallback path and the agent would silently
// take the writer-skill branch on every empty-mode request.
func TestDefaultAgentMode_IsValid(t *testing.T) {
	if _, ok := allowedAgentModes[workagentModel.DefaultAgentMode]; !ok {
		t.Fatalf("DefaultAgentMode %q is not in allowedAgentModes — drift between model/workagent and api/pro/tools/workagent", workagentModel.DefaultAgentMode)
	}
}

func TestDetermineAgentMode(t *testing.T) {
	cases := []struct {
		name         string
		metadataMode string
		threadMode   string
		want         string
	}{
		{"both empty → default", "", "", workagentModel.DefaultAgentMode},
		{"metadata wins over thread", "flashCard", "ppt", "flashCard"},
		{"thread used when metadata empty", "", "logo", "logo"},
		{"thread used when metadata invalid", "garbage", "logo", "logo"},
		{"default when both invalid", "garbage", "alsoGarbage", workagentModel.DefaultAgentMode},
		{"metadata whitespace falls through", "   ", "ppt", "ppt"},
		{"metadata trimmed before lookup", "  flashCard  ", "ppt", "flashCard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := determineAgentMode(tc.metadataMode, tc.threadMode)
			if got != tc.want {
				t.Fatalf("determineAgentMode(%q, %q) = %q, want %q", tc.metadataMode, tc.threadMode, got, tc.want)
			}
		})
	}
}

func TestRequiresSessionReset(t *testing.T) {
	cases := []struct {
		name     string
		prevMode string
		newMode  string
		want     bool
	}{
		{"new mode empty → no reset", "ppt", "", false},
		{"same mode → no reset", "ppt", "ppt", false},
		{"different mode → reset", "ppt", "flashCard", true},
		{"empty prev, new set → reset", "", "ppt", true},
		{"both empty → no reset", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := requiresSessionReset(tc.prevMode, tc.newMode)
			if got != tc.want {
				t.Fatalf("requiresSessionReset(%q, %q) = %v, want %v", tc.prevMode, tc.newMode, got, tc.want)
			}
		})
	}
}

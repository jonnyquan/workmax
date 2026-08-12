//go:build desktop

package pi_agent

import (
	"strings"
	"testing"

	agentruntime "server/desktop/agentruntime"
)

// The gap this pins shut: --tools carried `find` and `ls`, claudeToolName
// turned them into Find and Ls, and neither name was in AutoAllowed or
// AskAllowed. Consult denies anything in neither set outright — no card, no
// appeal — so the two tools the engine had deliberately enabled were one
// widened gate away from being refused on sight. It was invisible only because
// the embedded extension raises ui requests for write/edit alone, so a find
// never reached Consult at all.
//
// Deriving the list from the profile CONSTANTS is the point: they are the same
// strings that go on pi's argv, so a tool added to the profile shows up here
// without anybody remembering to add it.
func TestEveryEnabledPiToolHasAHomeInTheVocabulary(t *testing.T) {
	for _, profile := range []struct {
		name  string
		tools string
	}{
		{"read-only mode", readOnlyToolProfile},
		{"approval mode", approvalToolProfile},
	} {
		t.Run(profile.name, func(t *testing.T) {
			tools := strings.Split(profile.tools, ",")
			if len(tools) == 0 || profile.tools == "" {
				t.Fatal("the tool profile is empty; the argv contract cannot be checked")
			}
			for _, pi := range tools {
				shared := claudeToolName(pi)
				if shared == "" {
					t.Fatalf("pi tool %q normalizes to nothing", pi)
				}
				if !agentruntime.ApprovalSurfaceHas(shared) {
					t.Fatalf("pi enables %q (shared name %q), which is in neither "+
						"ApprovalReadSurface nor ApprovalWriteSurface: Consult would deny it outright",
						pi, shared)
				}
			}
		})
	}
}

// The write surface must ASK, never auto-allow: the extension gates exactly
// these two, and a name that drifted into the read set would silence the card.
func TestPiWriteToolsLandOnTheAskingSideOfTheVocabulary(t *testing.T) {
	auto := map[string]bool{}
	for _, t := range agentruntime.ApprovalReadSurface {
		auto[t] = true
	}
	ask := map[string]bool{}
	for _, t := range agentruntime.ApprovalWriteSurface {
		ask[t] = true
	}
	for _, pi := range []string{"write", "edit"} {
		shared := claudeToolName(pi)
		if auto[shared] {
			t.Fatalf("%q is auto-allowed; the approval card would never be shown", shared)
		}
		if !ask[shared] {
			t.Fatalf("%q must be askable", shared)
		}
	}
	// ...and the read tools must not ask, or every read raises a card the user
	// has to dismiss before the loop can look at a file.
	for _, pi := range []string{"read", "grep", "find", "ls"} {
		shared := claudeToolName(pi)
		if !auto[shared] {
			t.Fatalf("%q must never ask", shared)
		}
	}
}

// One pi tool, one shared name. pi's tool_execution_end cannot name the file it
// touched, so the renderer settles the oldest OPEN call of that tool — and two
// distinct tools folded onto one name (find and ls both becoming "Glob", say)
// would settle each other's row whenever both are in flight.
func TestEachPiToolKeepsANameOfItsOwn(t *testing.T) {
	seen := map[string]string{}
	for _, pi := range strings.Split(approvalToolProfile, ",") {
		shared := claudeToolName(pi)
		if prior, dup := seen[shared]; dup {
			t.Fatalf("pi tools %q and %q both normalize to %q", prior, pi, shared)
		}
		seen[shared] = pi
	}
}

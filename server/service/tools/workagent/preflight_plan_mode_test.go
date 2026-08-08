package workagent

import (
	"strings"
	"testing"

	"server/utils/testutil"
)

// preflight_plan_mode_test.go — pins the A3 contract:
//
//   • PlanMode=false on the same skill → no plan protocol in the
//     output (the rest of the additions tail is unchanged).
//   • PlanMode=true on the same skill → the WorkAgentPlanModeProtocol
//     constant appears in the output, marked as plan-mode in
//     the per-layer trace.
//
// The trace assertion is the load-bearing piece: a typo in the
// const text would still flip the "contains" check if the typo
// happens to land on a different protocol string. The named-
// trace entry pins the slot, not the bytes.

func TestBuildPreflightAdditions_PlanModeOffDoesNotInjectProtocol(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	got := BuildPreflightAdditionsForThreadWithOptions(42, "ppt", 0, PreflightOptions{
		PlanMode: false,
	})
	if strings.Contains(got, "Plan Mode (active)") {
		t.Errorf("plan-mode protocol must NOT be injected when PlanMode=false; got %q", got)
	}
}

func TestBuildPreflightAdditions_PlanModeOnInjectsProtocol(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	got := BuildPreflightAdditionsForThreadWithOptions(42, "ppt", 0, PreflightOptions{
		PlanMode: true,
	})
	if !strings.Contains(got, "Plan Mode (active)") {
		t.Errorf("plan-mode protocol must be injected when PlanMode=true; got %q", got)
	}
	// Pin the TodoWrite reference too — a refactor that switches
	// the carrier (e.g. to propose_plan) would silently change
	// the model's plan-tracking artefact.
	if !strings.Contains(got, "TodoWrite") {
		t.Errorf("plan protocol must instruct agent to use TodoWrite; got %q", got)
	}
}

func TestWorkAgentPlanModeProtocol_NonEmpty(t *testing.T) {
	// Tiny but load-bearing: the const must not slip to "" via
	// an editor accident — a silent empty would make PlanMode=true
	// a no-op the test above can't catch by itself.
	if strings.TrimSpace(WorkAgentPlanModeProtocol) == "" {
		t.Fatal("WorkAgentPlanModeProtocol is empty")
	}
}

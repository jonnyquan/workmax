package workagent

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"server/config"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

func testAgentWorkspaceConfig(t *testing.T) *config.ClaudeAgent {
	t.Helper()
	t.Cleanup(func() { _ = os.RemoveAll("agent_workspace") })
	return &config.ClaudeAgent{Enabled: true, WorkspaceRoot: "agent_workspace"}
}

// TestResetForRedo_WipesRedoSensitiveState — PR-9b/2b: every per-turn
// accumulator that the gate or the post-emit pipeline reads must be
// cleared between redo iterations. Identity fields (uid / agentMode /
// tempMessageID) survive the reset so the next iteration runs as the
// SAME user, SAME skill, SAME SSE channel — the redo is "this turn
// continues," not "a new turn starts."
func TestResetForRedo_WipesRedoSensitiveState(t *testing.T) {
	earlier := time.Now().Add(-1 * time.Hour)
	cb := &agentTurnCallbacks{
		uid:                    42,
		agentMode:              "ppt",
		tempMessageID:          "tmp-1",
		revisionParentID:       88,
		accumulatedText:        "<slide>fake-stat: 30% growth</slide>",
		gateBlocked:            true,
		gateRedoPrompt:         "<lint-findings priority=\"P0\">...",
		gateAutoRedoAllowed:    true,
		generatedFilesPayload:  []map[string]interface{}{{"path": "out.pptx"}},
		generatedThreadFileIDs: []uint{99},
		pptMissingOutput:       true,
		executionStartTime:     earlier,
	}

	cb.resetForRedo()

	// Wiped fields — must all be zero values
	if cb.accumulatedText != "" {
		t.Errorf("accumulatedText not wiped: got %q", cb.accumulatedText)
	}
	if cb.gateBlocked {
		t.Error("gateBlocked not cleared; redo loop would loop forever")
	}
	if cb.gateRedoPrompt != "" {
		t.Errorf("gateRedoPrompt not cleared: got %q", cb.gateRedoPrompt)
	}
	if cb.gateAutoRedoAllowed {
		t.Error("gateAutoRedoAllowed not cleared; stale gate flags would trigger a redo")
	}
	if cb.generatedFilesPayload != nil {
		t.Error("generatedFilesPayload not cleared; stale files would carry over")
	}
	if cb.generatedThreadFileIDs != nil {
		t.Error("generatedThreadFileIDs not cleared; stale lifecycle updates would hit prior artifacts")
	}
	if cb.pptMissingOutput {
		t.Error("pptMissingOutput not cleared; would falsely demote shouldFinalize")
	}
	if !cb.executionStartTime.After(earlier) {
		t.Error("executionStartTime not advanced; file scanner would see stale outputs")
	}

	// Identity fields — must survive across the reset
	if cb.uid != 42 {
		t.Errorf("uid lost across reset: got %d", cb.uid)
	}
	if cb.agentMode != "ppt" {
		t.Errorf("agentMode lost across reset: got %q", cb.agentMode)
	}
	if cb.tempMessageID != "tmp-1" {
		t.Errorf("tempMessageID lost across reset: got %q", cb.tempMessageID)
	}
	if cb.revisionParentID != 88 {
		t.Errorf("revisionParentID lost across reset: got %d", cb.revisionParentID)
	}
}

func TestParseRevisionParentArtifactID(t *testing.T) {
	prompt := "Revise this design artifact and produce a new version.\n\n- artifact_id: artifact-42\n- file_id: 9"
	if got := parseRevisionParentArtifactID(prompt); got != 42 {
		t.Fatalf("parseRevisionParentArtifactID = %d, want 42", got)
	}
	if got := parseRevisionParentArtifactID("artifact id artifact-42"); got != 0 {
		t.Fatalf("non-structured prompt parsed as %d, want 0", got)
	}
}

func TestMarkVariationManifestIssuesAsWarningAddsRepairPrompt(t *testing.T) {
	result := markVariationManifestIssuesAsWarning(
		json.RawMessage(`{"type":"result","content":"drafts generated"}`),
		[]string{"draft pass missing outputs/.workagent/pass_1_variations.json manifest"},
	)
	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if payload["subtype"] != "variation_manifest_issue" {
		t.Fatalf("subtype = %v, want variation_manifest_issue", payload["subtype"])
	}
	if payload["code"] != "PASS1_VARIATION_MANIFEST_REQUIRED" {
		t.Fatalf("code = %v", payload["code"])
	}
	if payload["is_error"] != false {
		t.Fatalf("is_error = %v, want false", payload["is_error"])
	}
	prompt, _ := payload["suggested_prompt"].(string)
	if !strings.Contains(prompt, "outputs/.workagent/pass_1_variations.json") ||
		!strings.Contains(prompt, "every file_path exists under outputs/") ||
		!strings.Contains(prompt, "Do not regenerate the final output yet") {
		t.Fatalf("suggested prompt missing repair instructions: %q", prompt)
	}
}

func TestAgentTurnOnDone_DraftOutputMissingManifestEmitsRepairWarningSmoke(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Draft manifest smoke")

	manager := workagentService.GetAgentClientManager()
	agentConfig := testAgentWorkspaceConfig(t)
	if manager.WorkspaceRoot() == "" {
		if err := manager.Initialize(agentConfig); err != nil {
			t.Fatalf("initialize manager: %v", err)
		}
	}
	threadWorkspace, err := manager.EnsureThreadWorkspace(thread.UID, thread.UUID)
	if err != nil {
		t.Fatalf("ensure thread workspace: %v", err)
	}
	outputs := filepath.Join(threadWorkspace, "outputs")
	if err := os.MkdirAll(outputs, 0o755); err != nil {
		t.Fatalf("mkdir outputs: %v", err)
	}
	outputPath := filepath.Join(outputs, "balanced.html")
	if err := os.WriteFile(outputPath, []byte("<!doctype html><html><body>balanced draft</body></html>"), 0o600); err != nil {
		t.Fatalf("write draft output: %v", err)
	}
	if !workagentService.PersistPassModeState(42, thread.Id, workagentService.WorkAgentPassModeState{
		Mode:   workagentService.WorkAgentPassModeDraft,
		Source: "smoke_test",
	}) {
		t.Fatalf("persist draft pass mode")
	}

	req, _ := http.NewRequest(http.MethodPost, "/agent", nil)
	var done *workagentModel.AgentSSEEvent
	cb := &agentTurnCallbacks{
		api:                NewAIChatApiNew(),
		ctx:                &gin.Context{Request: req},
		chatThread:         thread,
		workspaceRootPath:  manager.WorkspaceRoot(),
		threadWorkspace:    threadWorkspace,
		agentMode:          "webBanner",
		tempMessageID:      "tmp-smoke",
		uid:                42,
		executionStartTime: time.Now().Add(-1 * time.Second),
		sendEvent: func(event workagentModel.AgentSSEEvent) {
			if event.Type == workagentModel.AgentEventDone {
				copy := event
				done = &copy
			}
		},
	}
	cb.OnDone(json.RawMessage(`{"type":"result","content":"draft generated"}`))

	if done == nil {
		t.Fatal("expected done event")
	}
	var payload map[string]any
	if err := json.Unmarshal(done.Result, &payload); err != nil {
		t.Fatalf("decode done result: %v result=%s", err, string(done.Result))
	}
	if payload["subtype"] != "variation_manifest_issue" || payload["code"] != "PASS1_VARIATION_MANIFEST_REQUIRED" {
		t.Fatalf("done result missing variation manifest warning: %#v", payload)
	}
	prompt, _ := payload["suggested_prompt"].(string)
	if !strings.Contains(prompt, "outputs/.workagent/pass_1_variations.json") {
		t.Fatalf("suggested prompt missing manifest repair instruction: %#v", payload["suggested_prompt"])
	}
	files, ok := payload["generated_files"].([]any)
	if !ok || len(files) != 1 {
		t.Fatalf("generated_files = %#v, want one generated draft", payload["generated_files"])
	}
	if cb.generatedFilesPayload == nil || len(cb.generatedFilesPayload) != 1 {
		t.Fatalf("callback generatedFilesPayload = %#v", cb.generatedFilesPayload)
	}
	var registered workagentModel.ThreadFile
	if err := db.Where("thread_id = ? AND file_name = ?", thread.Id, "balanced.html").First(&registered).Error; err != nil {
		t.Fatalf("expected OnDone to register generated file: %v", err)
	}
}

func TestAgentTurnOnDone_DraftOutputDuplicateManifestFilePathEmitsRepairWarningSmoke(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Draft manifest duplicate file smoke")

	manager := workagentService.GetAgentClientManager()
	agentConfig := testAgentWorkspaceConfig(t)
	if manager.WorkspaceRoot() == "" {
		if err := manager.Initialize(agentConfig); err != nil {
			t.Fatalf("initialize manager: %v", err)
		}
	}
	threadWorkspace, err := manager.EnsureThreadWorkspace(thread.UID, thread.UUID)
	if err != nil {
		t.Fatalf("ensure thread workspace: %v", err)
	}
	outputs := filepath.Join(threadWorkspace, "outputs")
	manifestDir := filepath.Join(outputs, ".workagent")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	outputPath := filepath.Join(outputs, "shared.html")
	if err := os.WriteFile(outputPath, []byte("<!doctype html><html><body>shared draft</body></html>"), 0o600); err != nil {
		t.Fatalf("write draft output: %v", err)
	}
	manifest := `{"variations":[` +
		`{"id":"balanced","label":"Balanced","file_path":"shared.html"},` +
		`{"id":"bold","label":"Bold","file_path":"shared.html"}` +
		`]}`
	if err := os.WriteFile(filepath.Join(manifestDir, "pass_1_variations.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if !workagentService.PersistPassModeState(42, thread.Id, workagentService.WorkAgentPassModeState{
		Mode:   workagentService.WorkAgentPassModeDraft,
		Source: "smoke_test",
	}) {
		t.Fatalf("persist draft pass mode")
	}

	req, _ := http.NewRequest(http.MethodPost, "/agent", nil)
	var done *workagentModel.AgentSSEEvent
	cb := &agentTurnCallbacks{
		api:                NewAIChatApiNew(),
		ctx:                &gin.Context{Request: req},
		chatThread:         thread,
		workspaceRootPath:  manager.WorkspaceRoot(),
		threadWorkspace:    threadWorkspace,
		agentMode:          "webBanner",
		tempMessageID:      "tmp-duplicate-manifest",
		uid:                42,
		executionStartTime: time.Now().Add(-1 * time.Second),
		sendEvent: func(event workagentModel.AgentSSEEvent) {
			if event.Type == workagentModel.AgentEventDone {
				copy := event
				done = &copy
			}
		},
	}
	cb.OnDone(json.RawMessage(`{"type":"result","content":"draft generated"}`))

	if done == nil {
		t.Fatal("expected done event")
	}
	var payload map[string]any
	if err := json.Unmarshal(done.Result, &payload); err != nil {
		t.Fatalf("decode done result: %v result=%s", err, string(done.Result))
	}
	if payload["subtype"] != "variation_manifest_issue" || payload["code"] != "PASS1_VARIATION_MANIFEST_REQUIRED" {
		t.Fatalf("done result missing variation manifest warning: %#v", payload)
	}
	prompt, _ := payload["suggested_prompt"].(string)
	if !strings.Contains(prompt, `repeats file_path "shared.html"`) {
		t.Fatalf("suggested prompt missing duplicate file_path issue: %#v", payload["suggested_prompt"])
	}
	var registered workagentModel.ThreadFile
	if err := db.Where("thread_id = ? AND file_name = ?", thread.Id, "shared.html").First(&registered).Error; err != nil {
		t.Fatalf("expected OnDone to register generated file: %v", err)
	}
	if !strings.Contains(registered.Description, `"variation_id":"balanced"`) {
		t.Fatalf("registered file should keep first variation binding, got %q", registered.Description)
	}
}

func TestAgentTurnOnDone_DraftOutputWithManifestRegistersVariationDraftsSmoke(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Draft manifest success smoke")

	manager := workagentService.GetAgentClientManager()
	agentConfig := testAgentWorkspaceConfig(t)
	if manager.WorkspaceRoot() == "" {
		if err := manager.Initialize(agentConfig); err != nil {
			t.Fatalf("initialize manager: %v", err)
		}
	}
	threadWorkspace, err := manager.EnsureThreadWorkspace(thread.UID, thread.UUID)
	if err != nil {
		t.Fatalf("ensure thread workspace: %v", err)
	}
	outputs := filepath.Join(threadWorkspace, "outputs")
	manifestDir := filepath.Join(outputs, ".workagent")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	drafts := map[string]string{
		"conservative.html": "<!doctype html><html><body>conservative draft</body></html>",
		"balanced.html":     "<!doctype html><html><body>balanced draft</body></html>",
		"bold.html":         "<!doctype html><html><body>bold draft</body></html>",
	}
	for name, body := range drafts {
		if err := os.WriteFile(filepath.Join(outputs, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write draft %s: %v", name, err)
		}
	}
	manifest := `{"variations":[` +
		`{"id":"conservative","label":"Conservative","stance":"conservative","file_path":"conservative.html","design_system_basename":"minimal-editorial","asset_contract":"brand assets locked"},` +
		`{"id":"balanced","label":"Balanced","stance":"balanced","file_path":"balanced.html","design_system_basename":"minimal-editorial","asset_contract":"brand assets locked"},` +
		`{"id":"bold","label":"Bold","stance":"bold","file_path":"bold.html","design_system_basename":"expressive-grid","asset_contract":"brand assets locked"}` +
		`]}`
	if err := os.WriteFile(filepath.Join(manifestDir, "pass_1_variations.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if !workagentService.PersistPassModeState(42, thread.Id, workagentService.WorkAgentPassModeState{
		Mode:   workagentService.WorkAgentPassModeDraft,
		Source: "smoke_test",
	}) {
		t.Fatalf("persist draft pass mode")
	}

	req, _ := http.NewRequest(http.MethodPost, "/agent", nil)
	var done *workagentModel.AgentSSEEvent
	cb := &agentTurnCallbacks{
		api:                NewAIChatApiNew(),
		ctx:                &gin.Context{Request: req},
		chatThread:         thread,
		workspaceRootPath:  manager.WorkspaceRoot(),
		threadWorkspace:    threadWorkspace,
		agentMode:          "webBanner",
		tempMessageID:      "tmp-manifest-success",
		uid:                42,
		executionStartTime: time.Now().Add(-1 * time.Second),
		sendEvent: func(event workagentModel.AgentSSEEvent) {
			if event.Type == workagentModel.AgentEventDone {
				copy := event
				done = &copy
			}
		},
	}
	cb.OnDone(json.RawMessage(`{"type":"result","content":"drafts generated"}`))

	if done == nil {
		t.Fatal("expected done event")
	}
	var payload map[string]any
	if err := json.Unmarshal(done.Result, &payload); err != nil {
		t.Fatalf("decode done result: %v result=%s", err, string(done.Result))
	}
	if payload["subtype"] == "variation_manifest_issue" || payload["code"] == "PASS1_VARIATION_MANIFEST_REQUIRED" {
		t.Fatalf("valid manifest should not emit repair warning: %#v", payload)
	}
	files, ok := payload["generated_files"].([]any)
	if !ok || len(files) != 3 {
		t.Fatalf("generated_files = %#v, want three variation drafts", payload["generated_files"])
	}
	if cb.generatedFilesPayload == nil || len(cb.generatedFilesPayload) != 3 {
		t.Fatalf("callback generatedFilesPayload = %#v", cb.generatedFilesPayload)
	}

	for fileName, variationID := range map[string]string{
		"conservative.html": "conservative",
		"balanced.html":     "balanced",
		"bold.html":         "bold",
	} {
		var registered workagentModel.ThreadFile
		if err := db.Where("thread_id = ? AND file_name = ?", thread.Id, fileName).First(&registered).Error; err != nil {
			t.Fatalf("expected OnDone to register %s: %v", fileName, err)
		}
		if !strings.Contains(registered.Description, `"kind":"workagent_variation_draft"`) ||
			!strings.Contains(registered.Description, `"variation_id":"`+variationID+`"`) {
			t.Fatalf("%s description missing variation draft metadata: %q", fileName, registered.Description)
		}
	}

	var manifestRows int64
	if err := db.Model(&workagentModel.ThreadFile{}).Where("thread_id = ? AND file_name = ?", thread.Id, "pass_1_variations.json").Count(&manifestRows).Error; err != nil {
		t.Fatalf("count manifest rows: %v", err)
	}
	if manifestRows != 0 {
		t.Fatalf("manifest control file should not be registered, got %d rows", manifestRows)
	}
}

func TestParseComparisonArtifactID(t *testing.T) {
	prompt := "Compare these two design artifact versions.\n\n- latest_artifact_id: artifact-88\n- previous_artifact_id: artifact-42"
	if got := parseComparisonArtifactID(prompt); got != 88 {
		t.Fatalf("parseComparisonArtifactID = %d, want 88", got)
	}
	if got := parseComparisonArtifactID("latest artifact artifact-88"); got != 0 {
		t.Fatalf("non-structured prompt parsed as %d, want 0", got)
	}
}

func TestParseComparisonSource(t *testing.T) {
	comparePrompt := "Compare these two design artifact versions.\n\n- latest_artifact_id: artifact-88\n- previous_artifact_id: artifact-42"
	if got := parseComparisonSource(comparePrompt); got != "agent_compare" {
		t.Fatalf("parseComparisonSource compare = %q, want agent_compare", got)
	}
	diffPrompt := "Create a visual diff artifact for these two design versions.\n\n- latest_artifact_id: artifact-88\n- previous_artifact_id: artifact-42"
	if got := parseComparisonSource(diffPrompt); got != "agent_visual_diff" {
		t.Fatalf("parseComparisonSource visual diff = %q, want agent_visual_diff", got)
	}
	if got := parseComparisonSource("visual diff artifact artifact-88"); got != "" {
		t.Fatalf("parseComparisonSource unstructured = %q, want empty", got)
	}
}

func TestParseAssetCandidateArtifactID(t *testing.T) {
	prompt := "Prepare this finalized design artifact as an asset-library candidate.\n\n- artifact_id: artifact-88\n- file_id: 9\n\nFirst classify the asset_kind as exactly one of: reference."
	if got := parseAssetCandidateArtifactID(prompt); got != 88 {
		t.Fatalf("parseAssetCandidateArtifactID = %d, want 88", got)
	}
	if got := parseAssetCandidateArtifactID("Reuse this artifact.\n- artifact_id: artifact-88"); got != 0 {
		t.Fatalf("non asset-candidate prompt parsed as %d, want 0", got)
	}
}

func TestExtractAssetCandidateInput(t *testing.T) {
	text := strings.Join([]string{
		"Ready to save as a reusable reference.",
		"- asset_kind: reference",
		"- name: Summer Poster Reference",
		"- slug: summer-poster-reference",
		"- intended use: moodboard for campaign poster variants",
		"- visual guidance: retain high-contrast type and product framing",
		"- reusable constraints: do not reuse event-specific dates",
		"- negative reuse notes: do not copy the exact CTA",
	}, "\n")
	input, ok := extractAssetCandidateInput(text)
	if !ok {
		t.Fatal("extractAssetCandidateInput did not parse structured candidate")
	}
	if input.AssetKind != "reference" || input.Name != "Summer Poster Reference" || input.Slug != "summer-poster-reference" {
		t.Fatalf("candidate identity = %#v", input)
	}
	if input.Profile["intendedUse"] != "moodboard for campaign poster variants" {
		t.Fatalf("intendedUse profile = %#v", input.Profile["intendedUse"])
	}
	if _, ok := extractAssetCandidateInput("- name: Missing Kind"); ok {
		t.Fatal("candidate parsed without asset_kind")
	}
}

func TestExtractAssetCandidateInput_DesignSystemKind(t *testing.T) {
	text := strings.Join([]string{
		"- asset_kind: design_system",
		"- name: Bold Landing Page System",
		"- slug: bold-landing-page-system",
		"- intended use: reuse color, type, layout, and motion rules",
		"- visual guidance: preserve the 9-section schema",
		"- designSystemMarkdown: ```markdown",
		"# Design System - Bold Landing Page",
		"## 1. Color",
		"## 2. Typography",
		"```",
	}, "\n")
	input, ok := extractAssetCandidateInput(text)
	if !ok {
		t.Fatal("extractAssetCandidateInput did not parse design_system candidate")
	}
	if input.AssetKind != "design_system" {
		t.Fatalf("asset kind = %q, want design_system", input.AssetKind)
	}
	if input.Profile["visualGuidance"] != "preserve the 9-section schema" {
		t.Fatalf("visualGuidance profile = %#v", input.Profile["visualGuidance"])
	}
	if markdown, ok := input.Profile["designSystemMarkdown"].(string); !ok || !strings.Contains(markdown, "## 2. Typography") {
		t.Fatalf("designSystemMarkdown profile = %#v", input.Profile["designSystemMarkdown"])
	}
}

func TestExtractComparisonDecision(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "keep", text: "Recommendation: keep the latest version.", want: "keep"},
		{name: "revise", text: "Recommend revise because hierarchy regressed.", want: "revise"},
		{name: "rollback wins", text: "Revise is possible, but recommendation: rollback.", want: "rollback"},
		{name: "none", text: "The latest version changes contrast.", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractComparisonDecision(tc.text); got != tc.want {
				t.Fatalf("extractComparisonDecision = %q, want %q", got, tc.want)
			}
		})
	}
}

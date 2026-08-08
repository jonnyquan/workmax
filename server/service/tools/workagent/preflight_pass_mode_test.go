package workagent

import (
	"strings"
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"
)

func TestLoadPassModeProtocol_DefaultsToBriefing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	got := loadPassModeProtocol(9100)
	if !strings.Contains(got, "<pass-mode>") {
		t.Fatalf("expected pass-mode protocol, got %q", got)
	}
	if !strings.Contains(got, "mode: briefing") {
		t.Fatalf("expected default briefing mode, got %q", got)
	}
	if !strings.Contains(got, "2-3 distinct design directions") {
		t.Fatalf("expected Pass 1 direction guidance, got %q", got)
	}
	if !strings.Contains(got, "variations_picker block") {
		t.Fatalf("expected briefing protocol to mention variations_picker, got %q", got)
	}
}

func TestPersistPassMode_GuardsBadInputs(t *testing.T) {
	if PersistPassMode(0, 1, WorkAgentPassModeDraft, "question_form") {
		t.Error("bad uid should return false")
	}
	if PersistPassMode(1, 0, WorkAgentPassModeDraft, "question_form") {
		t.Error("bad thread should return false")
	}
	if PersistPassMode(1, 1, WorkAgentPassMode("unknown"), "question_form") {
		t.Error("unknown mode should return false")
	}
}

func TestPersistPassMode_LatestWins(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	if !PersistPassMode(42, 9200, WorkAgentPassModeDraft, "question_form") {
		t.Fatal("expected draft pass mode to persist")
	}
	if !PersistPassModeState(42, 9200, WorkAgentPassModeState{
		Mode:                WorkAgentPassModeFinalize,
		Source:              "variation_picker",
		SelectedVariationID: "balanced",
		SelectedArtifactID:  "artifact-2",
		SelectedFileID:      "file-2",
		DesignSystem:        "modern-minimal",
		AssetContract:       "brand locked",
	}) {
		t.Fatal("expected finalize pass mode to persist")
	}

	state := loadPassModeState(9200)
	if state.Mode != WorkAgentPassModeFinalize {
		t.Fatalf("latest pass mode should win, got %q", state.Mode)
	}
	if state.Source != "variation_picker" {
		t.Fatalf("expected latest source, got %q", state.Source)
	}
	if state.SelectedVariationID != "balanced" {
		t.Fatalf("expected selected variation, got %q", state.SelectedVariationID)
	}
	if state.SelectedArtifactID != "artifact-2" || state.SelectedFileID != "file-2" {
		t.Fatalf("expected selected artifact/file, got artifact=%q file=%q", state.SelectedArtifactID, state.SelectedFileID)
	}

	got := loadPassModeProtocol(9200)
	if !strings.Contains(got, "mode: finalize") {
		t.Fatalf("expected finalize protocol, got %q", got)
	}
	if !strings.Contains(got, "selected_variation_id: balanced") {
		t.Fatalf("expected selected variation in protocol, got %q", got)
	}
	if !strings.Contains(got, "selected_artifact_id: artifact-2") || !strings.Contains(got, "selected_file_id: file-2") {
		t.Fatalf("expected selected artifact/file in protocol, got %q", got)
	}
	if !strings.Contains(got, "design_system_basename: modern-minimal") || !strings.Contains(got, "asset_contract: brand locked") {
		t.Fatalf("expected selected design context in protocol, got %q", got)
	}
	if !strings.Contains(got, "lock its design system and asset contract") {
		t.Fatalf("expected finalize guidance, got %q", got)
	}
	if !strings.Contains(got, "selected draft artifact/file as the source of truth") {
		t.Fatalf("expected selected artifact finalize guidance, got %q", got)
	}
}

func TestFormatPassModeProtocol_DraftIncludesVariationBlockShape(t *testing.T) {
	got := formatPassModeProtocol(WorkAgentPassModeState{Mode: WorkAgentPassModeDraft, Source: "question_form"})
	for _, want := range []string{
		"variations_picker",
		"pass_1_variations",
		"outputs/.workagent/pass_1_variations.json",
		"file_path",
		"conservative",
		"balanced",
		"bold",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("draft protocol missing %q: %q", want, got)
		}
	}
}

func TestLoadPassModeState_RejectsMalformedMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	if err := DefaultMessageRepository().CreateAgentMessage(&workagentModel.ChatMessage{
		UID:         42,
		UUID:        "bad-pass-mode-" + t.Name(),
		ThreadID:    9300,
		ContentType: "discovery_marker",
		ChatMode:    string(workagentModel.ChatModeAgent),
		Metadata:    `{"kind":"workagent_pass_mode","mode":"draft"`,
	}); err != nil {
		t.Fatal(err)
	}

	state := loadPassModeState(9300)
	if state.Mode != WorkAgentPassModeBriefing || state.Source != "default" {
		t.Fatalf("bad metadata should fall back to briefing/default, got mode=%q source=%q", state.Mode, state.Source)
	}
}

func TestBuildPreflightAdditionsForThread_IncludesPassMode(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	if !PersistPassMode(42, 9400, WorkAgentPassModeRevision, "artifact_revise") {
		t.Fatal("expected revision pass mode to persist")
	}

	got := BuildPreflightAdditionsForThread(42, "ppt", 9400)
	if !strings.Contains(got, "<pass-mode>") {
		t.Fatalf("composed additions missing pass-mode: %q", got)
	}
	if !strings.Contains(got, "mode: revision") {
		t.Fatalf("composed additions missing revision mode: %q", got)
	}
	if !strings.Contains(got, "next artifact version") {
		t.Fatalf("revision protocol should instruct versioned redo, got %q", got)
	}
}

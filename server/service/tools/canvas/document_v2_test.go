package canvas

import (
	"encoding/json"
	"testing"

	"server/model"
)

// document_v2.go is pure type/constant declarations — the one thing
// that can go wrong is a drift between these Go types and the Desktop
// v2 wire shape. A renamed constant or a
// changed JSON tag would silently break the wire contract between the
// Go migrator and the React loader. These tests lock the public names
// and JSON field casings so such drift turns CI red.

func TestSchemaVersionV2_IsTwo(t *testing.T) {
	// §8.3 pins v2 as the integer 2 — the migrator writes this verbatim
	// and the validator refuses anything other value.
	if SchemaVersionV2 != 2 {
		t.Fatalf("SchemaVersionV2 drifted: want 2, got %d", SchemaVersionV2)
	}
}

func TestWorkMaxTypeName_Values(t *testing.T) {
	// The Desktop discriminated union reads these strings to route
	// records into elements/shots/assets/sessions. A rename on the Go
	// side would ship records the UI doesn't know how to classify.
	cases := map[WorkMaxTypeName]string{
		WorkMaxTypeElement: "element",
		WorkMaxTypeShot:    "shot",
		WorkMaxTypeAsset:   "asset",
		WorkMaxTypeSession: "session",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("WorkMaxTypeName drifted: want %q, got %q", want, string(got))
		}
	}
}

func TestTimelineTrackKind_Values(t *testing.T) {
	// Track kinds are how Desktop picks an icon / renderer for a
	// horizontal lane. They are stored in the `kind` JSON field of
	// every TimelineTrack — renames here would silently hide tracks.
	cases := map[TimelineTrackKind]string{
		TimelineTrackVideo:    "video",
		TimelineTrackAudio:    "audio",
		TimelineTrackSubtitle: "subtitle",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("TimelineTrackKind drifted: want %q, got %q", want, string(got))
		}
	}
}

func TestCanvasViewMode_Values(t *testing.T) {
	// View modes are persisted in `viewMode`. The UI's switch
	// statement falls through to 'freeform' on unknown strings, which
	// means a typo here would silently downgrade every saved Shot-Board
	// project to Freeform on reload.
	cases := map[CanvasViewMode]string{
		ViewModeFreeform:  "freeform",
		ViewModeShotBoard: "shot-board",
		ViewModeTimeline:  "timeline",
		ViewModeWorkflow:  "workflow",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("CanvasViewMode drifted: want %q, got %q", want, string(got))
		}
	}
}

func TestAssetScope_TypeAliases(t *testing.T) {
	// AssetScope* are re-exported from model so canvas callers avoid
	// importing model directly (which would cycle). Pin that the alias
	// still resolves to the same underlying value — a future removal
	// of the re-export would be caught here.
	if AssetScopeElement != model.AssetScopeElement {
		t.Errorf("AssetScopeElement alias drifted")
	}
	if AssetScopeShot != model.AssetScopeShot {
		t.Errorf("AssetScopeShot alias drifted")
	}
	if AssetScopeProject != model.AssetScopeProject {
		t.Errorf("AssetScopeProject alias drifted")
	}
}

func TestWorkMaxBaseRecord_JSONFieldCasing(t *testing.T) {
	// Wire contract: Desktop reads `typeName`, `versionNonce` in
	// camelCase. Go's default marshaller would capitalise them if the
	// tags went missing. This round-trip test pins every field name.
	record := WorkMaxBaseRecord{
		ID:           "rec-1",
		TypeName:     WorkMaxTypeShot,
		Version:      3,
		VersionNonce: 1_700_000_000_000,
	}
	out, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var asMap map[string]interface{}
	if err := json.Unmarshal(out, &asMap); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	for _, key := range []string{"id", "typeName", "version", "versionNonce"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("missing JSON key %q; got keys: %v", key, keysOf(asMap))
		}
	}
}

func TestShotLockState_JSONFieldCasing(t *testing.T) {
	// ShotLockState is written into w_canvas_version.document under
	// shots[].lock when a user starts editing a Shot in M4-W12. The
	// per-shot lock field is what Desktop reads to render the
	// "locked by" badge — renames here break the collab MVP.
	lock := ShotLockState{UserID: 42, JobID: "job-xyz", AcquiredAt: 1_700_000_000_000}
	out, _ := json.Marshal(lock)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	for _, key := range []string{"userId", "jobId", "acquiredAt"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("ShotLockState missing JSON key %q; got: %v", key, keysOf(asMap))
		}
	}
}

func TestShotLink_OmitemptyBehaviour(t *testing.T) {
	// Optional timeline fields should drop out of the payload when
	// unset. Without `omitempty` Desktop would see `null`s for
	// every Shot that hasn't been placed on a timeline yet.
	link := ShotLink{
		WorkMaxBaseRecord: WorkMaxBaseRecord{ID: "s-1", TypeName: WorkMaxTypeShot, Version: 1},
		ShotID:            9,
		LocalCardID:       "card-local-1",
		OrderIndex:        0,
	}
	out, _ := json.Marshal(link)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	for _, omitted := range []string{"timelineStart", "timelineDuration", "sessions", "lock"} {
		if _, ok := asMap[omitted]; ok {
			t.Errorf("ShotLink should omit %q when unset; got: %v", omitted, keysOf(asMap))
		}
	}
	// But shotId and orderIndex are required, so zero values must
	// still be emitted.
	if _, ok := asMap["shotId"]; !ok {
		t.Errorf("ShotLink must always emit shotId, got: %v", keysOf(asMap))
	}
}

func TestCanvasDocumentV2_TopLevelFieldCasing(t *testing.T) {
	// The final top-level payload is what goes into
	// w_canvas_version.document. A drift here flips the entire
	// stored shape for every project saved after the change.
	doc := CanvasDocumentV2{
		SchemaVersion: SchemaVersionV2,
		Viewport:      Viewport{X: 0, Y: 0, Scale: 1},
		ViewMode:      ViewModeFreeform,
	}
	out, _ := json.Marshal(doc)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	for _, key := range []string{"schemaVersion", "elements", "viewport", "viewMode"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("CanvasDocumentV2 missing required JSON key %q; got: %v", key, keysOf(asMap))
		}
	}
	// Optional collections should be omitted when nil/empty.
	for _, omitted := range []string{"shots", "timeline", "projectAssetBindings", "templateRef"} {
		if _, ok := asMap[omitted]; ok {
			t.Errorf("CanvasDocumentV2 must omit %q when unset; got: %v", omitted, keysOf(asMap))
		}
	}
}

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

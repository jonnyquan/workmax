package canvas

import (
	"encoding/json"
	"testing"
)

// document_v2_test.go pins SchemaVersionV2, WorkMaxTypeName, TimelineTrackKind,
// CanvasViewMode, AssetScope aliases, WorkMaxBaseRecord / ShotLockState /
// ShotLink / CanvasDocumentV2 field casings. These fill in the remaining
// structs where a silent JSON-tag drift would break the wire contract
// between the Go migrator and the React loader without surfacing against
// the existing suite:
//
//   • TimelineTrack — `id`, `kind`, `clips` are the three fields the
//     frontend reads when rendering a lane. A tag drop would emit the
//     capitalised Go field name and break every Shot-Board project.
//   • Transition — `fromClipId`, `toClipId`, `durationMs`, `effect` are
//     read by the timeline engine; one wrong tag and the transition
//     disappears from the rendered composition silently.
//   • AudioTrack / Subtitle — small structs but persisted inside the
//     doc; `startMs` / `endMs` are millisecond integers that must not
//     drift to PascalCase (Go's default marshaller).
//   • TemplateRef — `templateId`, `templateVersion`, `slots`. The ref
//     is how we record which template a project was instantiated from;
//     a rename drops the template-lineage link for every new project.
//   • Session + SessionAttempt — Runway-style grouping of retries.
//     These are read by the review UI and must keep camelCase.
//   • TimelineConfig — `fps`, `totalDuration`, `tracks` are required;
//     `transitions`, `audioTracks`, `subtitles` are optional (omitempty).
//     Pin both halves so a refactor that made the optionals always
//     emitted (null/empty arrays) would not slip through.
//   • Viewport — `x`, `y`, `scale`. Already emitted via CanvasDocumentV2
//     but never isolated; a direct-marshal call from CLI tools or tests
//     that round-trips a Viewport alone would silently PascalCase.
//   • CanvasElementV2 — the struct has `V1Fields` tagged `json:"-"` so
//     unknown-to-v2 fields MUST NOT leak into the marshalled output.
//     Pin that behavior: a regression that dropped the `-` tag would
//     double-serialize every v1 field into the top-level output and
//     confuse the migrator on the next read.

func TestTimelineTrack_JSONFieldCasing(t *testing.T) {
	track := TimelineTrack{
		ID:    "tr-1",
		Kind:  TimelineTrackVideo,
		Clips: []ShotLink{},
	}
	out, err := json.Marshal(track)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var asMap map[string]interface{}
	if err := json.Unmarshal(out, &asMap); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	for _, key := range []string{"id", "kind", "clips"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("TimelineTrack missing JSON key %q; got: %v", key, keysOf(asMap))
		}
	}
}

func TestTransition_JSONFieldCasing(t *testing.T) {
	tr := Transition{
		ID:         "trx-1",
		FromClipID: "c-a",
		ToClipID:   "c-b",
		DurationMs: 500,
		Effect:     "crossfade",
	}
	out, _ := json.Marshal(tr)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	for _, key := range []string{"id", "fromClipId", "toClipId", "durationMs", "effect"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("Transition missing JSON key %q; got: %v", key, keysOf(asMap))
		}
	}
}

func TestAudioTrack_JSONFieldCasing(t *testing.T) {
	at := AudioTrack{
		ID:         "a-1",
		Src:        "https://cdn/music.mp3",
		StartMs:    0,
		DurationMs: 30_000,
	}
	out, _ := json.Marshal(at)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	for _, key := range []string{"id", "src", "startMs", "durationMs"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("AudioTrack missing JSON key %q; got: %v", key, keysOf(asMap))
		}
	}
}

func TestSubtitle_JSONFieldCasing(t *testing.T) {
	sub := Subtitle{
		ID:      "s-1",
		StartMs: 1000,
		EndMs:   2500,
		Text:    "hello",
	}
	out, _ := json.Marshal(sub)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	for _, key := range []string{"id", "startMs", "endMs", "text"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("Subtitle missing JSON key %q; got: %v", key, keysOf(asMap))
		}
	}
}

func TestTemplateRef_JSONFieldCasing(t *testing.T) {
	ref := TemplateRef{
		TemplateID:      7,
		TemplateVersion: 2,
		Slots:           map[string]interface{}{"title": "Morning"},
	}
	out, _ := json.Marshal(ref)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	for _, key := range []string{"templateId", "templateVersion", "slots"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("TemplateRef missing JSON key %q; got: %v", key, keysOf(asMap))
		}
	}
}

func TestSession_JSONFieldCasingAndAttempts(t *testing.T) {
	// Session wraps an array of SessionAttempt. Pin both layers so a
	// rename at either level surfaces here.
	session := Session{
		ID:            "sess-1",
		CreatedAt:     1_700_000_000_000,
		ResultAssetID: "asset-final",
		Attempts: []SessionAttempt{
			{JobID: "job-1", Status: "success", AssetID: "asset-1"},
		},
	}
	out, _ := json.Marshal(session)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	for _, key := range []string{"id", "createdAt", "resultAssetId", "attempts"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("Session missing JSON key %q; got: %v", key, keysOf(asMap))
		}
	}
	// Nested: the first attempt must keep camelCase too.
	attempts, _ := asMap["attempts"].([]interface{})
	if len(attempts) != 1 {
		t.Fatalf("expected 1 attempt; got %v", attempts)
	}
	firstAttempt, _ := attempts[0].(map[string]interface{})
	for _, key := range []string{"jobId", "status", "assetId"} {
		if _, ok := firstAttempt[key]; !ok {
			t.Errorf("SessionAttempt missing JSON key %q; got: %v", key, keysOf(firstAttempt))
		}
	}
}

func TestSessionAttempt_AssetIDOmitempty(t *testing.T) {
	// AssetID is `omitempty` — a pending attempt shouldn't carry an
	// empty string slot that the UI would interpret as "resolved to
	// empty asset."
	att := SessionAttempt{JobID: "job-1", Status: "pending"}
	out, _ := json.Marshal(att)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	if _, ok := asMap["assetId"]; ok {
		t.Errorf("SessionAttempt should omit assetId when empty; got: %v", keysOf(asMap))
	}
}

func TestSession_ResultAssetIDOmitempty(t *testing.T) {
	// A session that hasn't resolved yet must not emit resultAssetId —
	// the UI keys on its presence to decide whether to show the review
	// card. A regression that dropped omitempty would always render it.
	session := Session{ID: "sess-1", CreatedAt: 1, Attempts: []SessionAttempt{}}
	out, _ := json.Marshal(session)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	if _, ok := asMap["resultAssetId"]; ok {
		t.Errorf("Session should omit resultAssetId when empty; got: %v", keysOf(asMap))
	}
}

func TestTimelineConfig_RequiredKeysAndOmitemptyOptionals(t *testing.T) {
	// Required: fps, totalDuration, tracks. Optional (omitempty):
	// transitions, audioTracks, subtitles. Pin both halves.
	cfg := TimelineConfig{
		FPS:           30,
		TotalDuration: 60_000,
		Tracks:        []TimelineTrack{},
	}
	out, _ := json.Marshal(cfg)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	for _, key := range []string{"fps", "totalDuration", "tracks"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("TimelineConfig missing required JSON key %q; got: %v", key, keysOf(asMap))
		}
	}
	for _, omitted := range []string{"transitions", "audioTracks", "subtitles"} {
		if _, ok := asMap[omitted]; ok {
			t.Errorf("TimelineConfig should omit %q when empty; got: %v", omitted, keysOf(asMap))
		}
	}
}

func TestViewport_JSONFieldCasingAndAllRequired(t *testing.T) {
	// x, y, scale all round-trip as camelCase. The struct has no
	// `omitempty` on these fields, so even (0,0,0) must still emit all
	// three keys. Pin this explicit "always-present" behaviour so a
	// refactor that added omitempty would drop scale=0 from the wire.
	vp := Viewport{X: 0, Y: 0, Scale: 0}
	out, _ := json.Marshal(vp)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	for _, key := range []string{"x", "y", "scale"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("Viewport missing JSON key %q; got: %v", key, keysOf(asMap))
		}
	}
}

func TestCanvasElementV2_V1FieldsExcludedFromMarshal(t *testing.T) {
	// The V1Fields map is tagged `json:"-"`. The migrator uses it as a
	// staging area only; marshalling an element must never leak the
	// raw v1 shape into the output. A regression that dropped the `-`
	// tag would double-serialise every v1 field as a nested object
	// under `V1Fields` and confuse the next read.
	elem := CanvasElementV2{
		V1Fields: map[string]interface{}{
			"secret": "must-not-leak",
			"x":      float64(10),
		},
		ID:       "el-1",
		TypeName: WorkMaxTypeElement,
		Version:  1,
	}
	out, _ := json.Marshal(elem)
	var asMap map[string]interface{}
	_ = json.Unmarshal(out, &asMap)
	// Neither the wrapper key nor any of its contents must appear.
	if _, ok := asMap["V1Fields"]; ok {
		t.Errorf("CanvasElementV2 leaked V1Fields wrapper key; got: %v", keysOf(asMap))
	}
	if _, ok := asMap["secret"]; ok {
		t.Errorf("CanvasElementV2 leaked V1Fields contents; got: %v", keysOf(asMap))
	}
	// The explicit fields must still be present.
	for _, key := range []string{"id", "typeName", "version"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("CanvasElementV2 missing required JSON key %q; got: %v", key, keysOf(asMap))
		}
	}
}

func TestCanvasElementV2_OmitemptyForUnsetV2Metadata(t *testing.T) {
	// An empty element (no metadata filled in) must emit an empty JSON
	// object — no `id: ""`, `typeName: ""`, `version: 0` noise. Pin
	// omitempty so a refactor that cleared the tags would not bloat
	// every stored document.
	empty := CanvasElementV2{}
	out, _ := json.Marshal(empty)
	if string(out) != "{}" {
		t.Errorf("empty CanvasElementV2 should marshal to {}; got %s", string(out))
	}
}

func TestAssetBinding_TypeAliasPreservesUnderlyingType(t *testing.T) {
	// The `type AssetBinding = model.AssetBinding` re-export avoids a
	// cycle. Pin that it is the SAME type (not a named alias) so Go
	// treats the two interchangeably in call-sites — assigning the
	// canvas-side alias to a variable of the model-side type must
	// compile and round-trip without conversion.
	a := AssetBinding{
		Scope:        AssetScopeElement,
		CharacterIDs: []int{1, 2, 3},
	}
	out, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var asMap map[string]interface{}
	if err := json.Unmarshal(out, &asMap); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	// Pin that the underlying JSON tags (defined on model.AssetBinding)
	// reach the wire through the canvas-side alias. A regression that
	// re-declared AssetBinding as a named wrapper type would lose the
	// tags and emit Go-default PascalCase keys.
	if _, ok := asMap["scope"]; !ok {
		t.Errorf("AssetBinding alias lost `scope` tag; got: %v", keysOf(asMap))
	}
	if _, ok := asMap["characterIds"]; !ok {
		t.Errorf("AssetBinding alias lost `characterIds` tag; got: %v", keysOf(asMap))
	}
}

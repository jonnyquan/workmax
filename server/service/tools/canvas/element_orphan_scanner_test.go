// Tests for the C-06 Stage 1 element-orphan scanner. The pure
// reference-extraction logic is unit-tested here; the DB-coupled
// ListOrphanElementAssetsByProject relies on those guarantees and
// gets its own _db_test.go in Stage 2 (when an admin sweep lands).

package canvas

import (
	"encoding/json"
	"testing"

	"server/model"
)

func TestExtractAssetReferences_NilAndEmpty(t *testing.T) {
	if got := extractAssetReferences(nil); len(got.GlobalAssetIDs) != 0 || len(got.URLs) != 0 {
		t.Fatalf("nil doc must produce empty set, got ids=%v urls=%v", got.GlobalAssetIDs, got.URLs)
	}
	if got := extractAssetReferences(model.JSONMap{}); len(got.GlobalAssetIDs) != 0 || len(got.URLs) != 0 {
		t.Fatalf("empty doc must produce empty set, got ids=%v urls=%v", got.GlobalAssetIDs, got.URLs)
	}
}

func TestExtractAssetReferences_PrimaryGlobalAssetIDChannel(t *testing.T) {
	// element.metadata.globalAssetId — the path written by
	// canvas_asset_service.go:1063. Most common channel today.
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{
				"id": "el-1",
				"metadata": map[string]interface{}{
					"globalAssetId": float64(42), // JSON unmarshal default
				},
			},
		},
	}
	refs := extractAssetReferences(doc)
	if _, ok := refs.GlobalAssetIDs[42]; !ok {
		t.Fatalf("expected id 42 in refs, got %v", refs.GlobalAssetIDs)
	}
}

func TestExtractAssetReferences_LegacyAssetIDAndResultAssetID(t *testing.T) {
	// document_v2.SessionAttempt.AssetID + Session.ResultAssetID
	// — both are strings in the v2 schema, but legacy v1 carriers
	// stored them as numbers. The walker must accept both.
	doc := model.JSONMap{
		"shots": []interface{}{
			map[string]interface{}{
				"sessions": []interface{}{
					map[string]interface{}{
						"resultAssetId": "101",
						"attempts": []interface{}{
							map[string]interface{}{"assetId": float64(7)},
							map[string]interface{}{"assetId": "8"},
						},
					},
				},
			},
		},
	}
	refs := extractAssetReferences(doc)
	for _, want := range []uint{7, 8, 101} {
		if _, ok := refs.GlobalAssetIDs[want]; !ok {
			t.Fatalf("expected id %d, got %v", want, refs.GlobalAssetIDs)
		}
	}
}

func TestExtractAssetReferences_URLChannel(t *testing.T) {
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{
				"src": "/uploads/canvas/uid/9/abc/img.png",
				"thumbUrl": "/uploads/canvas/uid/9/abc/img.thumb.png",
			},
			map[string]interface{}{
				"posterUrl": "https://cdn.example/x.jpg",
				"url":       "https://cdn.example/y.mp4",
			},
		},
	}
	refs := extractAssetReferences(doc)
	want := []string{
		"/uploads/canvas/uid/9/abc/img.png",
		"/uploads/canvas/uid/9/abc/img.thumb.png",
		"https://cdn.example/x.jpg",
		"https://cdn.example/y.mp4",
	}
	for _, u := range want {
		if _, ok := refs.URLs[u]; !ok {
			t.Fatalf("expected URL %q in refs, got %v", u, refs.URLs)
		}
	}
}

func TestExtractAssetReferences_IgnoresNonAssetURLs(t *testing.T) {
	// Keys outside the URL whitelist (href, link, page) must not
	// pollute the URL set — those don't point to canvas assets.
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{
				"href": "https://docs.example/page",
				"link": "/internal/page",
			},
		},
	}
	refs := extractAssetReferences(doc)
	if len(refs.URLs) != 0 {
		t.Fatalf("non-asset URL keys must be ignored, got %v", refs.URLs)
	}
}

func TestExtractAssetReferences_CoerceAcceptsJSONNumber(t *testing.T) {
	// json.Decoder with UseNumber() leaves numbers as json.Number;
	// some upstream paths use that. The walker must accept it.
	var doc model.JSONMap
	dec := json.NewDecoder(strReader(`{"elements":[{"metadata":{"globalAssetId":55}}]}`))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	refs := extractAssetReferences(doc)
	if _, ok := refs.GlobalAssetIDs[55]; !ok {
		t.Fatalf("expected id 55 (via json.Number), got %v", refs.GlobalAssetIDs)
	}
}

func TestExtractAssetReferences_RejectsBogusIDs(t *testing.T) {
	// Conservatism on the ID-coerce path: zero, negative, fractional,
	// and non-numeric strings must NOT enter the id set. (A live
	// asset incorrectly admitted as "id 0" would mask a real id 0
	// match — but more importantly, the orphan check shouldn't be
	// fooled into thinking everything matches.)
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{"assetId": float64(0)},
			map[string]interface{}{"assetId": float64(-3)},
			map[string]interface{}{"assetId": float64(1.5)},
			map[string]interface{}{"assetId": "not-a-number"},
			map[string]interface{}{"assetId": nil},
		},
	}
	refs := extractAssetReferences(doc)
	if len(refs.GlobalAssetIDs) != 0 {
		t.Fatalf("bogus ID values must be rejected, got %v", refs.GlobalAssetIDs)
	}
}

func TestExtractAssetReferences_WalksDeeplyNested(t *testing.T) {
	// V1Fields preserves the v1 element shape verbatim — references
	// can live under any depth. The walker recurses through maps and
	// arrays alike.
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{
				"id":   "el-1",
				"data": map[string]interface{}{
					"layers": []interface{}{
						map[string]interface{}{
							"src": "/deep/img.png",
							"meta": map[string]interface{}{
								"assetId": float64(99),
							},
						},
					},
				},
			},
		},
	}
	refs := extractAssetReferences(doc)
	if _, ok := refs.GlobalAssetIDs[99]; !ok {
		t.Fatalf("expected id 99 at depth, got %v", refs.GlobalAssetIDs)
	}
	if _, ok := refs.URLs["/deep/img.png"]; !ok {
		t.Fatalf("expected nested src URL, got %v", refs.URLs)
	}
}

func TestExtractAssetReferences_CaseInsensitiveKeys(t *testing.T) {
	// JSON tag casing in the codebase is camelCase, but the walker
	// matches case-insensitively so a future serializer change (e.g.
	// PascalCase from a Go marshal) doesn't silently miss references.
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{"GlobalAssetId": float64(11)},
			map[string]interface{}{"AssetID": float64(12)},
			map[string]interface{}{"URL": "/a.png"},
		},
	}
	refs := extractAssetReferences(doc)
	for _, want := range []uint{11, 12} {
		if _, ok := refs.GlobalAssetIDs[want]; !ok {
			t.Fatalf("expected id %d under PascalCase key, got %v", want, refs.GlobalAssetIDs)
		}
	}
	if _, ok := refs.URLs["/a.png"]; !ok {
		t.Fatalf("expected URL under uppercase URL key, got %v", refs.URLs)
	}
}

func TestComputeDistribution_EmptyAndSingle(t *testing.T) {
	if d := computeDistribution(nil); (d != OrphanDistributionStats{}) {
		t.Fatalf("nil input must produce zero distribution, got %+v", d)
	}
	if d := computeDistribution([]int{}); (d != OrphanDistributionStats{}) {
		t.Fatalf("empty input must produce zero distribution, got %+v", d)
	}
	if d := computeDistribution([]int{42}); d.Min != 42 || d.Max != 42 || d.Median != 42 || d.P99 != 42 {
		t.Fatalf("single-element input must collapse to that value, got %+v", d)
	}
}

func TestComputeDistribution_TypicalSkew(t *testing.T) {
	// 100 projects: 90 have 0 orphans, 9 have 1-5, 1 has 200.
	// Median should be 0 (mass at zero), p99 should be 200.
	counts := make([]int, 0, 100)
	for i := 0; i < 90; i++ {
		counts = append(counts, 0)
	}
	for i := 1; i <= 9; i++ {
		counts = append(counts, i)
	}
	counts = append(counts, 200)
	d := computeDistribution(counts)
	if d.Min != 0 {
		t.Fatalf("min: want 0, got %d", d.Min)
	}
	if d.Max != 200 {
		t.Fatalf("max: want 200, got %d", d.Max)
	}
	if d.Median != 0 {
		t.Fatalf("median: skewed-to-zero input should give 0, got %d", d.Median)
	}
	if d.P99 != 200 {
		t.Fatalf("p99: 100-element input top value should land at p99, got %d", d.P99)
	}
}

func TestInsertIntoTopK_OrdersDescending(t *testing.T) {
	var top []ProjectOrphanCount
	for _, in := range []ProjectOrphanCount{
		{ProjectID: 1, OrphanCount: 5},
		{ProjectID: 2, OrphanCount: 200},
		{ProjectID: 3, OrphanCount: 1},
		{ProjectID: 4, OrphanCount: 50},
		{ProjectID: 5, OrphanCount: 10},
	} {
		top = insertIntoTopK(top, in, 3)
	}
	if len(top) != 3 {
		t.Fatalf("expected len 3, got %d", len(top))
	}
	if top[0].OrphanCount != 200 || top[1].OrphanCount != 50 || top[2].OrphanCount != 10 {
		t.Fatalf("expected [200,50,10], got %v", []int{top[0].OrphanCount, top[1].OrphanCount, top[2].OrphanCount})
	}
}

func TestInsertIntoTopK_RejectsSmallerWhenFull(t *testing.T) {
	top := []ProjectOrphanCount{
		{ProjectID: 1, OrphanCount: 100},
		{ProjectID: 2, OrphanCount: 50},
	}
	// k=2, candidate smaller than min — must be rejected, no copy.
	got := insertIntoTopK(top, ProjectOrphanCount{ProjectID: 3, OrphanCount: 10}, 2)
	if len(got) != 2 || got[0].ProjectID != 1 || got[1].ProjectID != 2 {
		t.Fatalf("rejected candidate should not enter, got %+v", got)
	}
}

func TestInsertIntoTopK_TiesPreserveInsertionOrder(t *testing.T) {
	// Stable behaviour matters for reproducible reports.
	var top []ProjectOrphanCount
	top = insertIntoTopK(top, ProjectOrphanCount{ProjectID: 1, OrphanCount: 10}, 3)
	top = insertIntoTopK(top, ProjectOrphanCount{ProjectID: 2, OrphanCount: 10}, 3)
	top = insertIntoTopK(top, ProjectOrphanCount{ProjectID: 3, OrphanCount: 10}, 3)
	if len(top) != 3 {
		t.Fatalf("expected len 3, got %d", len(top))
	}
	if top[0].ProjectID != 1 || top[1].ProjectID != 2 || top[2].ProjectID != 3 {
		t.Fatalf("ties should preserve insertion order, got %v",
			[]uint{top[0].ProjectID, top[1].ProjectID, top[2].ProjectID})
	}
}

// strReader is a tiny helper to avoid pulling in strings.NewReader's
// import noise in a test-only context.
type strReader string

func (s strReader) Read(p []byte) (int, error) {
	n := copy(p, s)
	if n == 0 {
		return 0, errEOF
	}
	return n, nil
}

var errEOF = errSentinel("EOF")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

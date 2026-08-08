// Pure-function tests for the C-09 restore service. The dangling-
// asset diff helper is the one bit of standalone logic worth pinning
// without DB — the rest of RestoreCanvasSnapshot is a transactional
// orchestration covered by canvas_version_restore_db_test.go.

package canvas

import (
	"testing"

	"server/globals"
	"server/model"
)

// asset is a tiny constructor that injects the embedded GraMODEL.Id —
// Go composite literals can't set an embedded field's column directly,
// so this keeps the test bodies readable.
func asset(id uint, url, thumb string) model.GlobalAsset {
	return model.GlobalAsset{
		GraMODEL: globals.GraMODEL{Id: id},
		URL:      url,
		ThumbURL: thumb,
	}
}

func TestDiffAssetReferences_NilRefsReturnsNil(t *testing.T) {
	if got := diffAssetReferences(nil, []model.GlobalAsset{asset(1, "", "")}); got != nil {
		t.Fatalf("nil refs should produce nil, got %v", got)
	}
}

func TestDiffAssetReferences_EmptyRefsReturnsNil(t *testing.T) {
	refs := newAssetReferenceSet()
	if got := diffAssetReferences(refs, []model.GlobalAsset{asset(1, "", "")}); got != nil {
		t.Fatalf("empty refs should produce nil, got %v", got)
	}
}

func TestDiffAssetReferences_AllLiveReturnsEmpty(t *testing.T) {
	refs := newAssetReferenceSet()
	refs.GlobalAssetIDs[1] = struct{}{}
	refs.GlobalAssetIDs[2] = struct{}{}
	refs.URLs["/uploads/a.png"] = struct{}{}
	active := []model.GlobalAsset{
		asset(1, "/uploads/x.png", ""),
		asset(2, "/uploads/a.png", ""),
	}
	if got := diffAssetReferences(refs, active); got != nil {
		t.Fatalf("all-resolved should produce nil, got %v", got)
	}
}

func TestDiffAssetReferences_MissingIDsAndURLsBothFlagged(t *testing.T) {
	refs := newAssetReferenceSet()
	refs.GlobalAssetIDs[1] = struct{}{}
	refs.GlobalAssetIDs[99] = struct{}{} // missing
	refs.URLs["/uploads/a.png"] = struct{}{}
	refs.URLs["/uploads/gone.png"] = struct{}{} // missing
	active := []model.GlobalAsset{
		asset(1, "/uploads/a.png", ""),
	}
	got := diffAssetReferences(refs, active)
	if len(got) != 2 {
		t.Fatalf("expected 2 dangling refs, got %d (%v)", len(got), got)
	}
	// Sort order: numeric ids first ascending, then URL-only refs.
	if got[0].GlobalAssetID != 99 {
		t.Errorf("[0] should be id=99, got %+v", got[0])
	}
	if got[1].URL != "/uploads/gone.png" {
		t.Errorf("[1] should be URL=/uploads/gone.png, got %+v", got[1])
	}
}

func TestDiffAssetReferences_ThumbURLAlsoCountsAsLive(t *testing.T) {
	// An asset's thumb_url should resolve a doc reference, not just
	// the main url field — matches the conservatism in
	// ListOrphanElementAssetsByProject.
	refs := newAssetReferenceSet()
	refs.URLs["/uploads/img.thumb.png"] = struct{}{}
	active := []model.GlobalAsset{
		asset(1, "/uploads/img.png", "/uploads/img.thumb.png"),
	}
	if got := diffAssetReferences(refs, active); got != nil {
		t.Fatalf("thumb URL should resolve, got %v", got)
	}
}

func TestDiffAssetReferences_DeterministicOrder(t *testing.T) {
	// Map iteration is non-deterministic in Go; the diff helper must
	// sort so test assertions are stable.
	refs := newAssetReferenceSet()
	refs.GlobalAssetIDs[7] = struct{}{}
	refs.GlobalAssetIDs[3] = struct{}{}
	refs.GlobalAssetIDs[15] = struct{}{}
	refs.URLs["/z.png"] = struct{}{}
	refs.URLs["/a.png"] = struct{}{}
	got := diffAssetReferences(refs, nil) // no active = all dangling
	if len(got) != 5 {
		t.Fatalf("expected 5 dangling, got %d", len(got))
	}
	wantOrder := []DanglingAssetRef{
		{GlobalAssetID: 3},
		{GlobalAssetID: 7},
		{GlobalAssetID: 15},
		{URL: "/a.png"},
		{URL: "/z.png"},
	}
	for i, want := range wantOrder {
		if got[i] != want {
			t.Errorf("[%d]: want %+v, got %+v", i, want, got[i])
		}
	}
}

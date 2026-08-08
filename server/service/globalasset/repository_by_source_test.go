package globalasset

import (
	"testing"

	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// repository_by_source_test.go — covers LoadBySourceForAccess +
// the public SourceKind enum mapping. Mirrors the IDOR posture of
// LoadForAccess: owner OR project-member can read; cross-tenant
// collapses to gorm.ErrRecordNotFound; unknown kinds short-circuit
// to the same sentinel without touching the DB.

func seedSourceAsset(t *testing.T, db *gorm.DB, uid int, projectID *uint, sourceTable string, sourceID uint, uuid string) model.GlobalAsset {
	t.Helper()
	asset := model.GlobalAsset{
		UID:           uid,
		ProjectID:     projectID,
		UUID:          uuid,
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   sourceTable,
		SourceID:      uint64(sourceID),
		SourceItemKey: uuid,
		URL:           "/uploads/" + uuid + ".png",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
	}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatalf("create %s asset: %v", sourceTable, err)
	}
	return asset
}

func TestLoadBySourceForAccess_ResolvesEachKind(t *testing.T) {
	// Four kinds × one owner-direct asset each. Resolution by
	// (kind, sourceID) must return the same row LoadForAccess
	// would return for the asset's global_asset_id.
	db := testutil.NewTestDB(t)
	const uid = 42

	cases := []struct {
		kind        SourceKind
		sourceTable string
		sourceID    uint
		uuid        string
	}{
		{SourceKindCanvasProjectFile, sourceTableCanvasProjectFile, 101, "src-canvas"},
		{SourceKindGenerationObject, sourceTableGenerationObject, 202, "src-genobj"},
		{SourceKindWorkAgentFile, sourceTableWorkAgentFile, 303, "src-workagent"},
		{SourceKindReferenceUpload, sourceTableReferenceUpload, 404, "src-reference"},
	}
	seeded := make(map[SourceKind]model.GlobalAsset, len(cases))
	for _, tc := range cases {
		seeded[tc.kind] = seedSourceAsset(t, db, uid, nil, tc.sourceTable, tc.sourceID, tc.uuid)
	}

	repo := NewRepository(db)
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			got, err := repo.LoadBySourceForAccess(uid, tc.kind, tc.sourceID)
			if err != nil {
				t.Fatalf("LoadBySourceForAccess(%s, %d): %v", tc.kind, tc.sourceID, err)
			}
			want := seeded[tc.kind]
			if got.Id != want.Id || got.SourceTable != tc.sourceTable {
				t.Errorf("resolved row mismatch: got id=%d table=%q, want id=%d table=%q",
					got.Id, got.SourceTable, want.Id, tc.sourceTable)
			}
		})
	}
}

func TestLoadBySourceForAccess_ProjectMemberAllowed(t *testing.T) {
	// Cross-uid project member must be able to resolve a
	// project-scoped asset by its tool-local id — mirrors
	// LoadForAccess's member-read policy.
	db := testutil.NewTestDB(t)
	pid := uint(11)
	owner := seedSourceAsset(t, db, 42, &pid, sourceTableWorkAgentFile, 555, "src-shared")
	_ = owner
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: pid,
		UID:       99,
		Role:      model.GlobalProjectRoleViewer,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}
	repo := NewRepository(db)

	if _, err := repo.LoadBySourceForAccess(99, SourceKindWorkAgentFile, 555); err != nil {
		t.Errorf("project member should resolve project-scoped asset; got %v", err)
	}
}

func TestLoadBySourceForAccess_CrossTenantDenied(t *testing.T) {
	// uid 42 owns the asset; uid 99 (no membership) gets
	// gorm.ErrRecordNotFound — same sentinel as
	// LoadForAccess's denial path so no oracle.
	db := testutil.NewTestDB(t)
	seedSourceAsset(t, db, 42, nil, sourceTableGenerationObject, 600, "src-private")
	repo := NewRepository(db)

	_, err := repo.LoadBySourceForAccess(99, SourceKindGenerationObject, 600)
	if err == nil {
		t.Fatal("expected cross-tenant resolve to fail")
	}
}

func TestLoadBySourceForAccess_UnknownKindReturnsSentinel(t *testing.T) {
	// Unknown kind never touches the DB — resolves to
	// gorm.ErrRecordNotFound at the resolver boundary. Same
	// sentinel as the cross-tenant path so the FE can't
	// distinguish "I sent a bad kind" from "you don't have
	// access to that asset".
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)

	_, err := repo.LoadBySourceForAccess(42, SourceKind("nonsense"), 1)
	if err == nil {
		t.Fatal("expected unknown kind to fail")
	}
}

func TestLoadBySourceForAccess_ZeroIDShortCircuits(t *testing.T) {
	// Defensive: sourceID=0 / uid=0 short-circuit before DB.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)

	if _, err := repo.LoadBySourceForAccess(42, SourceKindWorkAgentFile, 0); err == nil {
		t.Error("expected sourceID=0 to fail")
	}
	if _, err := repo.LoadBySourceForAccess(0, SourceKindWorkAgentFile, 1); err == nil {
		t.Error("expected uid=0 to fail")
	}
}

func TestLoadBySourceForAccess_DeletedAssetExcluded(t *testing.T) {
	// A soft-deleted (Status=Deleted) bridge row must not
	// resolve. Pins the same protection LoadForAccess has —
	// the access query gates on status <> Deleted.
	db := testutil.NewTestDB(t)
	asset := seedSourceAsset(t, db, 42, nil, sourceTableCanvasProjectFile, 777, "src-deleted")
	if err := db.Model(&model.GlobalAsset{}).Where("id = ?", asset.Id).
		Update("status", model.GlobalAssetStatusDeleted).Error; err != nil {
		t.Fatalf("soft-delete: %v", err)
	}
	repo := NewRepository(db)

	if _, err := repo.LoadBySourceForAccess(42, SourceKindCanvasProjectFile, 777); err == nil {
		t.Error("deleted asset should not resolve via source lookup")
	}
}

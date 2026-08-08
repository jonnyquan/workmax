package product

// repository_test.go — P1 #5 slice 1.
// Pins the IDOR + lifecycle + search contracts every consumer
// (asset_library descriptor, REST surface, lookup_asset tool)
// will rely on. Single in-memory SQLite per test via
// testutil.NewTestDB.

import (
	"errors"
	"testing"

	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

func seedProduct(t *testing.T, db *gorm.DB, override func(*model.Product)) *model.Product {
	t.Helper()
	p := &model.Product{
		UID:        42,
		Name:       "fixture",
		Slug:       "fixture",
		SKU:        "SKU-001",
		Category:   "shoes",
		Confirmed:  true,
		Status:     model.ProductStatusActive,
		Lang:       "en",
		SourceKind: model.ProductSourceManual,
	}
	if override != nil {
		override(p)
	}
	if err := db.Select("*").Create(p).Error; err != nil {
		t.Fatalf("seed product: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------

func TestCreate_RejectsZeroUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	err := repo.Create(&model.Product{Name: "x"})
	if err == nil {
		t.Fatal("expected error on zero uid")
	}
}

func TestCreate_RejectsEmptyName(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	err := repo.Create(&model.Product{UID: 42, Name: ""})
	if err == nil {
		t.Fatal("expected error on empty name")
	}
}

func TestCreate_DefaultsSourceKindAndLang(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := &model.Product{UID: 42, Name: "x"}
	if err := repo.Create(p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.SourceKind != model.ProductSourceManual {
		t.Errorf("SourceKind = %q, want %q", p.SourceKind, model.ProductSourceManual)
	}
	if p.Lang != "en" {
		t.Errorf("Lang = %q, want en", p.Lang)
	}
	if p.Status != model.ProductStatusActive {
		t.Errorf("Status = %d, want active(1)", p.Status)
	}
}

func TestCreate_DraftConfirmedFalseSurvivesGormZeroValueSkip(t *testing.T) {
	// Workagent draft extractions write Confirmed=false. GORM's
	// zero-value-skip would let the DDL DEFAULT 1 override that
	// without our explicit Select("*"). Pin the contract.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	p := &model.Product{UID: 42, Name: "draft", Confirmed: false}
	if err := repo.Create(p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var row model.Product
	if err := db.First(&row, p.Id).Error; err != nil {
		t.Fatal(err)
	}
	if row.Confirmed {
		t.Errorf("Confirmed = true on a draft create; DDL default overrode explicit false")
	}
}

// ---------------------------------------------------------------------
// LoadByIDForOwner — IDOR posture
// ---------------------------------------------------------------------

func TestLoadByIDForOwner_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	seeded := seedProduct(t, db, nil)
	got, err := repo.LoadByIDForOwner(seeded.Id, 42)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != "fixture" {
		t.Errorf("Name = %q", got.Name)
	}
}

func TestLoadByIDForOwner_CrossTenantReturnsNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	seeded := seedProduct(t, db, func(p *model.Product) { p.UID = 100 })

	_, err := repo.LoadByIDForOwner(seeded.Id, 42)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("cross-tenant: errors.Is(err, ErrRecordNotFound) = false; got %v", err)
	}
}

func TestLoadByIDForOwner_RefusesZeroUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	seeded := seedProduct(t, db, nil)
	if _, err := repo.LoadByIDForOwner(seeded.Id, 0); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("uid=0 should return ErrRecordNotFound, got %v", err)
	}
}

func TestLoadByIDForOwner_SoftDeletedReturnsNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	seeded := seedProduct(t, db, nil)
	if err := repo.SoftDelete(seeded.Id, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadByIDForOwner(seeded.Id, 42); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("soft-deleted should not surface, got %v", err)
	}
}

// ---------------------------------------------------------------------
// FindLatestActive / FindLatest
// ---------------------------------------------------------------------

func TestFindLatestActiveForOwner_ExcludesDrafts(t *testing.T) {
	// Confirmed=false rows must NOT surface as active.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	_ = seedProduct(t, db, func(p *model.Product) {
		p.Confirmed = false
		p.Slug = "draft"
	})
	if _, err := repo.FindLatestActiveForOwner(42); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("draft should not surface; got %v", err)
	}
}

func TestFindLatestActiveForOwner_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	_ = seedProduct(t, db, nil)

	got, err := repo.FindLatestActiveForOwner(42)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "fixture" {
		t.Errorf("Name = %q", got.Name)
	}
}

func TestFindLatestForOwner_IncludesDrafts(t *testing.T) {
	// Watermark path: drafts are valid candidates here.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	_ = seedProduct(t, db, func(p *model.Product) {
		p.Confirmed = false
		p.Slug = "draft"
	})
	got, err := repo.FindLatestForOwner(42)
	if err != nil {
		t.Fatal(err)
	}
	if got.Confirmed {
		t.Errorf("expected the draft row; got confirmed")
	}
}

// ---------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------

func TestSearchByOwner_MatchesNameSlugAndSKU(t *testing.T) {
	// Product search adds SKU vs Brand's name+slug pair. Pin all
	// three columns hit the LIKE.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	_ = seedProduct(t, db, func(p *model.Product) {
		p.Name = "Cyber Runner"
		p.Slug = "cyber-runner"
		p.SKU = "CR-2042"
	})

	cases := []string{"cyber", "runner", "CR-20"}
	for _, q := range cases {
		rows, err := repo.SearchByOwner(42, q, 10)
		if err != nil {
			t.Errorf("Search %q: %v", q, err)
			continue
		}
		if len(rows) != 1 {
			t.Errorf("Search %q: got %d rows, want 1", q, len(rows))
		}
	}
}

func TestSearchByOwner_EmptyQueryReturnsNil(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	_ = seedProduct(t, db, nil)
	rows, err := repo.SearchByOwner(42, "  ", 10)
	if err != nil || rows != nil {
		t.Errorf("empty query should be (nil, nil); got (%v, %v)", rows, err)
	}
}

func TestSearchByOwner_ZeroUIDReturnsNil(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	rows, err := repo.SearchByOwner(0, "anything", 10)
	if err != nil || rows != nil {
		t.Errorf("uid=0 should be (nil, nil); got (%v, %v)", rows, err)
	}
}

// ---------------------------------------------------------------------
// MarkConfirmed / SoftDelete / Restore
// ---------------------------------------------------------------------

func TestMarkConfirmed_FlipsDraftToConfirmed(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	seeded := seedProduct(t, db, func(p *model.Product) { p.Confirmed = false })
	if err := repo.MarkConfirmed(seeded.Id, 42); err != nil {
		t.Fatal(err)
	}
	var row model.Product
	db.First(&row, seeded.Id)
	if !row.Confirmed {
		t.Errorf("Confirmed = false after MarkConfirmed")
	}
	if row.ConfirmedAt == nil {
		t.Errorf("ConfirmedAt should be stamped")
	}
}

func TestMarkConfirmed_AlreadyConfirmedReturnsNotFound(t *testing.T) {
	// Idempotency contract: re-confirming returns ErrRecordNotFound
	// so the caller's LoadByID-after disambiguates.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	seeded := seedProduct(t, db, nil) // already Confirmed=true
	err := repo.MarkConfirmed(seeded.Id, 42)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("already-confirmed re-confirm: got %v, want ErrRecordNotFound", err)
	}
}

func TestSoftDelete_StampsDeletedAt(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	seeded := seedProduct(t, db, nil)
	if err := repo.SoftDelete(seeded.Id, 42); err != nil {
		t.Fatal(err)
	}
	// Unscoped to inspect the soft-deleted row.
	var row model.Product
	if err := db.Unscoped().First(&row, seeded.Id).Error; err != nil {
		t.Fatal(err)
	}
	if row.DeletedAt == nil {
		t.Errorf("DeletedAt should be stamped")
	}
}

func TestRestore_ClearsDeletedAtAndResetsConfirmed(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	seeded := seedProduct(t, db, nil) // Confirmed=true
	if err := repo.SoftDelete(seeded.Id, 42); err != nil {
		t.Fatal(err)
	}
	if err := repo.Restore(seeded.Id, 42); err != nil {
		t.Fatal(err)
	}
	var row model.Product
	if err := db.First(&row, seeded.Id).Error; err != nil {
		t.Fatal(err)
	}
	if row.DeletedAt != nil {
		t.Errorf("DeletedAt should be cleared")
	}
	if row.Confirmed {
		t.Errorf("Confirmed should reset to false after Restore (re-vocalize required)")
	}
}

func TestSoftDelete_CrossTenantReturnsNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	seeded := seedProduct(t, db, func(p *model.Product) { p.UID = 100 })
	err := repo.SoftDelete(seeded.Id, 42)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("cross-tenant: got %v, want ErrRecordNotFound", err)
	}
}

// ---------------------------------------------------------------------
// List + project scoping
// ---------------------------------------------------------------------

func TestListByOwnerProject_ProjectIDZeroFallsThroughToUidOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	_ = seedProduct(t, db, func(p *model.Product) { p.Slug = "p1" })
	_ = seedProduct(t, db, func(p *model.Product) { p.Slug = "p2" })

	rows, err := repo.ListByOwnerProject(42, 0, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("projectID=0 should fall through to uid-only; got %d rows", len(rows))
	}
}

func TestListForOwner_ExcludesSoftDeleted(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	s1 := seedProduct(t, db, func(p *model.Product) { p.Slug = "alive" })
	_ = seedProduct(t, db, func(p *model.Product) { p.Slug = "to-be-deleted" })
	rows1, _ := repo.ListForOwner(42, 100, 0)
	if len(rows1) != 2 {
		t.Fatalf("before delete: %d rows, want 2", len(rows1))
	}
	_ = s1
	// Find the to-be-deleted row's id and delete it
	var row model.Product
	db.Where("slug = ?", "to-be-deleted").First(&row)
	if err := repo.SoftDelete(row.Id, 42); err != nil {
		t.Fatal(err)
	}
	rows2, _ := repo.ListForOwner(42, 100, 0)
	if len(rows2) != 1 {
		t.Errorf("after delete: %d rows, want 1", len(rows2))
	}
}

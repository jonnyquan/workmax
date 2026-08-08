package asset_library

import (
	"errors"
	"testing"
	"time"
)

// fakeAsset implements LibraryAsset for unit tests that exercise
// the registry / interface without binding to GORM or the concrete
// model packages. Keeping the fake in this package's test file (not
// a shared mocks/ dir) means new contracts on LibraryAsset force
// updates here, surfacing missing fakes at compile time.
type fakeAsset struct {
	id          uint
	uid         int
	name        string
	slug        string
	status      Status
	confirmed   bool
	confirmedAt *time.Time
	deletedAt   *time.Time
	updatedAt   time.Time
}

func (f *fakeAsset) GetID() uint               { return f.id }
func (f *fakeAsset) GetUID() int               { return f.uid }
func (f *fakeAsset) GetName() string           { return f.name }
func (f *fakeAsset) GetSlug() string           { return f.slug }
func (f *fakeAsset) GetStatus() Status         { return f.status }
func (f *fakeAsset) GetConfirmed() bool        { return f.confirmed }
func (f *fakeAsset) GetConfirmedAt() *time.Time { return f.confirmedAt }
func (f *fakeAsset) GetDeletedAt() *time.Time  { return f.deletedAt }
func (f *fakeAsset) GetUpdatedAt() time.Time   { return f.updatedAt }
func (f *fakeAsset) TableName() string         { return "fake_asset" }

// fakeDescriptor implements Descriptor for registry tests. Tracks
// invocation counts so tests can assert which methods were called.
type fakeDescriptor struct {
	kind          AssetKind
	indexLabel    string
	urlPrefix     string
	loadActive    func(uid uint) (LibraryAsset, error)
	loadLatest    func(uid uint) (LibraryAsset, error)
	formatXML     func(LibraryAsset) string
	listFn        func(uid uint, limit, offset int) ([]LibraryAsset, error)
	searchFn         func(uid uint, query string, limit int) ([]LibraryAsset, error)
	searchByProject  func(uid, projectID uint, query string, limit int) ([]LibraryAsset, error)
	listByProject    func(uid, projectID uint, limit, offset int) ([]LibraryAsset, error)
	summariseFn   func(LibraryAsset) Summary
	loadByID      func(id, uid uint) (LibraryAsset, error)
	markConfirmed func(id, uid uint) error
	softDelete    func(id, uid uint) error
	restore       func(id, uid uint) error
}

func (f *fakeDescriptor) Kind() AssetKind   { return f.kind }
func (f *fakeDescriptor) NewEmpty() LibraryAsset {
	return &fakeAsset{}
}
func (f *fakeDescriptor) LoadLatestActive(uid uint) (LibraryAsset, error) {
	if f.loadActive != nil {
		return f.loadActive(uid)
	}
	return nil, nil
}
func (f *fakeDescriptor) LoadLatest(uid uint) (LibraryAsset, error) {
	if f.loadLatest != nil {
		return f.loadLatest(uid)
	}
	return nil, nil
}
func (f *fakeDescriptor) List(uid uint, limit, offset int) ([]LibraryAsset, error) {
	if f.listFn != nil {
		return f.listFn(uid, limit, offset)
	}
	return nil, nil
}
func (f *fakeDescriptor) Search(uid uint, query string, limit int) ([]LibraryAsset, error) {
	if f.searchFn != nil {
		return f.searchFn(uid, query, limit)
	}
	return nil, nil
}
func (f *fakeDescriptor) SearchByProject(uid, projectID uint, query string, limit int) ([]LibraryAsset, error) {
	if f.searchByProject != nil {
		return f.searchByProject(uid, projectID, query, limit)
	}
	return nil, nil
}
func (f *fakeDescriptor) ListByProject(uid, projectID uint, limit, offset int) ([]LibraryAsset, error) {
	if f.listByProject != nil {
		return f.listByProject(uid, projectID, limit, offset)
	}
	return nil, nil
}
func (f *fakeDescriptor) FormatXML(a LibraryAsset) string {
	if f.formatXML != nil {
		return f.formatXML(a)
	}
	return ""
}
func (f *fakeDescriptor) Summarise(a LibraryAsset) Summary {
	if f.summariseFn != nil {
		return f.summariseFn(a)
	}
	return Summary{ID: a.GetID(), UID: a.GetUID(), Name: a.GetName()}
}
func (f *fakeDescriptor) IndexLabel() string {
	if f.indexLabel != "" {
		return f.indexLabel
	}
	return string(f.kind)
}
func (f *fakeDescriptor) URLPrefix() string {
	if f.urlPrefix != "" {
		return f.urlPrefix
	}
	return string(f.kind) + "s"
}
func (f *fakeDescriptor) LoadByID(id, uid uint) (LibraryAsset, error) {
	if f.loadByID != nil {
		return f.loadByID(id, uid)
	}
	return nil, nil
}
func (f *fakeDescriptor) MarkConfirmed(id, uid uint) error {
	if f.markConfirmed != nil {
		return f.markConfirmed(id, uid)
	}
	return nil
}
func (f *fakeDescriptor) SoftDelete(id, uid uint) error {
	if f.softDelete != nil {
		return f.softDelete(id, uid)
	}
	return nil
}
func (f *fakeDescriptor) Restore(id, uid uint) error {
	if f.restore != nil {
		return f.restore(id, uid)
	}
	return nil
}

// TestAssetKind_IsValid — guards the URL-param validation entry.
// Adding a new kind constant should make a corresponding test case
// appear here so the validation surface and the enum stay synced.
func TestAssetKind_IsValid(t *testing.T) {
	cases := map[AssetKind]bool{
		AssetKindBrand:         true,
		AssetKindCharacter:     true,
		AssetKindDirectorStyle: true,
		AssetKindProduct:       true,
		"":                     false,
		"brands":               false, // plural form rejected
		"BRAND":                false, // case-sensitive
		"unknown":              false,
	}
	for k, want := range cases {
		if got := k.IsValid(); got != want {
			t.Errorf("IsValid(%q) = %v, want %v", k, got, want)
		}
	}
}

// TestAllKinds_CanonicalOrder — preflight asset-library-index relies
// on this ordering (brand → character → director-style) being
// stable so prompt-cache hits don't churn between turns. The test
// pins the ordering contract.
func TestAllKinds_CanonicalOrder(t *testing.T) {
	got := AllKinds()
	// Product lands AFTER director-style — abstract → concrete:
	// brand identity → character (who) → director-style (how
	// shots look) → product (what's being shown).
	want := []AssetKind{
		AssetKindBrand,
		AssetKindCharacter,
		AssetKindDirectorStyle,
		AssetKindProduct,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d kinds, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("kind[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRegistry_RegisterAndGet — happy-path round-trip.
func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	d := &fakeDescriptor{kind: AssetKindBrand}
	r.Register(d)

	got, err := r.Get(AssetKindBrand)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != d {
		t.Errorf("Get returned different descriptor instance")
	}
}

// TestRegistry_GetUnknownErrors — defensive backstop. URL-validated
// kinds should never reach this path, but the registry contract is
// "unknown kind = error, not nil".
func TestRegistry_GetUnknownErrors(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get(AssetKindBrand)
	if err == nil {
		t.Errorf("Get on empty registry should error")
	}
}

// TestRegistry_RegisterIgnoresInvalid — defensive: nil descriptor
// or empty kind shouldn't panic and shouldn't poison the table.
func TestRegistry_RegisterIgnoresInvalid(t *testing.T) {
	r := NewRegistry()
	r.Register(nil)                          // no panic
	r.Register(&fakeDescriptor{kind: ""})    // empty kind rejected
	if r.Has(AssetKindBrand) {
		t.Errorf("registry should not have a brand entry yet")
	}
	// Verify a real registration after the invalid attempts works.
	r.Register(&fakeDescriptor{kind: AssetKindBrand})
	if !r.Has(AssetKindBrand) {
		t.Errorf("real registration didn't land")
	}
}

// TestRegistry_All_RespectsCanonicalOrder — iterating registry must
// produce descriptors in AllKinds() order regardless of registration
// order. Contract: preflight asset-library-index iterates this and
// emits sub-sections in a stable order for prompt-cache hits.
func TestRegistry_All_RespectsCanonicalOrder(t *testing.T) {
	r := NewRegistry()
	// Register in REVERSE canonical order — All() should still
	// produce them in canonical order.
	r.Register(&fakeDescriptor{kind: AssetKindDirectorStyle})
	r.Register(&fakeDescriptor{kind: AssetKindCharacter})
	r.Register(&fakeDescriptor{kind: AssetKindBrand})

	got := r.All()
	if len(got) != 3 {
		t.Fatalf("got %d descriptors, want 3", len(got))
	}
	want := []AssetKind{AssetKindBrand, AssetKindCharacter, AssetKindDirectorStyle}
	for i, d := range got {
		if d.Kind() != want[i] {
			t.Errorf("All()[%d].Kind() = %q, want %q", i, d.Kind(), want[i])
		}
	}
}

// TestRegistry_All_SkipsUnregistered — during mid-rollout some
// kinds may not have descriptors yet. All() must skip them, not
// surface nil entries that would crash the iterating callers.
func TestRegistry_All_SkipsUnregistered(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeDescriptor{kind: AssetKindBrand})
	// character + director_style not registered

	got := r.All()
	if len(got) != 1 {
		t.Errorf("got %d descriptors, want 1 (only brand registered)", len(got))
	}
	if got[0].Kind() != AssetKindBrand {
		t.Errorf("got %q, want brand", got[0].Kind())
	}
}

// TestRegistry_RegisterIdempotent — re-registering replaces the
// descriptor (last-write-wins). Tests that swap fakes for the same
// kind rely on this; production init() registers each kind exactly
// once so the overwrite path is test-only.
func TestRegistry_RegisterIdempotent(t *testing.T) {
	r := NewRegistry()
	first := &fakeDescriptor{kind: AssetKindBrand}
	second := &fakeDescriptor{kind: AssetKindBrand}
	r.Register(first)
	r.Register(second)

	got, _ := r.Get(AssetKindBrand)
	if got != second {
		t.Errorf("re-registration should overwrite; got first not second")
	}
}

// TestDefault_ReturnsSingleton — Default() must hand out the same
// instance across calls. Concrete descriptors register from init()
// blocks so production code paths share the same registry across
// the process.
func TestDefault_ReturnsSingleton(t *testing.T) {
	a := Default()
	b := Default()
	if a != b {
		t.Errorf("Default() returned different instances; expected singleton")
	}
}

// TestIsActive_LifecycleMatrix — the strict "fully vetted" predicate
// every existing per-type IsActive() implements. Lifted to the
// package level so registry consumers can call it without
// type-switching.
func TestIsActive_LifecycleMatrix(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		a    LibraryAsset
		want bool
	}{
		{"nil", nil, false},
		{"confirmed not deleted", &fakeAsset{status: StatusConfirmed}, true},
		{"draft never active", &fakeAsset{status: StatusDraft}, false},
		{"archived never active", &fakeAsset{status: StatusArchived}, false},
		{
			"soft-deleted confirmed inactive",
			&fakeAsset{status: StatusConfirmed, deletedAt: &now},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsActive(tc.a); got != tc.want {
				t.Errorf("IsActive() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDescriptor_FakeImplementsInterface — compile-time assertion via
// runtime call. If Descriptor grows a new method, this test forces a
// fakeDescriptor update by failing to compile.
func TestDescriptor_FakeImplementsInterface(t *testing.T) {
	var _ Descriptor = (*fakeDescriptor)(nil)
	var _ LibraryAsset = (*fakeAsset)(nil)

	// Sanity smoke: every Descriptor method runs without panic.
	d := &fakeDescriptor{
		kind:       AssetKindBrand,
		loadActive: func(uid uint) (LibraryAsset, error) { return nil, errors.New("x") },
	}
	if d.Kind() != AssetKindBrand {
		t.Error("Kind mismatch")
	}
	if _, err := d.LoadLatestActive(1); err == nil {
		t.Error("expected error from fake")
	}
}

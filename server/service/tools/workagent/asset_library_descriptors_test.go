package workagent

// asset_library_descriptors_test.go — Sprint-D 2/7. Cross-cutting
// smoke test that the three descriptors (brand / character /
// director_style) register with the package-level registry as a
// side-effect of importing this package. Importing
// workagent_test pulls workagent in, which fires every file's
// init() block — if any descriptor file's init() is missing, the
// registry won't have the kind and the test fails.
//
// Also covers the "canonical iteration order" guarantee from
// asset_library.AllKinds — Default().All() must walk in
// brand → character → director-style order regardless of which
// init ran first.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"server/model"
	"server/service/asset_library"
)

func TestDescriptors_RegisterAtInit(t *testing.T) {
	for _, kind := range asset_library.AllKinds() {
		if !asset_library.Default().Has(kind) {
			t.Errorf("descriptor for kind %q is not registered — init() missing or failed", kind)
		}
	}
}

func TestDescriptors_AllReturnsCanonicalOrder(t *testing.T) {
	got := asset_library.Default().All()
	if len(got) != 4 {
		t.Fatalf("Default().All() returned %d descriptors, want 4", len(got))
	}
	want := []asset_library.AssetKind{
		asset_library.AssetKindBrand,
		asset_library.AssetKindCharacter,
		asset_library.AssetKindDirectorStyle,
		asset_library.AssetKindProduct,
	}
	for i, d := range got {
		if d.Kind() != want[i] {
			t.Errorf("All()[%d].Kind() = %q, want %q", i, d.Kind(), want[i])
		}
	}
}

func TestDescriptors_NewEmptyReturnsCorrectType(t *testing.T) {
	cases := []struct {
		name string
		kind asset_library.AssetKind
	}{
		{"brand", asset_library.AssetKindBrand},
		{"character", asset_library.AssetKindCharacter},
		{"director_style", asset_library.AssetKindDirectorStyle},
		{"product", asset_library.AssetKindProduct},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := asset_library.Default().Get(tc.kind)
			if err != nil {
				t.Fatalf("descriptor %q not found: %v", tc.kind, err)
			}
			empty := d.NewEmpty()
			if empty == nil {
				t.Fatalf("NewEmpty() for %q returned nil", tc.kind)
			}
			// The empty asset must satisfy LibraryAsset (compile-time)
			// AND be safe to call accessors on (no nil deref).
			_ = empty.GetID()
			_ = empty.GetUID()
			_ = empty.GetName()
			_ = empty.GetSlug()
			_ = empty.GetStatus()
			_ = empty.GetConfirmed()
			_ = empty.TableName()
		})
	}
}

// TestWrappers_MarshalJSONProjectsStatus — parametric pin of the
// Polish #17 contract at the LibraryAsset interface level. Each
// wrapper must override MarshalJSON to project the model's raw
// `Status int8` onto the asset_library.Status enum string;
// otherwise the Detail wire shape ships `"status": 1` and the
// frontend AssetDetailDrawer's `=== 'draft'` gate fails for
// every kind.
//
// Per-kind tests in api/pro/tools/workagent/{brand,character,
// director_style,product}_asset_api_test.go pin this at the HTTP
// boundary; THIS test pins it at the abstraction level so a
// future 5th-kind addition that forgets MarshalJSON gets caught
// here without needing its own per-kind regression.
//
// Method: marshal each wrapper constructed around a freshly
// minted model with Confirmed=false (→ "draft" projection),
// inspect the bytes. If the wrapper ships "status":0 (the raw
// model.X.Status default int) instead of "status":"draft", the
// MarshalJSON override is missing or broken.
func TestWrappers_MarshalJSONProjectsStatus(t *testing.T) {
	now := time.Now()
	type wireCase struct {
		kind  string
		asset asset_library.LibraryAsset
	}
	cases := []wireCase{
		{
			kind: "brand",
			asset: brandLibraryAsset{Brand: func() *model.Brand {
				b := &model.Brand{UID: 1, Name: "b", Slug: "b", Status: 1, Confirmed: false}
				b.Id = 1
				b.CreatedAt = now
				b.UpdatedAt = now
				return b
			}()},
		},
		{
			kind: "character",
			asset: characterLibraryAsset{Character: func() *model.Character {
				c := &model.Character{UID: 1, Name: "c", Slug: "c", Status: 1, Confirmed: false}
				c.Id = 1
				c.CreatedAt = now
				c.UpdatedAt = now
				return c
			}()},
		},
		{
			kind: "director_style",
			asset: directorStyleLibraryAsset{DirectorStyle: func() *model.DirectorStyle {
				d := &model.DirectorStyle{UID: 1, Name: "d", Slug: "d", Status: 1, Confirmed: false}
				d.Id = 1
				d.CreatedAt = now
				d.UpdatedAt = now
				return d
			}()},
		},
		{
			kind: "product",
			asset: productLibraryAsset{Product: func() *model.Product {
				p := &model.Product{UID: 1, Name: "p", Slug: "p", Status: 1, Confirmed: false}
				p.Id = 1
				p.CreatedAt = now
				p.UpdatedAt = now
				return p
			}()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			raw, err := json.Marshal(tc.asset)
			if err != nil {
				t.Fatalf("marshal %s wrapper: %v", tc.kind, err)
			}
			s := string(raw)
			if !strings.Contains(s, `"status":"draft"`) {
				t.Errorf("%s wrapper must project Status to \"draft\"; got %s", tc.kind, s)
			}
			// Catch the raw-int leak directly so a partial fix
			// (override present but calling raw model.Marshal first
			// and forgetting to overwrite the key) is still caught.
			if strings.Contains(s, `"status":1`) || strings.Contains(s, `"status":0`) {
				t.Errorf("%s wrapper leaked raw int status: %s", tc.kind, s)
			}
		})
	}
}

func TestDescriptors_FormatXMLNilSafe(t *testing.T) {
	for _, kind := range asset_library.AllKinds() {
		d, err := asset_library.Default().Get(kind)
		if err != nil {
			t.Errorf("descriptor %q missing: %v", kind, err)
			continue
		}
		// FormatXML(nil) and Summarise(nil) must drop cleanly. The
		// preflight+API call sites pre-check for nil already, but
		// defending the boundary keeps "wrong kind" routing bugs
		// from blowing up.
		if got := d.FormatXML(nil); got != "" {
			t.Errorf("%s.FormatXML(nil) = %q, want empty string", kind, got)
		}
		if got := d.Summarise(nil); got.ID != 0 || got.Name != "" {
			t.Errorf("%s.Summarise(nil) returned non-zero summary", kind)
		}
	}
}

func TestFormatProductSpecXML_CarriesCreativeAssetContractHeader(t *testing.T) {
	got := formatProductSpecXML(&model.Product{
		Name:      "Trail Bottle",
		Slug:      "trail-bottle",
		SKU:       "TB-01",
		Category:  "outdoor",
		Confirmed: true,
	})
	for _, want := range []string{
		"<product-context>",
		"asset_kind: product",
		"contract_schema: creative_asset_contract.v1",
		"contract_status: confirmed",
		"name: Trail Bottle",
		"sku: TB-01",
		"</product-context>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("product context missing %q: %q", want, got)
		}
	}
}

func TestFormatCreativeAssetContractHeader_MarksDraftUnconfirmed(t *testing.T) {
	got := formatCharacterContextXML(&model.Character{
		Name:      "Draft Character",
		Slug:      "draft-character",
		Confirmed: false,
	})
	for _, want := range []string{
		"asset_kind: character",
		"contract_status: draft_unconfirmed",
		"status: [待品牌方确认]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("draft character context missing %q: %q", want, got)
		}
	}
}

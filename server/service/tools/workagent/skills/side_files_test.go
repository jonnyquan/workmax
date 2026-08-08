package skills

import (
	"strings"
	"testing"
)

func TestLoadSideFiles_ShippedSkills(t *testing.T) {
	// All five Sprint A skills must ship at least one asset for
	// PR-10's preflight injection to work.
	for _, skill := range []string{"ppt", "character", "productShot", "marketingPoster", "flashCard", "webBanner", "socialAd", "mobileStory"} {
		t.Run(skill, func(t *testing.T) {
			files := LoadSideFiles(skill)
			if len(files) == 0 {
				t.Errorf("expected at least one asset for skill %q", skill)
			}
			for _, f := range files {
				if f.Path == "" {
					t.Errorf("asset path empty")
				}
				if f.Contents == "" {
					t.Errorf("asset %q has empty contents", f.Path)
				}
			}
		})
	}
}

func TestLoadSideFiles_HTMLNativeTemplateSeeds(t *testing.T) {
	for _, skill := range []string{"ppt", "webBanner", "socialAd", "mobileStory"} {
		t.Run(skill, func(t *testing.T) {
			files := LoadSideFiles(skill)
			found := false
			for _, f := range files {
				if f.Path != "html-native-template-seed.md" {
					continue
				}
				found = true
				if !strings.Contains(f.Contents, "HTML-native") {
					t.Fatalf("seed for %s missing HTML-native marker", skill)
				}
				if !strings.Contains(f.Contents, "Export Readiness") {
					t.Fatalf("seed for %s missing export readiness section", skill)
				}
			}
			if !found {
				t.Fatalf("%s missing html-native-template-seed.md", skill)
			}
		})
	}
}

func TestLoadSideFiles_HTMLMotionHelpers(t *testing.T) {
	for _, skill := range []string{"ppt", "webBanner", "socialAd", "mobileStory"} {
		t.Run(skill, func(t *testing.T) {
			files := LoadSideFiles(skill)
			found := false
			for _, f := range files {
				if f.Path != "html-motion-helper.md" {
					continue
				}
				found = true
				if !strings.Contains(f.Contents, "timeline") {
					t.Fatalf("motion helper for %s missing timeline convention", skill)
				}
				if !strings.Contains(f.Contents, "reduced-motion") {
					t.Fatalf("motion helper for %s missing reduced-motion rule", skill)
				}
			}
			if !found {
				t.Fatalf("%s missing html-motion-helper.md", skill)
			}
		})
	}
}

func TestLoadSideFiles_CreativeAssetContractUsageExamples(t *testing.T) {
	for _, skill := range []string{"ppt", "webBanner", "socialAd", "mobileStory", "productShot"} {
		t.Run(skill, func(t *testing.T) {
			files := LoadSideFiles(skill)
			found := false
			for _, f := range files {
				if f.Path != "asset-contract-usage.md" {
					continue
				}
				found = true
				if !strings.Contains(f.Contents, "creative_asset_contract.v1") {
					t.Fatalf("asset contract usage for %s missing schema marker", skill)
				}
				if !strings.Contains(f.Contents, "asset_kind:") {
					t.Fatalf("asset contract usage for %s missing asset_kind examples", skill)
				}
				for _, kind := range []string{"scene_style", "copy_voice", "motion_rules"} {
					if !strings.Contains(f.Contents, "asset_kind: "+kind) {
						t.Fatalf("asset contract usage for %s missing %s examples", skill, kind)
					}
				}
			}
			if !found {
				t.Fatalf("%s missing asset-contract-usage.md", skill)
			}
		})
	}
}

func TestLoadSideFiles_MissingSkill(t *testing.T) {
	files := LoadSideFiles("imaginary-skill")
	if len(files) != 0 {
		t.Errorf("missing skill should return empty, got %d", len(files))
	}
}

func TestValidateSideFileForAuthoring(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		data       []byte
		wantSubstr string
	}{
		{name: "valid markdown", path: "template.md", data: []byte("body")},
		{name: "nested path", path: "nested/template.md", data: []byte("body"), wantSubstr: "must be top-level"},
		{name: "dotfile", path: ".DS_Store", data: []byte("body"), wantSubstr: "dotfile"},
		{name: "unsafe xml path", path: "bad\"name.md", data: []byte("body"), wantSubstr: "unsafe for XML"},
		{name: "unsupported extension", path: "sample.png", data: []byte("body"), wantSubstr: "text asset extension"},
		{name: "empty body", path: "template.md", data: nil, wantSubstr: "empty"},
		{name: "over size cap", path: "template.md", data: []byte(strings.Repeat("x", maxSideFileBytes+1)), wantSubstr: "exceeds"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSideFileForAuthoring(tc.path, tc.data)
			if tc.wantSubstr == "" {
				if err != nil {
					t.Fatalf("expected valid side file, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantSubstr, err)
			}
		})
	}
}

func TestLoadSideFiles_SortedDeterministic(t *testing.T) {
	files1 := LoadSideFiles("ppt")
	files2 := LoadSideFiles("ppt")
	if len(files1) != len(files2) {
		t.Fatalf("repeat call mismatch")
	}
	for i := range files1 {
		if files1[i].Path != files2[i].Path {
			t.Errorf("non-deterministic order: pos %d", i)
		}
	}
}

func TestFormatSideFilesXML_Empty(t *testing.T) {
	if got := FormatSideFilesXML(nil); got != "" {
		t.Errorf("empty input should produce empty XML, got %q", got)
	}
	if got := FormatSideFilesXML([]SideFile{}); got != "" {
		t.Errorf("empty slice should produce empty XML, got %q", got)
	}
}

func TestFormatSideFilesXML_Wraps(t *testing.T) {
	files := []SideFile{
		{Path: "a.md", Contents: "Alpha body"},
		{Path: "b.md", Contents: "Bravo body\n"},
	}
	got := FormatSideFilesXML(files)
	if !strings.HasPrefix(got, "<skill-side-files>") {
		t.Errorf("missing wrapper: %q", got)
	}
	if !strings.HasSuffix(got, "</skill-side-files>") {
		t.Errorf("missing closing tag: %q", got)
	}
	if !strings.Contains(got, `<asset path="a.md">`) {
		t.Errorf("a.md asset tag missing")
	}
	if !strings.Contains(got, "Alpha body") || !strings.Contains(got, "Bravo body") {
		t.Errorf("asset bodies missing")
	}
}

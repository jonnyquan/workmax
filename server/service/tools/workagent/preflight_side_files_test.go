package workagent

import (
	"strings"
	"testing"

	"server/utils/testutil"
)

// preflight_side_files_test.go — F1 (2026-05-17). The
// SkillSideFiles preflight injection was orphan code from
// 2026-04 until the F1 wire-up: LoadSideFiles + composer field +
// <skill-side-files> XML wrapper all existed but no preflight
// path assigned them, so the 5 skills with assets/*.md trees
// (ppt / character / flashCard / marketingPoster / productShot)
// shipped 222 lines of authored prompt content that never reached
// the model.
//
// These tests pin the contract end-to-end: BuildPreflight
// AdditionsForThread MUST emit a <skill-side-files> block for
// skills that have assets/ on disk, AND MUST drop the layer
// cleanly for skills that don't (single TrimRight path; no empty
// XML tags polluting the system prompt).

// TestBuildPreflightAdditions_InjectsSideFiles_PPT pins the
// load-bearing case: ppt is the largest authored side file
// (`template-shell.md`, 46 lines) and the one users hit most.
// If this test ever flips red, the F1 wire-up has been undone.
func TestBuildPreflightAdditions_InjectsSideFiles_PPT(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	got := BuildPreflightAdditionsForThread(42, "ppt", 0)
	if !strings.Contains(got, "<skill-side-files>") {
		t.Errorf("preflight for ppt missing <skill-side-files> block; got %q", got)
	}
	if !strings.Contains(got, "template-shell.md") {
		t.Errorf("preflight for ppt missing template-shell.md asset path; got %q", got)
	}
	if !strings.Contains(got, "</skill-side-files>") {
		t.Errorf("preflight for ppt missing </skill-side-files> closing tag; got %q", got)
	}
}

// TestBuildPreflightAdditions_InjectsSideFiles_AllAssetSkills —
// every skill that ships an assets/ tree must have its side
// files reach the system prompt. Pins the currently-authored
// skills + one canonical asset filename so a renamed file gets
// caught (rename without re-injection = silently dark file).
func TestBuildPreflightAdditions_InjectsSideFiles_AllAssetSkills(t *testing.T) {
	cases := []struct {
		skill string
		asset string
	}{
		{"ppt", "template-shell.md"},
		{"character", "pose-anchor-grid.md"},
		{"flashCard", "card-template.md"},
		{"marketingPoster", "poster-grid.md"},
		{"productShot", "composition-grid.md"},
		{"webBanner", "asset-contract-usage.md"},
		{"socialAd", "asset-contract-usage.md"},
		{"mobileStory", "asset-contract-usage.md"},
	}
	for _, tc := range cases {
		t.Run(tc.skill, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			installSystemDBForPreflight(t, db)

			got := BuildPreflightAdditionsForThread(42, tc.skill, 0)
			if !strings.Contains(got, tc.asset) {
				t.Errorf("preflight for %q missing asset %q; got %q", tc.skill, tc.asset, got)
			}
		})
	}
}

// TestBuildPreflightAdditions_InjectsHTMLNativeRuntimeSideFiles
// pins P6-4/P6-5 end-to-end. Loader-level tests prove the files
// exist; this test proves the template seed and motion helper
// actually reach the system prompt for each HTML-native skill.
func TestBuildPreflightAdditions_InjectsHTMLNativeRuntimeSideFiles(t *testing.T) {
	for _, skill := range []string{"ppt", "webBanner", "socialAd", "mobileStory"} {
		t.Run(skill, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			installSystemDBForPreflight(t, db)

			got := BuildPreflightAdditionsForThread(42, skill, 0)
			for _, want := range []string{
				`<asset path="html-native-template-seed.md">`,
				`<asset path="html-motion-helper.md">`,
				"Export Readiness",
				"reduced-motion",
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("preflight for %q missing HTML-native side-file marker %q; got %q", skill, want, got)
				}
			}
		})
	}
}

// TestBuildPreflightAdditions_NoSideFilesDropsCleanly pins the
// negative case: a skill without an assets/ tree (e.g. logo)
// must NOT emit an empty <skill-side-files></skill-side-files>
// tag. The composer's TrimRight + isEmpty checks should drop
// the layer entirely.
func TestBuildPreflightAdditions_NoSideFilesDropsCleanly(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	got := BuildPreflightAdditionsForThread(42, "logo", 0)
	if strings.Contains(got, "<skill-side-files>") {
		t.Errorf("preflight for logo (no assets/) must NOT emit <skill-side-files> tag; got %q", got)
	}
}

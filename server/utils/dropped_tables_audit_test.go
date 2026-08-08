// Dropped-tables audit — fails the test suite if any source file in
// server/ references a database table that has been DROPped
// by a migration. Catches the "we deleted the table but forgot to
// delete the model file" class of stale code that audit work
// 2026-05-16 surfaced.
//
// This executable inventory is authoritative now that the historical design
// archive is no longer part of the open-source repository.
//
// What's NOT audited (intentional):
//   - Files under any `/migrations/` path — DROP statements naturally
//     name the dropped table.
//   - Files under any `/archive/` path — archived design docs may
//     still discuss historical tables.
//   - `_test.go` files — test fixtures may legitimately seed via
//     `INSERT INTO <dropped-table>` to verify migration behaviour.
//   - `workmax.log` and any `/logs/` directory — runtime artifacts, not
//     source.
//   - `node_modules` / `.next` / `.git` — vendor + build output.
//   - This file itself, which names the dropped tables for cataloguing.

package utils

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// droppedTables is the canonical list of tables removed by DROP
// migrations. Update this slice when a new DROP migration lands.
//
// Tables that LOOK like they belong here but are actually still
// live (kept in production code) — DO NOT add:
//   - w_drama_character / w_drama_character_reference: used by
//     canvas + character API; explicitly preserved by migration
//     20260600's "Kept" comment.
var droppedTables = []string{
	// Single-tool retirements (20260534–20260551)
	"w_canvas_version",
	"w_music_video",
	"w_product_demo_video",
	"w_corporate_video",
	"w_real_estate_video",
	"w_realestate_video",
	"w_news_video",
	"w_compliance_audit",
	"w_compliance_review_assignment",
	"w_knowledge_unit",
	"w_knowledge_unit_video",
	"w_digital_human_avatar",
	"w_virtual_avatar_video",
	"w_audio_asset",
	"w_social_account_profile",
	"w_ad_variant",
	"w_batch_item",
	"w_batch_job",
	"w_ecommerce_render",
	"w_performance_metric",
	"w_publication",

	// 20260600 vertical-solution batch (33 tables — ad / comic /
	// drama-non-character / ecom)
	"w_ad_creative",
	"w_ad_creative_export_version",
	"w_ad_project",
	"w_ad_scene_render",
	"w_ad_template",
	"w_ad_test",
	"w_ad_test_variant",
	"w_ad_brand_profile",
	"w_ad_brand_reference",
	"w_comic_chapter",
	"w_comic_page",
	"w_comic_panel",
	"w_comic_project",
	"w_comic_storyboard",
	"w_comic_template",
	"w_drama_character_relationship",
	"w_drama_character_family",
	"w_drama_director_preset",
	"w_drama_episode",
	"w_drama_episode_activity",
	"w_drama_episode_export_version",
	"w_drama_location",
	"w_drama_location_reference",
	"w_drama_panel_shot",
	"w_drama_project",
	"w_drama_script",
	"w_drama_script_revision",
	"w_drama_storyboard",
	"w_drama_template",
	"w_ecom_product_asset",
	"w_ecom_product_image",
	"w_ecom_project",
	"w_ecom_template",
	"w_ecom_video",

	// Platform-ization cleanup (20260603–20260625)
	"w_canvas_audit",
	"w_character",
	"w_character_reference",
	"w_character_relationship",
	"w_canvas_asset",
}

// pathExclusionContains lists path substrings that exempt a file
// from the scan. Order matters only for legibility, not semantics.
var pathExclusionContains = []string{
	"/migrations/",
	"/archive/",
	"/node_modules/",
	"/.next/",
	"/.git/",
	"/logs/",
	// /scripts/ holds DB maintenance + migration-verification SQL that
	// BY DESIGN names dropped tables (e.g. server/scripts/check_drama_
	// migrations.sql verifies the 20260417 drama migrations applied
	// cleanly). Source-of-truth for the drop is the migration itself;
	// the verification script is an operational artifact.
	"/scripts/",
	"workmax.log",
	// This test + the inventory doc themselves name the dropped
	// tables for cataloguing.
	"dropped_tables_audit_test.go",
	"dropped-tables-inventory.md",
}

// pathExclusionSuffix lists path suffixes that exempt a file.
// _test.go covers Go tests; .test.ts/.test.tsx cover the vitest FE
// suites that may stub-seed dropped tables.
var pathExclusionSuffix = []string{
	"_test.go",
	".test.ts",
	".test.tsx",
}

// scannedExtensions is the file-type allowlist. Source code only —
// no images, no .json (i18n locale files can legitimately mention
// "personal" / etc. but the dropped tables are all `w_` prefixed
// so json scan would be noise-free anyway; skipped purely for
// throughput).
var scannedExtensions = map[string]bool{
	".go":   true,
	".ts":   true,
	".tsx":  true,
	".sql":  true, // ATTN: migrations/ are already excluded above
	".md":   true,
	".yaml": true,
	".yml":  true,
}

// repoRoot returns the repository root path from the test's CWD.
// The test runs in server/utils/, so two levels up is the root.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot anchor repo root")
	}
	// file = .../server/utils/dropped_tables_audit_test.go
	// dir  = .../server/utils
	// up2  = .../  (repo root)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func isExcluded(path string) bool {
	for _, sub := range pathExclusionContains {
		if strings.Contains(path, sub) {
			return true
		}
	}
	for _, suf := range pathExclusionSuffix {
		if strings.HasSuffix(path, suf) {
			return true
		}
	}
	return false
}

// buildDroppedPatterns returns the regexes that mark a TRUE bug. We
// match only contexts that indicate active DB access, NOT prose
// comments that reference the dropped name for historical
// documentation purposes. A migration like "w_drama_storyboard was
// renamed to w_global_..." in a code comment is INFORMATION, not a
// stale call site.
//
// Contexts considered "live ref":
//
//   - Table("w_x") / .Table("w_x")                — GORM dynamic table
//   - gorm:"table:w_x" struct tag                  — GORM static table
//   - TableName() returning the literal            — model.TableName binding
//   - FROM / INTO / UPDATE / JOIN / TABLE w_x (SQL) — raw SQL string
//
// What's deliberately NOT a live ref:
//
//   - Bare mentions in // line or /* block */ comments
//   - Doc strings / package-headers describing history
//   - Variable names that incidentally contain the substring
func buildDroppedPatterns() []*regexp.Regexp {
	escaped := make([]string, len(droppedTables))
	for i, t := range droppedTables {
		escaped[i] = regexp.QuoteMeta(t)
	}
	sort.Slice(escaped, func(i, j int) bool {
		return len(escaped[i]) > len(escaped[j])
	})
	alt := `(?:` + strings.Join(escaped, "|") + `)`
	return []*regexp.Regexp{
		// GORM dynamic: .Table("w_drama_storyboard")
		regexp.MustCompile(`\.Table\(\s*"` + alt + `"\s*\)`),
		// GORM struct tag: gorm:"...table:w_drama_storyboard..."
		regexp.MustCompile("`[^`]*table:" + alt + `\b[^` + "`" + `]*` + "`"),
		// TableName() return: return "w_drama_storyboard"
		regexp.MustCompile(`return\s+"` + alt + `"`),
		// Raw SQL: FROM/INTO/UPDATE/JOIN/TABLE w_drama_storyboard
		regexp.MustCompile(`(?i)\b(?:FROM|INTO|UPDATE|JOIN|TABLE)\s+` + alt + `\b`),
		// Backtick-quoted SQL identifier in raw query: ` + "`" + `w_drama_storyboard` + "`" + `
		regexp.MustCompile("`" + alt + "`"),
	}
}

type hit struct {
	path  string
	line  int
	match string
}

func TestNoLiveReferencesToDroppedTables(t *testing.T) {
	patterns := buildDroppedPatterns()
	root := repoRoot(t)

	// Scan the only service source root. Desktop consumes the Server contract
	// and does not own database table references.
	scanRoots := []string{
		filepath.Join(root, "server"),
	}

	var hits []hit
	for _, sr := range scanRoots {
		if _, err := os.Stat(sr); os.IsNotExist(err) {
			continue
		}
		err := filepath.Walk(sr, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				// Prune excluded directories early so we don't
				// recurse into node_modules / archive / etc.
				if isExcluded(path + "/") {
					return filepath.SkipDir
				}
				return nil
			}
			if !scannedExtensions[filepath.Ext(path)] {
				return nil
			}
			if isExcluded(path) {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			anyMatch := false
			for _, p := range patterns {
				if p.Match(data) {
					anyMatch = true
					break
				}
			}
			if !anyMatch {
				return nil
			}
			// Build per-line records for actionable error messages —
			// only count lines that match an ACTIVE-REF pattern, not
			// bare prose comment occurrences.
			lines := strings.Split(string(data), "\n")
			for i, line := range lines {
				for _, p := range patterns {
					if m := p.FindString(line); m != "" {
						hits = append(hits, hit{
							path:  strings.TrimPrefix(path, root+"/"),
							line:  i + 1,
							match: m,
						})
						break
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", sr, err)
		}
	}

	if len(hits) == 0 {
		return
	}

	// Sort for stable output across CI runs.
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].line < hits[j].line
	})

	var b strings.Builder
	b.WriteString("Found live references to DROPPed database tables.\n")
	b.WriteString("Each match below is a stale reference that must be removed or quoted under one of\n")
	b.WriteString("the documented exclusions (migrations / archive / _test.go / logs).\n")
	b.WriteString("Reference: exclusion rules in server/utils/dropped_tables_audit_test.go\n\n")
	for _, h := range hits {
		b.WriteString(h.path)
		b.WriteString(":")
		b.WriteString(itoa(h.line))
		b.WriteString(": ")
		b.WriteString(h.match)
		b.WriteString("\n")
	}
	t.Fatal(b.String())
}

// itoa is a tiny helper to avoid pulling fmt for this single use.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

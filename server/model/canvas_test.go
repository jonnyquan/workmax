package model

import "testing"

// The canvas model file is intentionally thin — it's entirely struct
// definitions with gorm tags plus a TableName() per entity. The two
// things that actually affect production are:
//
//   1. TableName() strings: migrations create tables keyed by these
//      exact names. If someone renames a struct and forgets to pin the
//      old TableName, the next deploy creates a new empty table and
//      silently orphans the old data. These tests pin the contract.
//
//   2. Exported status/scope constants: handlers, migrations,
//      and the frontend all reference the string/int
//      values. Changing any of them is a schema change masquerading as
//      a rename. Pin them here so the change is loud.

func TestCanvasTableNames_StayPinnedToDBSchema(t *testing.T) {
	// A rename in the Go struct must not silently move the physical
	// table — an accidental rename triggers a CREATE TABLE on next
	// auto-migrate and orphans every historical row. If a schema
	// migration intentionally renames the table, this test should
	// change alongside the migration.
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"CanvasProject", CanvasProject{}.TableName(), "w_global_project"},
		// CanvasVersion intentionally omitted — after M5-B-06 Retire-T5
		// the table w_canvas_version was dropped and the struct became a
		// pure response DTO (no TableName / no GORM mapping). The
		// snapshot table replaces its storage role; see CanvasSnapshot.
		{"CanvasSnapshot", CanvasSnapshot{}.TableName(), "w_canvas_snapshot"},
		{"CanvasTaskBinding", CanvasTaskBinding{}.TableName(), "w_canvas_task_binding"},
		{"Shot", Shot{}.TableName(), "w_canvas_shot"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s.TableName() = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestShotStatus_ConstantsMatchMigrationValues(t *testing.T) {
	// ShotStatus is persisted as a tinyint. Renumbering any of these
	// without a data migration silently reclassifies every historical
	// shot. The numeric values are the primary-key of meaning here —
	// pin them, not just the names.
	if ShotStatusDraft != 0 {
		t.Errorf("ShotStatusDraft = %d, want 0", ShotStatusDraft)
	}
	if ShotStatusReady != 1 {
		t.Errorf("ShotStatusReady = %d, want 1", ShotStatusReady)
	}
	if ShotStatusArchived != 2 {
		t.Errorf("ShotStatusArchived = %d, want 2", ShotStatusArchived)
	}
	// Disjoint numeric values — a duplicate would corrupt filter queries.
	seen := map[int8]string{}
	for name, v := range map[string]int8{
		"ShotStatusDraft":    ShotStatusDraft,
		"ShotStatusReady":    ShotStatusReady,
		"ShotStatusArchived": ShotStatusArchived,
	} {
		if dup, ok := seen[v]; ok {
			t.Errorf("status value %d is shared by %s and %s", v, dup, name)
		}
		seen[v] = name
	}
}

func TestAssetBindingScope_ConstantsMatchJSONWireFormat(t *testing.T) {
	// AssetBinding.Scope is serialised to JSON and read back on the
	// frontend. Any change here is a cross-layer contract change —
	// pin the exact wire strings.
	if string(AssetScopeElement) != "element" {
		t.Errorf("AssetScopeElement = %q, want %q", AssetScopeElement, "element")
	}
	if string(AssetScopeShot) != "shot" {
		t.Errorf("AssetScopeShot = %q, want %q", AssetScopeShot, "shot")
	}
	if string(AssetScopeProject) != "project" {
		t.Errorf("AssetScopeProject = %q, want %q", AssetScopeProject, "project")
	}
}

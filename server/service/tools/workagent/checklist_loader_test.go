package workagent

import (
	"strings"
	"testing"
)

const samplePPTChecklist = `# PPT Pre-Emit Checklist

> Schema documentation paragraph that the parser must skip.

## P0 · MUST PASS（阻塞 emit）

### P0-1 · honest_data
- detector: ` + "`honest_data`" + `
- description: 所有数字 / 百分比能溯源到 source
- on_fail: block
- rationale: 编造数字会爆雷

### P0-2 · brand_color_compliance
- detector: ` + "`brand_spec_grep`" + `
- description: 输出含 hex 必须溯源
- on_fail: block
- only_if: brand-spec.md exists

## P1 · SHOULD PASS（warning，可 override）

### P1-1 · color_contrast
- detector: ` + "`contrast_analyzer`" + `
- description: WCAG AA contrast
- on_fail: warn_require_ack

## P2 · NICE TO HAVE

### P2-1 · consistent_grid
- detector: ` + "`grid_alignment`" + `
- description: align to 8pt grid
`

func TestParseChecklist_Smoke(t *testing.T) {
	c := parseChecklistMarkdown("ppt", samplePPTChecklist)
	if c.SkillName != "ppt" {
		t.Errorf("SkillName: got %q want ppt", c.SkillName)
	}
	if len(c.P0) != 2 {
		t.Errorf("P0: got %d items, want 2", len(c.P0))
	}
	if len(c.P1) != 1 {
		t.Errorf("P1: got %d items, want 1", len(c.P1))
	}
	if len(c.P2) != 1 {
		t.Errorf("P2: got %d items, want 1", len(c.P2))
	}
}

func TestParseChecklist_StripsBackticks(t *testing.T) {
	c := parseChecklistMarkdown("ppt", samplePPTChecklist)
	if c.P0[0].Detector != "honest_data" {
		t.Errorf("expected detector=honest_data (no backticks), got %q", c.P0[0].Detector)
	}
}

func TestParseChecklist_CapturesAllFields(t *testing.T) {
	c := parseChecklistMarkdown("ppt", samplePPTChecklist)
	item := c.P0[1] // P0-2 brand_color
	if item.ID != "P0-2" {
		t.Errorf("ID: got %q", item.ID)
	}
	if item.Title != "brand_color_compliance" {
		t.Errorf("Title: got %q", item.Title)
	}
	if item.Detector != "brand_spec_grep" {
		t.Errorf("Detector: got %q", item.Detector)
	}
	if item.OnFail != "block" {
		t.Errorf("OnFail: got %q", item.OnFail)
	}
	if !strings.Contains(item.OnlyIf, "brand-spec.md") {
		t.Errorf("OnlyIf: got %q", item.OnlyIf)
	}
}

func TestParseChecklist_DropsItemWithoutDetector(t *testing.T) {
	body := `## P0
### P0-1 · half_authored
- description: someone forgot the detector field
- on_fail: block

### P0-2 · proper
- detector: honest_data
- description: ok
`
	c := parseChecklistMarkdown("test", body)
	if len(c.P0) != 1 {
		t.Errorf("expected 1 valid item (the malformed one dropped), got %d", len(c.P0))
	}
	if c.P0[0].Title != "proper" {
		t.Errorf("expected the well-formed item to survive")
	}
}

func TestParseChecklist_EmptyBody(t *testing.T) {
	c := parseChecklistMarkdown("test", "")
	if !c.IsEmpty() {
		t.Errorf("empty body should produce IsEmpty()=true")
	}
}

func TestParseChecklist_NoSections(t *testing.T) {
	c := parseChecklistMarkdown("test", "# Heading\n\nJust prose, no sections.")
	if !c.IsEmpty() {
		t.Errorf("body without P0/P1/P2 sections should be empty")
	}
}

func TestParseChecklist_TolerantOfExtraFields(t *testing.T) {
	body := `## P0
### P0-1 · t
- detector: honest_data
- description: x
- on_fail: block
- rationale: this gets ignored
- some_future_field: also ignored
`
	c := parseChecklistMarkdown("test", body)
	if len(c.P0) != 1 {
		t.Fatalf("expected 1 item, got %d", len(c.P0))
	}
	if c.P0[0].Detector != "honest_data" {
		t.Errorf("detector should still parse correctly with extra fields")
	}
}

func TestChecklist_Items_Ordering(t *testing.T) {
	c := parseChecklistMarkdown("ppt", samplePPTChecklist)
	all := c.Items()
	if len(all) != 4 {
		t.Errorf("Items() should return P0+P1+P2 = 4, got %d", len(all))
	}
	if all[0].ID != "P0-1" || all[3].ID != "P2-1" {
		t.Errorf("Items() should preserve P0→P1→P2 order, got %v", []string{all[0].ID, all[3].ID})
	}
}

func TestLoadChecklistForSkill_RealFiles(t *testing.T) {
	// Smoke against actual checklists shipped in PR-8.
	skillDir := "skills/ppt/references"
	c := LoadChecklistForSkill(skillDir, "ppt")
	if c.IsEmpty() {
		t.Fatal("real ppt checklist should not be empty")
	}
	if len(c.P0) < 1 {
		t.Errorf("ppt should have ≥1 P0 item, got %d", len(c.P0))
	}
	// Verify the gate-relevant detector names appear.
	foundHonest := false
	for _, item := range c.P0 {
		if item.Detector == "honest_data" {
			foundHonest = true
		}
	}
	if !foundHonest {
		t.Error("ppt P0 must reference honest_data detector")
	}
}

func TestLoadChecklistForSkill_MissingFile(t *testing.T) {
	c := LoadChecklistForSkill("/nonexistent/path", "imaginary")
	if !c.IsEmpty() {
		t.Errorf("missing file should produce empty checklist")
	}
}

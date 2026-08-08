package admin

import (
	"server/model"
	"testing"
)

func TestGenerationObjectStatusLabel(t *testing.T) {
	if got := generationObjectStatusLabel(model.GenerationObjectStatusActive); got != "active" {
		t.Fatalf("active label = %q", got)
	}
	if got := generationObjectStatusLabel(model.GenerationObjectStatusOrphan); got != "orphan" {
		t.Fatalf("orphan label = %q", got)
	}
	if got := generationObjectStatusLabel(model.GenerationObjectStatusDeleted); got != "deleted" {
		t.Fatalf("deleted label = %q", got)
	}
	if got := generationObjectStatusLabel(model.GenerationObjectStatusHidden); got != "hidden" {
		t.Fatalf("hidden label = %q", got)
	}
}

func TestNormalizeGenerationObjectStatusFilter(t *testing.T) {
	if got := normalizeGenerationObjectStatusFilter(" orphan "); got != model.GenerationObjectStatusOrphan {
		t.Fatalf("orphan filter = %d", got)
	}
	if got := normalizeGenerationObjectStatusFilter("deleted"); got != model.GenerationObjectStatusDeleted {
		t.Fatalf("deleted filter = %d", got)
	}
	if got := normalizeGenerationObjectStatusFilter("hidden"); got != model.GenerationObjectStatusHidden {
		t.Fatalf("hidden filter = %d", got)
	}
	if got := normalizeGenerationObjectStatusFilter("unknown"); got != 0 {
		t.Fatalf("unknown filter = %d", got)
	}
}

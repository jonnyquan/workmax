package workagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanOutputsDirectorySinceSkipsControlFilesAndAttachesVariationMetadata(t *testing.T) {
	threadWorkspace := t.TempDir()
	outputs := filepath.Join(threadWorkspace, "outputs")
	if err := os.MkdirAll(filepath.Join(outputs, ".workagent"), 0o755); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputs, "bold.html"), []byte("<html>bold</html>"), 0o600); err != nil {
		t.Fatalf("write draft: %v", err)
	}
	manifest := `{"variations":[{"id":"bold","label":"Bold","stance":"bold","file_path":"bold.html","design_system_basename":"expressive-grid","asset_contract":"brand locked"}]}`
	if err := os.WriteFile(filepath.Join(outputs, ".workagent", "pass_1_variations.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	files := scanOutputsDirectorySince(threadWorkspace, time.Time{})
	if len(files) != 1 {
		t.Fatalf("files = %#v, want only the draft output file", files)
	}
	if files[0].Name != "bold.html" {
		t.Fatalf("file name = %q, want bold.html", files[0].Name)
	}
	if !strings.Contains(files[0].Description, `"kind":"workagent_variation_draft"`) ||
		!strings.Contains(files[0].Description, `"variation_id":"bold"`) ||
		!strings.Contains(files[0].Description, `"design_system_basename":"expressive-grid"`) {
		t.Fatalf("description missing variation metadata: %s", files[0].Description)
	}
}

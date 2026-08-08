package workagent

import (
	"fmt"
	"testing"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"
)

func TestArtifactViewFromThreadFile_OutputPPTX(t *testing.T) {
	file := workagentModel.ThreadFile{
		GraMODEL:   globals.GraMODEL{Id: 42},
		UID:        7,
		ThreadID:   99,
		FileName:   "deck.pptx",
		FilePath:   "outputs/deck.pptx",
		FileSize:   1234,
		FileSource: workagentModel.FileSourceOutput,
		FileHash:   "abc123",
	}

	got := ArtifactViewFromThreadFile(file)
	if got.ID != "thread-file-42" {
		t.Fatalf("id = %q, want thread-file-42", got.ID)
	}
	if got.ArtifactType != "deck" {
		t.Errorf("artifact type = %q, want deck", got.ArtifactType)
	}
	if got.OutputType != "pptx" {
		t.Errorf("output type = %q, want pptx", got.OutputType)
	}
	if got.PreviewType != "deck" {
		t.Errorf("preview type = %q, want deck", got.PreviewType)
	}
	if got.Preview.Mode != "pdf-companion-or-download" || !got.Preview.Inline {
		t.Errorf("preview capability = %#v, want deck companion preview", got.Preview)
	}
	if got.Status != "draft" {
		t.Errorf("status = %q, want draft for output files", got.Status)
	}
	if !containsStringWA(got.ExportTargets, "pdf") {
		t.Errorf("export targets = %v, want pdf", got.ExportTargets)
	}
}

func TestArtifactViewFromThreadFile_UploadReferenceImage(t *testing.T) {
	file := workagentModel.ThreadFile{
		GraMODEL:   globals.GraMODEL{Id: 9},
		UID:        7,
		ThreadID:   99,
		FileName:   "logo.jpeg",
		FilePath:   "uploads/logo.jpeg",
		FileSource: workagentModel.FileSourceUpload,
	}

	got := ArtifactViewFromThreadFile(file)
	if got.ArtifactType != "reference" {
		t.Errorf("upload artifact type = %q, want reference", got.ArtifactType)
	}
	if got.OutputType != "jpg" {
		t.Errorf("jpeg should normalize to jpg, got %q", got.OutputType)
	}
	if got.PreviewType != "image" {
		t.Errorf("preview type = %q, want image", got.PreviewType)
	}
	if got.Preview.Mode != "image" || !got.Preview.Inline {
		t.Errorf("preview capability = %#v, want inline image preview", got.Preview)
	}
	if got.Status != "reference" {
		t.Errorf("status = %q, want reference", got.Status)
	}
}

func TestListArtifactViewsByThread_ScopedAndOrdered(t *testing.T) {
	repo, db := newFileRepo(t)
	newerID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "newer.html"
		f.FilePath = "outputs/newer.html"
		f.FileSource = workagentModel.FileSourceOutput
	})
	seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 99
		f.ThreadID = 50
		f.FileName = "cross-tenant.png"
	})

	got, err := ListArtifactViewsByThread(repo, 5, 50)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (cross-tenant row must not leak)", len(got))
	}
	if got[0].FileID != newerID {
		t.Errorf("file id = %d, want %d", got[0].FileID, newerID)
	}
	if got[0].PreviewType != "html" {
		t.Errorf("preview type = %q, want html", got[0].PreviewType)
	}
	for _, target := range []string{"html", "png", "pdf", "mp4", "gif", "zip"} {
		if !containsStringWA(got[0].ExportTargets, target) {
			t.Errorf("html export targets = %v, want %s", got[0].ExportTargets, target)
		}
	}
}

func TestListArtifactViewsByThread_AssignsVersionsForRepeatedArtifacts(t *testing.T) {
	repo, db := newFileRepo(t)
	olderAt := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	newerAt := olderAt.Add(time.Hour)
	olderID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.DisplayName = "poster.png"
		f.FilePath = "outputs/poster-v1.png"
		f.FileSource = workagentModel.FileSourceOutput
		f.CreatedAt = olderAt
	})
	newerID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.DisplayName = "poster.png"
		f.FilePath = "outputs/poster-v2.png"
		f.FileSource = workagentModel.FileSourceOutput
		f.CreatedAt = newerAt
	})

	got, err := ListArtifactViewsByThread(repo, 5, 50)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].FileID != newerID || got[0].Version != 2 {
		t.Fatalf("newest artifact = id %d version %d, want id %d version 2", got[0].FileID, got[0].Version, newerID)
	}
	wantParent := "thread-file-" + uintToStringWA(olderID)
	if got[0].ParentArtifactID != wantParent {
		t.Errorf("newest parent = %q, want %q", got[0].ParentArtifactID, wantParent)
	}
	if got[1].FileID != olderID || got[1].Version != 1 {
		t.Fatalf("oldest artifact = id %d version %d, want id %d version 1", got[1].FileID, got[1].Version, olderID)
	}
}

func containsStringWA(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func uintToStringWA(value uint) string {
	return fmt.Sprintf("%d", value)
}

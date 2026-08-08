package storage

import (
	"strings"
	"testing"
)

// BuildAttachmentContentDisposition and maxMultipartParts sit in the
// download/upload hot path: every presigned-URL download with a custom
// filename and every multipart upload part count flows through them.
// Both are pure but untested; a silent change to the sanitisation rules
// or the part-count ceiling would either break Content-Disposition
// parsing in browsers or exceed S3's 10_000-part hard cap on large
// uploads. Pin the contracts.

func TestBuildAttachmentContentDisposition_EmptyOrWhitespaceReturnsBareAttachment(t *testing.T) {
	// Bare "attachment" (no filename) is the documented fallback — callers
	// pass an empty name when they want the browser to use its own default.
	for _, name := range []string{"", "   ", "\t", "\n"} {
		got := BuildAttachmentContentDisposition(name)
		if got != "attachment" {
			t.Errorf("BuildAttachmentContentDisposition(%q) = %q, want %q", name, got, "attachment")
		}
	}
}

func TestBuildAttachmentContentDisposition_PlainFilenameRoundTrips(t *testing.T) {
	got := BuildAttachmentContentDisposition("report.pdf")
	// Envelope: attachment; filename="<safe>"; filename*=UTF-8''<pct-encoded>
	// Plain ASCII round-trips unchanged through both slots.
	want := `attachment; filename="report.pdf"; filename*=UTF-8''report.pdf`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildAttachmentContentDisposition_SanitisesDangerousChars(t *testing.T) {
	// Quote, backslash, CR, LF must be replaced in the quoted filename
	// slot — leaving them in would let a caller break out of the quoted
	// string and inject a second Content-Disposition header.
	got := BuildAttachmentContentDisposition("weird\"name\\with\r\nbreaks.txt")
	if !strings.Contains(got, `filename="weird_name_with__breaks.txt"`) {
		t.Errorf("dangerous chars not sanitised in quoted slot: %q", got)
	}
	// The RFC-5987 slot pct-encodes rather than substituting, so the
	// original bytes are recoverable by the client.
	if !strings.Contains(got, `filename*=UTF-8''weird%22name%5Cwith%0D%0Abreaks.txt`) {
		t.Errorf("extended slot missing pct-encoded original: %q", got)
	}
}

func TestBuildAttachmentContentDisposition_ReplacesControlChars(t *testing.T) {
	// Every byte < 0x20 becomes '_' in the quoted slot. A single bell
	// (0x07) embedded in a filename was enough to corrupt some older
	// curl builds; the blanket control-char sweep defends against that.
	got := BuildAttachmentContentDisposition("a\x00b\x07c\x1fd.bin")
	if !strings.Contains(got, `filename="a_b_c_d.bin"`) {
		t.Errorf("control chars not replaced: %q", got)
	}
}

func TestBuildAttachmentContentDisposition_EncodesUnicodeInExtendedSlot(t *testing.T) {
	// Unicode filenames pass through the quoted slot unchanged (they're
	// not in the sanitise set, and `strings.Map` only rewrites the listed
	// runes). The RFC-5987 slot pct-encodes them so legacy clients that
	// only honour the quoted slot still see readable bytes, while modern
	// clients decode filename* per spec.
	got := BuildAttachmentContentDisposition("报告.pdf")
	if !strings.Contains(got, `filename="报告.pdf"`) {
		t.Errorf("unicode stripped from quoted slot: %q", got)
	}
	if !strings.Contains(got, `filename*=UTF-8''`) {
		t.Errorf("missing filename* extension: %q", got)
	}
	// url.PathEscape encodes multi-byte UTF-8 as %xx sequences.
	if !strings.Contains(got, "%E6%8A%A5%E5%91%8A.pdf") {
		t.Errorf("unicode not pct-encoded: %q", got)
	}
}

func TestBuildAttachmentContentDisposition_TrimsOuterWhitespace(t *testing.T) {
	// Leading/trailing whitespace is stripped before both slots — so
	// "  file.txt  " and "file.txt" yield identical headers. The trim is
	// what lets the empty-string branch also catch pure-whitespace input.
	a := BuildAttachmentContentDisposition("  file.txt  ")
	b := BuildAttachmentContentDisposition("file.txt")
	if a != b {
		t.Errorf("whitespace not trimmed: %q vs %q", a, b)
	}
}

func TestMaxMultipartParts_ExactMultiplesDontAddExtraPart(t *testing.T) {
	partSize := int64(16 * 1024 * 1024) // default 16 MiB
	// When size is an exact multiple of partSize, the ceiling branch must
	// NOT fire — otherwise we'd request one extra empty part and S3 would
	// reject the complete call.
	if got := maxMultipartParts(partSize, partSize); got != 1 {
		t.Errorf("exact 1x partSize = %d, want 1", got)
	}
	if got := maxMultipartParts(partSize*4, partSize); got != 4 {
		t.Errorf("exact 4x partSize = %d, want 4", got)
	}
}

func TestMaxMultipartParts_NonAlignedSizeCeilings(t *testing.T) {
	partSize := int64(10)
	// 21 bytes / 10 byte parts = 2 full parts + 1 byte remainder → 3 parts.
	if got := maxMultipartParts(21, partSize); got != 3 {
		t.Errorf("maxMultipartParts(21,10) = %d, want 3", got)
	}
	if got := maxMultipartParts(1, partSize); got != 1 {
		t.Errorf("maxMultipartParts(1,10) = %d, want 1 (single sub-partSize part)", got)
	}
}

func TestMaxMultipartParts_MinimumOneFloor(t *testing.T) {
	// A zero-byte object still counts as one part — the min-1 floor is
	// there because S3 requires at least one part in a multipart upload
	// even though callers shouldn't ordinarily hit that path.
	if got := maxMultipartParts(0, 1024); got != 1 {
		t.Errorf("zero-size upload = %d parts, want 1 (min floor)", got)
	}
}

func TestMaxMultipartParts_ZeroPartSizeFallsBackToDefault(t *testing.T) {
	// A zero or negative partSize flows through NormalizeMultipartPartSizeBytes,
	// which returns 16 MiB as the default. Without this guard, integer
	// division by zero would panic at runtime.
	defaultPartSize := int64(16 * 1024 * 1024)

	// With default 16 MiB, a 32 MiB upload → 2 parts.
	if got := maxMultipartParts(defaultPartSize*2, 0); got != 2 {
		t.Errorf("maxMultipartParts(32MiB, 0) = %d, want 2 (uses 16 MiB default)", got)
	}
	// Negative partSize hits the same fallback.
	if got := maxMultipartParts(defaultPartSize, -1); got != 1 {
		t.Errorf("maxMultipartParts(16MiB, -1) = %d, want 1", got)
	}
}

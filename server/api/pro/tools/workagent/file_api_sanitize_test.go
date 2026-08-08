package workagent

import (
	"testing"
)

// sanitizeFileName: NFC normalization must collapse the two common
// Unicode encodings of the same visually-identical filename to one
// byte sequence so the upload-dedup branch in AddFile (keyed on
// uid+thread+file_name) doesn't miss them as different rows.
//
// Test inputs use the canonical NFC vs NFD pairs:
//
//	"café"   with U+00E9 (precomposed)        — NFC: e + acute
//	"café"   with U+0065 + U+0301 (decomposed) — NFD: e, then combining acute
//
// macOS-native filesystem APIs hand back NFD; Linux + most upload
// flows produce NFC. Without normalization, the same file uploaded
// twice from different OSes lands as two rows.
func TestSanitizeFileName_CollapsesNFCAndNFD(t *testing.T) {
	api := &AIChatApiNew{}

	// NFC: U+00E9 = "é" precomposed (single code point, 2 bytes UTF-8)
	nfc := "café.pdf"
	// NFD: e + U+0301 (combining acute accent), 3 bytes UTF-8 for the
	// "é" portion split as e + combining mark
	nfd := "café.pdf"

	gotNFC := api.sanitizeFileName(nfc)
	gotNFD := api.sanitizeFileName(nfd)

	if gotNFC != gotNFD {
		t.Errorf("NFC and NFD inputs must collapse to the same output\nNFC: %q -> %q\nNFD: %q -> %q",
			nfc, gotNFC, nfd, gotNFD)
	}

	// Both should preserve the visible "é" — neither aggressive
	// strip-non-ASCII nor a buggy normalization should drop it.
	if len(gotNFC) == 0 {
		t.Errorf("normalization stripped the entire filename: %q -> %q", nfc, gotNFC)
	}
}

// CJK normalization variants — Chinese characters can come back from
// HFS+ in NFD-style decomposed form; pin that they collapse cleanly.
func TestSanitizeFileName_CJKNormalization(t *testing.T) {
	api := &AIChatApiNew{}

	// Some CJK ideographs have decomposed variants. The "ファイル"
	// (Japanese "file") test below uses only precomposed chars but
	// the property the test pins is: NFC normalization is a no-op on
	// already-NFC input. Bumping NFC twice doesn't double-modify.
	input := "ファイル.txt"
	first := api.sanitizeFileName(input)
	second := api.sanitizeFileName(first)

	if first != second {
		t.Errorf("sanitizeFileName not idempotent on CJK: %q -> %q -> %q", input, first, second)
	}
}

// Pin that legit ASCII filenames still go through the regex+strip
// pipeline correctly — normalization step is additive, doesn't break
// the existing sanitisation contract. Expectations match the current
// behaviour (regex strips disallowed chars, then Trim removes leading/
// trailing punctuation, then "" → "file" fallback).
func TestSanitizeFileName_AsciiPreserved(t *testing.T) {
	api := &AIChatApiNew{}
	cases := map[string]string{
		"hello.pdf":        "hello.pdf",
		"my report.docx":   "my_report.docx",
		"weird&chars!.txt": "weird_chars.txt", // trailing _ stripped by Trim(._- )
		"  spaces  .csv":   "spaces.csv",
		// `.hidden` parses as extension only (filepath.Ext returns ".hidden"),
		// so base is empty → "file" fallback + ".hidden" extension.
		".hidden": "file.hidden",
	}
	for input, want := range cases {
		got := api.sanitizeFileName(input)
		if got != want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", input, got, want)
		}
	}
}

package tools

import (
	"testing"
)

func TestIssueAndLookupQuote(t *testing.T) {
	rec := IssueQuote(42, "video", "veo-3.1", map[string]interface{}{
		"duration":   5,
		"resolution": "1080p",
	}, 28, "official")
	defer ConsumeQuote(rec.ID)

	if rec.ID == "" {
		t.Fatal("expected generated quote id")
	}
	if rec.Credits != 28 {
		t.Fatalf("credits mismatch: %d", rec.Credits)
	}

	got, ok := LookupQuote(rec.ID)
	if !ok {
		t.Fatal("expected lookup to find quote")
	}
	if got.Fingerprint != rec.Fingerprint {
		t.Fatal("fingerprint mismatch on lookup")
	}
}

func TestVerifyQuoteMatchesFingerprintRegardlessOfKeyOrder(t *testing.T) {
	params := map[string]interface{}{
		"duration":   float64(5),
		"resolution": "1080p",
	}
	rec := IssueQuote(7, "video", "veo-3.1", params, 28, "official")
	defer ConsumeQuote(rec.ID)

	reordered := map[string]interface{}{
		"resolution": "1080p",
		"duration":   float64(5),
	}
	if _, ok := VerifyQuote(rec.ID, 7, "video", "veo-3.1", reordered); !ok {
		t.Fatal("expected VerifyQuote to match reordered params")
	}
}

func TestVerifyQuoteRejectsTampering(t *testing.T) {
	params := map[string]interface{}{"duration": float64(5)}
	rec := IssueQuote(7, "video", "veo-3.1", params, 28, "official")
	defer ConsumeQuote(rec.ID)

	cases := []struct {
		name   string
		uid    uint
		mode   string
		model  string
		params map[string]interface{}
	}{
		{"wrong uid", 8, "video", "veo-3.1", params},
		{"wrong mode", 7, "image", "veo-3.1", params},
		{"wrong model", 7, "video", "veo-3", params},
		{"wrong params", 7, "video", "veo-3.1", map[string]interface{}{"duration": float64(10)}},
	}
	for _, tc := range cases {
		if _, ok := VerifyQuote(rec.ID, tc.uid, tc.mode, tc.model, tc.params); ok {
			t.Fatalf("%s: expected VerifyQuote to reject", tc.name)
		}
	}
}

func TestCanonicalImageQuoteParamsIncludesQuality(t *testing.T) {
	params := CanonicalImageQuoteParams(1, "1k", "low")
	rec := IssueQuote(7, "image", "gpt-image-2", params, 2, "official")
	defer ConsumeQuote(rec.ID)

	if _, ok := VerifyQuote(rec.ID, 7, "image", "gpt-image-2", CanonicalImageQuoteParams(1, "1k", "low")); !ok {
		t.Fatal("expected quote to verify with matching quality")
	}
	if _, ok := VerifyQuote(rec.ID, 7, "image", "gpt-image-2", CanonicalImageQuoteParams(1, "1k", "medium")); ok {
		t.Fatal("expected quote to reject changed quality")
	}
}

func TestConsumeQuoteRemovesRecord(t *testing.T) {
	rec := IssueQuote(1, "image", "nano-banana", nil, 3, "official")
	if !ConsumeQuote(rec.ID) {
		t.Fatal("expected first consume to succeed")
	}
	if _, ok := LookupQuote(rec.ID); ok {
		t.Fatal("expected quote to be gone after consume")
	}
	if ConsumeQuote(rec.ID) {
		t.Fatal("expected second consume to return false")
	}
}

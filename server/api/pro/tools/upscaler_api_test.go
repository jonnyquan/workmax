package tools

import "testing"

func TestNormalizeUpscalerAspectRatio(t *testing.T) {
	if got := normalizeUpscalerAspectRatio(""); got != upscalerDefaultAspectRatio {
		t.Fatalf("normalizeUpscalerAspectRatio(\"\") = %q, want %q", got, upscalerDefaultAspectRatio)
	}

	if got := normalizeUpscalerAspectRatio("16:9"); got != "16:9" {
		t.Fatalf("normalizeUpscalerAspectRatio(\"16:9\") = %q", got)
	}
}

func TestMergeUpscalerEnhanceFlags(t *testing.T) {
	tests := []struct {
		name string
		req  UpscalerGenerateRequest
		want bool
	}{
		{name: "all_false", want: false},
		{name: "enhanceFace_top", req: UpscalerGenerateRequest{EnhanceFace: true}, want: true},
		{
			name: "enhanceFace_in_params",
			req:  UpscalerGenerateRequest{Params: map[string]interface{}{"enhanceFace": true}},
			want: true,
		},
		{
			// Legacy: older clients only stored the enhanceQuality alias inside
			// the params map. Reading it here keeps those payloads working.
			name: "enhanceQuality_in_params_legacy",
			req:  UpscalerGenerateRequest{Params: map[string]interface{}{"enhanceQuality": true}},
			want: true,
		},
		{
			name: "mixed_any_true_wins",
			req:  UpscalerGenerateRequest{EnhanceFace: false, Params: map[string]interface{}{"enhanceQuality": true, "enhanceFace": false}},
			want: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeUpscalerEnhanceFlags(&tc.req); got != tc.want {
				t.Errorf("mergeUpscalerEnhanceFlags() = %v, want %v", got, tc.want)
			}
		})
	}

	if got := mergeUpscalerEnhanceFlags(nil); got {
		t.Errorf("mergeUpscalerEnhanceFlags(nil) = true, want false")
	}
}

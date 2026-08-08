package tools

import "testing"

func TestCanvasChatCost(t *testing.T) {
	cases := []struct {
		name            string
		isThinking      bool
		isFast          bool
		useWeb          bool
		attachmentCount int
		want            int
	}{
		{"base only", false, false, false, 0, 2},
		{"thinking replaces base", true, false, false, 0, 6},
		{"fast adds one", false, true, false, 0, 3},
		{"web adds two", false, false, true, 0, 4},
		{"attachment capped at five", false, false, false, 10, 7},
		{"all modifiers stack", true, true, true, 3, 12},
		{"negative attachment count treated as zero", false, false, false, -5, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CanvasChatCost(tc.isThinking, tc.isFast, tc.useWeb, tc.attachmentCount)
			if got != tc.want {
				t.Fatalf("CanvasChatCost(%v,%v,%v,%d) = %d, want %d",
					tc.isThinking, tc.isFast, tc.useWeb, tc.attachmentCount, got, tc.want)
			}
		})
	}
}

func TestCanvasOptimizePromptCost(t *testing.T) {
	if got := CanvasOptimizePromptCost(); got != 2 {
		t.Fatalf("CanvasOptimizePromptCost() = %d, want 2", got)
	}
}


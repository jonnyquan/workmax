package tools

import (
	"server/model"
	"testing"
)

func TestQuoteVideoCreditsVeo1080pDuration(t *testing.T) {
	tests := []struct {
		name          string
		modelID       string
		duration      int
		resolution    string
		hasStartFrame bool
		hasEndFrame   bool
		wantBillable  int
		wantCredits   int
	}{
		{
			// Fast rate is half ($0.10/s vs $0.20/s) so 8s × $0.10 = $0.80 → 60 credits.
			name:         "veo31_fast 1080p 4s normalizes to 8s",
			modelID:      model.VEO_31_FAST,
			duration:     4,
			resolution:   "1080p",
			wantBillable: 8,
			wantCredits:  60,
		},
		{
			name:         "veo31_fast 1080p 6s normalizes to 8s",
			modelID:      model.VEO_31_FAST,
			duration:     6,
			resolution:   "1080p",
			wantBillable: 8,
			wantCredits:  60,
		},
		{
			name:         "veo31 1080p 8s bills 8s",
			modelID:      model.VEO_31,
			duration:     8,
			resolution:   "1080p",
			wantBillable: 8,
			wantCredits:  120,
		},
		{
			name:          "veo31_fast reference forces 8s",
			modelID:       model.VEO_31_FAST,
			duration:      4,
			resolution:    "1080p",
			hasStartFrame: true,
			wantBillable:  8,
			wantCredits:   60,
		},
		{
			name:         "veo31 4k still bills 8s at 4k rate",
			modelID:      model.VEO_31,
			duration:     4,
			resolution:   "4k",
			wantBillable: 8,
			wantCredits:  230,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quote, err := QuoteVideoCredits(tt.modelID, VideoQuoteOptions{
				Duration:      tt.duration,
				Resolution:    tt.resolution,
				HasStartFrame: tt.hasStartFrame,
				HasEndFrame:   tt.hasEndFrame,
			})
			if err != nil {
				t.Fatalf("QuoteVideoCredits() error = %v", err)
			}
			if quote.BillableDuration != tt.wantBillable {
				t.Fatalf("QuoteVideoCredits() billableDuration = %d, want %d", quote.BillableDuration, tt.wantBillable)
			}
			if quote.Credits != tt.wantCredits {
				t.Fatalf("QuoteVideoCredits() credits = %d, want %d", quote.Credits, tt.wantCredits)
			}
		})
	}
}

func TestValidateVideoRequestVeoEndFrameRequiresStartFrame(t *testing.T) {
	err := ValidateVideoRequest(model.VEO_31, VideoQuoteOptions{
		Duration:   6,
		Resolution: "1080p",
	}, "frames", false, true)
	if err == nil {
		t.Fatal("ValidateVideoRequest() expected error for end frame without start frame")
	}
}

func TestValidateVideoRequestVeoRejects1080pNonEightSecondDuration(t *testing.T) {
	err := ValidateVideoRequest(model.VEO_31_FAST, VideoQuoteOptions{
		Duration:   6,
		Resolution: "1080p",
	}, "frames", false, false)
	if err == nil {
		t.Fatal("ValidateVideoRequest() expected error for 1080p Veo duration that is not 8 seconds")
	}
}

func TestValidateVideoRequestRejectsInvalidMotionOrientation(t *testing.T) {
	err := ValidateVideoRequest(model.KLING_2_6, VideoQuoteOptions{
		Duration:          5,
		AspectRatio:       "16:9",
		MotionMode:        "std",
		MotionOrientation: "video",
	}, "motion", false, false)
	if err == nil {
		t.Fatal("ValidateVideoRequest() expected error for unsupported motionOrientation")
	}
}

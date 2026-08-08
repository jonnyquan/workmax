package provider

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestVeoBuildPredictPayloadUsesNumericDurationSeconds(t *testing.T) {
	provider := &VeoProvider{}

	payload, err := provider.buildPredictPayload(&GenerationRequest{
		Prompt:     "test prompt",
		Duration:   "6s",
		Resolution: "1080p",
		Width:      1920,
		Height:     1080,
	})
	if err != nil {
		t.Fatalf("buildPredictPayload returned error: %v", err)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	parameters, ok := body["parameters"].(map[string]interface{})
	if !ok {
		t.Fatalf("payload missing parameters: %#v", body)
	}

	durationSeconds, ok := parameters["durationSeconds"]
	if !ok {
		t.Fatalf("payload missing durationSeconds: %#v", parameters)
	}
	if _, ok := durationSeconds.(float64); !ok {
		t.Fatalf("durationSeconds should be numeric, got %T (%v)", durationSeconds, durationSeconds)
	}
}

func TestVeoBuildPredictPayloadUsesBytesBase64Encoded(t *testing.T) {
	// Regression: Veo predictLongRunning rejects the chat-style {inlineData:{...}}
	// shape with HTTP 400 "inlineData isn't supported by this model". The image
	// blob must be flat: {bytesBase64Encoded, mimeType}.
	provider := &VeoProvider{}

	cases := []struct {
		name string
		req  *GenerationRequest
		path []string
	}{
		{
			name: "image-to-video uses startFrame as image",
			req: &GenerationRequest{
				Prompt:     "test",
				StartFrame: "https://example.com/start.png",
				ReferenceImages: []ReferenceImageData{
					{MimeType: "image/png", Data: "AAAA"},
				},
			},
			path: []string{"instances", "0", "image"},
		},
		{
			name: "endFrame populates lastFrame",
			req: &GenerationRequest{
				Prompt:     "test",
				StartFrame: "https://example.com/start.png",
				EndFrame:   "https://example.com/end.png",
				ReferenceImages: []ReferenceImageData{
					{MimeType: "image/png", Data: "AAAA"},
					{MimeType: "image/png", Data: "BBBB"},
				},
			},
			path: []string{"instances", "0", "lastFrame"},
		},
		{
			name: "referenceImages entries use flat blob",
			req: &GenerationRequest{
				Prompt: "test",
				ReferenceImages: []ReferenceImageData{
					{MimeType: "image/png", Data: "AAAA"},
				},
			},
			path: []string{"instances", "0", "referenceImages", "0", "image"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := provider.buildPredictPayload(tc.req)
			if err != nil {
				t.Fatalf("buildPredictPayload returned error: %v", err)
			}

			var body interface{}
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatalf("failed to unmarshal payload: %v", err)
			}

			node := body
			for _, key := range tc.path {
				switch v := node.(type) {
				case map[string]interface{}:
					next, ok := v[key]
					if !ok {
						t.Fatalf("payload missing key %q at path %v: %#v", key, tc.path, body)
					}
					node = next
				case []interface{}:
					var idx int
					if _, err := fmt.Sscanf(key, "%d", &idx); err != nil {
						t.Fatalf("non-numeric index %q in path: %v", key, err)
					}
					if idx >= len(v) {
						t.Fatalf("index %d out of range in path %v: %#v", idx, tc.path, body)
					}
					node = v[idx]
				default:
					t.Fatalf("unexpected node type %T at key %q: %#v", node, key, body)
				}
			}

			blob, ok := node.(map[string]interface{})
			if !ok {
				t.Fatalf("expected blob map at %v, got %T (%v)", tc.path, node, node)
			}
			if _, found := blob["inlineData"]; found {
				t.Fatalf("payload uses unsupported inlineData shape at %v: %#v", tc.path, blob)
			}
			if _, ok := blob["bytesBase64Encoded"].(string); !ok {
				t.Fatalf("blob at %v missing bytesBase64Encoded string: %#v", tc.path, blob)
			}
			if _, ok := blob["mimeType"].(string); !ok {
				t.Fatalf("blob at %v missing mimeType string: %#v", tc.path, blob)
			}
		})
	}
}

func TestVeoResolveDurationReturnsIntegers(t *testing.T) {
	provider := &VeoProvider{}

	testCases := []struct {
		name       string
		duration   string
		forceEight bool
		want       int
	}{
		{name: "4s", duration: "4s", want: 4},
		{name: "5s normalizes to 6", duration: "5s", want: 6},
		{name: "6s", duration: "6s", want: 6},
		{name: "8s", duration: "8s", want: 8},
		{name: "force eight", duration: "4s", forceEight: true, want: 8},
		{name: "1080p provider path uses force eight", duration: "6s", forceEight: true, want: 8},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := provider.resolveDuration(tc.duration, tc.forceEight)
			if got != tc.want {
				t.Fatalf("resolveDuration(%q, %v) = %d, want %d", tc.duration, tc.forceEight, got, tc.want)
			}
		})
	}
}

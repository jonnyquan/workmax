package provider

import (
	"encoding/json"
	"testing"
)

func TestSeedance2PayloadKeepsGenericReferencesWithExplicitFrames(t *testing.T) {
	p := &SeedanceProvider{}
	payload, err := p.buildCreatePayload(&GenerationRequest{
		Model:      "seedance-2",
		Prompt:     "cinematic product shot",
		StartFrame: "/uploads/references/uid/42/start.png",
		EndFrame:   "/uploads/references/uid/42/end.png",
		ReferenceImages: []ReferenceImageData{
			{ID: "start-frame", MimeType: "image/png", Data: "start"},
			{ID: "end-frame", MimeType: "image/png", Data: "end"},
			{ID: "product-ref", MimeType: "image/png", Data: "product"},
		},
	}, defaultSeedance2Model)
	if err != nil {
		t.Fatalf("buildCreatePayload() error = %v", err)
	}

	var body struct {
		Content []map[string]interface{} `json:"content"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	roles := map[string]int{}
	for _, item := range body.Content {
		if role, ok := item["role"].(string); ok {
			roles[role]++
		}
	}
	if roles["first_frame"] != 1 || roles["last_frame"] != 1 || roles["reference_image"] != 1 {
		t.Fatalf("roles = %#v, want one first_frame, one last_frame, one reference_image", roles)
	}
}

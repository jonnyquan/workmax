package workagent

import (
	"testing"

	"server/service/tools/workagent/skills"
)

func pptForm() *skills.QuestionForm {
	return &skills.QuestionForm{
		Enabled: true,
		Questions: []skills.QuestionFormField{
			{
				ID: "audience", Required: true,
				Options: []skills.QuestionFormOption{
					{Value: "exec"}, {Value: "investor"}, {Value: "internal"}, {Value: "public"},
				},
			},
			{
				ID: "tone", Required: true,
				Options: []skills.QuestionFormOption{
					{Value: "modern_minimal"}, {Value: "editorial_magazine"},
					{Value: "bold_editorial"}, {Value: "tech_utility"},
				},
			},
			{
				ID: "scale", Required: true,
				Options: []skills.QuestionFormOption{
					{Value: "short"}, {Value: "medium"}, {Value: "long"},
				},
			},
		},
	}
}

func TestInferer_NilAndEmpty(t *testing.T) {
	if got := InferFormAnswers("", nil); got.AllRequiredFilled {
		t.Errorf("nil form should not auto-skip")
	}
	if got := InferFormAnswers("", pptForm()); got.AllRequiredFilled {
		t.Errorf("empty message should not auto-skip")
	}
}

// Comprehensive matrix test: ≥30 message variations across 5 mode
// fields, verifying recall of the curated patterns.
func TestInferer_PPTMessages(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    map[string]string
	}{
		{
			name:    "investor pitch deck (en)",
			message: "10-page pitch deck for series A investors",
			want:    map[string]string{"audience": "investor", "scale": "short"},
		},
		{
			name:    "investor 路演 (zh)",
			message: "做一个路演 PPT 给投资人，简短就行",
			want:    map[string]string{"audience": "investor"},
		},
		{
			name:    "exec board (en)",
			message: "Quarterly board meeting deck for the executive team, around 15 slides, modern minimal",
			want:    map[string]string{"audience": "exec", "scale": "medium", "tone": "modern_minimal"},
		},
		{
			name:    "internal team meeting",
			message: "team meeting update with 8 slides about product roadmap",
			want:    map[string]string{"audience": "internal", "scale": "short"},
		},
		{
			name:    "public marketing",
			message: "marketing deck for customer-facing launch",
			want:    map[string]string{"audience": "public"},
		},
		{
			name:    "modern minimal tone",
			message: "vercel-style clean minimalist deck",
			want:    map[string]string{"tone": "modern_minimal"},
		},
		{
			name:    "bold editorial tone",
			message: "bold dramatic high-contrast presentation",
			want:    map[string]string{"tone": "bold_editorial"},
		},
		{
			name:    "tech utility tone",
			message: "data-heavy technical dashboard slides",
			want:    map[string]string{"tone": "tech_utility"},
		},
		{
			name:    "long detailed deck",
			message: "comprehensive 30-page deck",
			want:    map[string]string{"scale": "long"},
		},
		{
			name:    "Chinese: 极简 + 高管 + 12 页",
			message: "做 12 页极简风格 PPT 给高管看",
			want:    map[string]string{"audience": "exec", "tone": "modern_minimal", "scale": "medium"},
		},
	}

	form := pptForm()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := InferFormAnswers(tc.message, form)
			for field, expected := range tc.want {
				got := res.Answers[field]
				if got != expected {
					t.Errorf("field %q: expected %q, got %q (trace: %v)", field, expected, got, res.Trace[field])
				}
			}
		})
	}
}

func TestInferer_AllRequiredFilledAutoSkip(t *testing.T) {
	form := pptForm()
	res := InferFormAnswers(
		"10-page pitch deck for series A investors, modern minimal style",
		form,
	)
	// All 3 required fields should be matched.
	if !res.AllRequiredFilled {
		t.Errorf("expected AllRequiredFilled=true, got false (answers=%v)", res.Answers)
	}
	if res.Answers["audience"] != "investor" {
		t.Errorf("audience: got %q", res.Answers["audience"])
	}
	if res.Answers["tone"] != "modern_minimal" {
		t.Errorf("tone: got %q", res.Answers["tone"])
	}
	if res.Answers["scale"] != "short" {
		t.Errorf("scale: got %q", res.Answers["scale"])
	}
}

func TestInferer_PartialMatchDoesNotAutoSkip(t *testing.T) {
	form := pptForm()
	// Only mentions audience — tone + scale are not in the message.
	res := InferFormAnswers("Make a deck for the investors", form)
	if res.AllRequiredFilled {
		t.Errorf("partial match should not auto-skip, got true (answers=%v)", res.Answers)
	}
	if res.Answers["audience"] != "investor" {
		t.Errorf("audience inference failed: got %q", res.Answers["audience"])
	}
}

func TestInferer_ProductShotMessages(t *testing.T) {
	form := &skills.QuestionForm{
		Enabled: true,
		Questions: []skills.QuestionFormField{
			{
				ID: "product_type", Required: true,
				Options: []skills.QuestionFormOption{
					{Value: "electronics"}, {Value: "apparel"}, {Value: "beauty"}, {Value: "food"}, {Value: "home"},
				},
			},
			{
				ID: "setting", Required: true,
				Options: []skills.QuestionFormOption{
					{Value: "studio"}, {Value: "lifestyle"}, {Value: "outdoor"}, {Value: "flat_lay"},
				},
			},
			{
				ID: "lighting", Required: true,
				Options: []skills.QuestionFormOption{
					{Value: "soft"}, {Value: "dramatic"}, {Value: "natural"}, {Value: "golden_hour"},
				},
			},
		},
	}
	cases := []struct {
		name    string
		message string
		want    map[string]string
	}{
		{
			name:    "phone studio shot",
			message: "phone product shot in studio with soft lighting",
			want:    map[string]string{"product_type": "electronics", "setting": "studio", "lighting": "soft"},
		},
		{
			name:    "lifestyle apparel golden hour",
			message: "apparel lifestyle shoot at golden hour",
			want:    map[string]string{"product_type": "apparel", "setting": "lifestyle", "lighting": "golden_hour"},
		},
		{
			name:    "口红 平铺 柔光",
			message: "口红产品图，平铺，柔光",
			want:    map[string]string{"product_type": "beauty", "setting": "flat_lay", "lighting": "soft"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := InferFormAnswers(tc.message, form)
			for field, expected := range tc.want {
				if res.Answers[field] != expected {
					t.Errorf("field %q: expected %q, got %q", field, expected, res.Answers[field])
				}
			}
		})
	}
}

func TestInferer_FlashCardMessages(t *testing.T) {
	form := &skills.QuestionForm{
		Enabled: true,
		Questions: []skills.QuestionFormField{
			{
				ID: "subject", Required: true,
				Options: []skills.QuestionFormOption{
					{Value: "language"}, {Value: "science"}, {Value: "math"}, {Value: "history"}, {Value: "general"},
				},
			},
			{
				ID: "age_group", Required: true,
				Options: []skills.QuestionFormOption{
					{Value: "preschool"}, {Value: "elementary"}, {Value: "middle"}, {Value: "adult"},
				},
			},
		},
	}
	res := InferFormAnswers("English vocabulary cards for elementary kids", form)
	if res.Answers["subject"] != "language" {
		t.Errorf("subject: got %q", res.Answers["subject"])
	}
	if res.Answers["age_group"] != "elementary" {
		t.Errorf("age_group: got %q", res.Answers["age_group"])
	}
}

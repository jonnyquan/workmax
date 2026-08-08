package skills

import (
	"strings"
	"testing"
)

// validateManifest sweeps. Goal is to make a malformed skill.yaml
// produce a clear parse-time error with a field name in it, instead
// of leaking through into a confusing runtime fs error at Build time.
func TestValidateManifest_RejectsMalformedManifests(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(m *Manifest)
		wantSubstr string
	}{
		{
			name:       "missing name",
			mutate:     func(m *Manifest) { m.Name = "" },
			wantSubstr: "name is required",
		},
		{
			name:       "name with slash",
			mutate:     func(m *Manifest) { m.Name = "ppt/evil" },
			wantSubstr: "must not contain path separators",
		},
		{
			name:       "name with parent traversal",
			mutate:     func(m *Manifest) { m.Name = "ppt..secret" },
			wantSubstr: "must not contain '..'",
		},
		{
			name:       "description over cap",
			mutate:     func(m *Manifest) { m.Description = strings.Repeat("x", maxDescriptionLen+1) },
			wantSubstr: "description exceeds",
		},
		{
			name:       "shared reference with slash",
			mutate:     func(m *Manifest) { m.References.Shared = []string{"../etc/passwd"} },
			wantSubstr: "must not contain path separators",
		},
		{
			name:       "duplicate shared reference",
			mutate:     func(m *Manifest) { m.References.Shared = []string{"asset-protocol", "asset-protocol"} },
			wantSubstr: `references.shared[1] "asset-protocol" is duplicated`,
		},
		{
			name:       "local reference with backslash",
			mutate:     func(m *Manifest) { m.References.Local = []string{"a\\b"} },
			wantSubstr: "must not contain path separators",
		},
		{
			name:       "duplicate local reference",
			mutate:     func(m *Manifest) { m.References.Local = []string{"checklist", "checklist"} },
			wantSubstr: `references.local[1] "checklist" is duplicated`,
		},
		{
			name: "script path traversal",
			mutate: func(m *Manifest) {
				m.Scripts = []ScriptDescriptor{{Name: "evil", Path: "../../etc/passwd", When: "post_generation"}}
			},
			wantSubstr: "must not contain '..' segments",
		},
		{
			name: "script absolute path",
			mutate: func(m *Manifest) {
				m.Scripts = []ScriptDescriptor{{Name: "evil", Path: "/etc/passwd", When: "post_generation"}}
			},
			wantSubstr: "must be relative",
		},
		{
			name: "script backslash path",
			mutate: func(m *Manifest) {
				m.Scripts = []ScriptDescriptor{{Name: "x", Path: "scripts\\x.py", When: "post_generation"}}
			},
			wantSubstr: "must use '/' (not '\\') as separator",
		},
		{
			name: "script missing when",
			mutate: func(m *Manifest) {
				m.Scripts = []ScriptDescriptor{{Name: "x", Path: "scripts/x.py"}}
			},
			wantSubstr: `scripts[0].when "" is not supported`,
		},
		{
			name: "unsupported script when",
			mutate: func(m *Manifest) {
				m.Scripts = []ScriptDescriptor{{Name: "x", Path: "scripts/x.py", When: "pre_send"}}
			},
			wantSubstr: `scripts[0].when "pre_send" is not supported`,
		},
		{
			name: "duplicate script name",
			mutate: func(m *Manifest) {
				m.Scripts = []ScriptDescriptor{
					{Name: "validate", Path: "scripts/validate.py", When: "post_generation"},
					{Name: "validate", Path: "scripts/validate-other.py", When: "post_generation"},
				}
			},
			wantSubstr: `scripts[1].name "validate" is duplicated`,
		},
		{
			name: "duplicate script path",
			mutate: func(m *Manifest) {
				m.Scripts = []ScriptDescriptor{
					{Name: "validate", Path: "scripts/validate.py", When: "post_generation"},
					{Name: "postcheck", Path: "scripts/validate.py", When: "post_generation"},
				}
			},
			wantSubstr: `scripts[1].path "scripts/validate.py" is duplicated`,
		},
		{
			name: "required input missing kind",
			mutate: func(m *Manifest) {
				m.RequiredInputs = []InputRequirement{{Kind: "", When: ""}}
			},
			wantSubstr: "required_inputs[0].kind is required",
		},
		{
			name: "required input unsafe when",
			mutate: func(m *Manifest) {
				m.RequiredInputs = []InputRequirement{{Kind: "brand_assets", When: "../brand"}}
			},
			wantSubstr: "required_inputs[0].when must not contain path separators",
		},
		{
			name: "required input duplicate kind and when",
			mutate: func(m *Manifest) {
				m.RequiredInputs = []InputRequirement{
					{Kind: "brand_assets", When: "brand_specified"},
					{Kind: "brand_assets", When: "brand_specified"},
				}
			},
			wantSubstr: `required_inputs[1] "brand_assets" is duplicated`,
		},
		{
			name: "artifact primary type missing",
			mutate: func(m *Manifest) {
				m.Artifacts = &ArtifactMetadata{
					OutputTypes:  []string{"pptx"},
					PreviewTypes: []string{"deck"},
				}
			},
			wantSubstr: "artifacts.primary_type is required",
		},
		{
			name: "artifact output types missing",
			mutate: func(m *Manifest) {
				m.Artifacts = &ArtifactMetadata{
					PrimaryType:  "deck",
					PreviewTypes: []string{"deck"},
				}
			},
			wantSubstr: "artifacts.output_types is required",
		},
		{
			name: "artifact preview types missing",
			mutate: func(m *Manifest) {
				m.Artifacts = &ArtifactMetadata{
					PrimaryType: "deck",
					OutputTypes: []string{"pptx"},
				}
			},
			wantSubstr: "artifacts.preview_types is required",
		},
		{
			name: "artifact export targets missing",
			mutate: func(m *Manifest) {
				m.Artifacts = &ArtifactMetadata{
					PrimaryType:     "deck",
					OutputTypes:     []string{"pptx"},
					PreviewTypes:    []string{"deck"},
					CritiqueAnchors: []string{"craft"},
				}
			},
			wantSubstr: "artifacts.export_targets is required",
		},
		{
			name: "artifact critique anchors missing",
			mutate: func(m *Manifest) {
				m.Artifacts = &ArtifactMetadata{
					PrimaryType:   "deck",
					OutputTypes:   []string{"pptx"},
					PreviewTypes:  []string{"deck"},
					ExportTargets: []string{"pptx"},
				}
			},
			wantSubstr: "artifacts.critique_anchors is required",
		},
		{
			name: "artifact output type with slash",
			mutate: func(m *Manifest) {
				m.Artifacts = &ArtifactMetadata{
					PrimaryType:     "deck",
					OutputTypes:     []string{"pptx/evil"},
					PreviewTypes:    []string{"deck"},
					ExportTargets:   []string{"pptx"},
					CritiqueAnchors: []string{"craft"},
				}
			},
			wantSubstr: "must not contain path separators",
		},
		{
			name: "unsupported artifact output type",
			mutate: func(m *Manifest) {
				m.Artifacts = &ArtifactMetadata{
					PrimaryType:     "deck",
					OutputTypes:     []string{"exe"},
					PreviewTypes:    []string{"deck"},
					ExportTargets:   []string{"exe"},
					CritiqueAnchors: []string{"craft"},
				}
			},
			wantSubstr: `artifacts.output_types[0] "exe" is not supported`,
		},
		{
			name: "unsupported artifact preview type",
			mutate: func(m *Manifest) {
				m.Artifacts = &ArtifactMetadata{
					PrimaryType:     "deck",
					OutputTypes:     []string{"pptx"},
					PreviewTypes:    []string{"browser"},
					ExportTargets:   []string{"pptx"},
					CritiqueAnchors: []string{"craft"},
				}
			},
			wantSubstr: `artifacts.preview_types[0] "browser" is not supported`,
		},
		{
			name: "unsupported artifact export target",
			mutate: func(m *Manifest) {
				m.Artifacts = &ArtifactMetadata{
					PrimaryType:     "deck",
					OutputTypes:     []string{"pptx"},
					PreviewTypes:    []string{"deck"},
					ExportTargets:   []string{"tar"},
					CritiqueAnchors: []string{"craft"},
				}
			},
			wantSubstr: `artifacts.export_targets[0] "tar" is not supported`,
		},
		{
			name: "unsupported critique anchor",
			mutate: func(m *Manifest) {
				m.Artifacts = &ArtifactMetadata{
					PrimaryType:     "deck",
					OutputTypes:     []string{"pptx"},
					PreviewTypes:    []string{"deck"},
					ExportTargets:   []string{"pptx"},
					CritiqueAnchors: []string{"vibes"},
				}
			},
			wantSubstr: `artifacts.critique_anchors[0] "vibes" is not supported`,
		},
		{
			name: "enabled question form without questions",
			mutate: func(m *Manifest) {
				m.QuestionForm = &QuestionForm{Enabled: true}
			},
			wantSubstr: "question_form.questions is required",
		},
		{
			name: "select question without options",
			mutate: func(m *Manifest) {
				m.QuestionForm = &QuestionForm{
					Enabled: true,
					Questions: []QuestionFormField{{
						ID:       "audience",
						LabelKey: "form.ppt.audience.label",
						Type:     "single_select",
					}},
				}
			},
			wantSubstr: "options is required",
		},
		{
			name: "unsupported question form trigger",
			mutate: func(m *Manifest) {
				m.QuestionForm = &QuestionForm{
					Enabled: true,
					Trigger: "after_export",
					Questions: []QuestionFormField{{
						ID:       "audience",
						LabelKey: "form.ppt.audience.label",
						Type:     "text",
					}},
				}
			},
			wantSubstr: `question_form.trigger "after_export" is not supported`,
		},
		{
			name: "unsafe question form skip key",
			mutate: func(m *Manifest) {
				m.QuestionForm = &QuestionForm{
					Enabled: true,
					SkipKey: "../form.skip",
					Questions: []QuestionFormField{{
						ID:       "audience",
						LabelKey: "form.ppt.audience.label",
						Type:     "text",
					}},
				}
			},
			wantSubstr: "question_form.skip_label_key must not contain path separators",
		},
		{
			name: "question label key outside form namespace",
			mutate: func(m *Manifest) {
				m.QuestionForm = &QuestionForm{
					Enabled: true,
					Questions: []QuestionFormField{{
						ID:       "audience",
						LabelKey: "admin.secret.label",
						Type:     "text",
					}},
				}
			},
			wantSubstr: `question_form.questions[0].label_key "admin.secret.label" must start with "form."`,
		},
		{
			name: "question id with slash",
			mutate: func(m *Manifest) {
				m.QuestionForm = &QuestionForm{
					Enabled: true,
					Questions: []QuestionFormField{{
						ID:       "../audience",
						LabelKey: "form.ppt.audience.label",
						Type:     "text",
					}},
				}
			},
			wantSubstr: "question_form.questions[0].id must not contain path separators",
		},
		{
			name: "question option value with slash",
			mutate: func(m *Manifest) {
				m.QuestionForm = &QuestionForm{
					Enabled: true,
					Questions: []QuestionFormField{{
						ID:       "audience",
						LabelKey: "form.ppt.audience.label",
						Type:     "single_select",
						Options: []QuestionFormOption{{
							Value:    "../exec",
							LabelKey: "form.ppt.audience.exec",
						}},
					}},
				}
			},
			wantSubstr: "question_form.questions[0].options[0].value must not contain path separators",
		},
		{
			name: "question option label key outside form namespace",
			mutate: func(m *Manifest) {
				m.QuestionForm = &QuestionForm{
					Enabled: true,
					Questions: []QuestionFormField{{
						ID:       "audience",
						LabelKey: "form.ppt.audience.label",
						Type:     "single_select",
						Options: []QuestionFormOption{{
							Value:    "exec",
							LabelKey: "admin.secret.exec",
						}},
					}},
				}
			},
			wantSubstr: `question_form.questions[0].options[0].label_key "admin.secret.exec" must start with "form."`,
		},
		{
			name: "duplicate question id",
			mutate: func(m *Manifest) {
				m.QuestionForm = &QuestionForm{
					Enabled: true,
					Questions: []QuestionFormField{
						{
							ID:       "audience",
							LabelKey: "form.ppt.audience.label",
							Type:     "text",
						},
						{
							ID:       "audience",
							LabelKey: "form.ppt.audience2.label",
							Type:     "text",
						},
					},
				}
			},
			wantSubstr: `question_form.questions[1].id "audience" is duplicated`,
		},
		{
			name: "duplicate question option value",
			mutate: func(m *Manifest) {
				m.QuestionForm = &QuestionForm{
					Enabled: true,
					Questions: []QuestionFormField{{
						ID:       "audience",
						LabelKey: "form.ppt.audience.label",
						Type:     "single_select",
						Options: []QuestionFormOption{
							{Value: "exec", LabelKey: "form.ppt.audience.exec"},
							{Value: "exec", LabelKey: "form.ppt.audience.exec2"},
						},
					}},
				}
			},
			wantSubstr: `question_form.questions[0].options[1].value "exec" is duplicated`,
		},
		{
			name: "unsupported question type",
			mutate: func(m *Manifest) {
				m.QuestionForm = &QuestionForm{
					Enabled: true,
					Questions: []QuestionFormField{{
						ID:       "audience",
						LabelKey: "form.ppt.audience.label",
						Type:     "date_picker",
					}},
				}
			},
			wantSubstr: "is not supported",
		},
		{
			name: "unsupported directions picker source",
			mutate: func(m *Manifest) {
				m.DirectionsFallback = &DirectionsFallback{
					Enabled:      true,
					PickerSource: "custom_rotation",
				}
			},
			wantSubstr: "directions_fallback.picker_source",
		},
		{
			name: "unsafe directions skip key",
			mutate: func(m *Manifest) {
				m.DirectionsFallback = &DirectionsFallback{
					Enabled: true,
					SkipKey: "../form.skip",
				}
			},
			wantSubstr: "directions_fallback.skip_label_key must not contain path separators",
		},
		{
			name: "directions skip key outside form namespace",
			mutate: func(m *Manifest) {
				m.DirectionsFallback = &DirectionsFallback{
					Enabled: true,
					SkipKey: "admin.secret.skip",
				}
			},
			wantSubstr: `directions_fallback.skip_label_key "admin.secret.skip" must start with "form."`,
		},
		{
			name: "legacy identity wrong prefix",
			mutate: func(m *Manifest) {
				m.Legacy = &LegacyInherits{InheritsIdentity: "secret_thing"}
			},
			wantSubstr: `must start with "identity_"`,
		},
		{
			name: "legacy output wrong prefix",
			mutate: func(m *Manifest) {
				m.Legacy = &LegacyInherits{InheritsOutput: "secret_thing"}
			},
			wantSubstr: `must start with "output_format_"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validBaseline()
			tc.mutate(m)
			err := validateManifest(m)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q missing expected substring %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// validateManifest accepts the canonical shape that all 25 official
// skills already use today. Regression: future tightening must not
// reject real production manifests.
func TestValidateManifest_AcceptsCanonicalManifests(t *testing.T) {
	m := validBaseline()
	if err := validateManifest(m); err != nil {
		t.Errorf("baseline manifest should be valid; got %v", err)
	}

	// With everything optional populated.
	m.Description = "Short description with utf-8 中文"
	m.References.Shared = []string{"fact-verification", "anti-ai-slop"}
	m.References.Local = []string{"rubric-detail"}
	m.Scripts = []ScriptDescriptor{{Name: "validate-pptx", Path: "scripts/validate-pptx.py", When: "post_generation"}}
	m.RequiredInputs = []InputRequirement{{Kind: "topic"}, {Kind: "brand_assets", When: "brand_specified"}}
	m.Artifacts = &ArtifactMetadata{
		PrimaryType:     "deck",
		OutputTypes:     []string{"pptx", "pdf"},
		PreviewTypes:    []string{"deck", "pdf"},
		ExportTargets:   []string{"pptx", "pdf", "zip"},
		CritiqueAnchors: []string{"hierarchy", "functionality"},
	}
	m.Legacy = &LegacyInherits{InheritsIdentity: "identity_ppt", InheritsOutput: "output_format_ppt"}
	m.QuestionForm = &QuestionForm{
		Enabled: true,
		Questions: []QuestionFormField{{
			ID:       "audience",
			LabelKey: "form.ppt.audience.label",
			Type:     "single_select",
			Default:  "exec",
			Options: []QuestionFormOption{{
				Value:    "exec",
				LabelKey: "form.ppt.audience.exec",
			}},
		}},
	}
	m.DirectionsFallback = &DirectionsFallback{
		Enabled:      true,
		Trigger:      "no_brand_no_direction_selected",
		PickerSource: "fallback_5",
	}
	if err := validateManifest(m); err != nil {
		t.Errorf("fully-populated manifest should be valid; got %v", err)
	}
}

func TestValidateManifestForSkillDir(t *testing.T) {
	m := validBaseline()
	m.Triggers.AgentMode = "ppt"
	if err := validateManifestForSkillDir("ppt", m); err != nil {
		t.Fatalf("expected matching skill dir to pass, got %v", err)
	}

	m = validBaseline()
	m.Triggers.AgentMode = "ppt"
	if err := validateManifestForSkillDir("logo", m); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected name mismatch error, got %v", err)
	}

	m = validBaseline()
	m.Triggers.AgentMode = ""
	if err := validateManifestForSkillDir("ppt", m); err == nil || !strings.Contains(err.Error(), "triggers.agent_mode is required") {
		t.Fatalf("expected missing trigger error, got %v", err)
	}
}

// parseManifest must surface validation errors with the skill.yaml:
// prefix so the calling Loader can wrap with the skill name and
// produce something like: skill "evil": skill.yaml: name is required.
func TestParseManifest_WrapsValidationError(t *testing.T) {
	yaml := []byte(`name: ""
version: "1.0"
`)
	_, err := parseManifest(yaml)
	if err == nil {
		t.Fatal("expected validation error on empty name")
	}
	if !strings.Contains(err.Error(), "skill.yaml:") {
		t.Errorf("error missing skill.yaml prefix: %v", err)
	}
}

func validBaseline() *Manifest {
	return &Manifest{
		Name:    "ppt",
		Version: "2.0.0",
	}
}

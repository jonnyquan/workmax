package config

import "testing"

func TestWorkAgentFeatures_AlwaysOnForAuthenticatedUsers(t *testing.T) {
	var nilFeatures *WorkAgentFeatures
	features := &WorkAgentFeatures{}

	for name, fn := range map[string]func(*WorkAgentFeatures) bool{
		"question_form":       func(f *WorkAgentFeatures) bool { return f.IsQuestionFormEnabledFor(1) },
		"directions_fallback": func(f *WorkAgentFeatures) bool { return f.IsDirectionsFallbackEnabledFor(1) },
		"pre_emit_gate":       func(f *WorkAgentFeatures) bool { return f.IsPreEmitGateEnabledForSkill(1, "ppt") },
		"pre_emit_redo":       func(f *WorkAgentFeatures) bool { return f.IsPreEmitGateAutoRedoEnabledFor(1, "ppt") },
		"critique_gate":       func(f *WorkAgentFeatures) bool { return f.IsCritiqueGateEnabledForSkill(1, "ppt") },
		"critique_redo":       func(f *WorkAgentFeatures) bool { return f.IsCritiqueGateAutoRedoEnabledFor(1, "ppt") },
	} {
		t.Run(name+"_nil_receiver", func(t *testing.T) {
			if !fn(nilFeatures) {
				t.Fatalf("nil receiver should still use always-on defaults")
			}
		})
		t.Run(name+"_empty_config", func(t *testing.T) {
			if !fn(features) {
				t.Fatalf("empty config should still use always-on defaults")
			}
		})
	}
}

func TestWorkAgentFeatures_RejectsUnauthenticatedUser(t *testing.T) {
	var f *WorkAgentFeatures
	if f.IsQuestionFormEnabledFor(0) {
		t.Error("uid=0 should not pass question form")
	}
	if f.IsDirectionsFallbackEnabledFor(0) {
		t.Error("uid=0 should not pass directions fallback")
	}
	if f.IsPreEmitGateEnabledForSkill(0, "ppt") {
		t.Error("uid=0 should not pass pre-emit gate")
	}
	if f.IsCritiqueGateEnabledForSkill(0, "ppt") {
		t.Error("uid=0 should not pass critique gate")
	}
}

func TestWorkAgentFeatures_GlobalAlwaysOnHelpers(t *testing.T) {
	var f *WorkAgentFeatures
	// (F2 2026-05-17) IsBrandSpecProtocolEnabled removed.
	if !f.IsHonestDataDetectorEnabled() {
		t.Error("honest data detector should be always on")
	}
	if !f.IsSkillPreflightEnabled() {
		t.Error("skill preflight should be always on")
	}
	if got := f.KnowledgeRetrieverBackendName(); got != "local-vector" {
		t.Fatalf("default knowledge retriever backend = %q, want local-vector", got)
	}
	if got := (&WorkAgentFeatures{KnowledgeRetrieverBackend: "lexical"}).KnowledgeRetrieverBackendName(); got != "lexical" {
		t.Fatalf("configured knowledge retriever backend = %q, want lexical", got)
	}
	if got := f.DisabledPromptLayers(); len(got) != 0 {
		t.Fatalf("prompt layers should not be config-disabled, got %v", got)
	}
}

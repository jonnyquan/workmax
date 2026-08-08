package workagent

import (
	"strings"
	"testing"

	"server/config"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/utils/testutil"
)

// TestExtractUserLocale_ReadsTopLevelLang pins the wire contract
// between the frontend's `lang` field on ChatStreamRequest and the
// i18n resolver used by the question_form / directions_picker
// dispatchers. Previously this helper returned "" unconditionally
// (PR-4 stub awaiting PR-5 wiring); the wiring landed on the
// frontend but the BE stub stayed in place, so every non-EN user
// saw English form labels even when their locale catalog existed
// on disk. Caught by the wire-shape audit 2026-05-12.
func TestExtractUserLocale_ReadsTopLevelLang(t *testing.T) {
	cases := []struct {
		name string
		lang string
		want string
	}{
		{"empty falls through to resolver default", "", ""},
		{"plain locale", "zh", "zh"},
		{"region-suffixed locale passed through verbatim", "zh-CN", "zh-CN"},
		{"ja", "ja", "ja"},
		{"ko", "ko", "ko"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := ChatStreamRequest{Lang: tc.lang}
			got := extractUserLocale(req)
			if got != tc.want {
				t.Errorf("extractUserLocale(Lang=%q) = %q, want %q", tc.lang, got, tc.want)
			}
		})
	}
}

func TestMaybeEmitQuestionForm_NLPInferredAnswersMovePassModeToDraft(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	thread := workagentModel.ChatThread{
		UID:          42,
		UUID:         "nlp-pass-mode-" + t.Name(),
		Name:         "fixture",
		AgentMode:    "ppt",
		MessageCount: 0,
	}
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	api := NewAIChatApiNew()
	emitted := false
	shortCircuited := api.maybeEmitQuestionForm(maybeEmitQuestionFormInput{
		uid:         42,
		chatThread:  &thread,
		chatRequest: ChatStreamRequest{Lang: "en"},
		userMessage: "Create a 12-slide clean deck for executives.",
		agentMode:   "ppt",
		sendEvent: func(workagentModel.AgentSSEEvent) {
			emitted = true
		},
	})
	if shortCircuited {
		t.Fatal("fully inferred answers should skip the form and fall through to SDK")
	}
	if emitted {
		t.Fatal("fully inferred answers should not emit a question_form block")
	}

	repo := workagentService.DefaultMessageRepository()
	discovery, err := repo.FindLatestDiscoveryAnswersForSkill(thread.Id, "ppt")
	if err != nil || discovery == nil {
		t.Fatalf("expected inferred discovery answers, got msg=%v err=%v", discovery, err)
	}
	if !strings.Contains(discovery.Metadata, `"skip_reason":"nlp_inferred"`) {
		t.Fatalf("expected nlp_inferred discovery metadata, got %q", discovery.Metadata)
	}
	passMode, err := repo.FindMostRecentByMetadataKind(thread.Id, "workagent_pass_mode")
	if err != nil || passMode == nil {
		t.Fatalf("expected pass mode marker, got msg=%v err=%v", passMode, err)
	}
	if !strings.Contains(passMode.Metadata, `"mode":"draft"`) {
		t.Fatalf("expected draft pass mode, got %q", passMode.Metadata)
	}
	if !strings.Contains(passMode.Metadata, `"source":"question_form_nlp_inferred"`) {
		t.Fatalf("expected nlp pass mode source, got %q", passMode.Metadata)
	}
}

func TestMaybeEmitQuestionForm_PartialNLPAnswersPrefillFormWithoutPersistingDiscovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	prev := config.GetWorkAgentFeatures()
	config.SetWorkAgentFeaturesAccessor(func() *config.WorkAgentFeatures {
		return &config.WorkAgentFeatures{QuestionFormEnabled: true}
	})
	t.Cleanup(func() {
		config.SetWorkAgentFeaturesAccessor(func() *config.WorkAgentFeatures {
			return prev
		})
	})

	thread := workagentModel.ChatThread{
		UID:          42,
		UUID:         "nlp-partial-prefill-" + t.Name(),
		Name:         "fixture",
		AgentMode:    "ppt",
		MessageCount: 0,
	}
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	api := NewAIChatApiNew()
	events := []workagentModel.AgentSSEEvent{}
	shortCircuited := api.maybeEmitQuestionForm(maybeEmitQuestionFormInput{
		uid:         42,
		chatThread:  &thread,
		chatRequest: ChatStreamRequest{Lang: "en"},
		userMessage: "Make a deck for the investors",
		agentMode:   "ppt",
		sendEvent: func(event workagentModel.AgentSSEEvent) {
			events = append(events, event)
		},
	})
	if !shortCircuited {
		t.Fatal("partial inferred answers should still emit the question form")
	}
	if len(events) != 2 {
		t.Fatalf("expected block + done events, got %d", len(events))
	}
	if !strings.Contains(string(events[0].Block), `"id":"audience"`) {
		t.Fatalf("expected emitted form to include audience question, got %s", string(events[0].Block))
	}
	if !strings.Contains(string(events[0].Block), `"default":"investor"`) {
		t.Fatalf("expected partial NLP answer to prefill audience=investor, got %s", string(events[0].Block))
	}

	discovery, err := workagentService.DefaultMessageRepository().FindLatestDiscoveryAnswersForSkill(thread.Id, "ppt")
	if err != nil {
		t.Fatalf("lookup discovery answers: %v", err)
	}
	if discovery != nil {
		t.Fatalf("partial prefill must not persist discovery answers before user submit, got metadata=%q", discovery.Metadata)
	}
}

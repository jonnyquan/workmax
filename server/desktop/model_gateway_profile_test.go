//go:build desktop

package desktop

import (
	"context"
	"strings"
	"testing"

	agentruntime "server/desktop/agentruntime"
	cloudproxy "server/desktop/cloud_proxy"
	localagent "server/desktop/local_agent"
	localinference "server/desktop/local_inference"
)

// Which endpoint a local turn talks to is now a resolution with two answers,
// and the failure modes of getting it wrong are asymmetric: pointing an
// official turn at the user's own endpoint spends their money on a model they
// did not choose, and pointing a user's turn at the gateway spends their
// membership on a model they did not choose. So both directions are pinned,
// for both engines, plus every reason the official answer can be refused.

type profileFixture struct {
	reader   *LocalModelProfileReader
	settings *LocalModelSettingsStore
	gateway  *ModelGateway
	bound    bool
}

const profileTestUID = uint64(7788)

func newProfileFixture(t *testing.T) *profileFixture {
	t.Helper()
	db := openMigratedTestDB(t)
	gateway, err := NewModelGateway()
	if err != nil {
		t.Fatalf("NewModelGateway: %v", err)
	}
	gateway.SetPort(51999)
	fixture := &profileFixture{
		settings: NewLocalModelSettingsStore(db, newMemKeychain()),
		gateway:  gateway,
		bound:    true,
	}
	fixture.reader = &LocalModelProfileReader{
		Store:      fixture.settings,
		UID:        func() uint64 { return profileTestUID },
		Gateway:    gateway,
		CloudBound: func() bool { return fixture.bound },
	}
	return fixture
}

func (f *profileFixture) chooseOfficial(t *testing.T, protocol, modelID string) {
	t.Helper()
	if _, err := f.settings.Put(profileTestUID, LocalModelSettingsPut{
		PreferredRoute:  ModelRouteLocal,
		OfficialModelID: &modelID,
		Local:           &LocalModelProfilePut{Protocol: protocol},
	}); err != nil {
		t.Fatalf("choose official model: %v", err)
	}
}

func (f *profileFixture) chooseOwnEndpoint(t *testing.T, protocol, baseURL, modelID, apiKey string) {
	t.Helper()
	if _, err := f.settings.Put(profileTestUID, LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol: protocol,
			BaseURL:  baseURL,
			ModelID:  modelID,
			APIKey:   &apiKey,
		},
	}); err != nil {
		t.Fatalf("save own endpoint: %v", err)
	}
}

// An endpoint the user stood up themselves is untouched by any of this.
func TestProfileKeepsTheUsersOwnEndpoint(t *testing.T) {
	fixture := newProfileFixture(t)
	fixture.chooseOwnEndpoint(t, LocalProtocolAnthropicCompatible, "http://127.0.0.1:8080", "local-llama", "sk-mine")

	protocol, baseURL, modelID, apiKey, err := fixture.reader.LocalInferenceProfile()
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if protocol != LocalProtocolAnthropicCompatible || baseURL != "http://127.0.0.1:8080" ||
		modelID != "local-llama" || apiKey != "sk-mine" {
		t.Fatalf("own endpoint drifted: %s %s %s %s", protocol, baseURL, modelID, apiKey)
	}
	if apiKey == fixture.gateway.Token() {
		t.Fatalf("the gateway credential must never stand in for the user's key")
	}
}

// No endpoint of their own plus an official model chosen: the loopback
// gateway, the catalog model, and a credential that cannot leave this machine.
func TestProfileResolvesTheOfficialModelToTheLoopbackGateway(t *testing.T) {
	for _, testCase := range []struct {
		protocol string
		wantBase string
	}{
		{LocalProtocolAnthropicCompatible, "http://127.0.0.1:51999/model-gateway/anthropic"},
		{LocalProtocolOpenAICompatible, "http://127.0.0.1:51999/model-gateway/openai"},
	} {
		t.Run(testCase.protocol, func(t *testing.T) {
			fixture := newProfileFixture(t)
			fixture.chooseOfficial(t, testCase.protocol, "work-pro")

			protocol, baseURL, modelID, apiKey, err := fixture.reader.LocalInferenceProfile()
			if err != nil {
				t.Fatalf("profile: %v", err)
			}
			if protocol != testCase.protocol {
				t.Fatalf("protocol = %q: the engine choice still belongs to the user", protocol)
			}
			if baseURL != testCase.wantBase {
				t.Fatalf("base URL = %q, want %q", baseURL, testCase.wantBase)
			}
			if modelID != "work-pro" {
				t.Fatalf("model = %q, want the chosen catalog id", modelID)
			}
			if apiKey != fixture.gateway.Token() {
				t.Fatalf("the official path must authenticate with the loopback token")
			}
		})
	}
}

// The three refusals. Each is a different next action for the user, so each
// has to be a different sentence — and none of them may quietly become a turn
// on something else.
func TestProfileRefusesTheOfficialModelExplicitly(t *testing.T) {
	t.Run("no account connected", func(t *testing.T) {
		fixture := newProfileFixture(t)
		fixture.chooseOfficial(t, LocalProtocolAnthropicCompatible, "work-pro")
		fixture.bound = false

		_, baseURL, _, _, err := fixture.reader.LocalInferenceProfile()
		assertProfileRefusal(t, err, cloudproxy.KindAuthRequired, "连接 WorkMax 账号")
		if baseURL != "" {
			t.Fatalf("a refusal must not hand back an endpoint: %q", baseURL)
		}
	})

	t.Run("no official model chosen", func(t *testing.T) {
		fixture := newProfileFixture(t)
		fixture.chooseOfficial(t, LocalProtocolAnthropicCompatible, "")

		_, _, _, _, err := fixture.reader.LocalInferenceProfile()
		assertProfileRefusal(t, err, cloudproxy.KindBadRequest, "尚未选择官方模型")
	})

	t.Run("gateway not addressable", func(t *testing.T) {
		fixture := newProfileFixture(t)
		fixture.chooseOfficial(t, LocalProtocolAnthropicCompatible, "work-pro")
		fixture.gateway.SetPort(0)

		_, _, _, _, err := fixture.reader.LocalInferenceProfile()
		assertProfileRefusal(t, err, cloudproxy.KindServiceUnavailable, "网关未就绪")
	})
}

func assertProfileRefusal(t *testing.T, err error, kind cloudproxy.ProxyErrorKind, wantText string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an explicit refusal, got a usable profile")
	}
	pe, typed := localinference.ProfileProxyError(err)
	if !typed {
		t.Fatalf("refusal must be typed so the engines can surface it: %v", err)
	}
	if pe.Kind != kind {
		t.Fatalf("kind = %q, want %q", pe.Kind, kind)
	}
	if !strings.Contains(pe.Message, wantText) {
		t.Fatalf("message = %q, want it to mention %q", pe.Message, wantText)
	}
	if !strings.Contains(pe.Message, "本地模型 endpoint") {
		t.Fatalf("every refusal must name the other way out: %q", pe.Message)
	}
}

// recordingRuntime is an agentruntime.Runtime that answers nothing and
// remembers what it was asked to run with.
type recordingRuntime struct {
	root string
	in   agentruntime.TurnInput
}

func (r *recordingRuntime) Name() string          { return "recording" }
func (r *recordingRuntime) WorkspaceRoot() string { return r.root }
func (r *recordingRuntime) RunTurn(_ context.Context, in agentruntime.TurnInput, _ agentruntime.EmitFunc) error {
	r.in = in
	return nil
}

// The L2 seam, end to end: both engines are local_agent.Engine around a
// runtime, so what has to be pinned is that the TurnInput they hand their
// subprocess carries the right endpoint in each configuration.
func TestToolLoopEnginesPointAtTheRightEndpoint(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		protocol string
		official bool
		wantBase string
		wantKey  func(*profileFixture) string
		wantID   string
	}{
		{
			name:     "claude engine on the official model",
			protocol: LocalProtocolAnthropicCompatible,
			official: true,
			wantBase: "http://127.0.0.1:51999/model-gateway/anthropic",
			wantKey:  func(f *profileFixture) string { return f.gateway.Token() },
			wantID:   "work-pro",
		},
		{
			name:     "claude engine on the user's endpoint",
			protocol: LocalProtocolAnthropicCompatible,
			official: false,
			wantBase: "http://127.0.0.1:8080",
			wantKey:  func(*profileFixture) string { return "sk-mine" },
			wantID:   "local-llama",
		},
		{
			name:     "pi engine on the official model",
			protocol: LocalProtocolOpenAICompatible,
			official: true,
			wantBase: "http://127.0.0.1:51999/model-gateway/openai",
			wantKey:  func(f *profileFixture) string { return f.gateway.Token() },
			wantID:   "work-pro",
		},
		{
			name:     "pi engine on the user's endpoint",
			protocol: LocalProtocolOpenAICompatible,
			official: false,
			wantBase: "http://127.0.0.1:11434/v1",
			wantKey:  func(*profileFixture) string { return "sk-mine" },
			wantID:   "local-llama",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newProfileFixture(t)
			if testCase.official {
				fixture.chooseOfficial(t, testCase.protocol, "work-pro")
			} else {
				base := "http://127.0.0.1:8080"
				if testCase.protocol == LocalProtocolOpenAICompatible {
					base = "http://127.0.0.1:11434/v1"
				}
				fixture.chooseOwnEndpoint(t, testCase.protocol, base, "local-llama", "sk-mine")
			}

			runtime := &recordingRuntime{root: t.TempDir()}
			engine := localagent.NewEngineWithRuntime(fixture.reader, fixture.settings.db, nil, nil, runtime)
			writer := &recordingSSEWriter{}
			err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
				ThreadID:   1,
				ThreadUUID: "de305d54-75b4-431b-adb2-eb6b9e546014",
				TurnUUID:   "5c9f8a1e-2b7d-4f31-9a6c-1d2e3f4a5b6c",
				UID:        profileTestUID,
				UserText:   "hi",
			}, writer)
			if err != nil {
				t.Fatalf("Chat: %v (errors: %v)", err, writer.errors)
			}
			if runtime.in.BaseURL != testCase.wantBase {
				t.Fatalf("BaseURL = %q, want %q", runtime.in.BaseURL, testCase.wantBase)
			}
			if runtime.in.APIKey != testCase.wantKey(fixture) {
				t.Fatalf("APIKey is not the credential this configuration should use")
			}
			if runtime.in.ModelID != testCase.wantID {
				t.Fatalf("ModelID = %q, want %q", runtime.in.ModelID, testCase.wantID)
			}
		})
	}
}

// And when the official model cannot run, the tool loop says so instead of
// starting a subprocess against anything else.
func TestToolLoopSurfacesTheProfileRefusal(t *testing.T) {
	fixture := newProfileFixture(t)
	fixture.chooseOfficial(t, LocalProtocolAnthropicCompatible, "work-pro")
	fixture.bound = false

	runtime := &recordingRuntime{root: t.TempDir()}
	engine := localagent.NewEngineWithRuntime(fixture.reader, fixture.settings.db, nil, nil, runtime)
	writer := &recordingSSEWriter{}
	if err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID:   1,
		ThreadUUID: "de305d54-75b4-431b-adb2-eb6b9e546014",
		TurnUUID:   "5c9f8a1e-2b7d-4f31-9a6c-1d2e3f4a5b6c",
		UID:        profileTestUID,
		UserText:   "hi",
	}, writer); err == nil {
		t.Fatalf("an unrunnable official turn must fail")
	}
	if runtime.in.Prompt != "" {
		t.Fatalf("no subprocess may be started for a turn that cannot run")
	}
	if len(writer.errors) != 1 {
		t.Fatalf("expected exactly one proxy_error, got %v", writer.errors)
	}
	if writer.errors[0].Kind != cloudproxy.KindAuthRequired ||
		!strings.Contains(writer.errors[0].Message, "连接 WorkMax 账号") {
		t.Fatalf("the refusal reached the user as %v", writer.errors[0])
	}
}

type recordingSSEWriter struct {
	events []cloudproxy.SSEEvent
	errors []cloudproxy.ProxyError
}

func (w *recordingSSEWriter) WriteEvent(ev cloudproxy.SSEEvent) error {
	w.events = append(w.events, ev)
	return nil
}

func (w *recordingSSEWriter) WriteProxyError(pe cloudproxy.ProxyError) error {
	w.errors = append(w.errors, pe)
	return nil
}

func (w *recordingSSEWriter) WriteKeepalive() error { return nil }

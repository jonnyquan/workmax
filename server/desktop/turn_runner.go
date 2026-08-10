//go:build desktop

package desktop

import (
	"context"

	cloudproxy "server/desktop/cloud_proxy"
	localinference "server/desktop/local_inference"
)

// TurnRunner 执行一次 agent turn，把 workmax 格式 SSEEvent 写入 dst。
// *cloudproxy.Proxy（云端路由）与 *localinference.Engine（本地路由）均满足
// 此接口；streamLegacyAgentTurn 按 PreferredRoute 二选一，调用方对该路由无感。
// 接口在消费方定义（Go 惯例），实现方无需显式声明实现。
type TurnRunner interface {
	Chat(ctx context.Context, req cloudproxy.ChatRequest, dst cloudproxy.SSEWriter) error
}

// shouldUseLocalRoute 报告当前是否走本地推理路由。仅当 preferred_route==local
// 且 LocalInference/ModelSettings 均已接线时为 true。本地 profile 是否填妥
// （base_url/model_id）不在此判断——由 Engine.Chat 显式报 proxy_error
// （遵循 OSS-4：禁止静默跨路由回退，route=local 但未配置时宁可报错也不走云端）。
// localTurnRunner picks which local engine serves this turn. The protocol is
// the fork the user already chose in their model settings: the claude tool
// loop speaks only the Anthropic wire protocol (kill-checked), so
// anthropic_compatible goes to L2 when a CLI is wired; openai_compatible
// goes to the pi engine when one is wired (dual-runtime plan §1); everything
// else — including either protocol with no engine — runs as L1 pure chat
// rather than failing.
func (s *Server) localTurnRunner() TurnRunner {
	if s.cfg.ModelSettings != nil {
		if dto, err := s.cfg.ModelSettings.Get(s.resolveIdentity().UID); err == nil {
			switch dto.Local.Protocol {
			case LocalProtocolAnthropicCompatible:
				if s.cfg.LocalAgent != nil {
					return s.cfg.LocalAgent
				}
			case LocalProtocolOpenAICompatible:
				if s.cfg.PiAgent != nil {
					return s.cfg.PiAgent
				}
			}
		}
	}
	return s.cfg.LocalInference
}

// localToolLoopActive reports whether a local turn sent right now would run
// an L2 tool loop (claude or pi) — the same condition localTurnRunner
// applies, exposed so the modes route can tell the renderer the truth about
// capability.
func (s *Server) localToolLoopActive() bool {
	if !s.shouldUseLocalRoute() {
		return false
	}
	dto, err := s.cfg.ModelSettings.Get(s.resolveIdentity().UID)
	if err != nil {
		return false
	}
	switch dto.Local.Protocol {
	case LocalProtocolAnthropicCompatible:
		return s.cfg.LocalAgent != nil
	case LocalProtocolOpenAICompatible:
		return s.cfg.PiAgent != nil
	}
	return false
}

// localRouteUID is the identity the signed-out local route runs as: the
// active local account. Falls back to the reserved single-user uid on any
// bookkeeping failure — an accounts problem must never lock a user out.
func (s *Server) localRouteUID() uint64 {
	return activeLocalAccountUID(s.cfg.DB)
}

func (s *Server) shouldUseLocalRoute() bool {
	if s.cfg.LocalInference == nil || s.cfg.ModelSettings == nil {
		return false
	}
	dto, err := s.cfg.ModelSettings.Get(s.resolveIdentity().UID)
	if err != nil {
		return false
	}
	return dto.PreferredRoute == ModelRouteLocal
}

// LocalModelProfileReader 把 LocalModelSettingsStore 适配成
// local_inference.ProfileReader。依赖倒置：local_inference 不 import desktop
// （否则循环），由本包提供实现。
// UID answers "whose key" at the moment the engine asks, not at wiring time:
// the local route can be driven by any identity on this machine, and reading
// the key of whoever happened to be signed in when the sidecar booted is the
// cross-identity leak this partition exists to close.
type LocalModelProfileReader struct {
	Store *LocalModelSettingsStore
	UID   func() uint64

	// Gateway is the sidecar's own loopback model gateway. It is what a local
	// turn runs against when the user chose an official model instead of
	// standing up an endpoint of their own. Nil → official-model local turns
	// fail with an explicit error rather than falling back to anything.
	Gateway *ModelGateway

	// CloudBound reports whether a WorkMax account is connected right now.
	// Asked at turn time, not at wiring time: a machine can be signed out
	// between two turns, and the honest answer then is "this turn cannot run",
	// not "run it on something else".
	CloudBound func() bool
}

// LocalInferenceProfile 实现 local_inference.ProfileReader：合并 SQLite 里的
// 非密钥字段与 Keychain 里的 API key。
//
// It answers one question — what endpoint does a local turn talk to — and
// there are now two ways for it to be answered:
//
//   - the user stood up an endpoint of their own (base_url filled): unchanged,
//     their URL and their key, exactly as before;
//   - the user chose an official model instead (base_url empty): the sidecar's
//     loopback gateway, the model they picked from the cloud catalog, and the
//     in-memory gateway token as the "API key".
//
// The second branch never silently becomes the first, and neither ever
// silently becomes the cloud agent route: every reason the official path
// cannot run is a typed ProfileError the engines surface verbatim.
func (r *LocalModelProfileReader) LocalInferenceProfile() (protocol, baseURL, modelID, apiKey string, err error) {
	uid := localSingleUserUID
	if r.UID != nil {
		uid = r.UID()
	}
	dto, err := r.Store.Get(uid)
	if err != nil {
		return "", "", "", "", err
	}
	if dto.Local.BaseURL != "" {
		// The user's own endpoint. Its key is theirs, from their Keychain slot.
		key, keyErr := r.Store.LoadAPIKey(uid)
		if keyErr != nil {
			return "", "", "", "", keyErr
		}
		return dto.Local.Protocol, dto.Local.BaseURL, dto.Local.ModelID, key, nil
	}
	return r.officialModelProfile(dto)
}

// officialModelProfile resolves the loopback-gateway triple, or explains why
// it cannot. The order of the checks is the order the user can act on them.
func (r *LocalModelProfileReader) officialModelProfile(dto LocalModelSettingsDTO) (string, string, string, string, error) {
	if r.CloudBound == nil || !r.CloudBound() {
		return "", "", "", "", &localinference.ProfileError{
			Kind:    cloudproxy.KindAuthRequired,
			Message: modelGatewayUnboundMessage,
		}
	}
	if dto.OfficialModelID == "" {
		return "", "", "", "", &localinference.ProfileError{
			Kind:    cloudproxy.KindBadRequest,
			Message: modelGatewayNoModelMessage,
		}
	}
	// The protocol still decides which engine runs the turn (the claude tool
	// loop or pi) and therefore which wire shape the gateway must speak, so it
	// remains required even when the endpoint behind it is ours.
	base := ""
	if r.Gateway != nil {
		base = r.Gateway.BaseURLFor(dto.Local.Protocol)
	}
	if base == "" {
		return "", "", "", "", &localinference.ProfileError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   modelGatewayUnavailableMessage,
			Retryable: false,
		}
	}
	return dto.Local.Protocol, base, dto.OfficialModelID, r.Gateway.Token(), nil
}

// 编译期断言：*LocalModelProfileReader 实现 local_inference.ProfileReader。
var _ localinference.ProfileReader = (*LocalModelProfileReader)(nil)

// officialModelIDFor is the model this identity picked for official turns, or
// "" when it never picked one (or settings are unreadable — an unanswerable
// preference must not stop a turn that would otherwise run on the account
// default). It reads the frozen turn identity rather than the current request
// so a login committing mid-turn cannot change which model the in-flight turn
// asked for.
func (s *Server) officialModelIDFor(uid uint64) string {
	if s.cfg.ModelSettings == nil {
		return ""
	}
	dto, err := s.cfg.ModelSettings.Get(uid)
	if err != nil {
		return ""
	}
	return dto.OfficialModelID
}

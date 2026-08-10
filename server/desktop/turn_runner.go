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
		if dto, err := s.cfg.ModelSettings.Get(); err == nil {
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
	dto, err := s.cfg.ModelSettings.Get()
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
	dto, err := s.cfg.ModelSettings.Get()
	if err != nil {
		return false
	}
	return dto.PreferredRoute == ModelRouteLocal
}

// LocalModelProfileReader 把 LocalModelSettingsStore 适配成
// local_inference.ProfileReader。依赖倒置：local_inference 不 import desktop
// （否则循环），由本包提供实现。
type LocalModelProfileReader struct {
	Store *LocalModelSettingsStore
}

// LocalInferenceProfile 实现 local_inference.ProfileReader：合并 SQLite 里的
// 非密钥字段与 Keychain 里的 API key。
func (r *LocalModelProfileReader) LocalInferenceProfile() (protocol, baseURL, modelID, apiKey string, err error) {
	dto, err := r.Store.Get()
	if err != nil {
		return "", "", "", "", err
	}
	key, err := r.Store.LoadAPIKey()
	if err != nil {
		return "", "", "", "", err
	}
	return dto.Local.Protocol, dto.Local.BaseURL, dto.Local.ModelID, key, nil
}

// 编译期断言：*LocalModelProfileReader 实现 local_inference.ProfileReader。
var _ localinference.ProfileReader = (*LocalModelProfileReader)(nil)

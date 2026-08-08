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
// the fork the user already chose in their model settings: the tool loop
// speaks only the Anthropic wire protocol (kill-checked), so
// anthropic_compatible goes to L2 when a CLI is wired, and everything else —
// including anthropic_compatible with no CLI — runs as L1 pure chat rather
// than failing.
func (s *Server) localTurnRunner() TurnRunner {
	if s.cfg.LocalAgent != nil && s.cfg.ModelSettings != nil {
		if dto, err := s.cfg.ModelSettings.Get(); err == nil &&
			dto.Local.Protocol == LocalProtocolAnthropicCompatible {
			return s.cfg.LocalAgent
		}
	}
	return s.cfg.LocalInference
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

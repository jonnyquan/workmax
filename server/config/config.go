package config

type Server struct {
	System          System       `mapstructure:"system" json:"system" yaml:"system"`
	GormMysqlSystem GormMysql    `mapstructure:"mysql_system" json:"mysql_system" yaml:"mysql_system"`
	Redis           Redis        `mapstructure:"redis" json:"redis" yaml:"redis"`
	Zap             Zap          `mapstructure:"zap" json:"zap" yaml:"zap"`
	Google          Google       `mapstructure:"google" json:"google" yaml:"google"`
	JWT             JWT          `mapstructure:"jwt" json:"jwt" yaml:"jwt"`
	Stripe          Stripe       `mapstructure:"stripe" json:"stripe" yaml:"stripe"`
	Aihub           Aihub        `mapstructure:"aihub" json:"aihub" yaml:"aihub"`
	ClaudeAgent     *ClaudeAgent `mapstructure:"claude_agent" json:"claude_agent" yaml:"claude_agent"`
	// WorkAgentFeatures gates the work-agent module's incremental
	// rollout (M1 turn-1 form, M5 checklist gate, DS-2 skill assets,
	// etc.). See server/config/workagent_features.go for the per-flag
	// contract and ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md
	// for the platform-level rollout policy.
	WorkAgentFeatures *WorkAgentFeatures `mapstructure:"workagent_features" json:"workagent_features" yaml:"workagent_features"`
	// AgentPlatformRollout is a target-only, default-off cutover contract. The
	// separate agent-worker consumes only its Worker-owned subset; no current
	// route or Desktop transport consumes it.
	AgentPlatformRollout *AgentPlatformRollout `mapstructure:"agent_platform_rollout" json:"agent_platform_rollout" yaml:"agent_platform_rollout"`
	// ModelGateway configures the Desktop model gateway — the bare-model
	// proxy the local tool loop calls so it can use official models without
	// ever holding a provider key. Absent block = enabled with built-in
	// abuse guards; see server/config/model_gateway.go.
	ModelGateway *ModelGateway `mapstructure:"model_gateway" json:"model_gateway" yaml:"model_gateway"`
	Generator    Generator     `mapstructure:"generator" json:"generator" yaml:"generator"`
	Canvas       Canvas        `mapstructure:"canvas" json:"canvas" yaml:"canvas"`
	Statics      Statics       `mapstructure:"statics" json:"statics" yaml:"statics"`
	Credits      Credits       `mapstructure:"credits" json:"credits" yaml:"credits"`
}

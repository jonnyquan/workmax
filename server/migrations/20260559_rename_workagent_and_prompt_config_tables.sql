-- R3-e · Workagent 4 表统一前缀 + Prompt config 2 表收前缀。
--
-- Workagent 工具当前用了三种前缀（w_agent_account / w_chat_* /
-- w_chat_thread_file），不符合 platform-state.md §5 "工具专属表统一
-- w_<tool>_*" 规则。借参考 w_canvas_* 收成 w_workagent_*。
--
-- Prompt config 两张表（image / video）按概念应该是同一组配置的两
-- 个变体，原命名 w_image_prompt_config / w_video_prompt_config 把
-- 业务前缀（image / video）放在 prompt_config 之前，分组性差。改为
-- w_prompt_config_image / w_prompt_config_video，alphabetical 一起，
-- 也方便未来 w_prompt_config_<X> 扩展（如 audio / tts）。
--
-- 同步改动：
--   server/model/workagent/{chat_message,chat_thread,chat_thread_file}.go
--   server/model/agent_account.go
--   server/model/{image,video}_prompt_config.go
--   raw SQL: server/api/pro/tools/workagent/base.go,
--            server/api/pro/dashboard/dashboard_api.go,
--            server/service/tools/workagent/agent_account_pool.go
--   注释: server/config/claude_agent.go, server/initialize/agent_client.go
--   testdb.go 同步（如有）

RENAME TABLE
  `w_agent_account`        TO `w_workagent_account`,
  `w_chat_thread`          TO `w_workagent_thread`,
  `w_chat_message`         TO `w_workagent_message`,
  `w_chat_thread_file`     TO `w_workagent_thread_file`,
  `w_image_prompt_config`  TO `w_prompt_config_image`,
  `w_video_prompt_config`  TO `w_prompt_config_video`;

-- Desktop model gateway: record WHICH endpoint each row was.
--
-- Measured, not guessed. The claude CLI (2.1.226) driven through our own SDK
-- with the production launch recipe calls a SECOND endpoint besides
-- POST /v1/messages: POST /v1/messages/count_tokens?beta=true, whenever a tool
-- result is large enough that the CLI wants it sized before sending it (a Read
-- of a ~40 KiB file triggered it in a path-recording probe; see
-- server/desktop/local_agent/engine_cli_test.go). Until now that call met the
-- sidecar's local-token perimeter and got a 403 the CLI could not read, so the
-- tool loop silently fell back to a chars/4 estimate.
--
-- Proxying it means it also has to be metered, and it cannot be metered like a
-- completion: the provider does not bill count_tokens, and its response body
-- carries an `input_tokens` field that is the ANSWER to the question, not a
-- charge. Without this column a spend report would add up every byte the tool
-- loop ever measured. So the row records the operation and leaves the token
-- columns at zero for count_tokens.
--
-- Existing rows all predate the second endpoint, so the default backfills them
-- correctly: they were all completions.
ALTER TABLE `w_desktop_model_gateway_usage`
  ADD COLUMN `operation` varchar(24) NOT NULL DEFAULT 'messages'
    COMMENT 'messages / count_tokens：调用的是协议下的哪个端点'
    AFTER `protocol`;

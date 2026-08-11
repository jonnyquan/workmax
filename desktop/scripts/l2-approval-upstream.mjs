#!/usr/bin/env node
//
// A scripted Anthropic Messages endpoint for the L2 approval smoke.
//
// The smoke needs a real claude CLI subprocess to make a real tool call at a
// moment the harness chooses, without any external model service. So this
// stands in for the upstream: it speaks the streaming Messages protocol the
// CLI expects, and it decides what to "answer" by reading a directive that the
// harness planted in the user's own prompt.
//
// Directive, verbatim in the turn's user_text:
//
//	TOOLPLAN <ToolName> <absolute-path>
//
// The path is absolute because the endpoint has no idea which per-thread
// workspace this turn runs in — the harness does, and it writes the directive.
//
// Reply rules, per request (see plan() for why they are block-positional):
//   - a directive is in force and no tool_result has answered it -> that tool_use
//   - otherwise                                                  -> text + end_turn
//
// Usage: node l2-approval-upstream.mjs [--port 0] [--log <file>]
// Prints "upstream: http://127.0.0.1:<port>" on stdout once listening.

import http from "node:http";
import fs from "node:fs";

const args = process.argv.slice(2);
let port = 0;
let logPath = "";
for (let i = 0; i < args.length; i += 1) {
  if (args[i] === "--port") port = Number(args[i + 1] ?? "0");
  else if (args[i] === "--log") logPath = args[i + 1] ?? "";
}

const FINAL_TEXT = "L2-APPROVAL-SMOKE-COMPLETE";

function note(line) {
  if (!logPath) return;
  fs.appendFileSync(logPath, `${new Date().toISOString()} ${line}\n`);
}

function sse(res, event, data) {
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
}

// collectText flattens every text-ish fragment of one content block into a
// list, so a directive is found whether the CLI sent a plain string or a
// typed block.
function collectText(value, out) {
  if (typeof value === "string") {
    out.push(value);
    return;
  }
  if (Array.isArray(value)) {
    for (const item of value) collectText(item, out);
    return;
  }
  if (value && typeof value === "object") {
    for (const key of Object.keys(value)) collectText(value[key], out);
  }
}

// plan answers two questions: which directive is in force, and has it already
// been dispatched.
//
// "Already dispatched" is decided by IDENTITY, not by position, and that is
// the whole trick. Position looks obvious and is wrong: a resumed CLI session
// replays every earlier turn, and the CLI packs the new user text into the
// SAME user message that carries the accumulated tool_results — with the text
// last. So "is there a tool_result after the directive" is permanently false,
// and the endpoint re-issues the same tool call until maxTurns kills the turn.
// Measured, not guessed (the 24-tool_use run that produced this comment).
//
// Each scenario's target path is unique, so a tool_use block already naming it
// is proof the call went out. That holds whatever the CLI does with ordering.
function plan(body) {
  const flat = [];
  collectText(body.messages ?? [], flat);
  const matches = [...flat.join("\n").matchAll(/TOOLPLAN\s+([A-Za-z]+)\s+(\S+)/g)];
  if (matches.length === 0) return { directive: null, satisfied: false };
  const last = matches[matches.length - 1];
  const directive = { tool: last[1], target: last[2] };

  let satisfied = false;
  for (const message of body.messages ?? []) {
    const content = message?.content;
    if (!Array.isArray(content)) continue;
    for (const block of content) {
      if (block && typeof block === "object" && block.type === "tool_use") {
        if (JSON.stringify(block.input ?? {}).includes(directive.target)) satisfied = true;
      }
    }
  }
  return { directive, satisfied };
}

function toolInputFor(tool, target) {
  switch (tool) {
    case "Write":
      return { file_path: target, content: "written by the L2 approval smoke\n" };
    case "Edit":
      return { file_path: target, old_string: "seed", new_string: "edited by the L2 approval smoke" };
    case "Bash":
      // The surface probe: Bash is outside the loop's declared tool set, so
      // the target doubles as the file a shell would create if the CLI ran it.
      return { command: `touch ${target}`, description: "surface probe" };
    default:
      // Read and friends: a path is all they need here.
      return { file_path: target };
  }
}

function streamStart(res) {
  res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store" });
  sse(res, "message_start", {
    type: "message_start",
    message: {
      id: "msg_l2smoke",
      type: "message",
      role: "assistant",
      model: "l2-approval-smoke",
      content: [],
      stop_reason: null,
      usage: { input_tokens: 1, output_tokens: 1 },
    },
  });
}

function streamToolUse(res, tool, input) {
  sse(res, "content_block_start", {
    type: "content_block_start",
    index: 0,
    content_block: { type: "tool_use", id: `toolu_${Date.now()}`, name: tool, input: {} },
  });
  sse(res, "content_block_delta", {
    type: "content_block_delta",
    index: 0,
    delta: { type: "input_json_delta", partial_json: JSON.stringify(input) },
  });
  sse(res, "content_block_stop", { type: "content_block_stop", index: 0 });
  sse(res, "message_delta", {
    type: "message_delta",
    delta: { stop_reason: "tool_use", stop_sequence: null },
    usage: { output_tokens: 5 },
  });
}

function streamText(res, text) {
  sse(res, "content_block_start", {
    type: "content_block_start",
    index: 0,
    content_block: { type: "text", text: "" },
  });
  sse(res, "content_block_delta", {
    type: "content_block_delta",
    index: 0,
    delta: { type: "text_delta", text },
  });
  sse(res, "content_block_stop", { type: "content_block_stop", index: 0 });
  sse(res, "message_delta", {
    type: "message_delta",
    delta: { stop_reason: "end_turn", stop_sequence: null },
    usage: { output_tokens: 2 },
  });
}

const server = http.createServer((req, res) => {
  const chunks = [];
  req.on("data", (chunk) => chunks.push(chunk));
  req.on("end", () => {
    const url = new URL(req.url, "http://127.0.0.1");
    note(`${req.method} ${url.pathname}`);
    if (url.pathname === "/v1/messages/count_tokens") {
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ input_tokens: 128 }));
      return;
    }
    if (url.pathname !== "/v1/messages") {
      res.writeHead(404).end();
      return;
    }
    let body = {};
    try {
      body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    } catch {
      body = {};
    }
    streamStart(res);
    const { directive, satisfied } = plan(body);
    if (directive && !satisfied) {
      note(`-> tool_use ${directive.tool} ${directive.target}`);
      streamToolUse(res, directive.tool, toolInputFor(directive.tool, directive.target));
    } else {
      note("-> end_turn");
      streamText(res, FINAL_TEXT);
    }
    sse(res, "message_stop", { type: "message_stop" });
    res.end();
  });
});

server.listen(port, "127.0.0.1", () => {
  process.stdout.write(`upstream: http://127.0.0.1:${server.address().port}\n`);
});

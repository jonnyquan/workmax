#!/usr/bin/env node
//
// A scripted OpenAI-compatible chat/completions endpoint for the pi L2 smoke.
//
// The pi runtime only ever speaks to an openai_compatible endpoint, so the
// smoke needs one that answers on command instead of a real model service.
// This stands in: it speaks the streaming chat/completions protocol pi's
// openai-completions client expects, and it decides what to "answer" by
// reading a directive the harness planted in the user's own prompt.
//
// Directive, verbatim in the turn's user_text:
//
//	TOOLPLAN <toolName> <absolute-path>
//
// The path is absolute because the endpoint has no idea which per-thread
// workspace this turn runs in — the harness does, and it writes the directive.
// TOOLPLAN none asks for a plain text answer.
//
// Reply rules, per request:
//   - a directive is in force and no assistant tool_call already named its
//     target -> that tool call (identity, not position: a resumed pi session
//     replays every earlier message, so "is there a tool result after the
//     directive" is unreliable — same lesson as l2-approval-upstream.mjs)
//   - otherwise -> a thinking-ish preamble, text, and finish_reason stop.
//
// Extra behaviours the smoke needs, all keyed off directives in the prompt:
//   NOISE     — emit unknown/garbage stdout-adjacent content? no: that is the
//               runtime's business. Here it means: send an unknown SSE chunk
//               shape and an unparseable data line before the real ones, so
//               the client's own tolerance is exercised.
//   REJECT    — answer the HTTP request with 400, to drive the failure path.
//   TWOCALLS  — emit two tool calls in one assistant message.
//
// Usage: node pi-smoke-upstream.mjs [--port 0] [--log <file>]
// Prints "upstream: http://127.0.0.1:<port>/v1" on stdout once listening.

import http from "node:http";
import fs from "node:fs";

const args = process.argv.slice(2);
let port = 0;
let logPath = "";
for (let i = 0; i < args.length; i += 1) {
  if (args[i] === "--port") port = Number(args[i + 1] ?? "0");
  else if (args[i] === "--log") logPath = args[i + 1] ?? "";
}

const FINAL_TEXT = "PI-SMOKE-COMPLETE";
const THINKING_TEXT = "PI-SMOKE-REASONING";

function note(line) {
  if (!logPath) return;
  fs.appendFileSync(logPath, `${new Date().toISOString()} ${line}\n`);
}

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

// plan answers two questions: which directive is in force, and has the call
// already gone out. "Already dispatched" is decided by IDENTITY — an assistant
// tool_call whose arguments already name this scenario's unique target — so a
// replayed session cannot make the endpoint re-issue the same call forever.
function plan(body) {
  const flat = [];
  collectText(body.messages ?? [], flat);
  const joined = flat.join("\n");
  const matches = [...joined.matchAll(/TOOLPLAN\s+([A-Za-z_]+)\s*(\S*)/g)];
  const flags = {
    noise: joined.includes("PISMOKE_NOISE"),
    twoCalls: joined.includes("PISMOKE_TWOCALLS"),
  };
  if (matches.length === 0) return { directive: null, satisfied: false, flags };
  const last = matches[matches.length - 1];
  if (last[1] === "none") return { directive: null, satisfied: false, flags };
  const directive = { tool: last[1], target: last[2] };

  let satisfied = false;
  for (const message of body.messages ?? []) {
    if (message?.role !== "assistant") continue;
    for (const call of message.tool_calls ?? []) {
      const raw = call?.function?.arguments;
      if (typeof raw === "string" && raw.includes(directive.target)) satisfied = true;
    }
  }
  return { directive, satisfied, flags };
}

function toolArgsFor(tool, target) {
  switch (tool) {
    case "write":
      return { path: target, content: "written by the pi L2 smoke\n" };
    case "edit":
      return { path: target, edits: [{ oldText: "seed", newText: "edited by the pi L2 smoke" }] };
    case "bash":
      // The surface probe: bash is outside the profile the runtime declares,
      // so the target doubles as the file a shell would create if it ran.
      return { command: `touch ${target}` };
    default:
      return { path: target };
  }
}

function chunk(res, delta, finish) {
  const payload = {
    id: "chatcmpl-pismoke",
    object: "chat.completion.chunk",
    created: Math.floor(Date.now() / 1000),
    model: "pi-smoke",
    choices: [{ index: 0, delta, finish_reason: finish ?? null }],
  };
  res.write(`data: ${JSON.stringify(payload)}\n\n`);
}

function streamToolCalls(res, calls) {
  chunk(res, { role: "assistant", content: "" });
  calls.forEach((call, index) => {
    chunk(res, {
      tool_calls: [
        { index, id: `call_pismoke_${index}`, type: "function", function: { name: call.tool, arguments: "" } },
      ],
    });
    // Split the arguments across two deltas: the client has to accumulate.
    const raw = JSON.stringify(toolArgsFor(call.tool, call.target));
    const cut = Math.max(1, Math.floor(raw.length / 2));
    chunk(res, { tool_calls: [{ index, function: { arguments: raw.slice(0, cut) } }] });
    chunk(res, { tool_calls: [{ index, function: { arguments: raw.slice(cut) } }] });
  });
  chunk(res, {}, "tool_calls");
}

function streamText(res) {
  chunk(res, { role: "assistant", content: "" });
  // reasoning_content is the de-facto field OpenAI-compatible servers use for
  // chain of thought; pi maps it to thinking deltas.
  chunk(res, { reasoning_content: THINKING_TEXT });
  chunk(res, { content: FINAL_TEXT });
  chunk(res, {}, "stop");
}

const server = http.createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    const url = new URL(req.url, "http://127.0.0.1");
    // The Authorization header is logged because the smoke asserts on it: the
    // user's configured key has to survive the trip into the subprocess.
    note(`${req.method} ${url.pathname} auth=${req.headers.authorization ?? "(none)"}`);
    if (url.pathname === "/v1/models") {
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ object: "list", data: [{ id: "pi-smoke", object: "model" }] }));
      return;
    }
    if (url.pathname !== "/v1/chat/completions") {
      res.writeHead(404).end();
      return;
    }
    let body = {};
    try {
      body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    } catch {
      body = {};
    }
    const flat = [];
    collectText(body.messages ?? [], flat);
    if (flat.join("\n").includes("PISMOKE_REJECT")) {
      note("-> 400 upstream refusal");
      res.writeHead(400, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: { message: "pi smoke: upstream refused", type: "invalid_request_error" } }));
      return;
    }

    res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store" });
    const { directive, satisfied, flags } = plan(body);
    if (flags.noise) {
      // Two lines the client has no business understanding, before anything
      // real: an unknown chunk shape and a non-JSON data line.
      res.write(`data: ${JSON.stringify({ id: "x", object: "chat.completion.chunk", choices: [], workmax_unknown: true })}\n\n`);
      res.write("data: not json at all\n\n");
    }
    if (directive && !satisfied) {
      const calls = flags.twoCalls
        ? [
            { tool: directive.tool, target: `${directive.target}.a` },
            { tool: directive.tool, target: `${directive.target}.b` },
          ]
        : [{ tool: directive.tool, target: directive.target }];
      note(`-> tool_calls ${calls.map((c) => `${c.tool} ${c.target}`).join(", ")}`);
      streamToolCalls(res, calls);
    } else {
      note("-> stop");
      streamText(res);
    }
    res.write("data: [DONE]\n\n");
    res.end();
  });
});

server.listen(port, "127.0.0.1", () => {
  process.stdout.write(`upstream: http://127.0.0.1:${server.address().port}/v1\n`);
});

//go:build desktop

package main

// The kill-check page is split into markup and an external script on purpose:
// the UI origin serves script-src 'self', which forbids inline scripts. That
// the first version of this page silently did nothing is itself a result —
// the policy is real. The shipped renderer already follows the same rule (one
// external script, no inline handlers, no inline styles).

const killCheckHTML = `<!doctype html>
<meta charset="utf-8">
<title>WorkMax SSE kill check</title>
<link rel="stylesheet" href="./harness.css">
<h3>WorkMax — W1 kill check ①: SSE through WKWebView</h3>
<div id="out">running…</div>
<script src="./killcheck.js"></script>
`

// killCheckJS is the webview half of kill check ①/③. It is a faithful,
// minimal port of the Electron preload's stream consumer: fetch with
// Accept: text/event-stream, response.body.getReader(), an incremental
// TextDecoder, and an SSE frame parser that only recognises fields at line
// start. Nothing else — no framework, no bundler — so a failure can only come
// from the webview's fetch/ReadableStream implementation or from the sidecar.
const killCheckJS = `
const out = document.getElementById("out");
function log(msg) { out.textContent += "\n" + msg; }

// --- SSE frame parser, ported from desktop/electron/src/preload.ts ---------
// Line endings are handled the same way (\n, \r\n, and a lone \r that is not
// yet known to be followed by \n is held back until more bytes arrive), and
// "data:"/"event:" are only recognised at the start of a line.
class SSEParser {
  constructor(onFrame) {
    this.decoder = new TextDecoder("utf-8", { fatal: true });
    this.buffer = "";
    this.event = "";
    this.data = [];
    this.onFrame = onFrame;
  }
  push(chunk) { this.buffer += this.decoder.decode(chunk, { stream: true }); this.drain(false); }
  finish() { this.buffer += this.decoder.decode(); this.drain(true); }
  drain(final) {
    for (;;) {
      const ending = this.findLineEnding(final);
      if (!ending) break;
      const line = this.buffer.slice(0, ending.index);
      this.buffer = this.buffer.slice(ending.index + ending.length);
      this.consume(line);
    }
    if (final && this.buffer !== "") { const line = this.buffer; this.buffer = ""; this.consume(line); }
    if (final && this.data.length > 0) { this.dispatch(); this.reset(); }
  }
  findLineEnding(final) {
    for (let i = 0; i < this.buffer.length; i++) {
      const c = this.buffer[i];
      if (c === "\n") return { index: i, length: 1 };
      if (c !== "\r") continue;
      if (i + 1 === this.buffer.length && !final) return null;
      return { index: i, length: this.buffer[i + 1] === "\n" ? 2 : 1 };
    }
    return null;
  }
  consume(line) {
    if (line === "") { if (this.data.length > 0) this.dispatch(); this.reset(); return; }
    if (line.startsWith(":")) return;
    const colon = line.indexOf(":");
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") this.event = value;
    else if (field === "data") this.data.push(value);
  }
  dispatch() { this.onFrame(this.event === "" ? "message" : this.event, this.data.join("\n")); }
  reset() { this.event = ""; this.data = []; }
}

// probe runs one full SSE turn and reports what happened. Two callers:
// "direct" (cross-origin to the loopback listener, the D2 design) and
// "proxy" (same-origin through the Wails asset server, remedy (d)).
async function probe(source, url, extraHeaders, params) {
  const result = {
    source: source, ok: false, status: 0, contentType: "",
    frames: 0, text: "", terminal: "", detail: "",
  };
  try {
    const response = await fetch(url, {
      method: "POST",
      credentials: "omit",
      redirect: "error",
      headers: Object.assign({
        "Content-Type": "application/json",
        "Accept": "text/event-stream",
      }, extraHeaders),
      body: JSON.stringify({
        turn_uuid: params.turnUUID,
        thread_uuid: params.threadUUID,
        user_text: "kill check",
        chat_mode: "ppt",
        payload: { stream: true },
      }),
    });
    result.status = response.status;
    result.contentType = response.headers.get("content-type") || "";
    log("[" + source + "] HTTP " + response.status + " content-type=" + result.contentType);

    if (!response.ok) {
      result.detail = "HTTP " + response.status + " " + (await response.text()).slice(0, 400);
      return result;
    }
    if (!response.body) { result.detail = "response.body is null — no ReadableStream"; return result; }

    let text = "";
    let frames = 0;
    let terminal = "";
    const parser = new SSEParser((event, data) => {
      frames++;
      if (event === "text_delta") {
        try { text += (JSON.parse(data).delta || ""); }
        catch (e) { result.detail = "text_delta payload is not JSON: " + e; }
      } else if (event === "done" || event === "proxy_error" || event === "canceled") {
        terminal = event;
      }
    });

    const reader = response.body.getReader();
    let chunks = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) { parser.finish(); break; }
      if (value) { chunks++; parser.push(value); }
    }
    log("[" + source + "] read " + chunks + " chunk(s), " + frames + " frame(s)");

    result.ok = true;
    result.frames = frames;
    result.text = text;
    result.terminal = terminal;
    if (text !== params.expected) {
      result.detail = "text mismatch: got " + text.length + " chars, want " + params.expected.length;
    }
    return result;
  } catch (e) {
    result.detail = String(e && e.stack ? e.stack : e);
    log("[" + source + "] threw: " + result.detail);
    return result;
  }
}

// containment runs the kill ③ checks: can this page reach anything it should
// not, and does the Wails runtime survive being loaded over loopback HTTP
// instead of the wails:// scheme (which decides whether W2 can use bindings)?
async function containment(params) {
  const c = {};

  // connect-src 'self' must stop the page reaching a different local origin,
  // even though that origin is up and would answer. A reachable target is the
  // point: a blocked fetch and an unreachable host both throw, so the target
  // has to be live for the result to mean anything.
  try {
    await fetch(params.foreignOrigin + "/chat/completions", { method: "GET" });
    c.cspBlocksForeignOrigin = false;
  } catch (e) {
    c.cspBlocksForeignOrigin = true;
    c.cspError = String(e);
  }

  // Only bundled assets exist; anything else is a 404, not a fall-through.
  try { c.unknownPath = (await fetch("definitely-not-bundled.js")).status; }
  catch (e) { c.unknownPath = String(e); }

  // Path traversal must not escape the asset FS.
  try { c.traversal = (await fetch("/../../etc/passwd")).status; }
  catch (e) { c.traversal = String(e); }

  // window.open with no WKUIDelegate: WebKit's default is to refuse.
  try { c.windowOpenReturnedNull = window.open("https://example.com", "_blank") === null; }
  catch (e) { c.windowOpenReturnedNull = "threw: " + e; }

  // Does the Wails runtime exist on an http:// origin — and, the question that
  // actually matters for W2, does a binding CALL succeed? The runtime posts to
  // window.location.origin + "/wails/runtime", which on this origin is our own
  // UI server, so the object existing proves nothing on its own.
  c.wailsRuntime = typeof window.wails;
  c.wailsInternal = typeof window._wails;
  try {
    const call = window.wails && window.wails.Call;
    c.wailsCallApi = typeof (call && call.ByName);
    if (call && typeof call.ByName === "function") {
      const started = Date.now();
      const res = await Promise.race([
        call.ByName("RuntimeAPI.Loopback"),
        new Promise((_, rej) => setTimeout(() => rej(new Error("binding call timed out")), 4000)),
      ]);
      c.wailsCall = "ok: " + JSON.stringify(res).slice(0, 120) + " (" + (Date.now() - started) + "ms)";
    } else {
      c.wailsCall = "no Call.ByName on this runtime";
    }
  } catch (e) {
    c.wailsCall = "FAILED: " + String(e).slice(0, 200);
  }
  return c;
}

async function run() {
  log("page origin: " + location.origin);
  const params = await (await fetch("killcheck/params")).json();
  log("params ok: sidecar port=" + params.port + ", expect " + params.expected.length + " chars");

  const probes = [];
  // (1) The D2 design: renderer fetches the sidecar's loopback port directly.
  probes.push(await probe(
    "direct",
    "http://127.0.0.1:" + params.port + "/agent/chat",
    { "X-Local-Token": params.token },
    params));
  // (2) The adopted shape: same-origin through the production UI routing,
  // which reverse-proxies to the sidecar and injects the token in Go.
  probes.push(await probe("sameorigin", "api/agent/chat", {}, params));

  const c = await containment(params);
  log("containment: " + JSON.stringify(c));

  const report = { origin: location.origin, probes: probes, containment: c };
  try {
    await fetch(params.reportURL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(report),
    });
  } catch (e) { log("report failed: " + e); }
  return report;
}

run().then((rep) => {
  for (const r of rep.probes) {
    const good = r.ok && !r.detail && r.terminal === "done";
    out.innerHTML += "\n<span class='" + (good ? "pass" : "fail") + "'>" +
      r.source + ": " + (good ? "PASS" : "FAIL") + "</span> " + (r.detail || "");
  }
});
`

// Served as a file, not an inline <style>: the UI origin sets
// style-src 'self', so a harness page with inline styles would not be
// testing what production serves.
const killCheckCSS = `body { font: 13px ui-monospace, Menlo, monospace; padding: 16px; white-space: pre-wrap; }
  .pass { color: #0a7; font-weight: bold; }
  .fail { color: #c33; font-weight: bold; }`

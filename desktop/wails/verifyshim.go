//go:build desktop

package main

// W3 acceptance: load the REAL renderer directory — the shipped
// lib/desktop-bridge.js and shim.js, served through the production UI routing
// — and check inside WKWebView that the two globals renderer.js depends on
// exist with the right shape, and that a full agent turn streams through the
// typed bridge.
//
// Deliberately not a unit test of a copy of the shim: the failure this guards
// against is the shipped files being wrong together, which only a real load
// can show.

// verifyShimHTML pulls in the same three scripts index.html does, minus
// renderer.js (which wants a full UI), plus the assertions.
const verifyShimHTML = `<!doctype html>
<meta charset="utf-8">
<title>WorkMax shim verification</title>
<link rel="stylesheet" href="./harness.css">
<h3>WorkMax — W3 shim verification</h3>
<div id="out">running…</div>
<script src="./lib/desktop-bridge.js"></script>
<script src="./shim.js"></script>
<script src="./verify.js"></script>
`

const verifyShimJS = `
const out = document.getElementById("out");
function log(m) { out.textContent += "\n" + m; }

// The exact surface renderer.js reaches for. Sourced from its own guard
// functions (desktopAuthBridge, desktopAgentBridge, desktopAgentCreateBridge,
// desktopAgentRecoveryBridge, desktopSettingsBridge) — if the shim satisfies
// these, renderer.js will accept the bridge instead of degrading.
const REQUIRED = {
  "auth.beginLogin": (b) => b.auth.beginLogin,
  "auth.loginStatus": (b) => b.auth.loginStatus,
  "auth.submitLoginPassword": (b) => b.auth.submitLoginPassword,
  "auth.cancelLogin": (b) => b.auth.cancelLogin,
  "agent.listSkills": (b) => b.agent.listSkills,
  "agent.startTurn": (b) => b.agent.startTurn,
  "agent.cancelTurn": (b) => b.agent.cancelTurn,
  "agent.uploadThreadFile": (b) => b.agent.uploadThreadFile,
  "agent.createThread": (b) => b.agent.createThread,
  "agent.listRecoverableTurns": (b) => b.agent.listRecoverableTurns,
  "agent.resumeTurn": (b) => b.agent.resumeTurn,
  "settings.getModelRoute": (b) => b.settings.getModelRoute,
  "settings.putModelRoute": (b) => b.settings.putModelRoute,
};

async function run() {
  const result = { source: "shim", ok: false, status: 0, contentType: "",
                   frames: 0, text: "", terminal: "", detail: "" };
  const missing = [];
  try {
    const params = await (await fetch("killcheck/params")).json();

    if (!window.workmaxLocal || typeof window.workmaxLocal.fetch !== "function") {
      missing.push("window.workmaxLocal.fetch");
    }
    const b = window.desktopBridge;
    if (!b) {
      missing.push("window.desktopBridge");
    } else {
      if (b.version !== "1.0.0-alpha.7") missing.push("version=" + b.version);
      for (const name of Object.keys(REQUIRED)) {
        let fn = null;
        try { fn = REQUIRED[name](b); } catch (e) { /* namespace missing */ }
        if (typeof fn !== "function") missing.push(name);
      }
    }
    // The token must never have reached the page: that is the property the
    // same-origin proxy bought back (D4 reversal). Guarded on length so a
    // degenerate test token cannot match incidental text and report a leak
    // that is not there.
    const leaked = JSON.stringify(window.workmaxLocal || {}) + " " + JSON.stringify(b && b.runtime || {});
    if (typeof params.token === "string" && params.token.length >= 16 && leaked.indexOf(params.token) !== -1) {
      missing.push("LOCAL TOKEN LEAKED INTO THE PAGE");
    }

    if (missing.length) {
      result.detail = "missing/incorrect: " + missing.join(", ");
      log(result.detail);
      return result;
    }
    log("bridge surface ok (" + Object.keys(REQUIRED).length + " methods, version " + b.version + ")");

    // Legacy surface: the three raw fetches renderer.js makes.
    const authStatus = await window.workmaxLocal.fetch("/auth/status");
    log("workmaxLocal.fetch('/auth/status') -> HTTP " + authStatus.status);
    result.status = authStatus.status;
    if (!authStatus.ok) { result.detail = "auth/status returned " + authStatus.status; return result; }

    // Typed surface end to end: a real streaming turn through the shipped shim.
    let text = "", frames = 0, terminal = "";
    await new Promise((resolve) => {
      // The typed bridge owns the wire shape: callers name intent
      // (threadUUID/userText/chatMode) and it builds thread_uuid/user_text/
      // chat_mode/payload itself. Passing the wire shape is rejected, which is
      // the contract doing its job.
      const open = b.agent.startTurn(
        { threadUUID: params.threadUUID, userText: "shim verification", chatMode: "ppt" },
        (event) => {
          frames++;
          if (event.type === "text_delta") text += event.delta;
          else if (event.type === "done" || event.type === "proxy_error" ||
                   event.type === "canceled" || event.type === "protocol_error") {
            terminal = event.type;
            if (event.error) result.detail = JSON.stringify(event.error);
            resolve();
          }
        });
      if (!open || !open.turnID) { result.detail = "startTurn returned no turnID"; resolve(); }
      setTimeout(() => { if (!terminal) { terminal = "timeout"; resolve(); } }, 20000);
    });

    result.ok = true;
    result.frames = frames;
    result.text = text;
    result.terminal = terminal;
    result.contentType = "text/event-stream";
    log("turn: " + frames + " events, " + text.length + " chars, terminal=" + terminal);
    if (text !== params.expected) {
      result.detail = "text mismatch: got " + text.length + " chars, want " + params.expected.length;
    }
    return result;
  } catch (e) {
    result.detail = String(e && e.stack ? e.stack : e);
    log("threw: " + result.detail);
    return result;
  }
}

run().then(async (r) => {
  const params = await (await fetch("killcheck/params")).json();
  await fetch(params.reportURL, { method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ origin: location.origin, probes: [r], containment: {} }) }).catch(() => {});
});
`

// Served as a file, not an inline <style>: the UI origin sets
// style-src 'self', so a harness page with inline styles would not be
// testing what production serves.
const verifyShimCSS = `body{font:13px ui-monospace,Menlo,monospace;padding:16px;white-space:pre-wrap}`

// The right-hand column: what the current turn is doing and what it drew on.
//
// Work log, reasoning caption, tool-approval cards, retrieved sources and
// produced deliverables. Everything here is a projection of a turn that
// events.js is already narrating — this module owns no turn state of its own,
// which is why it can be re-rendered at any moment without consulting anyone.
import { messageList, turnState } from "./dom.js";
import {
  MAX_TURN_TEXT_BYTES,
  isRecord,
  parseDesktopBridgeResult,
  utf8ByteLength,
} from "./protocol.js";
import { scheduleStreamFlush, streamBatch } from "./events.js";
import { formatMessageTime, scrollMessagesToEnd } from "./transcript.js";
import { desktopAgentBridge, state } from "./renderer.js";

// ---------------------------------------------------------------------------
// Task context panel
// ---------------------------------------------------------------------------
//
// The right rail describes the current run: which steps have happened, what
// the agent was given, and what it produced. Every value here is derived from
// state this renderer already holds or reads back from the sidecar.
//
// Deliverables is deliberately an empty state with a reason rather than a
// hidden section. A local turn produces text, not files — the panel says so
// instead of showing an empty box that looks broken.

// Looked up on use rather than bound at module scope. These functions are
// called from code that runs earlier in the file, and a const initialised down
// here would be in its temporal dead zone at that point — a ReferenceError
// that would only appear once an attachment or a turn touched the panel.
function ctxEl(id) {
  return document.querySelector(`#${id}`);
}

export const contextState = {
  sources: [],
  sourcesThreadUUID: null,
  retrieved: [],
  // The current turn's tool activity, in order. Per-turn like retrieval:
  // cleared when a new turn starts, because last turn's Writes are not what
  // this turn is doing.
  toolActivity: [],
  // Files the tool loop produced in this thread's workspace — the
  // Deliverables panel's content. Loaded on selection, refreshed when a turn
  // completes (that is when new files can exist).
  deliverables: [],
  deliverablesTruncated: false,
  // The finished turn's work log: its tool steps plus the files that
  // appeared. The in-place reconcile keeps the strip's own nodes alive, but
  // its fallback repaints the transcript from cache, and the cache stores
  // none of this — without a survivor copy the story would vanish whenever
  // the fallback runs.
  lastTurnLog: null,
  // File ids the user has checked in the Sources panel to send with the NEXT
  // request. Per-request on purpose — the label says "next request", so the
  // set clears once a turn owns the ids, exactly like the upload tray.
  selectedFileIDs: new Set(),
};

// Retrieval provenance is per-turn and lives only in memory: the sidecar
// announces it on the stream and does not persist it, so there is nothing to
// read back. Clearing on every new turn is therefore not tidiness — leaving
// the previous turn's list up would attribute this answer to sources it never
// saw, which is worse than showing nothing.
export function setRetrievedContext(sources) {
  contextState.retrieved = Array.isArray(sources) ? sources : [];
  renderTaskContext();
}

export function recordToolActivity(entry, activeTurn) {
  // Bounded: a pathological turn making thousands of calls must not grow an
  // unbounded array behind the panel.
  if (contextState.toolActivity.length >= 200) return;
  contextState.toolActivity.push(entry);
  renderTaskContext();
  // The step also lands inline, Codex-style: the transcript is a work log,
  // not a chat with a hidden engine room.
  if (activeTurn?.assistantBubble?.parentNode) {
    renderWorkLog(activeTurn.assistantBubble.parentNode, contextState.toolActivity, [], true);
  }
}

// Past this many steps, a finished log collapses to its summary — the
// Codex idiom: the work is a receipt once it is done, a narration only while
// it is happening.
const WORKLOG_COLLAPSE_AFTER = 3;

// renderWorkLog paints the step strip on one assistant message: tool steps
// in order, then the files the turn produced. Idempotent — it replaces the
// strip it finds, so streaming updates and post-repaint re-attachment share
// one code path.
//
// live=true is a streaming turn: always expanded, because watching the agent
// work is the point. A finished log longer than the threshold collapses to
// "N steps · K blocked"; produced rows stay visible either way — they are
// the deliverable, not the plumbing. Expansion survives re-renders by
// reading the outgoing strip before replacing it.
function renderWorkLog(wrapper, steps, produced, live = false, duration = "") {
  if (!wrapper) return;
  let wasExpanded = false;
  for (const child of Array.from(wrapper.children || [])) {
    if (child.classList?.contains("message-worklog")) {
      // Expansion survives a re-render only as a user's choice. A live strip
      // is expanded by definition, not by choice — when it settles in place
      // (the in-place reconcile keeps its nodes) it must still fold to the
      // receipt, exactly as it does after a full repaint.
      wasExpanded =
        child.classList.contains("expanded") && !child.classList.contains("live");
      child.remove();
    }
  }
  if (steps.length === 0 && produced.length === 0) return;
  const strip = document.createElement("ul");
  strip.className = "message-worklog";
  if (live) strip.classList.add("live");

  const collapsible = !live && steps.length > WORKLOG_COLLAPSE_AFTER;
  const expanded = live || !collapsible || wasExpanded;
  if (expanded) strip.classList.add("expanded");

  if (collapsible) {
    const denied = steps.filter((s) => s.denied).length;
    const header = document.createElement("li");
    header.className = "worklog-summary";
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "worklog-toggle";
    const parts = [`${steps.length} steps`];
    if (denied > 0) parts.push(`${denied} blocked`);
    if (duration) parts.push(duration);
    toggle.textContent = `${expanded ? "▾" : "▸"} ${parts.join(" · ")}`;
    toggle.addEventListener("click", () => {
      strip.classList.toggle("expanded");
      // Re-render through the same path so the glyph and rows agree.
      renderWorkLog(wrapper, steps, produced, live, duration);
    });
    header.appendChild(toggle);
    strip.appendChild(header);
  }

  const stepRows = expanded ? steps : [];
  for (const step of stepRows) {
    const row = document.createElement("li");
    row.className = "worklog-step" + (step.denied ? " denied" : "");
    const verb = document.createElement("span");
    verb.className = "worklog-verb";
    verb.textContent = step.name;
    row.appendChild(verb);
    if (step.target) {
      const target = document.createElement("span");
      target.className = "worklog-target";
      target.textContent = step.target;
      row.appendChild(target);
    }
    if (step.denied) {
      const why = document.createElement("span");
      why.className = "worklog-denied";
      why.textContent = step.reason ? `blocked — ${step.reason}` : "blocked";
      row.appendChild(why);
    }
    strip.appendChild(row);
  }
  for (const file of produced) {
    const row = document.createElement("li");
    row.className = "worklog-step produced";
    const verb = document.createElement("span");
    verb.className = "worklog-verb";
    verb.textContent = "Produced";
    const target = document.createElement("span");
    target.className = "worklog-target";
    target.textContent = file.path;
    row.append(verb, target);
    row.addEventListener("click", () => {
      const agent = window.desktopBridge?.agent;
      if (state.selectedThreadUUID && typeof agent?.revealWorkspace === "function") {
        void agent.revealWorkspace(state.selectedThreadUUID);
      }
    });
    strip.appendChild(row);
  }
  // Above the bubble: the steps happen before the words that explain them.
  const bubble = Array.from(wrapper.children || []).find((c) => c.classList?.contains("bubble"));
  if (bubble && typeof wrapper.insertBefore === "function") {
    wrapper.insertBefore(strip, bubble);
  } else {
    wrapper.appendChild(strip);
  }
}

// attachLastTurnLog re-hangs the survivor copy on the transcript's final
// assistant message — the one the cache repaint just rebuilt.
export function attachLastTurnLog() {
  const log = contextState.lastTurnLog;
  if (!log || log.threadUUID !== state.selectedThreadUUID) return;
  const assistants = Array.from(messageList.children || []).filter((n) =>
    n.classList?.contains("assistant")
  );
  const last = assistants[assistants.length - 1];
  if (last) renderWorkLog(last, log.steps, log.produced, false, log.duration);
}

// --- Reasoning caption and L2 tool approvals -------------------------------
//
// Both live on the streaming assistant message, above the bubble, and both are
// per-turn state kept on the activeTurn object itself: a superseded turn's
// caption or cards can never repaint under a newer turn because every entry
// point is already fenced by isCurrentTurn.

// insertAboveBubble parks a strip on the message wrapper, above the words it
// narrates — same placement rule as the work log.
function insertAboveBubble(wrapper, node) {
  const bubble = Array.from(wrapper.children || []).find((child) =>
    child.classList?.contains("bubble")
  );
  if (bubble && typeof wrapper.insertBefore === "function") {
    wrapper.insertBefore(node, bubble);
  } else {
    wrapper.appendChild(node);
  }
}

// The caption shows one line: the last non-empty line of the reasoning so far.
// Scanning backwards keeps the cost proportional to the tail, not the text.
function reasoningCaptionLine(text) {
  let end = text.length;
  while (end > 0) {
    const start = text.lastIndexOf("\n", end - 1);
    const line = text.slice(start + 1, end).trim();
    if (line !== "") return line;
    if (start < 0) break;
    end = start;
  }
  return "";
}

function ensureReasoningStrip(activeTurn) {
  const wrapper = activeTurn.assistantBubble?.parentNode;
  if (!wrapper) return null;
  if (
    activeTurn.reasoningStrip &&
    activeTurn.reasoningStrip.parentNode === wrapper
  ) {
    return activeTurn.reasoningStrip;
  }
  const strip = document.createElement("div");
  strip.className = "reasoning-strip";
  const caption = document.createElement("span");
  caption.className = "reasoning-caption";
  strip.appendChild(caption);
  insertAboveBubble(wrapper, strip);
  activeTurn.reasoningStrip = strip;
  activeTurn.reasoningCaption = caption;
  return strip;
}

export function recordReasoningDelta(activeTurn, delta) {
  const total = (activeTurn.reasoningTextBytes || 0) + utf8ByteLength(delta);
  // Reasoning is narration: past the display bound the rest is dropped rather
  // than failing a turn whose answer is still arriving.
  if (total > MAX_TURN_TEXT_BYTES) return;
  activeTurn.reasoningText = (activeTurn.reasoningText || "") + delta;
  activeTurn.reasoningTextBytes = total;
  // The caption repaints once per frame with the rest of the stream; per-delta
  // it would re-render a line nobody could read at that rate.
  scheduleStreamFlush(activeTurn);
  streamBatch.reasoningDirty = true;
}

export function paintReasoningCaption(activeTurn) {
  const strip = ensureReasoningStrip(activeTurn);
  if (!strip) return;
  const line = reasoningCaptionLine(activeTurn.reasoningText || "");
  activeTurn.reasoningCaption.textContent = line
    ? `Thinking… ${line}`
    : "Thinking…";
}

// When the turn settles, the live caption folds into a "Thought" label whose
// click reveals the full reasoning text. A turn that never sent reasoning has
// no strip; a strip whose turn ends before any text is removed outright.
function settleReasoningStrip(activeTurn) {
  const strip = activeTurn.reasoningStrip;
  if (!strip) return;
  const text = activeTurn.reasoningText || "";
  if (!text) {
    strip.remove();
    activeTurn.reasoningStrip = null;
    return;
  }
  strip.textContent = "";
  strip.classList.add("settled");
  const toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "reasoning-toggle";
  const detail = document.createElement("pre");
  detail.className = "reasoning-detail";
  detail.textContent = text;
  detail.hidden = true;
  const paint = () => {
    toggle.textContent = `${detail.hidden ? "▸" : "▾"} Thought`;
  };
  paint();
  toggle.addEventListener("click", () => {
    detail.hidden = !detail.hidden;
    paint();
  });
  strip.append(toggle, detail);
}

// Bounded like the work log: a runaway turn cannot stack cards without limit.
const MAX_APPROVAL_CARDS_PER_TURN = 40;

// Order fixed by the sidecar's decision vocabulary: [decision, button label,
// settled label].
const APPROVAL_DECISIONS = [
  ["allow_once", "Allow once", "Allowed once"],
  ["allow_session", "Allow this session", "Allowed for this session"],
  ["allow_always", "Always allow", "Always allowed"],
  ["deny", "Deny", "Denied"],
];

function desktopAgentApprovalBridge() {
  const desktop = window.desktopBridge;
  if (
    !isRecord(desktop) ||
    !isRecord(desktop.agent) ||
    typeof desktop.agent.approveTurnTool !== "function"
  ) {
    return null;
  }
  return desktop.agent;
}

// The card collapses in place to its outcome — the question is answered, so
// the buttons go away rather than merely disabling.
function settleApprovalCard(entry, label, tone) {
  entry.answered = true;
  entry.card.textContent = "";
  entry.card.classList.add("approval-settled");
  const result = document.createElement("span");
  result.className = tone ? `approval-result ${tone}` : "approval-result";
  result.textContent = label;
  entry.card.appendChild(result);
}

export function presentApprovalRequest(activeTurn, event) {
  const wrapper = activeTurn.assistantBubble?.parentNode;
  if (!wrapper) return;
  if (!activeTurn.approvalCards) activeTurn.approvalCards = new Map();
  // One card per approval id: a duplicated frame must not stack a second
  // question for the same answer.
  if (activeTurn.approvalCards.has(event.id)) return;
  if (activeTurn.approvalCards.size >= MAX_APPROVAL_CARDS_PER_TURN) return;

  const card = document.createElement("div");
  card.className = "approval-card";
  const title = document.createElement("div");
  title.className = "approval-title";
  title.textContent = event.target
    ? `Agent requests to run ${event.name} · ${event.target}`
    : `Agent requests to run ${event.name}`;
  const note = document.createElement("div");
  note.className = "approval-note";
  note.hidden = true;
  const actions = document.createElement("div");
  actions.className = "approval-actions";
  const entry = { card, note, actions, answered: false };
  for (const [decision, buttonLabel, settledLabel] of APPROVAL_DECISIONS) {
    const button = document.createElement("button");
    button.type = "button";
    button.className =
      decision === "deny" ? "approval-button deny" : "approval-button";
    button.textContent = buttonLabel;
    button.addEventListener("click", () => {
      submitApprovalDecision(activeTurn, event.id, decision, settledLabel, entry);
    });
    actions.appendChild(button);
  }
  card.append(title, actions, note);
  insertAboveBubble(wrapper, card);
  activeTurn.approvalCards.set(event.id, entry);
  scrollMessagesToEnd();
}

function setApprovalButtonsDisabled(entry, disabled) {
  for (const button of Array.from(entry.actions.children || [])) {
    button.disabled = disabled;
  }
}

function submitApprovalDecision(activeTurn, approvalID, decision, settledLabel, entry) {
  if (entry.answered) return;
  const agent = desktopAgentApprovalBridge();
  if (!agent || !activeTurn.turnID) {
    settleApprovalCard(entry, "Approvals are unavailable", "expired");
    return;
  }
  // Answered the moment the click lands: a second click while the request is
  // in flight must not send a second decision.
  entry.answered = true;
  entry.note.hidden = true;
  setApprovalButtonsDisabled(entry, true);
  agent
    .approveTurnTool(activeTurn.turnID, {
      approval_id: approvalID,
      decision,
    })
    .then((result) => {
      if (!isRecord(result) || typeof result.ok !== "boolean") {
        throw new Error("Malformed approval result");
      }
      if (result.ok) {
        settleApprovalCard(
          entry,
          settledLabel,
          decision === "deny" ? "denied" : "allowed"
        );
        return;
      }
      if (result.status === 404) {
        // The pending set no longer knows this id: the turn moved on
        // (timeout, cancel, completion). Expired, not an error.
        settleApprovalCard(entry, "Expired", "expired");
        return;
      }
      throw new Error("Approval delivery failed");
    })
    .catch(() => {
      // The decision never landed, so the question is still open — unless the
      // turn already settled this card as expired while the request was out.
      if (entry.card.classList.contains("approval-settled")) return;
      entry.answered = false;
      setApprovalButtonsDisabled(entry, false);
      entry.note.textContent = "The decision could not be delivered. Try again.";
      entry.note.hidden = false;
    });
}

// A terminal turn takes its unanswered questions with it: the sidecar's
// pending set is keyed by turn, so a card that outlives the turn could only
// ever answer into a 404.
function expireApprovalCards(activeTurn) {
  if (!activeTurn.approvalCards) return;
  for (const entry of activeTurn.approvalCards.values()) {
    if (entry.answered) continue;
    settleApprovalCard(entry, "Expired", "expired");
  }
}

// settleTurnNarration is the single hook every local turn terminal calls:
// done, canceled, proxy/protocol errors, busy retention — the caption folds
// and the open approval cards expire together.
export function settleTurnNarration(activeTurn) {
  settleReasoningStrip(activeTurn);
  expireApprovalCards(activeTurn);
}

function formatRetrievalScore(score) {
  if (typeof score !== "number" || !Number.isFinite(score)) return "";
  return `${Math.round(score * 100)}% match`;
}

function formatFileSize(bytes) {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// Mirrors the reference implementation's four steps. "Brief captured" is true
// once the thread has any message or a turn is running, because that is the
// point at which the agent has actually been told something.
function buildRunSteps() {
  const running = Boolean(state.activeTurn);
  const messageCount = messageList ? messageList.children.length : 0;
  const sourceCount = contextState.sources.length + state.pendingFiles.length;
  // The tone class, not the text: the pill's label now carries a duration
  // suffix, and deriving state from prose is how this comparison silently
  // broke the moment the label grew.
  const failed = turnState && turnState.classList?.contains("is-error");

  const brief = messageCount > 0 || running;
  return [
    {
      label: "Brief captured",
      state: brief ? "complete" : "pending",
      detail: brief ? "Complete" : "Waiting",
    },
    {
      label: "Sources",
      // No sources is a valid way to run, so this is never "pending" —
      // reporting it as incomplete would imply the run is blocked on it.
      state: sourceCount > 0 ? "complete" : "neutral",
      detail: sourceCount > 0
        ? `${sourceCount} source${sourceCount === 1 ? "" : "s"}`
        : "None attached",
    },
    {
      label: "Agent execution",
      state: failed ? "failed" : running ? "active" : brief ? "complete" : "pending",
      detail: agentExecutionDetail(running, failed, brief),
    },
    {
      label: "Deliverables",
      state: contextState.deliverables.length > 0 ? "complete" : "neutral",
      detail: contextState.deliverables.length > 0
        ? `${contextState.deliverables.length} file${contextState.deliverables.length === 1 ? "" : "s"}`
        : "None yet",
    },
  ];
}

// The execution step used to be a binary; with a tool loop it has a story.
// While running it names the latest tool; finished, it counts what ran and
// what was blocked.
function agentExecutionDetail(running, failed, brief) {
  const activity = contextState.toolActivity;
  const denied = activity.filter((a) => a.denied).length;
  if (running) {
    const last = activity[activity.length - 1];
    if (last) return last.denied ? `${last.name} blocked` : `${last.name}…`;
    return "In progress";
  }
  if (failed) return "Failed";
  if (!brief) return "Waiting";
  if (activity.length === 0) return "Complete";
  const calls = `${activity.length - denied} tool call${activity.length - denied === 1 ? "" : "s"}`;
  return denied > 0 ? `${calls} · ${denied} blocked` : calls;
}

const RUN_STEP_MARKS = {
  complete: "✓",
  active: "◐",
  failed: "✕",
  pending: "○",
  neutral: "–",
};

function renderRunOverview() {
  if (!ctxEl("run-overview-list")) return;
  const steps = buildRunSteps();
  ctxEl("run-overview-list").innerHTML = "";
  for (const step of steps) {
    const item = document.createElement("li");
    item.className = `run-step is-${step.state}`;

    const mark = document.createElement("span");
    mark.className = "run-step-mark";
    mark.textContent = RUN_STEP_MARKS[step.state] ?? "–";

    const label = document.createElement("span");
    label.className = "run-step-label";
    label.textContent = step.label;

    const detail = document.createElement("span");
    detail.className = "run-step-detail";
    detail.textContent = step.detail;

    item.append(mark, label, detail);
    ctxEl("run-overview-list").appendChild(item);
  }
  const done = steps.filter((s) => s.state === "complete").length;
  if (ctxEl("run-overview-meta")) ctxEl("run-overview-meta").textContent = `${done}/${steps.length}`;
}

function renderSources() {
  if (!ctxEl("sources-list")) return;
  // Files uploaded in this session but not yet reloaded from the sidecar are
  // shown alongside the persisted ones, so a just-attached file appears
  // immediately rather than after the next refresh.
  const pending = state.pendingFiles
    .filter((file) => file.status !== "error")
    .map((file) => ({
      file_id: file.id,
      file_name: file.name,
      file_size: file.size,
      on_disk: true,
      pending: file.status === "uploading",
    }));
  const persistedIds = new Set(contextState.sources.map((f) => f.file_id));
  const items = [
    ...contextState.sources,
    ...pending.filter((f) => !f.file_id || !persistedIds.has(f.file_id)),
  ];

  // Ids that stopped existing (file deleted, thread reloaded) must not linger
  // in the selection, or the count lies and a dead id rides into a turn.
  const selectable = new Set(
    contextState.sources
      .filter((f) => f.on_disk !== false)
      .map((f) => f.file_id)
  );
  for (const id of Array.from(contextState.selectedFileIDs)) {
    if (!selectable.has(id)) contextState.selectedFileIDs.delete(id);
  }

  ctxEl("sources-list").innerHTML = "";
  for (const file of items) {
    const item = document.createElement("li");
    item.className = "context-item" + (file.on_disk === false ? " is-missing" : "");

    // Persisted, readable files can be re-attached to the next request. A
    // fresh upload is already armed through the tray, and a file whose bytes
    // are gone has nothing to attach — neither gets a checkbox.
    const checkable = !file.pending && file.on_disk !== false && persistedIds.has(file.file_id);
    if (checkable) {
      const label = document.createElement("label");
      label.className = "context-item-select";
      const box = document.createElement("input");
      box.type = "checkbox";
      box.className = "source-select";
      box.checked = contextState.selectedFileIDs.has(file.file_id);
      box.addEventListener("change", () => {
        if (box.checked) contextState.selectedFileIDs.add(file.file_id);
        else contextState.selectedFileIDs.delete(file.file_id);
        renderTaskContext();
      });
      const name = document.createElement("span");
      name.className = "context-item-name";
      name.textContent = file.file_name;
      label.append(box, name);
      item.appendChild(label);
    } else {
      const name = document.createElement("span");
      name.className = "context-item-name";
      name.textContent = file.file_name;
      item.appendChild(name);
    }

    const meta = document.createElement("span");
    meta.className = "context-item-meta";
    meta.textContent = file.on_disk === false
      // The row survives but the bytes do not, which is a different problem
      // from "no attachments" and needs to be visible.
      ? "Missing on disk"
      : file.pending
        ? "Uploading…"
        : formatFileSize(file.file_size);

    item.appendChild(meta);
    ctxEl("sources-list").appendChild(item);
  }

  if (ctxEl("sources-empty")) ctxEl("sources-empty").hidden = items.length > 0;
  if (ctxEl("sources-meta")) ctxEl("sources-meta").textContent = String(items.length);
  if (ctxEl("sources-selected")) {
    const chosen = contextState.selectedFileIDs.size;
    ctxEl("sources-selected").hidden = chosen === 0;
    ctxEl("sources-selected").textContent =
      `${chosen} selected for the next request`;
  }
  if (ctxEl("context-count")) ctxEl("context-count").textContent = String(items.length);
}

// What the answer was actually grounded in. Until this existed the retrieval
// step was invisible: the model answered from indexed documents and the user
// had no way to tell that from the model inventing it.
function renderRetrieved() {
  if (!ctxEl("retrieved-list")) return;
  const items = contextState.retrieved;
  ctxEl("retrieved-list").innerHTML = "";
  for (const source of items) {
    const item = document.createElement("li");
    item.className = `context-item is-${source.kind}`;

    const name = document.createElement("span");
    name.className = "context-item-name";
    name.textContent = source.label;

    const meta = document.createElement("span");
    meta.className = "context-item-meta";
    meta.textContent = formatRetrievalScore(source.score);

    item.append(name, meta);

    // The passage itself, not a summary of it — a summary would be a second
    // thing that could be wrong about the thing being checked.
    if (source.snippet) {
      const snippet = document.createElement("p");
      snippet.className = "context-item-snippet";
      snippet.textContent = source.snippet;
      item.appendChild(snippet);
    }
    ctxEl("retrieved-list").appendChild(item);
  }
  if (ctxEl("retrieved-empty")) ctxEl("retrieved-empty").hidden = items.length > 0;
  if (ctxEl("retrieved-meta")) ctxEl("retrieved-meta").textContent = String(items.length);
  // The whole section stands down when there is nothing retrieved: it is
  // per-turn transient, and an empty module whose body explains its own
  // emptiness costs more attention than it returns.
  if (ctxEl("context-retrieved")) ctxEl("context-retrieved").hidden = items.length === 0;
}

// What the agent produced: the workspace listing, newest first. Until L2
// this panel could only explain its own emptiness; now local tool-loop turns
// put real files here.
function renderDeliverables() {
  if (!ctxEl("deliverables-list")) return;
  const items = contextState.deliverables;
  ctxEl("deliverables-list").innerHTML = "";
  for (const file of items) {
    const item = document.createElement("li");
    item.className = "context-item";
    const name = document.createElement("span");
    name.className = "context-item-name";
    name.textContent = file.path;
    const meta = document.createElement("span");
    meta.className = "context-item-meta";
    meta.textContent = `${formatFileSize(file.size)} · ${formatMessageTime(file.modified_at)}`;
    item.append(name, meta);
    ctxEl("deliverables-list").appendChild(item);
  }
  if (ctxEl("deliverables-empty")) ctxEl("deliverables-empty").hidden = items.length > 0;
  if (ctxEl("open-workspace-button")) {
    // Offered only when there is something to open, and only when the bridge
    // can actually open it — a button that silently fails is worse than none.
    ctxEl("open-workspace-button").hidden =
      items.length === 0 ||
      typeof window.desktopBridge?.agent?.revealWorkspace !== "function";
  }
  if (ctxEl("deliverables-meta")) {
    ctxEl("deliverables-meta").textContent = contextState.deliverablesTruncated
      ? `${items.length}+`
      : String(items.length);
  }
}

export function renderTaskContext() {
  renderRunOverview();
  renderSources();
  renderRetrieved();
  renderDeliverables();
}

// Reads what the tool loop produced in this thread's workspace. Failure
// degrades to an empty panel — the conversation itself is unaffected.
export async function loadWorkspaceDeliverables(threadUUID) {
  const agent = window.desktopBridge?.agent;
  if (!threadUUID || !agent || typeof agent.listWorkspaceFiles !== "function") return;
  try {
    const result = parseDesktopBridgeResult(
      await agent.listWorkspaceFiles(threadUUID),
      "agent workspace result"
    );
    // The selection may have moved while this was in flight.
    if (contextState.sourcesThreadUUID !== threadUUID) return;
    if (result.ok && isRecord(result.data) && Array.isArray(result.data.items)) {
      contextState.deliverables = result.data.items.filter(
        (f) => isRecord(f) && typeof f.path === "string"
      );
      contextState.deliverablesTruncated = result.data.truncated === true;
    }
  } catch {
    return;
  }
  renderTaskContext();
}

// Reads the attachments the sidecar has for this thread. Uploads persisted
// before this route existed, but nothing could read them back, so reopening a
// thread showed an empty Sources panel while the files were still on disk.
export async function loadThreadSources(threadUUID) {
  contextState.sourcesThreadUUID = threadUUID;
  contextState.sources = [];
  // Provenance belongs to a turn, and the turn belongs to a thread. Carrying
  // it across a switch would credit this thread's answer to another one's
  // documents. The selection likewise: these ids name another thread's files.
  contextState.retrieved = [];
  contextState.toolActivity = [];
  contextState.deliverables = [];
  contextState.deliverablesTruncated = false;
  contextState.lastTurnLog = null;
  // Redundant with renderSources' pruning-to-current-sources, deliberately:
  // either alone prevents one thread's ids riding into another's turn, and
  // the negative test only fails when both are removed.
  contextState.selectedFileIDs = new Set();
  renderTaskContext();
  void loadWorkspaceDeliverables(threadUUID);
  if (!threadUUID) return;

  const agent = desktopAgentBridge();
  if (!agent || typeof agent.listThreadFiles !== "function") return;
  try {
    const result = await agent.listThreadFiles(threadUUID);
    // A thread switch mid-flight must not paint the previous thread's files.
    if (contextState.sourcesThreadUUID !== threadUUID) return;
    if (result && result.ok && result.data && Array.isArray(result.data.items)) {
      contextState.sources = result.data.items;
    }
  } catch {
    // The panel degrades to session-only sources; the conversation itself is
    // unaffected, so this must not surface as a turn error.
  }
  renderTaskContext();
}

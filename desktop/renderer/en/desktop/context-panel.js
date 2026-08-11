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

// A step is SETTLED once something closed it: the guard or the user blocked
// it, or the tool ran and reported back. The distinction carries weight
// beyond styling — a settled step is no longer a candidate for the frames
// that arrive about a call in flight (a denial, an approval question), so
// this is the predicate that keeps a second Write to the same file from
// swallowing the first one's answer.
function isStepSettled(step) {
  return Boolean(step.denied || step.done);
}

// repaintWorkLog re-hangs the live strip on the streaming message. Every
// mutation of a step goes through here, so a row that changes shape (a
// denial folding in, an approval card becoming the step) repaints by the
// same path that drew it.
function repaintWorkLog(activeTurn) {
  const wrapper = activeTurn?.assistantBubble?.parentNode;
  if (!wrapper) return;
  renderWorkLog(wrapper, contextState.toolActivity, [], true);
}

export function recordToolActivity(entry, activeTurn) {
  // A denial lands as a SECOND frame about a call the log already has: the
  // sidecar announces the tool the moment the model asks for it, and only
  // then does the guard refuse it or the user decline the card. Appending
  // would read as two steps — "Write outline.md" followed by "Write
  // outline.md blocked" — and count as two in the "N steps · 1 blocked"
  // receipt. One call is one row: fold the denial into the step it settles.
  if (entry.denied) {
    for (let i = contextState.toolActivity.length - 1; i >= 0; i -= 1) {
      const step = contextState.toolActivity[i];
      if (isStepSettled(step) || step.name !== entry.name || step.target !== entry.target) continue;
      step.denied = true;
      step.reason = entry.reason;
      renderTaskContext();
      repaintWorkLog(activeTurn);
      return;
    }
  }
  // Bounded: a pathological turn making thousands of calls must not grow an
  // unbounded array behind the panel.
  if (contextState.toolActivity.length >= 200) return;
  contextState.toolActivity.push(entry);
  renderTaskContext();
  // The step also lands inline, Codex-style: the transcript is a work log,
  // not a chat with a hidden engine room.
  repaintWorkLog(activeTurn);
}

// The far end of a tool call. Both engines report it — the CLI feeds the
// result back as a user message, pi sends tool_execution_end — and neither
// adds a row: the step is already on screen, and this is the frame that
// closes it. A result the log has no open step for settles nothing and is
// dropped, because a row with a verb and no story is worse than silence.
export function recordToolResult(entry, activeTurn) {
  for (const step of contextState.toolActivity) {
    if (isStepSettled(step) || step.name !== entry.name) continue;
    // pi's tool_execution_end cannot name the file it touched, so a result
    // with no target settles the OLDEST open call of that tool — which is
    // the one that just returned, both loops being sequential. When the
    // engine does name a target (the claude pump correlates it through the
    // tool_use id), the match is exact.
    if (entry.target !== "" && step.target !== entry.target) continue;
    step.done = true;
    step.failed = entry.isError === true;
    renderTaskContext();
    repaintWorkLog(activeTurn);
    return;
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
    strip.appendChild(renderWorkLogStep(step));
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

// One row of the work log. A step is a line of prose about a call — verb,
// target, and whatever settled it — and when an approval question is about
// THIS call the question is asked on this row rather than beside it: the
// tool_use frame always precedes the approval_request (the CLI announces
// before it asks, and no renderer can reorder that), so a separate card
// meant the screen showed a step that looked done next to someone asking
// whether it may happen. Answering resolves the row in place.
function renderWorkLogStep(step) {
  const row = document.createElement("li");
  const approval = step.approval;
  const awaiting = Boolean(approval) && !approval.settled;
  row.className = "worklog-step";
  if (step.denied) row.classList.add("denied");
  else if (step.failed) row.classList.add("failed");
  else if (step.done) row.classList.add("done");
  if (awaiting) row.classList.add("awaiting");

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
  } else if (step.failed) {
    const why = document.createElement("span");
    why.className = "worklog-denied";
    why.textContent = "failed";
    row.appendChild(why);
  }
  if (!approval) return row;

  if (awaiting) {
    const ask = document.createElement("span");
    ask.className = "approval-ask";
    ask.textContent = "Awaiting approval";
    row.appendChild(ask);
    row.appendChild(buildApprovalActions(approval));
    if (approval.note) {
      const note = document.createElement("span");
      note.className = "approval-note";
      note.textContent = approval.note;
      row.appendChild(note);
    }
    return row;
  }
  // A denial that folded into this step already states the outcome, in the
  // vocabulary the log uses for every other refusal. "Denied" on top of
  // "blocked — 用户拒绝了此操作" is the same fact twice.
  if (!step.denied) {
    const result = document.createElement("span");
    result.className = approval.tone ? `approval-result ${approval.tone}` : "approval-result";
    result.textContent = approval.label;
    row.appendChild(result);
  }
  return row;
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

// buildApprovalActions draws the four decisions. Shared by both shapes a
// question can take — a row of the work log, or a card of its own — because
// the buttons are the same question either way, and the merged row is
// rebuilt from scratch on every repaint.
function buildApprovalActions(entry) {
  const actions = document.createElement("div");
  actions.className = "approval-actions";
  for (const [decision, buttonLabel, settledLabel] of APPROVAL_DECISIONS) {
    const button = document.createElement("button");
    button.type = "button";
    button.className =
      decision === "deny" ? "approval-button deny" : "approval-button";
    button.textContent = buttonLabel;
    button.disabled = entry.answered;
    button.addEventListener("click", () => {
      submitApprovalDecision(entry, decision, settledLabel);
    });
    actions.appendChild(button);
  }
  return actions;
}

// refreshApproval reflects entry state that is not yet an outcome — buttons
// disabled while a decision is in flight, a delivery failure to retry past.
// A merged question repaints through the work log; a standalone card owns
// its nodes and is edited in place.
function refreshApproval(entry) {
  if (entry.merged) {
    repaintWorkLog(entry.turn);
    return;
  }
  if (!entry.card) return;
  for (const button of Array.from(entry.actions.children || [])) {
    button.disabled = entry.answered;
  }
  entry.noteEl.textContent = entry.note;
  entry.noteEl.hidden = entry.note === "";
}

// The question collapses in place to its outcome — answered, so the buttons
// go away rather than merely disabling. In place means the step's own row
// when the question was merged into one: the turn ends with as many rows as
// it made calls.
function settleApproval(entry, label, tone) {
  entry.answered = true;
  entry.settled = true;
  entry.label = label;
  entry.tone = tone;
  entry.note = "";
  if (entry.merged) {
    repaintWorkLog(entry.turn);
    return;
  }
  entry.card.textContent = "";
  entry.card.classList.add("approval-settled");
  const result = document.createElement("span");
  result.className = tone ? `approval-result ${tone}` : "approval-result";
  result.textContent = label;
  entry.card.appendChild(result);
}

// findStepForApproval returns the work-log step this question is about, or
// null when there is none to merge with.
//
// The rule is deliberately narrow: same turn (the log is per-turn), same
// tool, same target, and the step must still be open. Forward scan taking
// the first unclaimed match — two identical calls announced back to back are
// asked about in the same order, so the first question belongs to the first
// step. Anything that misses (a guard denial that never announced, a
// question that arrived before any tool_use, a second question for a step
// that already owns one) keeps its own card, which is the honest shape when
// there is no step on screen to point at.
function findStepForApproval(event) {
  for (const step of contextState.toolActivity) {
    if (step.approval || isStepSettled(step)) continue;
    if (step.name !== event.name || step.target !== event.target) continue;
    return step;
  }
  return null;
}

function buildApprovalCard(wrapper, entry) {
  const card = document.createElement("div");
  card.className = "approval-card";
  const title = document.createElement("div");
  title.className = "approval-title";
  title.textContent = entry.target
    ? `Agent requests to run ${entry.name} · ${entry.target}`
    : `Agent requests to run ${entry.name}`;
  const note = document.createElement("div");
  note.className = "approval-note";
  note.hidden = true;
  entry.card = card;
  entry.noteEl = note;
  entry.actions = buildApprovalActions(entry);
  card.append(title, entry.actions, note);
  insertAboveBubble(wrapper, card);
}

export function presentApprovalRequest(activeTurn, event) {
  const wrapper = activeTurn.assistantBubble?.parentNode;
  if (!wrapper) return;
  if (!activeTurn.approvalCards) activeTurn.approvalCards = new Map();
  // One question per approval id: a duplicated frame must not stack a second
  // question for the same answer.
  if (activeTurn.approvalCards.has(event.id)) return;
  if (activeTurn.approvalCards.size >= MAX_APPROVAL_CARDS_PER_TURN) return;

  const entry = {
    id: event.id,
    turn: activeTurn,
    name: event.name,
    target: event.target,
    answered: false,
    settled: false,
    label: "",
    tone: "",
    note: "",
    // merged: the question is drawn as a row of the work log rather than a
    // card of its own. A flag, not a reference back to the step — the step
    // already points here, and two objects pointing at each other is a cycle
    // nobody needs to own.
    merged: false,
    card: null,
    actions: null,
    noteEl: null,
  };
  activeTurn.approvalCards.set(event.id, entry);

  const step = findStepForApproval(event);
  if (step) {
    entry.merged = true;
    step.approval = entry;
    renderTaskContext();
    repaintWorkLog(activeTurn);
  } else {
    buildApprovalCard(wrapper, entry);
  }
  scrollMessagesToEnd();
}

function submitApprovalDecision(entry, decision, settledLabel) {
  if (entry.answered) return;
  const agent = desktopAgentApprovalBridge();
  if (!agent || !entry.turn.turnID) {
    settleApproval(entry, "Approvals are unavailable", "expired");
    return;
  }
  // Answered the moment the click lands: a second click while the request is
  // in flight must not send a second decision.
  entry.answered = true;
  entry.note = "";
  refreshApproval(entry);
  agent
    .approveTurnTool(entry.turn.turnID, {
      approval_id: entry.id,
      decision,
    })
    .then((result) => {
      if (!isRecord(result) || typeof result.ok !== "boolean") {
        throw new Error("Malformed approval result");
      }
      if (result.ok) {
        settleApproval(
          entry,
          settledLabel,
          decision === "deny" ? "denied" : "allowed"
        );
        return;
      }
      if (result.status === 404) {
        // The pending set no longer knows this id: the turn moved on
        // (timeout, cancel, completion). Expired, not an error.
        settleApproval(entry, "Expired", "expired");
        return;
      }
      throw new Error("Approval delivery failed");
    })
    .catch(() => {
      // The decision never landed, so the question is still open — unless the
      // turn already settled it as expired while the request was out.
      if (entry.settled) return;
      entry.answered = false;
      entry.note = "The decision could not be delivered. Try again.";
      refreshApproval(entry);
    });
}

// A terminal turn takes its unanswered questions with it: the sidecar's
// pending set is keyed by turn, so a question that outlives the turn could
// only ever answer into a 404. Merged questions expire on their own row,
// which is the last thing that happens to a step nobody answered for.
function expireApprovalCards(activeTurn) {
  if (!activeTurn.approvalCards) return;
  for (const entry of activeTurn.approvalCards.values()) {
    if (entry.answered) continue;
    settleApproval(entry, "Expired", "expired");
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
    // A step holding an unanswered question is what the run is actually
    // waiting on, wherever it sits in the list — saying "Write…" while the
    // loop is blocked on the user reads as progress that is not happening.
    const asking = activity.find((a) => a.approval && !a.approval.settled);
    if (asking) return `${asking.name} · awaiting approval`;
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

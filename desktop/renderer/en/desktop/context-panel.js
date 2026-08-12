// The right-hand column: what the current turn is doing and what it drew on.
//
// Work log, reasoning caption, tool-approval cards, retrieved sources and
// produced deliverables. Everything here is a projection of a turn that
// events.js is already narrating — this module owns no turn state of its own,
// which is why it can be re-rendered at any moment without consulting anyone.
import { messageList } from "./dom.js";
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
// Workspace panel
// ---------------------------------------------------------------------------
//
// The right rail is the thread's materials ledger: what the agent produced,
// what you gave it, what the answer was grounded in. It narrates nothing about
// the run.
//
// That is the split, and it is deliberate. A tool step is read against the
// words it produced, so the work log stays on the message — it is the same
// object as the answer. A file is read against the thread, because turn 1
// wrote it and turn 5 rewrote it and only the current version matters; the
// rail is the only place that can say that once. The old panel tried to be
// both and ended up disagreeing with itself: "Agent execution · 4 tool calls"
// beside an inline strip reading "5 steps" (it counted denials differently),
// Sources and Deliverables each stated twice, and four fixed phases —
// Brief captured / Sources / Agent execution / Deliverables — imposed on a
// pure-chat turn that has no phases and on a fifteen-tool turn that has
// fifteen.
//
// What survives from the run is one live line, and only while the turn is
// running: what the loop is on, loudest when the loop is blocked on the user.
// A reader who has scrolled away cannot afford to miss that one, and it is a
// pointer rather than a summary — it disappears when the turn settles, and the
// finished story stays on the message.
//
// Sections appear only when they have rows, so a fresh thread is one sentence
// rather than three boxes explaining their own emptiness.

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

// The live line. One sentence about a turn that is happening, and nothing at
// all otherwise — a finished run is told on the message it produced, and a
// second telling here was the duplication this panel is being rid of.
//
// The step number is the only count in the rail with a moving referent, and it
// is spelled out ("Step 4") because a bare 4 beside a tool name is unreadable.
// A question the loop is blocked on wins over the latest call wherever it sits
// in the list: saying "Write…" while nothing can proceed reads as progress
// that is not happening.
function renderRunLine() {
  const line = ctxEl("context-run-line");
  if (!line) return;
  if (!state.activeTurn) {
    line.hidden = true;
    line.textContent = "";
    line.className = "context-run";
    return;
  }
  const activity = contextState.toolActivity;
  const asking = activity.find((a) => a.approval && !a.approval.settled);
  const last = activity[activity.length - 1];
  line.hidden = false;
  line.textContent = "";
  line.className = asking ? "context-run is-blocked" : "context-run";

  const label = document.createElement("span");
  label.className = "context-run-label";
  const detail = document.createElement("span");
  detail.className = "context-run-detail";
  if (asking) {
    label.textContent = "Needs your approval";
    detail.textContent = asking.target ? `${asking.name} · ${asking.target}` : asking.name;
  } else if (last) {
    label.textContent = `Step ${activity.length}`;
    detail.textContent = last.target ? `${last.name} · ${last.target}` : last.name;
  } else {
    label.textContent = "Running";
    detail.textContent = "";
  }
  line.append(label, detail);
}

// One row of the rail: a name on the left, its facts on the right, nothing
// drawn around it. The old rows were cards — a background, an inset border and
// eight pixels of padding each — which in a 300px column meant three files
// filled the screen. Alignment groups them at a fraction of the height.
function buildContextLine(name, meta) {
  const line = document.createElement("div");
  line.className = "context-line";
  line.append(name, meta);
  return line;
}

function contextMeta(text) {
  const meta = document.createElement("span");
  meta.className = "context-item-meta";
  meta.textContent = text;
  return meta;
}

// renderSources draws the attachments and returns how many rows it drew, which
// is what decides whether the rail has anything to say at all.
function renderSources() {
  if (!ctxEl("sources-list")) return 0;
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
    let nameNode;
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
      nameNode = label;
    } else {
      nameNode = document.createElement("span");
      nameNode.className = "context-item-name";
      nameNode.textContent = file.file_name;
    }

    item.appendChild(
      buildContextLine(
        nameNode,
        contextMeta(
          file.on_disk === false
            // The row survives but the bytes do not, which is a different
            // problem from "no attachments" and needs to be visible.
            ? "Missing on disk"
            : file.pending
              ? "Uploading…"
              : formatFileSize(file.file_size)
        )
      )
    );
    ctxEl("sources-list").appendChild(item);
  }

  // The count says what it counts. A bare "2" beside a heading was a number
  // whose referent you had to guess at, and this rail had four of them.
  if (ctxEl("sources-meta")) ctxEl("sources-meta").textContent = fileCountLabel(items.length);
  if (ctxEl("sources-selected")) {
    const chosen = contextState.selectedFileIDs.size;
    ctxEl("sources-selected").hidden = chosen === 0;
    ctxEl("sources-selected").textContent =
      `${chosen} selected for the next request`;
  }
  if (ctxEl("context-sources")) ctxEl("context-sources").hidden = items.length === 0;
  return items.length;
}

function fileCountLabel(count, truncated = false) {
  return `${count}${truncated ? "+" : ""} file${count === 1 && !truncated ? "" : "s"}`;
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

    item.appendChild(buildContextLine(name, contextMeta(formatRetrievalScore(source.score))));

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
  if (ctxEl("retrieved-meta")) {
    ctxEl("retrieved-meta").textContent =
      `${items.length} passage${items.length === 1 ? "" : "s"}`;
  }
  // The whole section stands down when there is nothing retrieved: it is
  // per-turn transient, and an empty module whose body explains its own
  // emptiness costs more attention than it returns.
  if (ctxEl("context-retrieved")) ctxEl("context-retrieved").hidden = items.length === 0;
}

// What the agent produced: the workspace listing, newest first. Cumulative,
// not per-turn — a file written in the first turn and rewritten in the fifth
// is one row here, which is the thing the inline work log cannot say.
//
// The rows this turn actually touched are marked, from the diff finishActiveTurn
// already computes against the pre-turn snapshot. That mark is why the section
// can be cumulative without burying today's work in last week's: the list is
// the whole workspace and the eye still finds the two files that just changed.
function renderDeliverables() {
  if (!ctxEl("deliverables-list")) return;
  const items = contextState.deliverables;
  const log = contextState.lastTurnLog;
  const fresh = new Set(
    log && log.threadUUID === state.selectedThreadUUID
      ? log.produced.map((file) => file.path)
      : []
  );
  ctxEl("deliverables-list").innerHTML = "";
  for (const file of items) {
    ctxEl("deliverables-list").appendChild(buildDeliverableRow(file, fresh.has(file.path)));
  }
  if (ctxEl("open-workspace-button")) {
    // Offered only when there is something to open, and only when the bridge
    // can actually open it — a button that silently fails is worse than none.
    ctxEl("open-workspace-button").hidden =
      items.length === 0 ||
      typeof window.desktopBridge?.agent?.revealWorkspace !== "function";
  }
  if (ctxEl("deliverables-meta")) {
    ctxEl("deliverables-meta").textContent = fileCountLabel(
      items.length,
      contextState.deliverablesTruncated
    );
  }
  if (ctxEl("context-deliverables")) ctxEl("context-deliverables").hidden = items.length === 0;
}

// One produced file. The directory is set in muted type ahead of the name so
// the filename is what the eye lands on — in a 300px column a list of
// "deck/section-two/outline.md" reads as a list of "deck/section-two/".
//
// The row is a button, and what it opens is the workspace FOLDER: the sidecar
// exposes exactly one reveal route and it takes a thread uuid, no path. So the
// row promises what it can keep — its title says folder — rather than
// pretending to a per-file open that does not exist on this side of the
// bridge. Same behaviour as the inline "Produced" rows, deliberately: one
// gesture, one outcome, wherever the file is named.
function buildDeliverableRow(file, isNew) {
  const item = document.createElement("li");
  item.className = "context-item";
  const row = document.createElement("button");
  row.type = "button";
  row.className = "context-line deliverable-row";
  row.setAttribute("title", "Open the workspace folder");

  const name = document.createElement("span");
  name.className = "context-item-name";
  const cut = file.path.lastIndexOf("/");
  if (cut >= 0) {
    const dir = document.createElement("span");
    dir.className = "context-item-dir";
    dir.textContent = file.path.slice(0, cut + 1);
    name.appendChild(dir);
  }
  name.appendChild(document.createTextNode(file.path.slice(cut + 1)));

  const meta = contextMeta(
    `${formatFileSize(file.size)} · ${formatMessageTime(file.modified_at)}`
  );
  row.append(name, meta);
  if (isNew) {
    const tag = document.createElement("span");
    tag.className = "context-tag";
    tag.textContent = "New";
    name.appendChild(tag);
  }
  row.addEventListener("click", () => {
    const agent = window.desktopBridge?.agent;
    if (state.selectedThreadUUID && typeof agent?.revealWorkspace === "function") {
      void agent.revealWorkspace(state.selectedThreadUUID);
    }
  });
  item.appendChild(row);
  return item;
}

// The rail's whole empty state, in one line. Three sections each explaining
// their own emptiness was most of what a 300px column showed on a thread that
// had not run yet; a thread that has run needs no explanation at all.
function renderContextEmpty(sourceCount) {
  const note = ctxEl("context-empty-note");
  if (!note) return;
  note.hidden =
    Boolean(state.activeTurn) ||
    sourceCount > 0 ||
    contextState.deliverables.length > 0 ||
    contextState.retrieved.length > 0;
}

export function renderTaskContext() {
  renderRunLine();
  const sourceCount = renderSources();
  renderRetrieved();
  renderDeliverables();
  renderContextEmpty(sourceCount);
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

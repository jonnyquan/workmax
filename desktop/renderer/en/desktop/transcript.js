// The transcript pane: the message list, what is mounted in it, and where it
// is scrolled.
//
// The list is windowed — a long conversation loads in full but mounts only its
// most recent turns, with the rest behind an "earlier messages" control (see
// TRANSCRIPT_WINDOW_TURNS). Everything that appends, replaces or measures a
// message therefore goes through here, so "is that turn actually in the DOM?"
// has one answer and one place that knows it.
import { fences } from "./fence.js";
import {
  chatInput,
  emptyState,
  jumpLatestButton,
  messageList,
  messageViewport,
  newThreadCancelButton,
  newThreadSubmitButton,
  threadMeta,
  threadPanel,
  threadTitle,
  turnState,
} from "./dom.js";
import { formatDate, parseMessages } from "./protocol.js";
import { buildCopyButton, renderMarkdownInto } from "./markdown.js";
import {
  cancelNewThreadDraft,
  selectedRecoverableTurn,
  updateComposerState,
} from "./composer.js";
import { localCalendarDay, renderThreads } from "./threads.js";
import { attachLastTurnLog, loadThreadSources, renderTaskContext } from "./context-panel.js";
import {
  chooseModeForThread,
  closeRenameForm,
  handleScopedError,
  invalidateActiveTurn,
  isCurrentTurn,
  readSidecarJSON,
  renameThreadBridgeAvailable,
  renderSkillOptions,
  setStatus,
  sidecarFetch,
  state,
  submitChat,
} from "./renderer.js";

export function selectionContext(threadUUID = state.selectedThreadUUID) {
  return {
    sessionGeneration: fences.session.snapshot(),
    selectionGeneration: fences.selection.snapshot(),
    turnGeneration: fences.turn.snapshot(),
    threadUUID,
  };
}

export function isCurrentSelection(context) {
  return (
    fences.session.isCurrent(context.sessionGeneration) &&
    fences.selection.isCurrent(context.selectionGeneration) &&
    fences.turn.isCurrent(context.turnGeneration) &&
    context.threadUUID === state.selectedThreadUUID
  );
}

export async function loadMessagesForSelection(thread, context, completedTurn = null) {
  const res = await sidecarFetch(`/agent/threads/${encodeURIComponent(thread.uuid)}/messages`);
  const items = parseMessages(
    await readSidecarJSON(res, "/agent/threads/:uuid/messages")
  );
  if (!isCurrentSelection(context)) {
    return false;
  }
  // A turn that just completed already painted its own two messages; when the
  // snapshot confirms them, the rest of the transcript has no reason to be
  // torn down and rebuilt. Any doubt falls through to the full repaint.
  if (completedTurn && reconcileTurnInPlace(items, completedTurn)) {
    return true;
  }
  renderCachedMessages(items);
  return true;
}

// --- The window --------------------------------------------------------------
//
// Opening a long conversation used to build every message at once: a 500-turn
// thread is a thousand-plus articles, each with a Markdown parse and a
// highlighted code block or two, all on the click that selected the thread.
// Phase 1 removed the per-turn full repaint; this removes the one that was
// left, which is the first one a returning user pays.
//
// What is windowed is the MOUNT, not the data. The snapshot is fetched whole
// and kept whole (there is no paginated messages endpoint on the sidecar, and
// inventing one is a server change this deliberately does not make), so
// "earlier messages" is a DOM operation with no request behind it and no
// spinner. That also keeps every existing consumer honest: search, export and
// the reconcile all still see the entire conversation.
//
// The alternative was a real virtual list. It was rejected: messages have
// wildly variable height (a one-line "ok" next to a forty-line code block), so
// it needs a measurement cache, and that cache has to survive the streaming
// growth of the last message and the in-place reconcile that follows it — a
// large amount of machinery to avoid mounting the forty rows somebody is
// actually reading.
export const TRANSCRIPT_WINDOW_TURNS = 40;

// The whole snapshot, and where the mounted part of it starts. mountedFrom is
// an index into snapshot: everything from it to the end has DOM, everything
// before it is behind the control.
let snapshot = [];
let mountedFrom = 0;

// How many transcript rows a snapshot renders: renderCachedMessages makes one
// article per present half of each exchange.
function expectedMessageArticles(items) {
  let expected = 0;
  for (const item of items) {
    if (item.user_text) expected += 1;
    if (item.ai_text || item.streaming_state !== "complete") expected += 1;
  }
  return expected;
}

// The control is a child of the message list like everything else, so every
// count of "how many rows are mounted" has to know it might be there. One
// predicate, used by both places that count.
function earlierControlNode() {
  const first = messageList.children[0];
  return first?.classList?.contains("transcript-earlier") ? first : null;
}

function buildEarlierControl(hiddenCount) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "transcript-earlier";
  button.textContent =
    hiddenCount === 1 ? "Show 1 earlier exchange" : `Show ${hiddenCount} earlier exchanges`;
  button.addEventListener("click", mountEarlierMessages);
  return button;
}

function syncEarlierControl() {
  const existing = earlierControlNode();
  if (mountedFrom <= 0) {
    if (existing) existing.remove();
    return;
  }
  const control = buildEarlierControl(mountedFrom);
  if (existing) messageList.replaceChild(control, existing);
  else messageList.insertBefore(control, messageList.children[0] ?? null);
}

// Mounts the previous window's worth of exchanges ABOVE what is already there,
// without touching a single existing node.
//
// Rebuilding the list from scratch with a wider window would have been fewer
// lines and wrong: the rows already mounted can be carrying a streaming
// answer, an unanswered tool-approval card or a settled reasoning caption,
// none of which live in the snapshot and all of which a repaint would drop.
function mountEarlierMessages() {
  if (mountedFrom <= 0) return;
  // Measured before the insert, so the scroll can be put back where the reader
  // had it. Without this, adding content above the viewport shoves what they
  // were reading off the bottom of the screen.
  const heightBefore = messageViewport.scrollHeight;
  const topBefore = messageViewport.scrollTop;

  const nextFrom = Math.max(0, mountedFrom - TRANSCRIPT_WINDOW_TURNS);
  const anchor = earlierControlNode()?.nextSibling ?? messageList.children[0] ?? null;
  for (let i = nextFrom; i < mountedFrom; i++) {
    for (const node of buildExchangeNodes(snapshot[i], false)) {
      messageList.insertBefore(node, anchor);
    }
  }
  mountedFrom = nextFrom;
  syncEarlierControl();

  // The reader has not moved; the document grew above them by exactly this.
  messageViewport.scrollTop = topBefore + (messageViewport.scrollHeight - heightBefore);
}

// The rows for one exchange. `isLast` decides Regenerate, which only the
// transcript's final completed answer may offer — see attachMessageActions.
function buildExchangeNodes(item, isLast) {
  const nodes = [];
  if (item.user_text) {
    nodes.push(renderMessage("user", item.user_text, "complete", item.created_at));
  }
  if (item.ai_text || item.streaming_state !== "complete") {
    const regenerable = isLast && item.user_text && item.streaming_state === "complete";
    nodes.push(
      renderMessage(
        "assistant",
        item.ai_text || "Response interrupted before text was cached.",
        item.streaming_state,
        item.updated_at || item.created_at,
        regenerable ? { regenerateText: item.user_text } : {}
      )
    );
  }
  return nodes;
}

// The in-place half of the post-turn reconcile. The optimistic pair the turn
// streamed into must still be the transcript's last two rows, and the
// snapshot's last exchange must be exactly what was streamed — same words,
// completed. Then the only things the cache repaint would have added are
// applied directly: timestamps, the Regenerate affordance, and the partial
// flag coming off. Everything else — earlier messages, the work log strip,
// the settled reasoning caption, answered approval cards — keeps its nodes.
// Any mismatch (canceled turn, server-rewritten text, drifted row count)
// returns false and the caller takes the full-rebuild path, whose correctness
// this optimization must never outrun.
function reconcileTurnInPlace(items, completedTurn) {
  const wrapper = completedTurn.assistantWrapper;
  const userNode = completedTurn.userNode;
  if (!wrapper || !userNode) return false;
  const last = items.length > 0 ? items[items.length - 1] : null;
  if (!last || last.streaming_state !== "complete") return false;
  if (!last.user_text || last.user_text !== completedTurn.userText) return false;
  if (!last.ai_text || last.ai_text !== completedTurn.assistantText) return false;
  const children = messageList.children;
  if (children.length < 2) return false;
  if (children[children.length - 1] !== wrapper) return false;
  if (children[children.length - 2] !== userNode) return false;
  // The row count is compared against the MOUNTED part of the snapshot, not
  // all of it: everything before mountedFrom is deliberately absent, and the
  // control standing in for it is a row too. Comparing against the whole
  // snapshot would make every long conversation fail this check and fall back
  // to the full repaint — the exact cost the window exists to remove, paid on
  // every turn instead of once.
  //
  // items is the fresh snapshot and holds one more exchange than the one the
  // window was built from, appended at the end, so indices below mountedFrom
  // still line up.
  const mountedRows =
    expectedMessageArticles(items.slice(mountedFrom)) + (mountedFrom > 0 ? 1 : 0);
  if (children.length !== mountedRows) return false;

  wrapper.classList.remove("pending");
  wrapper.classList.remove("partial");
  ensureMessageTime(userNode, last.created_at);
  ensureMessageTime(wrapper, last.updated_at || last.created_at);
  ensureRegenerateAction(wrapper, last.user_text);
  // The snapshot the window indexes into has to keep up, or the next reconcile
  // counts against a conversation one turn shorter than the one on screen.
  snapshot = items;
  // No scroll write: the reader stays exactly where the stream left them.
  return true;
}

// Adds (or refreshes) the stored timestamp a streamed message could not show
// until the sidecar had one to give.
function ensureMessageTime(wrapper, timestamp) {
  const time = formatMessageTime(timestamp);
  if (!time) return;
  const label = Array.from(wrapper.children || []).find((child) =>
    child.classList?.contains("message-role")
  );
  if (!label) return;
  for (const child of Array.from(label.children || [])) {
    if (child.classList?.contains("message-time")) {
      child.textContent = time;
      return;
    }
  }
  const when = document.createElement("span");
  when.className = "message-time";
  when.textContent = time;
  label.appendChild(when);
}

// The finished final answer gains Regenerate without rebuilding its row. The
// action row may already exist (copy attaches at the terminal) or not (no
// clipboard, no copy) — either way exactly one Regenerate results.
function ensureRegenerateAction(wrapper, regenerateText) {
  if (!regenerateText) return;
  let actions = Array.from(wrapper.children || []).find((child) =>
    child.classList?.contains("message-actions")
  );
  if (!actions) {
    actions = document.createElement("div");
    actions.className = "message-actions";
    wrapper.appendChild(actions);
  }
  for (const child of Array.from(actions.children || [])) {
    if (child.classList?.contains("message-action-regenerate")) return;
  }
  actions.appendChild(buildRegenerateButton(regenerateText));
}

// Only the transcript's FINAL answer may offer Regenerate. A new turn makes
// the previous final answer non-final, so its button comes off the moment the
// next exchange is appended — the in-place reconcile never revisits old rows.
function retireStaleRegenerateActions() {
  const children = messageList.children;
  const lastAssistant = children[children.length - 1];
  if (!lastAssistant?.classList?.contains("assistant")) return;
  const actions = Array.from(lastAssistant.children || []).find((child) =>
    child.classList?.contains("message-actions")
  );
  if (!actions) return;
  for (const child of Array.from(actions.children || [])) {
    if (child.classList?.contains("message-action-regenerate")) child.remove();
  }
  if ((actions.children || []).length === 0) actions.remove();
}

function renderCachedMessages(items) {
  snapshot = items;
  mountedFrom = Math.max(0, items.length - TRANSCRIPT_WINDOW_TURNS);
  messageList.textContent = "";
  if (items.length === 0) {
    messageList.appendChild(renderNotice("No cached messages for this thread yet."));
    return;
  }
  syncEarlierControl();
  for (let i = mountedFrom; i < items.length; i++) {
    for (const node of buildExchangeNodes(items[i], i === items.length - 1)) {
      messageList.appendChild(node);
    }
  }
  attachLastTurnLog();
  scrollMessagesToEnd(true);
}

// stashComposerDraft remembers the composer's unsent text for the thread that
// owns it. Called before anything rewrites chatInput.value on a thread switch
// or a refresh: silently dropping a half-written long prompt is data loss.
// An emptied composer clears the stash — keeping a stale draft would resurrect
// words the user deliberately deleted.
export function stashComposerDraft() {
  const threadUUID = state.selectedThreadUUID;
  if (!threadUUID) return;
  if (chatInput.value.trim() !== "") {
    state.composerDrafts.set(threadUUID, chatInput.value);
  } else {
    state.composerDrafts.delete(threadUUID);
  }
}

export function selectThread(thread) {
  if (state.activeTurn && thread.uuid === state.selectedThreadUUID) {
    return;
  }
  if (state.createFormOpen) {
    if (state.createDraft?.attempted) {
      const retryable = state.createDraft.retryable === true;
      setStatus(
        retryable
          ? "Retry or cancel the current thread draft before switching."
          : "Cancel the current thread draft before switching.",
        "error"
      );
      (retryable ? newThreadSubmitButton : newThreadCancelButton).focus();
      return;
    }
    cancelNewThreadDraft(false);
  }
  if (state.activeTurn) {
    invalidateActiveTurn(true);
  }
  stashComposerDraft();
  fences.selection.bump();
  state.selectedThreadUUID = thread.uuid;
  // Restore this thread's stashed draft, or a clean box. Before the map the
  // outgoing thread's words simply stayed in the composer — leaking into the
  // next thread, and overwritten the moment anything typed there.
  chatInput.value = state.composerDrafts.get(thread.uuid) ?? "";
  // The context panel follows the selection: its sources belong to a thread,
  // not to the session.
  void loadThreadSources(thread.uuid);
  fences.recovery.bump();
  state.resumingTurn = false;
  state.dismissingRecovery = false;
  state.recoveryFeedback = "";
  state.recoveryFeedbackKind = "default";
  const context = selectionContext(thread.uuid);
  renderThreads();
  emptyState.hidden = true;
  threadPanel.hidden = false;
  updateSelectedThreadHeading();
  chooseModeForThread(thread);
  renderSkillOptions();
  updateComposerState();
  setTurnState(selectedRecoverableTurn() ? "Interrupted" : "Ready");
  messageList.textContent = "";
  messageList.appendChild(renderNotice("Loading cached messages..."));
  void loadMessagesForSelection(thread, context).catch((error) => {
    void handleScopedError(error, context);
  });
}

export function renderNotice(text) {
  const node = document.createElement("p");
  node.className = "status-card";
  node.textContent = text;
  return node;
}

// formatMessageTime keeps timestamps quiet: today's messages show only the
// clock, older ones add the date. A full locale string on every row is noise
// that says the same day twenty times.
export function formatMessageTime(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const sameDay =
    localCalendarDay(date) === localCalendarDay(new Date());
  return sameDay
    ? date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
    : date.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function renderMessage(role, text, streamingState = "complete", timestamp = "", actionOptions = {}) {
  const wrapper = document.createElement("article");
  wrapper.className = `message ${role}`;
  wrapper.classList.toggle(
    "partial",
    role === "assistant" && streamingState !== "complete"
  );
  const label = document.createElement("div");
  label.className = "message-role";
  // The wrapper class stays the raw role (tests and CSS key off it); the
  // visible text speaks the product's voice. "ASSISTANT" is a protocol word,
  // not a name. The assistant also gets the brand mark — the same "v" the
  // sidebar wears — so its answers are visually signed.
  if (role === "assistant") {
    const avatar = document.createElement("span");
    avatar.className = "message-avatar";
    avatar.textContent = "v";
    label.appendChild(avatar);
    label.appendChild(document.createTextNode("WorkMax"));
  } else {
    label.textContent = role === "user" ? "You" : role;
  }
  // Cached messages carry their stored time; a streaming message shows none
  // until the post-turn reconcile repaints it from cache — which is when a
  // real timestamp exists to show.
  const time = formatMessageTime(timestamp);
  if (time) {
    const when = document.createElement("span");
    when.className = "message-time";
    when.textContent = time;
    label.appendChild(when);
  }
  const bubble = document.createElement("div");
  bubble.className = "bubble";
  // Only the assistant's text is Markdown. What the user typed is shown back
  // exactly as typed — rendering their asterisks as emphasis would change the
  // record of what they asked.
  if (role === "assistant") {
    renderMarkdownInto(bubble, text);
  } else {
    bubble.textContent = text;
  }
  wrapper.append(label, bubble);

  attachMessageActions(wrapper, role, text, actionOptions);
  return wrapper;
}

// Built separately from the bubble because a streamed answer starts empty:
// renderMessage cannot offer "copy" for text that has not arrived yet, so the
// finished turn calls this once it has.
export function attachMessageActions(wrapper, role, text, options = {}) {
  if (!wrapper) return;
  // Idempotent. No current path reaches this: renderMessage builds the
  // streaming bubble from empty text, which yields no row, so the finished
  // turn is always the first to add one. Kept because "one row per message" is
  // the invariant, and the day a placeholder action appears on a streaming
  // bubble the alternative is two of every button.
  for (const child of Array.from(wrapper.children || [])) {
    if (child.classList?.contains("message-actions")) return;
  }
  const actions = document.createElement("div");
  actions.className = "message-actions";
  const copy = buildCopyButton(text, role === "assistant" ? "Copy answer" : "Copy");
  if (copy) actions.appendChild(copy);
  if (role === "assistant" && options.regenerateText) {
    actions.appendChild(buildRegenerateButton(options.regenerateText));
  }
  if (role === "user" && text) {
    // Not a retry: it puts the words back in the composer so they can be
    // changed first. Re-running a prompt verbatim is rarely what someone wants
    // when they did not like the answer, and a one-click resend would also
    // have to duplicate the turn machinery it would be bypassing.
    const reuse = document.createElement("button");
    reuse.type = "button";
    reuse.className = "message-action";
    reuse.textContent = "Edit and resend";
    reuse.addEventListener("click", () => {
      chatInput.value = text;
      updateComposerState();
      chatInput.focus();
    });
    actions.appendChild(reuse);
  }
  if (actions.children.length > 0) wrapper.appendChild(actions);
}

// Only the FINAL answer is regenerable — re-running an earlier prompt
// mid-conversation would fork history the transcript cannot show. The
// click is the consent: unlike "Edit and resend", the whole point here
// is running the same words again. The dedicated class is how the in-place
// reconcile finds and retires the button once the answer stops being final.
function buildRegenerateButton(regenerateText) {
  const regen = document.createElement("button");
  regen.type = "button";
  regen.className = "message-action message-action-regenerate";
  regen.textContent = "Regenerate";
  regen.addEventListener("click", () => {
    if (chatInput.disabled) return;
    chatInput.value = regenerateText;
    updateComposerState();
    submitChat({ preventDefault() {} });
  });
  return regen;
}

// Whether the viewport is glued to the newest content. True until the user
// scrolls away from the bottom; a stream that keeps yanking the reader back
// down while they are checking an earlier answer is hostile, not helpful.
let viewportSticky = true;

function viewportNearBottom() {
  const remaining =
    messageViewport.scrollHeight -
    messageViewport.scrollTop -
    (messageViewport.clientHeight || 0);
  return remaining < 48;
}

// force is for the user's own actions — sending a message, opening a thread,
// jumping — where "take me to the newest" is exactly what they asked for.
// Streaming deltas pass no force: they follow only a reader who is already
// following, and otherwise light the jump affordance instead.
// formatTurnDuration speaks in the units a person watches a turn in.
export function formatTurnDuration(ms) {
  const secs = Math.max(0, Math.round(ms / 1000));
  if (secs < 60) return `${secs}s`;
  return `${Math.floor(secs / 60)}m ${secs % 60}s`;
}

// setTurnState is the one place the pill's text and colour change together.
// The class is derived from the label so the two can never disagree.
export function setTurnState(label, detail = "") {
  turnState.textContent = detail ? `${label} · ${detail}` : label;
  const tone =
    label === "Working" || label === "Resuming" || label === "Stopping"
      ? "busy"
      : label === "Done"
        ? "ok"
        : label === "Error"
          ? "error"
          : label === "Interrupted" || label === "Stopped" || label === "Stopped locally" || label === "Session changed"
            ? "warn"
            : "idle";
  turnState.className = `turn-state is-${tone}`;
}

export function scrollMessagesToEnd(force = false) {
  if (force || viewportSticky) {
    messageViewport.scrollTop = messageViewport.scrollHeight;
    viewportSticky = true;
    if (jumpLatestButton) jumpLatestButton.hidden = true;
    return;
  }
  if (jumpLatestButton) jumpLatestButton.hidden = false;
}

export function updateSelectedThreadHeading() {
  if (!state.selectedThreadUUID) return;
  const thread = state.threads.find(
    (candidate) => candidate.uuid === state.selectedThreadUUID
  );
  if (!thread) return;
  threadTitle.textContent = thread.name || "Untitled thread";
  threadMeta.textContent = `${thread.agent_mode || "agent"} · ${thread.message_count || 0} messages · ${formatMessageTime(thread.updated_at) || formatDate(thread.updated_at)}`;
  // Rename lives where the title is read, and only where the sidecar would
  // accept it: a synced thread's name belongs to the cloud copy, which the
  // sync worker would restore over any local edit.
  const renameButton = document.querySelector("#rename-thread-button");
  if (renameButton) {
    renameButton.hidden =
      thread.cloud_sync_state !== "local" || !renameThreadBridgeAvailable();
  }
  // Export needs messages to export and a bridge to ask through; unlike
  // rename it works for synced threads too — the file is a copy, not an
  // edit, so the sync worker has nothing to restore.
  const exportButton = document.querySelector("#export-thread-button");
  if (exportButton) {
    exportButton.hidden =
      (thread.message_count || 0) === 0 ||
      typeof window.desktopBridge?.agent?.exportThread !== "function";
  }
  closeRenameForm();
}

export function appendOptimisticTurn(userText) {
  const notices = Array.from(messageList.children).filter(
    (node) => node.classList?.contains("status-card")
  );
  for (const notice of notices) {
    notice.remove();
  }
  // The answer about to be superseded stops being regenerable now, because
  // the in-place reconcile will not repaint it later.
  retireStaleRegenerateActions();
  const userNode = renderMessage("user", userText);
  const assistantNode = renderMessage("assistant", "");
  // Waiting for the first token had no face: the bubble sat empty. The
  // pending class puts a typing indicator there until text or a terminal
  // event arrives.
  assistantNode.classList.add("pending");
  messageList.append(userNode, assistantNode);
  scrollMessagesToEnd(true);
  return { userNode, assistantNode, assistantBubble: assistantNode.children[1] };
}

export function failTurnOpen(activeTurn, userText, message) {
  if (!isCurrentTurn(activeTurn)) return;
  state.activeTurn = null;
  fences.turn.bump();
  activeTurn.assistantBubble.textContent = message;
  chatInput.value = userText;
  setTurnState("Error");
  renderTaskContext();
  updateComposerState();
  setStatus(message, "error");
}

export function appendRecoveredAssistant() {
  const notices = Array.from(messageList.children).filter(
    (node) => node.classList?.contains("status-card")
  );
  for (const notice of notices) {
    notice.remove();
  }
  const partial = Array.from(messageList.children)
    .reverse()
    .find(
      (node) =>
        node.classList?.contains("assistant") &&
        node.classList?.contains("partial")
    );
  if (partial?.children?.[1]) {
    partial.children[1].textContent = "";
    return partial.children[1];
  }
  const assistantNode = renderMessage("assistant", "", "streaming");
  messageList.appendChild(assistantNode);
  scrollMessagesToEnd(true);
  return assistantNode.children[1];
}

messageViewport.addEventListener("scroll", () => {
  const wasSticky = viewportSticky;
  viewportSticky = viewportNearBottom();
  if (viewportSticky && !wasSticky) {
    if (jumpLatestButton) jumpLatestButton.hidden = true;
  }
});
if (jumpLatestButton) {
  jumpLatestButton.addEventListener("click", () => {
    scrollMessagesToEnd(true);
  });
}

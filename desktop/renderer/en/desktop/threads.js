// The sidebar: the thread list, its grouping and filtering, pin and delete,
// the quick switcher, and the message-content search that backs it.
//
// Title filtering is local (the list is already in memory); message bodies
// live in SQLite, so those come from the sidecar behind a debounce and a
// fence.
import { fences } from "./fence.js";
import {
  emptyState,
  messageList,
  quickSwitcher,
  quickSwitcherInput,
  quickSwitcherList,
  threadList,
  threadPanel,
} from "./dom.js";
import {
  formatDate,
  isRecord,
  parseDesktopBridgeResult,
  parseSearchMatches,
  sanitizeErrorMessage,
} from "./protocol.js";
import { formatMessageTime, renderNotice, selectThread } from "./transcript.js";
import {
  canOpenNewThread,
  openNewThreadForm,
  renderEmptyState,
  updateComposerState,
} from "./composer.js";
import { contextState, renderTaskContext } from "./context-panel.js";
import {
  THEME_CHOICES,
  THEME_HINTS,
  THEME_LABELS,
  exportSelectedThread,
  loadThreads,
  openModelSettings,
  setStatus,
  setTheme,
  state,
  themeChoice,
} from "./renderer.js";

// Groups threads by the user's local calendar day rather than elapsed hours.
//
// Ported from the web client, including the two edge cases that make the
// difference between a list that reads right and one that surprises people: a
// conversation from 11pm last night belongs under "Yesterday" at 1am, not
// "3 hours ago", and a timestamp that will not parse goes to Older rather than
// silently disappearing. Future timestamps stay in Today.
export function localCalendarDay(date) {
  return Date.UTC(date.getFullYear(), date.getMonth(), date.getDate()) / 86400000;
}

const THREAD_GROUPS = [
  { key: "pinned", label: "Pinned" },
  { key: "today", label: "Today" },
  { key: "week", label: "Previous 7 days" },
  { key: "older", label: "Older" },
];

export function groupThreads(threads, now) {
  const today = localCalendarDay(now);
  const groups = { pinned: [], today: [], week: [], older: [] };
  for (const thread of threads) {
    if (thread.pinned) {
      // A pin overrides the calendar: that is what pinning means.
      groups.pinned.push(thread);
      continue;
    }
    const updated = new Date(thread.updated_at);
    if (Number.isNaN(updated.getTime())) {
      // Unreachable through the normal path today: parseThread rejects an
      // unparseable updated_at, and parseThreadList maps over every item, so a
      // single bad row empties the whole list before it gets here. Kept
      // because grouping should not be the thing that breaks if that parser
      // ever softens — and because a thread with no readable date is still a
      // thread the user has.
      groups.older.push(thread);
      continue;
    }
    const ageInDays = today - localCalendarDay(updated);
    if (ageInDays <= 0) groups.today.push(thread);
    else if (ageInDays <= 7) groups.week.push(thread);
    else groups.older.push(thread);
  }
  return groups;
}

// Matches on the title only. Message bodies live in SQLite and are not loaded
// for unselected threads, so searching them here would quietly match a subset
// — the threads that happen to be open — which is worse than not offering it.
function threadMatchesQuery(thread, query) {
  if (!query) return true;
  return (thread.name || "Untitled thread").toLowerCase().includes(query);
}

async function toggleThreadPin(thread) {
  const agent = window.desktopBridge?.agent;
  const method = thread.pinned ? agent?.unpinThread : agent?.pinThread;
  if (typeof method !== "function") return;
  try {
    const result = parseDesktopBridgeResult(
      await method.call(agent, thread.uuid),
      "thread pin result"
    );
    if (!result.ok) {
      const raw = isRecord(result.error) ? result.error.error : result.error;
      throw new Error(sanitizeErrorMessage(raw) || "Could not update the pin");
    }
  } catch (error) {
    setStatus(String(error.message || error), "error");
    return;
  }
  // Reload rather than patch: the pin changes the server-side sort, and the
  // sidebar should show exactly what the sidecar would answer, not a local
  // approximation of it.
  await loadThreads();
}

// Deleting is offered only where the sidecar would allow it: threads that
// exist solely on this machine. A synced thread has a cloud copy and a sync
// worker that would pull it straight back, so showing a delete that undoes
// itself would be worse than showing none.
function threadIsDeletable(thread) {
  return thread.cloud_sync_state === "local";
}

async function deleteThread(thread) {
  const agent = window.desktopBridge?.agent;
  if (!agent || typeof agent.deleteThread !== "function") return;
  try {
    const result = parseDesktopBridgeResult(
      await agent.deleteThread(thread.uuid),
      "agent delete result"
    );
    if (!result.ok) {
      const code = isRecord(result.error) ? result.error.error : "";
      setStatus(
        code === "thread_busy"
          ? "That conversation still has a response in flight. Stop it first."
          : "Could not delete the conversation.",
        "error"
      );
      return;
    }
  } catch {
    setStatus("Could not delete the conversation.", "error");
    return;
  }
  state.threads = state.threads.filter((t) => t.uuid !== thread.uuid);
  state.recoverableTurns = state.recoverableTurns.filter(
    (turn) => turn.thread_uuid !== thread.uuid
  );
  if (state.selectedThreadUUID === thread.uuid) {
    fences.selection.bump();
    state.selectedThreadUUID = null;
    messageList.textContent = "";
    emptyState.hidden = false;
    threadPanel.hidden = true;
    contextState.sources = [];
    contextState.retrieved = [];
  }
  renderThreads();
  renderTaskContext();
  updateComposerState();
  setStatus("Conversation deleted from this machine.");
}

function renderThreadButton(thread) {
  const item = document.createElement("li");
  item.className = "thread-item";
  const button = document.createElement("button");
  button.type = "button";
  button.className = "thread-button";
  button.classList.toggle("active", thread.uuid === state.selectedThreadUUID);
  const title = document.createElement("strong");
  title.textContent = thread.name || "Untitled thread";
  const meta = document.createElement("p");
  meta.textContent = `${thread.message_count || 0} messages · ${formatMessageTime(thread.updated_at) || formatDate(thread.updated_at)}`;
  button.append(title, meta);
  if (state.recoverableTurns.some((turn) => turn.thread_uuid === thread.uuid)) {
    const badge = document.createElement("span");
    badge.className = "thread-recovery-badge";
    badge.textContent = "Interrupted";
    button.appendChild(badge);
  }
  button.addEventListener("click", () => {
    selectThread(thread);
  });
  item.appendChild(button);
  const pin = document.createElement("button");
  pin.type = "button";
  pin.className = "thread-pin";
  pin.classList.toggle("pinned", thread.pinned === true);
  pin.textContent = thread.pinned ? "Unpin" : "Pin";
  pin.setAttribute(
    "aria-label",
    (thread.pinned ? "Unpin " : "Pin ") + (thread.name || "Untitled thread")
  );
  pin.addEventListener("click", () => {
    pin.disabled = true;
    void toggleThreadPin(thread);
  });
  item.appendChild(pin);
  if (threadIsDeletable(thread)) {
    // Two clicks, one control: the first arms it, the second deletes. A modal
    // would be heavier machinery for the same guarantee — that no single
    // misclick destroys a conversation.
    const del = document.createElement("button");
    del.type = "button";
    del.className = "thread-delete";
    del.textContent = "Delete";
    del.setAttribute("aria-label", `Delete ${thread.name || "Untitled thread"}`);
    del.addEventListener("click", () => {
      if (!del.classList.contains("armed")) {
        del.classList.add("armed");
        del.textContent = "Confirm";
        setTimeout(() => {
          del.classList.remove("armed");
          del.textContent = "Delete";
        }, DELETE_ARM_MS);
        return;
      }
      del.disabled = true;
      void deleteThread(thread);
    });
    item.appendChild(del);
  }
  return item;
}

// Long enough to move the pointer one row down and click again; short enough
// that an armed Delete does not lie in wait for a later stray click.
export const DELETE_ARM_MS = 4000;

export function renderThreads() {
  threadList.textContent = "";
  // A filter above a list with nothing in it is noise — and worse, it reads as
  // "your search found nothing" when the truth is that nothing is cached yet.
  // Looked up here rather than held in a module const: this runs before the
  // element lookups at the bottom of the file have been evaluated.
  const searchPanel = document.querySelector("#thread-search-panel");
  if (searchPanel) searchPanel.hidden = state.threads.length === 0;
  if (state.threads.length === 0) {
    const item = document.createElement("li");
    item.appendChild(renderNotice("No cached threads yet."));
    threadList.appendChild(item);
    renderEmptyState();
    return;
  }

  const query = (state.threadQuery || "").trim().toLowerCase();
  const matches = state.threads.filter((thread) => threadMatchesQuery(thread, query));
  if (matches.length === 0) {
    const item = document.createElement("li");
    // Naming the query distinguishes "nothing matched" from "nothing cached",
    // which the empty list alone cannot.
    item.appendChild(renderNotice(`No conversations match “${state.threadQuery.trim()}”.`));
    threadList.appendChild(item);
    renderEmptyState();
    return;
  }

  const groups = groupThreads(matches, new Date());
  for (const group of THREAD_GROUPS) {
    const bucket = groups[group.key];
    if (bucket.length === 0) continue;
    const heading = document.createElement("li");
    heading.className = "thread-group";
    heading.textContent = group.label;
    threadList.appendChild(heading);
    for (const thread of bucket) {
      threadList.appendChild(renderThreadButton(thread));
    }
  }
  renderEmptyState();
}
let quickSwitcherIndex = 0;

// The palette's action half: things you can DO from the keyboard, not just
// places you can go. Context-aware — each command appears only when the app
// would honour it, so the list never offers a dead end.
function quickSwitcherCommands(query) {
  const commands = [];
  const current = state.threads.find(
    (candidate) => candidate.uuid === state.selectedThreadUUID
  );
  const agent = window.desktopBridge?.agent;
  // Gated by the SAME predicate the action itself checks: a command whose
  // Enter would silently no-op is a dead end wearing a label. (Found live:
  // during boot, before skills load, the weaker gate offered a New thread
  // that went nowhere.)
  if (canOpenNewThread()) {
    commands.push({
      kind: "command",
      label: "New thread",
      hint: "Start a conversation",
      run: () => openNewThreadForm(),
    });
  }
  if (current) {
    commands.push({
      kind: "command",
      label: current.pinned ? "Unpin this conversation" : "Pin this conversation",
      hint: current.name || "Untitled thread",
      run: () => void toggleThreadPin(current),
    });
    if ((current.message_count || 0) > 0 && typeof agent?.exportThread === "function") {
      commands.push({
        kind: "command",
        label: "Export as Markdown",
        hint: current.name || "Untitled thread",
        run: () => void exportSelectedThread(),
      });
    }
  }
  commands.push({
    kind: "command",
    label: "Open model settings",
    hint: "Local route, protocol, API key",
    run: () => void openModelSettings(),
  });
  // Appearance. Three named entries rather than one cycling toggle: a toggle
  // that walks system → light → dark never tells you where it will land, and
  // the palette is the one place in the app where you say what you want rather
  // than press until it looks right. The active one says so instead of being
  // hidden — a list that silently drops the current state reads as a bug.
  for (const choice of THEME_CHOICES) {
    commands.push({
      kind: "command",
      label: `Appearance: ${THEME_LABELS[choice]}`,
      hint: choice === themeChoice ? "Current" : THEME_HINTS[choice],
      run: () => setTheme(choice),
    });
  }
  if (!query) return commands;
  return commands.filter((command) => command.label.toLowerCase().includes(query));
}

function quickSwitcherCandidates() {
  const query = (quickSwitcherInput?.value || "").trim().toLowerCase();
  const threads = state.threads
    .filter((thread) => threadMatchesQuery(thread, query))
    .slice(0, 8)
    .map((thread) => ({ kind: "thread", thread }));
  return threads.concat(quickSwitcherCommands(query));
}

export function renderQuickSwitcher() {
  if (!quickSwitcherList) return;
  const candidates = quickSwitcherCandidates();
  if (quickSwitcherIndex >= candidates.length) quickSwitcherIndex = Math.max(0, candidates.length - 1);
  quickSwitcherList.textContent = "";
  if (candidates.length === 0) {
    const empty = document.createElement("li");
    empty.className = "quick-switcher-empty";
    empty.textContent = "Nothing matches.";
    quickSwitcherList.appendChild(empty);
    return;
  }
  let commandsHeadingShown = false;
  candidates.forEach((candidate, index) => {
    if (candidate.kind === "command" && !commandsHeadingShown) {
      commandsHeadingShown = true;
      const heading = document.createElement("li");
      heading.className = "quick-switcher-heading";
      heading.textContent = "Actions";
      quickSwitcherList.appendChild(heading);
    }
    const item = document.createElement("li");
    const button = document.createElement("button");
    button.type = "button";
    if (candidate.kind === "thread") {
      button.className = "quick-switcher-item" + (index === quickSwitcherIndex ? " active" : "");
      const title = document.createElement("strong");
      title.textContent = candidate.thread.name || "Untitled thread";
      const meta = document.createElement("span");
      meta.textContent = `${candidate.thread.message_count || 0} messages`;
      button.append(title, meta);
    } else {
      button.className = "quick-switcher-command" + (index === quickSwitcherIndex ? " active" : "");
      const title = document.createElement("strong");
      title.textContent = candidate.label;
      const meta = document.createElement("span");
      meta.textContent = candidate.hint;
      button.append(title, meta);
    }
    button.addEventListener("click", () => {
      commitQuickSwitcherChoice(candidate);
    });
    item.appendChild(button);
    quickSwitcherList.appendChild(item);
  });
}

export function openQuickSwitcher() {
  // Commands make the palette useful even before the first conversation
  // exists, so an empty thread list no longer keeps it closed.
  if (!quickSwitcher || quickSwitcherCandidates().length === 0) return;
  quickSwitcherIndex = 0;
  if (quickSwitcherInput) quickSwitcherInput.value = "";
  quickSwitcher.hidden = false;
  renderQuickSwitcher();
  quickSwitcherInput?.focus();
}

export function closeQuickSwitcher() {
  if (quickSwitcher) quickSwitcher.hidden = true;
}

function commitQuickSwitcherChoice(candidate) {
  closeQuickSwitcher();
  if (candidate.kind === "command") {
    candidate.run();
    return;
  }
  selectThread(candidate.thread);
}

if (quickSwitcherInput) {
  quickSwitcherInput.addEventListener("input", () => {
    quickSwitcherIndex = 0;
    renderQuickSwitcher();
  });
  quickSwitcherInput.addEventListener("keydown", (event) => {
    const candidates = quickSwitcherCandidates();
    if (event.key === "ArrowDown") {
      event.preventDefault();
      quickSwitcherIndex = Math.min(quickSwitcherIndex + 1, Math.max(0, candidates.length - 1));
      renderQuickSwitcher();
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      quickSwitcherIndex = Math.max(0, quickSwitcherIndex - 1);
      renderQuickSwitcher();
    } else if (event.key === "Enter") {
      event.preventDefault();
      const chosen = candidates[quickSwitcherIndex];
      if (chosen) commitQuickSwitcherChoice(chosen);
    } else if (event.key === "Escape") {
      closeQuickSwitcher();
    }
  });
}
if (quickSwitcher) {
  // Clicking the dimmed backdrop (not the panel) dismisses.
  quickSwitcher.addEventListener("click", (event) => {
    if (event.target === quickSwitcher) closeQuickSwitcher();
  });
}

// Content search: titles filter instantly in memory; message bodies live in
// SQLite, so the sidecar answers those. Debounced so a fast typist asks
// once, generation-guarded so a slow answer to an old query can never
// overwrite the results of a newer one.
let contentSearchTimer = null;

function clearContentMatches() {
  fences.contentSearch.bump();
  const panel = document.querySelector("#content-match-panel");
  const list = document.querySelector("#content-match-list");
  if (panel) panel.hidden = true;
  if (list) list.textContent = "";
}

function renderContentMatches(matches) {
  const panel = document.querySelector("#content-match-panel");
  const list = document.querySelector("#content-match-list");
  if (!panel || !list) return;
  list.textContent = "";
  if (matches.length === 0) {
    panel.hidden = true;
    return;
  }
  panel.hidden = false;
  for (const match of matches) {
    const item = document.createElement("li");
    const button = document.createElement("button");
    button.type = "button";
    button.className = "content-match";
    const title = document.createElement("strong");
    title.textContent =
      (isRecord(match) && typeof match.thread_name === "string" && match.thread_name) ||
      "Untitled thread";
    const snippet = document.createElement("span");
    snippet.textContent = (match.role === "you" ? "You: " : "") + match.snippet;
    button.append(title, snippet);
    button.addEventListener("click", () => {
      const thread = state.threads.find((candidate) => candidate.uuid === match.thread_uuid);
      if (thread) selectThread(thread);
    });
    item.appendChild(button);
    list.appendChild(item);
  }
}

async function runContentSearch(query) {
  const agent = window.desktopBridge?.agent;
  if (typeof agent?.searchMessages !== "function") return;
  const generation = fences.contentSearch.bump();
  let matches;
  try {
    const result = parseDesktopBridgeResult(
      await agent.searchMessages(query),
      "search messages result"
    );
    if (!result.ok) return;
    matches = parseSearchMatches(result.data);
  } catch {
    // Content search is an enhancement on top of the title filter; a failure
    // degrades to exactly the behaviour the sidebar always had.
    return;
  }
  if (!fences.contentSearch.isCurrent(generation)) return;
  renderContentMatches(matches);
}

export function scheduleContentSearch() {
  if (contentSearchTimer) clearTimeout(contentSearchTimer);
  const query = (state.threadQuery || "").trim();
  if (query.length < 2) {
    clearContentMatches();
    return;
  }
  contentSearchTimer = setTimeout(() => {
    void runContentSearch(query);
  }, 250);
}

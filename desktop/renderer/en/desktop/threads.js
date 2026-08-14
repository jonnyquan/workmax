// The sidebar: the thread list, its grouping, pin and delete — and the quick
// switcher, which is now the only search in the app.
//
// The rail used to carry a search field of its own. It filtered the list in
// place AND asked the sidecar for message bodies, while ⌘K filtered the same
// titles and offered actions: two entry points, overlapping vocabularies, and
// neither one complete. They are one control now. Title matching is local (the
// list is already in memory); message bodies live in SQLite, so those come
// from the sidecar behind a debounce and a fence.
import { fences } from "./fence.js";
import {
  emptyState,
  messageList,
  quickSwitcher,
  quickSwitcherInput,
  quickSwitcherList,
  sidebarSearchButton,
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

// The date-based groups, used for threads that have neither a pin nor a
// project. Pinned threads and project threads are split out separately (below),
// so what remains is "the rest" — and the rest are still grouped by local
// calendar day, because the case that makes the difference is a conversation
// from late last night read at 1am: "3 hours ago" is true and useless,
// "Yesterday" is what someone is looking for.
const UNGROUPED_GROUPS = [
  { key: "today", label: "Today" },
  { key: "week", label: "Previous 7 days" },
  { key: "older", label: "Older" },
];

const PINNED_GROUP_LABEL = "Pinned";

export function groupThreads(threads, now) {
  const today = localCalendarDay(now);
  const pinned = [];
  const projects = {};
  const ungrouped = { today: [], week: [], older: [] };
  for (const thread of threads) {
    if (thread.pinned) {
      // A pin overrides everything — the calendar AND a project assignment.
      // That is what pinning means: this conversation leads the list, full
      // stop, and its project membership is paused until it is unpinned.
      pinned.push(thread);
      continue;
    }
    const projectKey = typeof thread.project === "string" ? thread.project.trim() : "";
    if (projectKey) {
      // Insertion order is preserved on plain objects for string keys, so the
      // first thread to mention a project defines where that project's section
      // appears — which is the server's sort order, already decided, not
      // re-sorted here.
      if (!Object.prototype.hasOwnProperty.call(projects, projectKey)) {
        projects[projectKey] = [];
      }
      projects[projectKey].push(thread);
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
      ungrouped.older.push(thread);
      continue;
    }
    const ageInDays = today - localCalendarDay(updated);
    if (ageInDays <= 0) ungrouped.today.push(thread);
    else if (ageInDays <= 7) ungrouped.week.push(thread);
    else ungrouped.older.push(thread);
  }
  return { pinned, projects, ungrouped };
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

// Project assignment. Like pin, the sidecar owns the sort: a thread that moves
// into a project changes where it appears in the list, and the sidebar should
// show what the sidecar would answer rather than a local guess at the new
// layout. Empty string clears the assignment.
async function commitThreadProject(thread, projectKey) {
  const agent = window.desktopBridge?.agent;
  const trimmed = (projectKey || "").trim();
  const method = trimmed ? agent?.setThreadProject : agent?.clearThreadProject;
  if (typeof method !== "function") return;
  try {
    const result = parseDesktopBridgeResult(
      trimmed
        ? await method.call(agent, thread.uuid, trimmed)
        : await method.call(agent, thread.uuid),
      "thread project result"
    );
    if (!result.ok) {
      const raw = isRecord(result.error) ? result.error.error : result.error;
      throw new Error(sanitizeErrorMessage(raw) || "Could not set the project");
    }
  } catch (error) {
    setStatus(String(error.message || error), "error");
    return;
  }
  await loadThreads();
}

// The inline editor for "Move to project". window.prompt would be the simplest
// input a browser can offer, but WKWebView (the only webview this app ships in)
// does not honour it: the call returns null without blocking or showing any
// UI, so the user clicks "Project" and nothing happens. An inline field on the
// row itself is the reliable path, and the only one a test harness can drive
// without a real prompt implementation.
function openProjectEditor(thread, item) {
  if (item.querySelector?.(".thread-project-editor")) return;
  const editor = document.createElement("div");
  editor.className = "thread-project-editor";
  const input = document.createElement("input");
  input.type = "text";
  input.className = "thread-project-input";
  input.value = thread.project || "";
  input.setAttribute("placeholder", "Project name");
  input.setAttribute(
    "aria-label",
    `Project for ${thread.name || "Untitled thread"}`
  );
  // A flag, not a DOM probe: blur fires again when the editor is torn down
  // (Enter or Escape), and without it the second blur would double-commit or
  // double-cancel. document.body.contains would do the same job in a real
  // browser but is absent from the test harness's fake DOM.
  let settled = false;
  const commit = () => {
    if (settled) return;
    settled = true;
    void commitThreadProject(thread, input.value);
    editor.remove();
  };
  const cancel = () => {
    if (settled) return;
    settled = true;
    editor.remove();
  };
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      event.preventDefault();
      commit();
    } else if (event.key === "Escape") {
      event.preventDefault();
      cancel();
    }
  });
  input.addEventListener("blur", () => {
    // Blur without Enter is the user clicking away — treat it as cancel, not
    // commit, so a stray click does not assign a half-typed name.
    cancel();
  });
  editor.appendChild(input);
  item.appendChild(editor);
  input.focus();
  input.select?.();
}

// The sync switch: "does this conversation leave my machine?"
//
// Offered only for threads the cloud already knows about. A thread that has
// never been uploaded (cloud_sync_state === "local") has nothing to pause, and
// a switch that claimed to stop a sync that was never happening would tell the
// user their data had just been protected by an action that did nothing.
function threadSyncSwitchable(thread) {
  return (
    (thread.cloud_sync_state === "synced" || thread.cloud_sync_state === "paused") &&
    typeof window.desktopBridge?.agent?.setThreadCloudSync === "function"
  );
}

async function toggleThreadCloudSync(thread) {
  const agent = window.desktopBridge?.agent;
  if (!agent || typeof agent.setThreadCloudSync !== "function") return;
  const next = thread.cloud_sync_state === "paused" ? "synced" : "paused";
  try {
    const result = parseDesktopBridgeResult(
      await agent.setThreadCloudSync(thread.uuid, next),
      "thread cloud sync result"
    );
    if (!result.ok) {
      const raw = isRecord(result.error) ? result.error.error : result.error;
      throw new Error(
        raw === "thread_not_synced"
          ? "That conversation has never left this machine."
          : sanitizeErrorMessage(raw) || "Could not change the sync setting"
      );
    }
  } catch (error) {
    setStatus(String(error.message || error), "error");
    return;
  }
  setStatus(
    next === "paused"
      ? "Kept on this machine. Existing cloud copies are untouched."
      : "Syncing to WorkMax again."
  );
  // Reload rather than patch: the sidecar owns cloud_sync_state, and the
  // sidebar should show what it would answer.
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
  item.className = "thread-item reveal-on-hover-host";
  item.dataset.threadUuid = thread.uuid;
  const button = document.createElement("button");
  button.type = "button";
  button.className = "thread-button";
  button.classList.toggle("active", thread.uuid === state.selectedThreadUUID);
  const title = document.createElement("strong");
  title.textContent = thread.name || "Untitled thread";
  // One line per conversation, Codex-style. The date group headers already
  // say when; the message count said little and doubled every row's height.
  // The title carries the timestamp as its tooltip instead.
  title.title = `${formatMessageTime(thread.updated_at) || formatDate(thread.updated_at)} · ${thread.message_count || 0} messages`;
  button.appendChild(title);
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
  pin.className = "thread-pin reveal-on-hover";
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
  // "Move to project": same shape and position pattern as pin. The editor opens
  // inline rather than via window.prompt — see openProjectEditor for why.
  if (typeof window.desktopBridge?.agent?.setThreadProject === "function") {
    const project = document.createElement("button");
    project.type = "button";
    project.className = "thread-project reveal-on-hover";
    project.textContent = "Project";
    project.setAttribute(
      "aria-label",
      `Set project for ${thread.name || "Untitled thread"}`
    );
    project.addEventListener("click", () => {
      openProjectEditor(thread, item);
    });
    item.appendChild(project);
  }
  if (threadSyncSwitchable(thread)) {
    const paused = thread.cloud_sync_state === "paused";
    const sync = document.createElement("button");
    sync.type = "button";
    sync.className = "thread-sync reveal-on-hover";
    sync.classList.toggle("paused", paused);
    sync.textContent = paused ? "Local only" : "Syncing";
    sync.setAttribute(
      "aria-label",
      (paused ? "Resume syncing " : "Stop syncing ") +
        (thread.name || "Untitled thread")
    );
    sync.addEventListener("click", () => {
      sync.disabled = true;
      void toggleThreadCloudSync(thread);
    });
    item.appendChild(sync);
  }
  if (threadIsDeletable(thread)) {
    // Two clicks, one control: the first arms it, the second deletes. A modal
    // would be heavier machinery for the same guarantee — that no single
    // misclick destroys a conversation.
    const del = document.createElement("button");
    del.type = "button";
    del.className = "thread-delete reveal-on-hover";
    del.textContent = "Delete";
    del.setAttribute("aria-label", `Delete ${thread.name || "Untitled thread"}`);
    del.addEventListener("click", () => {
      if (!del.classList.contains("armed")) {
        del.classList.add("armed", "revealed");
        del.textContent = "Confirm";
        setTimeout(() => {
          del.classList.remove("armed", "revealed");
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

// Collapses or expands a project section. Walks forward through the list's
// children from the heading until the next group heading, hiding each thread
// item in between. The indicator is passed in by the heading builder so the
// toggle does not need querySelector — the test harness's fake element does
// not implement it, and the reference is already in scope where the heading is
// built.
function toggleProjectGroup(heading, indicator) {
  const collapsed = heading.classList.toggle("thread-project-collapsed");
  if (indicator) indicator.textContent = collapsed ? "▸" : "▾";
  heading.setAttribute("aria-expanded", collapsed ? "false" : "true");
  let node = heading.nextSibling;
  while (node && !(node.classList?.contains("thread-group"))) {
    if (node.classList?.contains("thread-item")) {
      node.hidden = collapsed;
    }
    node = node.nextSibling;
  }
}

// The rail lists every cached conversation, always. It used to also be the
// result surface for a filter typed into the rail's own search field, which is
// why it had a second empty state ("No conversations match …"); searching
// happens in the palette now, and a list that quietly shortens itself while
// another window has focus is not something this column should do.
export function renderThreads() {
  threadList.textContent = "";
  if (state.threads.length === 0) {
    const item = document.createElement("li");
    item.appendChild(renderNotice("No cached threads yet."));
    threadList.appendChild(item);
    renderEmptyState();
    return;
  }

  const groups = groupThreads(state.threads, new Date());

  // Pinned leads. A pin overrides project and calendar alike.
  if (groups.pinned.length > 0) {
    const heading = document.createElement("li");
    heading.className = "thread-group";
    heading.textContent = PINNED_GROUP_LABEL;
    threadList.appendChild(heading);
    for (const thread of groups.pinned) {
      threadList.appendChild(renderThreadButton(thread));
    }
  }

  // Projects: each project key gets its own collapsible section, in the order
  // the keys were first seen (insertion order of the plain object, which is the
  // server's sort order already applied to the list).
  for (const [projectKey, bucket] of Object.entries(groups.projects)) {
    if (bucket.length === 0) continue;
    const heading = document.createElement("li");
    heading.className = "thread-group thread-project-group";
    const indicator = document.createElement("span");
    indicator.className = "thread-project-toggle";
    indicator.setAttribute("aria-hidden", "true");
    indicator.textContent = "▾"; // ▾ expanded
    const label = document.createElement("span");
    label.className = "thread-project-label";
    label.textContent = projectKey;
    heading.append(indicator, label);
    heading.setAttribute("role", "button");
    heading.setAttribute("tabindex", "0");
    heading.setAttribute("aria-expanded", "true");
    heading.addEventListener("click", () => {
      toggleProjectGroup(heading, indicator);
    });
    heading.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        toggleProjectGroup(heading, indicator);
      }
    });
    threadList.appendChild(heading);
    for (const thread of bucket) {
      threadList.appendChild(renderThreadButton(thread));
    }
  }

  // Ungrouped: threads with neither a pin nor a project, grouped by local
  // calendar day.
  for (const group of UNGROUPED_GROUPS) {
    const bucket = groups.ungrouped[group.key];
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
// Update the running indicator on the thread that has an active turn, without
// re-rendering the whole list. Called when a turn starts and when it settles.
export function updateThreadRunning(threadUUID, running) {
  if (!threadList) return;
  for (const item of threadList.children) {
    const button = item.querySelector?.(".thread-button");
    if (!button) continue;
    const isActive = item.dataset?.threadUuid === threadUUID;
    button.classList.toggle("thread-running", isActive && running);
  }
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

// Message-body hits, for the query currently in the box. Held here rather than
// recomputed per render because they are the one group that cannot be answered
// synchronously — the bodies are in SQLite.
let contentMatches = [];

// Three groups, in the order they can be produced: names (already in memory),
// actions (a fixed list), then message bodies (a round trip). Bodies come LAST
// on purpose. They land a debounce and a request after the keystroke that
// asked for them, and a group that appears in the MIDDLE of the list moves
// every row under it — which, with a highlight sitting on one of those rows,
// means Enter runs something the reader did not point at. Appended, the
// arriving group never disturbs what is already selected.
function quickSwitcherCandidates() {
  const query = (quickSwitcherInput?.value || "").trim().toLowerCase();
  const threads = state.threads
    .filter((thread) => threadMatchesQuery(thread, query))
    .slice(0, 8)
    .map((thread) => ({ kind: "thread", thread }));
  const matches = contentMatches.map((match) => ({ kind: "match", match }));
  return threads.concat(quickSwitcherCommands(query), matches);
}

const QUICK_SWITCHER_HEADINGS = { command: "Actions", match: "In messages" };

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
  let headingShown = "";
  candidates.forEach((candidate, index) => {
    const heading = QUICK_SWITCHER_HEADINGS[candidate.kind];
    if (heading && headingShown !== candidate.kind) {
      headingShown = candidate.kind;
      const label = document.createElement("li");
      label.className = "quick-switcher-heading";
      label.textContent = heading;
      quickSwitcherList.appendChild(label);
    }
    const item = document.createElement("li");
    const button = document.createElement("button");
    button.type = "button";
    const active = index === quickSwitcherIndex ? " active" : "";
    const title = document.createElement("strong");
    const meta = document.createElement("span");
    if (candidate.kind === "thread") {
      button.className = "quick-switcher-item" + active;
      title.textContent = candidate.thread.name || "Untitled thread";
      meta.textContent = `${candidate.thread.message_count || 0} messages`;
    } else if (candidate.kind === "match") {
      // The conversation the words are in, then the words. Named first
      // because "where was that said" is the question this group answers.
      button.className = "quick-switcher-item quick-switcher-match" + active;
      title.textContent =
        (isRecord(candidate.match) &&
          typeof candidate.match.thread_name === "string" &&
          candidate.match.thread_name) ||
        "Untitled thread";
      meta.textContent =
        (candidate.match.role === "you" ? "You: " : "") + candidate.match.snippet;
    } else {
      button.className = "quick-switcher-command" + active;
      title.textContent = candidate.label;
      meta.textContent = candidate.hint;
    }
    button.append(title, meta);
    button.addEventListener("click", () => {
      commitQuickSwitcherChoice(candidate);
    });
    item.appendChild(button);
    quickSwitcherList.appendChild(item);
  });
}

export function openQuickSwitcher() {
  if (!quickSwitcher) return;
  // Reset BEFORE asking whether there is anything to show. The other order
  // asked the question of the previous query — so a palette last used to find
  // a word inside a message refused to open at all the next time, because
  // nothing matched a query the reader was no longer looking at.
  quickSwitcherIndex = 0;
  if (quickSwitcherInput) quickSwitcherInput.value = "";
  clearContentMatches();
  // Commands make the palette useful even before the first conversation
  // exists, so an empty thread list no longer keeps it closed.
  if (quickSwitcherCandidates().length === 0) return;
  quickSwitcher.hidden = false;
  renderQuickSwitcher();
  quickSwitcherInput?.focus();
}

export function closeQuickSwitcher() {
  if (quickSwitcher) quickSwitcher.hidden = true;
  // Nothing outstanding survives the close: an answer to a query the reader
  // has already dismissed would otherwise be waiting in the list the next time
  // the palette opens.
  clearContentMatches();
}

function commitQuickSwitcherChoice(candidate) {
  closeQuickSwitcher();
  if (candidate.kind === "command") {
    candidate.run();
    return;
  }
  if (candidate.kind === "match") {
    const thread = state.threads.find(
      (item) => item.uuid === candidate.match.thread_uuid
    );
    if (thread) selectThread(thread);
    return;
  }
  selectThread(candidate.thread);
}

if (quickSwitcherInput) {
  quickSwitcherInput.addEventListener("input", () => {
    quickSwitcherIndex = 0;
    scheduleContentSearch();
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

// The title bar's search icon. It opens this — it does not open a second,
// smaller search of its own, which is the whole point of the icon replacing
// the field that used to sit under the product name.
if (sidebarSearchButton) {
  sidebarSearchButton.addEventListener("click", () => {
    if (quickSwitcher && !quickSwitcher.hidden) closeQuickSwitcher();
    else openQuickSwitcher();
  });
}

// The palette's third group. Names and actions are answered from memory the
// moment a key lands; message bodies live in SQLite, so the sidecar answers
// those — debounced so a fast typist asks once, generation-guarded so a slow
// answer to an old query can never overwrite the results of a newer one.
let contentSearchTimer = null;

function clearContentMatches() {
  fences.contentSearch.bump();
  if (contentSearchTimer) clearTimeout(contentSearchTimer);
  contentSearchTimer = null;
  contentMatches = [];
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
    // Message-body search is an enhancement on top of the name match; a
    // failure degrades to exactly the palette this app always had.
    return;
  }
  if (!fences.contentSearch.isCurrent(generation)) return;
  // The palette may have been dismissed while the request was in flight, and a
  // closed palette has no list to paint into.
  if (quickSwitcher?.hidden) return;
  contentMatches = matches;
  renderQuickSwitcher();
}

export function scheduleContentSearch() {
  if (contentSearchTimer) clearTimeout(contentSearchTimer);
  contentSearchTimer = null;
  const query = (quickSwitcherInput?.value || "").trim();
  // One character matches most of a database and tells the reader nothing, and
  // an empty box is not a question.
  if (query.length < 2) {
    clearContentMatches();
    return;
  }
  fences.contentSearch.bump();
  contentMatches = [];
  contentSearchTimer = setTimeout(() => {
    void runContentSearch(query);
  }, 250);
}

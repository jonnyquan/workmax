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
import { currentLocale, t } from "./i18n.js";
import {
  emptyState,
  messageList,
  quickSwitcher,
  quickSwitcherInput,
  quickSwitcherList,
  sidebarSearchButton,
  threadList,
  threadMenu,
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
  canSendTurn,
  handleNewChat,
  renderEmptyState,
  updateComposerState,
} from "./composer.js";
import { contextState, renderTaskContext } from "./context-panel.js";
import {
  THEME_CHOICES,
  THEME_HINTS,
  THEME_LABELS,
  exportSelectedThread,
  exportThreadByUUID,
  loadThreads,
  openModelSettings,
  renameThreadByUUID,
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
// The short time that sits at the right end of a row, the way Codex places
// it: "3m", "2h", "5d" — one glance, no sentence. Older than a week falls
// back to a date; the group header above already carries the calendar story.
export function relativeTime(value) {
  const then = new Date(value).getTime();
  if (Number.isNaN(then)) return "";
  const minutes = Math.floor((Date.now() - then) / 60000);
  if (minutes < 1) return t("now");
  if (minutes < 60) return t("{n}m", { n: minutes });
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t("{n}h", { n: hours });
  const days = Math.floor(hours / 24);
  if (days < 7) return t("{n}d", { n: days });
  const date = new Date(then);
  const sameYear = date.getFullYear() === new Date().getFullYear();
  // The window's language, not the OS's: a zh window on an en machine (or the
  // reverse) should date its rows in the language the rest of the row speaks.
  const dateLocale = currentLocale() === "zh" ? "zh-CN" : "en-US";
  return sameYear
    ? date.toLocaleDateString(dateLocale, { month: "short", day: "numeric" })
    : date.toLocaleDateString(dateLocale, { year: "numeric", month: "short", day: "numeric" });
}

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
  return (thread.name || t("Untitled thread")).toLowerCase().includes(query);
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
      throw new Error(sanitizeErrorMessage(raw) || t("Could not update the pin"));
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
      throw new Error(sanitizeErrorMessage(raw) || t("Could not set the project"));
    }
  } catch (error) {
    setStatus(String(error.message || error), "error");
    return;
  }
  await loadThreads();
}

// The inline editor a row uses when the menu asks it for a word — a project
// name, a new title. window.prompt would be the simplest input a browser can
// offer, but WKWebView (the only webview this app ships in) does not honour
// it: the call returns null without blocking or showing any UI, so the user
// picks the menu item and nothing happens. An inline field on the row itself
// is the reliable path, and the only one a test harness can drive without a
// real prompt implementation.
//
// One editor for both questions rather than two that drift. The Enter /
// Escape / blur handling below is the part worth not writing twice: it is
// three lines of intent and one subtle guard, and a second copy would be the
// one that forgets the guard.
function openRowEditor(item, { value, placeholder, ariaLabel, commit: onCommit }) {
  if (item.querySelector?.(".thread-project-editor")) return;
  const editor = document.createElement("div");
  editor.className = "thread-project-editor";
  const input = document.createElement("input");
  input.type = "text";
  input.className = "thread-project-input";
  input.value = value;
  input.setAttribute("placeholder", placeholder);
  input.setAttribute("aria-label", ariaLabel);
  // A flag, not a DOM probe: blur fires again when the editor is torn down
  // (Enter or Escape), and without it the second blur would double-commit or
  // double-cancel. document.body.contains would do the same job in a real
  // browser but is absent from the test harness's fake DOM.
  let settled = false;
  const commit = () => {
    if (settled) return;
    settled = true;
    onCommit(input.value);
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

function openProjectEditor(thread, item) {
  openRowEditor(item, {
    value: thread.project || "",
    placeholder: t("Project name"),
    ariaLabel: t("Project for {name}", { name: thread.name || t("Untitled thread") }),
    commit: (value) => void commitThreadProject(thread, value),
  });
}

// Renaming from the rail, without opening the conversation first. The header
// form is the other way to ask; both land in renameThreadByUUID, so the
// validation and the in-flight guard exist once.
function openRenameEditor(thread, item) {
  openRowEditor(item, {
    value: thread.name || "",
    placeholder: t("Conversation name"),
    ariaLabel: t("New name for {name}", { name: thread.name || t("Untitled thread") }),
    commit: (value) => void renameThreadByUUID(thread.uuid, value),
  });
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
          ? t("That conversation has never left this machine.")
          : sanitizeErrorMessage(raw) || t("Could not change the sync setting")
      );
    }
  } catch (error) {
    setStatus(String(error.message || error), "error");
    return;
  }
  setStatus(
    next === "paused"
      ? t("Kept on this machine. Existing cloud copies are untouched.")
      : t("Syncing to WorkMax again.")
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
          ? t("That conversation still has a response in flight. Stop it first.")
          : t("Could not delete the conversation."),
        "error"
      );
      return;
    }
  } catch {
    setStatus(t("Could not delete the conversation."), "error");
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
  setStatus(t("Conversation deleted from this machine."));
}

function renderThreadButton(thread, inProject = false) {
  const item = document.createElement("li");
  // Inside a project the row indents: the project header is the parent, the
  // threads are its children, and the indentation is what says so.
  item.className = inProject
    ? "thread-item thread-in-project reveal-on-hover-host"
    : "thread-item reveal-on-hover-host";
  item.dataset.threadUuid = thread.uuid;
  const button = document.createElement("button");
  button.type = "button";
  button.className = "thread-button";
  button.classList.toggle("active", thread.uuid === state.selectedThreadUUID);
  const title = document.createElement("strong");
  title.textContent = thread.name || t("Untitled thread");
  // One line per conversation: the title, and a short relative time at the
  // right end — visible, not behind a tooltip, the way Codex places it. The
  // tooltip keeps the full sentence for when the short form is not enough.
  title.title = `${formatMessageTime(thread.updated_at) || formatDate(thread.updated_at)} · ${t("{n} messages", { n: thread.message_count || 0 })}`;
  const when = document.createElement("span");
  when.className = "thread-when";
  when.textContent = relativeTime(thread.updated_at);
  button.append(title, when);
  if (state.recoverableTurns.some((turn) => turn.thread_uuid === thread.uuid)) {
    const badge = document.createElement("span");
    badge.className = "thread-recovery-badge";
    badge.textContent = t("Interrupted");
    button.appendChild(badge);
  }
  // A paused sync is a STANDING FACT about the conversation, not an action, so
  // it stays on the row at rest while the switch that changes it moves into
  // the menu. It used to be a hover-revealed button doing both jobs, which
  // meant "this one never leaves my machine" was only true-looking while the
  // pointer was on it. A fact you can only see by hovering is a fact the user
  // does not have.
  if (thread.cloud_sync_state === "paused") {
    const badge = document.createElement("span");
    badge.className = "thread-local-badge";
    badge.textContent = t("Local only");
    button.appendChild(badge);
  }
  button.addEventListener("click", () => {
    selectThread(thread);
  });
  item.appendChild(button);
  item.appendChild(buildThreadActions(thread, item));
  // Right-click anywhere on the row opens the same menu the ⋯ opens. On this
  // platform that also covers Control-click, which is how a Mac trackpad
  // without a second button asks for a context menu.
  item.addEventListener("contextmenu", (event) => {
    event.preventDefault?.();
    openThreadMenu(thread, item, event);
  });
  return item;
}

// --- Row actions: two icons, and a menu for everything else ------------------
//
// Four text buttons used to sit in the row's corner — Pin, Project, Syncing,
// Delete — each appearing on hover, each a different width, and each having to
// be told where to sit relative to the others. The stylesheet carried five
// pixel offsets (right: 62px / 74px / 122px / 134px …) whose only job was to
// keep four variable-width labels from overlapping, recomputed by hand every
// time a label changed length. Translating the labels would have invalidated
// every one of those numbers, which is how a layout built on arithmetic fails:
// silently, in the language nobody tested.
//
// Two icons now, in a flex row that measures itself. Pin is the one action
// worth a click of its own — it is cheap, reversible and used constantly — and
// ⋯ carries the rest. Both are the same size, so nothing has to know about
// anything else's width.
//
// The ⋯ is not decoration: right-click is the fast path, but a menu reachable
// ONLY by right-click is a menu a keyboard cannot open, and Delete used to be
// Tab-reachable. The button keeps that promise.
function buildThreadActions(thread, item) {
  const actions = document.createElement("div");
  actions.className = "thread-actions reveal-on-hover";
  const name = thread.name || t("Untitled thread");

  const pin = document.createElement("button");
  pin.type = "button";
  pin.className = "thread-action thread-pin";
  pin.classList.toggle("pinned", thread.pinned === true);
  // Both marks, and they say the same thing: title is the tooltip a pointer
  // gets, aria-label is what a screen reader reads. An icon button with only
  // one of them is either unlabelled or untooltipped, and which one is missing
  // depends on which user you are.
  const pinLabel = t(thread.pinned ? "Unpin {name}" : "Pin {name}", { name });
  pin.title = thread.pinned ? t("Unpin") : t("Pin");
  pin.setAttribute("aria-label", pinLabel);
  pin.appendChild(buildIcon(PIN_ICON));
  pin.addEventListener("click", (event) => {
    event.stopPropagation?.();
    pin.disabled = true;
    void toggleThreadPin(thread);
  });
  actions.appendChild(pin);

  const more = document.createElement("button");
  more.type = "button";
  more.className = "thread-action thread-more";
  more.title = t("More");
  more.setAttribute("aria-label", t("More actions for {name}", { name }));
  more.setAttribute("aria-haspopup", "menu");
  more.appendChild(buildIcon(MORE_ICON));
  more.addEventListener("click", (event) => {
    event.stopPropagation?.();
    openThreadMenu(thread, item, event);
  });
  actions.appendChild(more);
  return actions;
}

// Icons are hand-written paths on the app's 16-unit grid, drawn with
// createElementNS — not innerHTML. The whole renderer holds the line that
// markup is never parsed from a string, and an icon is not a good enough
// reason to open that door for the first time.
const PIN_ICON = [
  "M6 1.9h4",
  "M6.6 1.9v3.5a2.6 2.6 0 0 1-1.4 2.3l-.8.5a1.2 1.2 0 0 0-.6 1v.3h8.4v-.3a1.2 1.2 0 0 0-.6-1l-.8-.5a2.6 2.6 0 0 1-1.4-2.3V1.9",
  "M8 9.5v4.6",
];
const MORE_ICON = ["M3.6 8h.01", "M8 8h.01", "M12.4 8h.01"];

function buildIcon(paths) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");
  for (const d of paths) {
    const path = document.createElementNS(ns, "path");
    path.setAttribute("d", d);
    svg.appendChild(path);
  }
  return svg;
}

// --- The row menu ------------------------------------------------------------
//
// One element for the whole rail, refilled on open. Its items are conditional
// — a synced thread has no delete, a local one has no sync switch — so the
// menu is built from the thread rather than shown from the markup.
//
// Projects lead it, and as a LIST rather than a text field: the projects that
// exist are the answer most of the time, and making the reader retype a name
// they already used is how a second project called "Q3 deck " (trailing space)
// gets created. The field is still there under "New project…" for the first
// one, and for the times the answer is not on the list.
let openMenuThreadUUID = "";

export function closeThreadMenu() {
  if (!threadMenu) return;
  threadMenu.hidden = true;
  threadMenu.textContent = "";
  openMenuThreadUUID = "";
}

// Every project the rail knows about, in the order the sidecar sorted them.
// Derived rather than stored: the thread list IS the record of which projects
// exist, and a second list would be one that could disagree with it.
function knownProjects() {
  const seen = [];
  for (const candidate of state.threads) {
    const key = typeof candidate.project === "string" ? candidate.project.trim() : "";
    if (key && !seen.includes(key)) seen.push(key);
  }
  return seen;
}

function menuItem(label, run, kind = "") {
  const button = document.createElement("button");
  button.type = "button";
  button.className = kind ? `thread-menu-item ${kind}` : "thread-menu-item";
  button.setAttribute("role", "menuitem");
  button.textContent = label;
  button.addEventListener("click", (event) => {
    event.stopPropagation?.();
    run(button);
  });
  return button;
}

function menuHeading(label) {
  const heading = document.createElement("div");
  heading.className = "thread-menu-heading";
  heading.textContent = label;
  return heading;
}

export function openThreadMenu(thread, item, event) {
  if (!threadMenu) return;
  // Always (re)open at the pointer rather than toggling on a repeat press.
  // A toggle here would be dead code as well as wrong: pointerdown fires
  // before contextmenu, so the document-level dismissal has already closed
  // any open menu by the time this runs. Native context menus re-open at the
  // new position too.
  closeThreadMenu();
  openMenuThreadUUID = thread.uuid;

  const heading = document.createElement("div");
  heading.className = "thread-menu-title";
  heading.textContent = thread.name || t("Untitled thread");
  threadMenu.appendChild(heading);

  threadMenu.appendChild(
    menuItem(thread.pinned ? t("Unpin") : t("Pin"), () => {
      closeThreadMenu();
      void toggleThreadPin(thread);
    })
  );

  // Rename and Export are about this conversation rather than about where it
  // is filed, so they sit with Pin rather than under the Projects heading.
  // Both used to require opening the thread first — the controls lived in the
  // header of the open conversation and nowhere else — which made "rename
  // this one" a three-step errand ending somewhere you did not mean to go.
  if (typeof window.desktopBridge?.agent?.renameThread === "function") {
    threadMenu.appendChild(
      menuItem(t("Rename"), () => {
        closeThreadMenu();
        openRenameEditor(thread, item);
      })
    );
  }
  // Offered only where it would produce something: exporting an empty
  // conversation writes a file with a title and nothing under it, and the
  // sidecar answers thread_empty rather than pretending otherwise.
  if (
    (thread.message_count || 0) > 0 &&
    typeof window.desktopBridge?.agent?.exportThread === "function"
  ) {
    threadMenu.appendChild(
      menuItem(t("Export as Markdown"), () => {
        closeThreadMenu();
        void exportThreadByUUID(thread.uuid);
      })
    );
  }

  if (typeof window.desktopBridge?.agent?.setThreadProject === "function") {
    const projects = knownProjects().filter((key) => key !== thread.project);
    if (projects.length > 0) threadMenu.appendChild(menuHeading(t("Move to project")));
    for (const key of projects) {
      threadMenu.appendChild(
        menuItem(key, () => {
          closeThreadMenu();
          void commitThreadProject(thread, key);
        }, "thread-menu-project")
      );
    }
    threadMenu.appendChild(
      menuItem(t("New project…"), () => {
        closeThreadMenu();
        openProjectEditor(thread, item);
      })
    );
    if (thread.project) {
      threadMenu.appendChild(
        menuItem(t("Remove from project"), () => {
          closeThreadMenu();
          void commitThreadProject(thread, "");
        })
      );
    }
  }

  if (threadSyncSwitchable(thread)) {
    const paused = thread.cloud_sync_state === "paused";
    threadMenu.appendChild(
      menuItem(paused ? t("Resume syncing") : t("Keep on this machine"), () => {
        closeThreadMenu();
        void toggleThreadCloudSync(thread);
      })
    );
  }

  if (threadIsDeletable(thread)) {
    // Two clicks, one item: the first arms it, the second deletes — the same
    // guarantee the row's Delete button used to carry, kept because moving a
    // destructive action into a menu is not a reason to make it cheaper. The
    // menu deliberately stays OPEN after the first click; closing it would
    // discard the arming and make the second click land somewhere else.
    threadMenu.appendChild(
      menuItem(t("Delete"), (button) => {
        if (!button.classList.contains("armed")) {
          button.classList.add("armed");
          button.textContent = t("Confirm");
          setTimeout(() => {
            button.classList.remove("armed");
            button.textContent = t("Delete");
          }, DELETE_ARM_MS);
          return;
        }
        closeThreadMenu();
        void deleteThread(thread);
      }, "danger")
    );
  }

  threadMenu.hidden = false;
  placeThreadMenu(event);
  // Focus the first item so the keyboard lands inside the thing it opened.
  const first = Array.from(threadMenu.children || []).find(
    (child) => typeof child.focus === "function" && child.classList?.contains("thread-menu-item")
  );
  first?.focus?.();
}

// Viewport coordinates, clamped so a row near the bottom of a tall rail does
// not open its menu below the window. The measurements are read from the live
// element, which is why this runs after `hidden` comes off: a hidden element
// measures zero and every menu would think it fits.
function placeThreadMenu(event) {
  const x = Number.isFinite(event?.clientX) ? event.clientX : 0;
  const y = Number.isFinite(event?.clientY) ? event.clientY : 0;
  const width = threadMenu.offsetWidth || 220;
  const height = threadMenu.offsetHeight || 0;
  const maxX = (globalThis.innerWidth || 0) - width - 8;
  const maxY = (globalThis.innerHeight || 0) - height - 8;
  threadMenu.style.left = `${Math.max(8, Math.min(x, maxX > 8 ? maxX : x))}px`;
  threadMenu.style.top = `${Math.max(8, Math.min(y, maxY > 8 ? maxY : y))}px`;
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
  // The menu belongs to a row that is about to be replaced, and a menu whose
  // row is gone is a menu whose Delete points at nothing. Every path that
  // repaints the rail comes through here, which is why this is the only place
  // that has to remember.
  closeThreadMenu();
  threadList.textContent = "";
  if (state.threads.length === 0) {
    const item = document.createElement("li");
    item.appendChild(renderNotice(t("No cached threads yet.")));
    threadList.appendChild(item);
    renderEmptyState();
    return;
  }
  const groups = groupThreads(state.threads, new Date());

  // Pinned leads. A pin overrides project and calendar alike.
  if (groups.pinned.length > 0) {
    const heading = document.createElement("li");
    heading.className = "thread-group";
    heading.textContent = t(PINNED_GROUP_LABEL);
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
      threadList.appendChild(renderThreadButton(thread, true));
    }
  }

  // Ungrouped: threads with neither a pin nor a project, grouped by local
  // calendar day.
  for (const group of UNGROUPED_GROUPS) {
    const bucket = groups.ungrouped[group.key];
    if (bucket.length === 0) continue;
    const heading = document.createElement("li");
    heading.className = "thread-group";
    heading.textContent = t(group.label);
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
  if (canOpenNewThread() || (state.selectedThreadUUID && canSendTurn())) {
    commands.push({
      kind: "command",
      label: t("New chat"),
      hint: t("Start a conversation"),
      run: () => handleNewChat(),
    });
  }
  if (current) {
    commands.push({
      kind: "command",
      label: current.pinned ? t("Unpin this conversation") : t("Pin this conversation"),
      hint: current.name || t("Untitled thread"),
      run: () => void toggleThreadPin(current),
    });
    if ((current.message_count || 0) > 0 && typeof agent?.exportThread === "function") {
      commands.push({
        kind: "command",
        label: t("Export as Markdown"),
        hint: current.name || t("Untitled thread"),
        run: () => void exportSelectedThread(),
      });
    }
  }
  commands.push({
    kind: "command",
    label: t("Open model settings"),
    hint: t("Local route, protocol, API key"),
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
      label: t("Appearance: {label}", { label: t(THEME_LABELS[choice]) }),
      hint: choice === themeChoice ? t("Current") : t(THEME_HINTS[choice]),
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
// Values above are catalog keys; translated at render time (renderQuickSwitcher).

export function renderQuickSwitcher() {
  if (!quickSwitcherList) return;
  const candidates = quickSwitcherCandidates();
  if (quickSwitcherIndex >= candidates.length) quickSwitcherIndex = Math.max(0, candidates.length - 1);
  quickSwitcherList.textContent = "";
  if (candidates.length === 0) {
    const empty = document.createElement("li");
    empty.className = "quick-switcher-empty";
    empty.textContent = t("Nothing matches.");
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
      label.textContent = t(heading);
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
      title.textContent = candidate.thread.name || t("Untitled thread");
      meta.textContent = t("{n} messages", { n: candidate.thread.message_count || 0 });
    } else if (candidate.kind === "match") {
      // The conversation the words are in, then the words. Named first
      // because "where was that said" is the question this group answers.
      button.className = "quick-switcher-item quick-switcher-match" + active;
      title.textContent =
        (isRecord(candidate.match) &&
          typeof candidate.match.thread_name === "string" &&
          candidate.match.thread_name) ||
        t("Untitled thread");
      meta.textContent =
        (candidate.match.role === "you" ? t("You: ") : "") + candidate.match.snippet;
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

// The menu's four ways out. Each is a different way of saying "the reader has
// moved on": a press somewhere else, Escape, a scroll that would leave the
// menu floating over the wrong row, and the rail repainting under it
// (renderThreads). pointerdown rather than click so the menu is gone before
// whatever was pressed acts on it.
document.addEventListener("pointerdown", (event) => {
  if (!threadMenu || threadMenu.hidden) return;
  // A press INSIDE the menu is the menu being used, not dismissed. The stub
  // DOM has no contains(), and neither does a synthetic event without a
  // target, so both absences fall through to "outside" — which closes a menu
  // that was about to run its item anyway, one tick before the item's own
  // handler. The item still runs; it is its own listener.
  if (typeof threadMenu.contains === "function" && event.target &&
      threadMenu.contains(event.target)) {
    return;
  }
  closeThreadMenu();
});
// Escape closes it; the arrows walk it. A menu that can only be entered with
// Tab is one a keyboard user has to leave the menu to escape from — Tab does
// not wrap, so the last item's next stop is whatever comes after the menu in
// the document, which is nothing to do with the conversation they opened it
// for. Arrows wrap, which is what makes the menu a place rather than a
// stretch of the tab order.
document.addEventListener("keydown", (event) => {
  if (!threadMenu || threadMenu.hidden) return;
  if (event.key === "Escape") {
    event.stopPropagation?.();
    closeThreadMenu();
    return;
  }
  const step =
    event.key === "ArrowDown" ? 1 :
    event.key === "ArrowUp" ? -1 :
    event.key === "Home" ? 0 :
    event.key === "End" ? -0.5 :
    null;
  if (step === null) return;
  const items = Array.from(threadMenu.children || []).filter((child) =>
    child.classList?.contains("thread-menu-item")
  );
  if (items.length === 0) return;
  event.preventDefault?.();
  const here = items.findIndex((child) => child.focused === true || child === activeMenuItem());
  const next =
    step === 0 ? 0 :
    step === -0.5 ? items.length - 1 :
    here < 0 ? (step > 0 ? 0 : items.length - 1) :
    (here + step + items.length) % items.length;
  items[next]?.focus?.();
});

// document.activeElement in a real browser; the stub does not model one, so
// the arrow walk falls back to the `focused` flag its elements carry. Both
// answers are "which item is the keyboard on", and neither is authoritative
// on the other's platform.
function activeMenuItem() {
  return globalThis.document?.activeElement ?? null;
}
if (threadList) {
  // Scrolling moves the row out from under a menu pinned to viewport
  // coordinates. Closing is the honest answer; re-anchoring on every scroll
  // frame is machinery for a gesture that means "I am looking elsewhere".
  threadList.addEventListener("scroll", () => closeThreadMenu());
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

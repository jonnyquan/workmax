// The mind (心智体): the title-bar icon, the right-column panel behind it, and
// the one piece of state they share.
//
// A mind is a long-lived persona over this identity's single knowledge base:
// a BRAIN (the model it reasons with), a CEREBELLUM (the skills it practises),
// and a MEMORY (the material it has been taught). Several minds share one
// library and differ in viewpoint — that decision is why nothing here creates
// a second index, and why switching a mind is a cheap read rather than a
// workspace migration.
//
// What this module deliberately does NOT do is narrate the running turn. The
// transcript and the workspace column already own that. The one place the two
// touch is the icon's motion, and the rule there is strict: it moves only when
// something mental is really happening — reasoning tokens arriving, or a
// material just taught — and is otherwise perfectly still. An icon that
// breathes on a timer is decoration pretending to be information.
import {
  mindAnatomyMeta,
  mindBrainNote,
  mindBrainValue,
  mindButton,
  mindCreateButton,
  mindCreateName,
  mindEditCancel,
  mindEditDelete,
  mindEditForm,
  mindEditModel,
  mindEditName,
  mindEditNote,
  mindEditRole,
  mindEditSave,
  mindFeedButton,
  mindFeedError,
  mindFeedStatus,
  mindFeedText,
  mindFeedTitle,
  mindMemoryList,
  mindMemoryMeta,
  mindMemoryNote,
  mindMemorySection,
  mindMemoryValue,
  mindRoster,
  mindRosterError,
  mindSkillsNote,
  mindSkillsValue,
  mindSubtitle,
} from "./dom.js";
import { isRecord, parseDesktopBridgeResult, sanitizeErrorMessage } from "./protocol.js";
import { state } from "./renderer.js";

// The two things that count as real mental activity, and how long each one is
// worth showing. Thinking re-arms on every reasoning delta, so its window only
// has to outlast the gap between two tokens; learning is a single event and
// gets long enough to be noticed by someone whose eyes were elsewhere.
const MIND_ACTIVITY_MS = { thinking: 1200, learning: 2600 };

// Whether the panel is on screen is NOT here: the right column owns that, and
// it is one state with three values (workspace / mind / neither) rather than a
// boolean per panel — see renderer.js, "The right column". A local `open` flag
// beside it would be a second answer to the same question, and the two would
// disagree the first time the column was folded from the other button.
export const mindState = {
  minds: [],
  activeID: null,
  status: null,
  loading: false,
  feeding: false,
  // Which mind the editor is open on, or null. Held here rather than read off
  // the DOM because loadMinds repaints the roster and the editor has to know
  // whether it survived that.
  editing: null,
  // What the icon is currently saying, or "" for the still, idle state.
  activity: "",
};

let activityTimer = null;

// noteMindActivity is called from the stream (a reasoning delta) and from the
// feed path (a material landed). It is on the hot path for reasoning, so it
// touches the DOM only when the state actually changes — a class write per
// token would be the kind of cost this renderer's batcher exists to avoid.
export function noteMindActivity(kind) {
  const duration = MIND_ACTIVITY_MS[kind];
  if (!duration) return;
  if (mindState.activity !== kind) {
    mindState.activity = kind;
    if (mindButton) mindButton.setAttribute("data-mind-activity", kind);
  }
  if (activityTimer !== null) clearTimeout(activityTimer);
  activityTimer = setTimeout(() => {
    activityTimer = null;
    mindState.activity = "";
    if (mindButton) mindButton.removeAttribute("data-mind-activity");
  }, duration);
}

function mindBridge() {
  const bridge = window.desktopBridge?.mind;
  return isRecord(bridge) && typeof bridge.list === "function" ? bridge : null;
}

function setMindRosterError(message) {
  if (!mindRosterError) return;
  mindRosterError.textContent = message;
  mindRosterError.hidden = message === "";
}

function setMindFeedError(message) {
  if (!mindFeedError) return;
  mindFeedError.textContent = message;
  mindFeedError.hidden = message === "";
}

function setMindFeedStatus(message) {
  if (mindFeedStatus) mindFeedStatus.textContent = message;
}

// The active mind, or the first one — a list is never empty in practice (the
// sidecar seeds a default), and a nullable "current mind" would put a guard on
// every read below for a state that cannot happen.
export function activeMind() {
  return (
    mindState.minds.find((mind) => mind.id === mindState.activeID) ||
    mindState.minds.find((mind) => mind.active) ||
    mindState.minds[0] ||
    null
  );
}

// Called by the right column when the mind becomes the panel on screen.
// Painted from whatever is already known, then refreshed: the column fills in
// rather than showing a blank one while a loopback request completes.
export function mindPanelShown() {
  renderMindPanel();
  void loadMinds();
}

// And when it stops being the panel on screen. Only the two error lines are
// cleared: they are about the last thing that was attempted, not about the
// mind, and a failure from before lunch has no business greeting the next
// person to press the icon. What the reader had half-typed into the teach form
// stays exactly where it was — this column comes and goes at the press of a
// title-bar icon now, and a panel that eats a draft when it is folded away is
// a panel nobody folds twice.
export function mindPanelHidden() {
  setMindFeedError("");
  setMindRosterError("");
}

// loadMinds reads the roster and then the active mind's status. Two calls
// rather than one fat endpoint because they answer different questions and
// change at different rates: the roster moves when a user creates or switches,
// the status moves whenever the mind is taught something.
export async function loadMinds() {
  const bridge = mindBridge();
  if (!bridge) {
    // A shell too old to carry the mind routes is a build fact, not a user
    // error — said inside the panel that cannot work, not on the status strip.
    setMindRosterError("Minds need a newer Desktop shell.");
    return;
  }
  mindState.loading = true;
  try {
    const result = parseDesktopBridgeResult(await bridge.list(), "mind.list");
    if (!result.ok) throw new Error("minds unavailable");
    mindState.minds = Array.isArray(result.data?.items) ? result.data.items : [];
    const current = activeMind();
    mindState.activeID = current ? current.id : null;
    setMindRosterError("");
  } catch (error) {
    mindState.minds = [];
    setMindRosterError(sanitizeErrorMessage(String(error.message || error)) || "Minds could not be loaded.");
  } finally {
    mindState.loading = false;
  }
  renderMindPanel();
  await loadMindStatus();
}

async function loadMindStatus() {
  const bridge = mindBridge();
  const mind = activeMind();
  if (!bridge || !mind) {
    mindState.status = null;
    renderMindPanel();
    return;
  }
  try {
    const result = parseDesktopBridgeResult(await bridge.status(mind.id), "mind.status");
    mindState.status = result.ok ? result.data : null;
  } catch {
    // A status that will not parse is no status: the panel keeps naming the
    // mind and stops making claims about what it holds.
    mindState.status = null;
  }
  renderMindPanel();
}

export async function selectMind(id) {
  const bridge = mindBridge();
  if (!bridge) return;
  try {
    const result = parseDesktopBridgeResult(await bridge.select(id), "mind.select");
    if (!result.ok) throw new Error("could not switch mind");
    mindState.activeID = id;
    setMindRosterError("");
  } catch (error) {
    setMindRosterError(sanitizeErrorMessage(String(error.message || error)) || "Could not switch mind.");
    return;
  }
  await loadMinds();
}

export async function submitCreateMind(event) {
  event?.preventDefault?.();
  const bridge = mindBridge();
  if (!bridge || !mindCreateName) return;
  const name = mindCreateName.value.trim();
  if (name === "") {
    setMindRosterError("A mind needs a name.");
    return;
  }
  if (mindCreateButton) mindCreateButton.disabled = true;
  try {
    const result = parseDesktopBridgeResult(await bridge.create({ name }), "mind.create");
    if (!result.ok) throw new Error("could not create this mind");
    mindCreateName.value = "";
    setMindRosterError("");
  } catch (error) {
    setMindRosterError(sanitizeErrorMessage(String(error.message || error)) || "Could not create this mind.");
  } finally {
    if (mindCreateButton) mindCreateButton.disabled = false;
  }
  await loadMinds();
}

// --- Editing -----------------------------------------------------------------
//
// A mind's describable parts are editable because otherwise a role hint is
// written once and forever: correcting one would mean creating a new mind,
// which means losing everything the old one was taught. An identity's memory
// should not be the price of a typo.
//
// Delete lives in here rather than beside the switch button on the row. The
// two would sit a few pixels apart, they do opposite things, and one of them
// erases every material this mind was ever fed. Opening the editor is a speed
// bump, and it is also the one place with room to say so.
function findMind(id) {
  return mindState.minds.find((mind) => mind.id === id) || null;
}

export function openMindEditor(id) {
  const mind = findMind(id);
  if (!mind || !mindEditForm) return;
  mindState.editing = id;
  if (mindEditName) mindEditName.value = mind.name;
  if (mindEditRole) mindEditRole.value = mind.role_hint || "";
  if (mindEditModel) mindEditModel.value = mind.model_override || "";
  mindEditForm.hidden = false;
  setMindEditNote("");
  disarmMindDelete();
  if (mindEditName?.focus) mindEditName.focus();
}

export function closeMindEditor() {
  mindState.editing = null;
  if (mindEditForm) mindEditForm.hidden = true;
  setMindEditNote("");
  disarmMindDelete();
}

function setMindEditNote(text) {
  if (!mindEditNote) return;
  mindEditNote.textContent = text;
  mindEditNote.hidden = text === "";
}

function disarmMindDelete() {
  if (!mindEditDelete) return;
  mindEditDelete.classList.remove("armed");
  mindEditDelete.textContent = "Delete";
}

export async function submitEditMind(event) {
  event?.preventDefault?.();
  const bridge = mindBridge();
  const id = mindState.editing;
  if (!bridge || !id || !mindEditName) return;
  const name = mindEditName.value.trim();
  if (name === "") {
    setMindEditNote("A mind needs a name.");
    return;
  }
  if (mindEditSave) mindEditSave.disabled = true;
  try {
    const result = parseDesktopBridgeResult(
      await bridge.update(id, {
        name,
        role_hint: mindEditRole ? mindEditRole.value.trim() : "",
        model_override: mindEditModel ? mindEditModel.value.trim() : "",
      }),
      "mind.update"
    );
    if (!result.ok) throw new Error("could not save this mind");
    closeMindEditor();
    setMindRosterError("");
  } catch (error) {
    setMindEditNote(sanitizeErrorMessage(String(error.message || error)) || "Could not save this mind.");
  } finally {
    if (mindEditSave) mindEditSave.disabled = false;
  }
  await loadMinds();
}

// Two clicks, one control — the same grammar a conversation delete already
// uses. The first click says what is about to happen, because "delete this
// mind" and "delete everything it was taught" are different sentences and only
// the second one is true.
export async function submitDeleteMind() {
  const bridge = mindBridge();
  const id = mindState.editing;
  if (!bridge || !id || !mindEditDelete) return;
  if (!mindEditDelete.classList.contains("armed")) {
    mindEditDelete.classList.add("armed");
    mindEditDelete.textContent = "Delete for good";
    setMindEditNote("This also erases everything this mind was taught.");
    return;
  }
  mindEditDelete.disabled = true;
  try {
    const result = parseDesktopBridgeResult(await bridge.remove(id), "mind.delete");
    if (!result.ok) throw new Error("could not delete this mind");
    closeMindEditor();
    setMindRosterError("");
  } catch (error) {
    setMindEditNote(sanitizeErrorMessage(String(error.message || error)) || "Could not delete this mind.");
    disarmMindDelete();
  } finally {
    mindEditDelete.disabled = false;
  }
  await loadMinds();
}

// Teaching. The material is sent, indexed under this mind's mark, and the
// panel re-reads its own status — so the count on screen is the sidecar's
// answer rather than an optimistic increment that could disagree with it.
export async function submitFeedMind(event) {
  event?.preventDefault?.();
  const bridge = mindBridge();
  const mind = activeMind();
  if (!bridge || !mind || !mindFeedTitle || !mindFeedText) return;
  const title = mindFeedTitle.value.trim();
  const text = mindFeedText.value.trim();
  if (title === "" || text === "") {
    setMindFeedError("Both a title and some material are needed.");
    return;
  }
  mindState.feeding = true;
  setMindFeedError("");
  setMindFeedStatus("Teaching…");
  if (mindFeedButton) mindFeedButton.disabled = true;
  try {
    const result = parseDesktopBridgeResult(await bridge.feed(mind.id, { title, text }), "mind.feed");
    if (!result.ok) throw new Error("this material could not be indexed");
    const chunks = result.data?.chunks ?? 0;
    mindFeedTitle.value = "";
    mindFeedText.value = "";
    setMindFeedStatus(`Learned "${title}" as ${chunks} ${chunks === 1 ? "passage" : "passages"}.`);
    // The one place the icon's motion is earned by something other than the
    // model: the memory genuinely just grew.
    noteMindActivity("learning");
  } catch (error) {
    setMindFeedStatus("");
    setMindFeedError(sanitizeErrorMessage(String(error.message || error)) || "This material could not be taught.");
  } finally {
    mindState.feeding = false;
    if (mindFeedButton) mindFeedButton.disabled = false;
  }
  await loadMindStatus();
}

function countLabel(count, singular, plural) {
  return `${count} ${count === 1 ? singular : plural}`;
}

function renderMindRoster() {
  if (!mindRoster) return;
  mindRoster.innerHTML = "";
  const current = activeMind();
  for (const mind of mindState.minds) {
    const row = document.createElement("li");
    const button = document.createElement("button");
    button.type = "button";
    button.className = "mind-roster-item";
    const isCurrent = current && mind.id === current.id;
    if (isCurrent) {
      button.classList.add("active");
      button.setAttribute("aria-current", "true");
    }

    const name = document.createElement("span");
    name.className = "mind-roster-name";
    name.textContent = mind.name;
    button.appendChild(name);

    // The subtitle says what distinguishes this mind: its role if it was given
    // one, its own model if it has one, and otherwise nothing at all rather
    // than a filler line that makes every row look the same.
    const hint = mind.role_hint || mind.description || mind.model_override;
    if (hint) {
      const note = document.createElement("span");
      note.className = "mind-roster-note";
      note.textContent = hint;
      button.appendChild(note);
    }

    button.addEventListener("click", () => {
      if (isCurrent) return;
      void selectMind(mind.id);
    });
    row.appendChild(button);

    // Editing is a second, quieter control on the row rather than a mode the
    // whole panel enters. It reveals on hover like every other row action in
    // this app, which also means a keyboard reaches it — see .reveal-on-hover.
    const edit = document.createElement("button");
    edit.type = "button";
    edit.className = "mind-roster-edit reveal-on-hover";
    edit.textContent = "Edit";
    edit.setAttribute("aria-label", `Edit ${mind.name}`);
    edit.addEventListener("click", () => openMindEditor(mind.id));
    row.appendChild(edit);
    row.className = "mind-roster-row reveal-on-hover-host";

    mindRoster.appendChild(row);
  }
}

function renderMindMemoryList(sources) {
  if (!mindMemoryList || !mindMemorySection) return;
  mindMemoryList.innerHTML = "";
  for (const source of sources) {
    const item = document.createElement("li");
    item.className = "mind-memory-item";

    const title = document.createElement("span");
    title.className = "mind-memory-title";
    title.textContent = source.title;

    const meta = document.createElement("span");
    meta.className = "mind-memory-meta";
    meta.textContent = countLabel(source.chunks, "passage", "passages");

    item.append(title, meta);
    mindMemoryList.appendChild(item);
  }
  mindMemorySection.hidden = sources.length === 0;
  if (mindMemoryMeta) {
    mindMemoryMeta.textContent = countLabel(sources.length, "material", "materials");
  }
}

export function renderMindPanel() {
  const mind = activeMind();
  const status = mindState.status;

  // The head describes the COLUMN; the roster describes each mind. It used to
  // put the active mind's role line here, which was fine in a 720px dialog and
  // reads as a bug in a 300px column: the same sentence landed twice, forty
  // pixels apart, once under "Mind" and again under "General mind" in the row
  // right below it. The roster keeps the role — it is what tells two minds
  // apart — and this line goes back to saying what the panel is.
  if (mindSubtitle) {
    mindSubtitle.textContent = mind
      ? "What this identity thinks with, and what it has been taught."
      : "No mind is available on this identity yet.";
  }
  if (mindAnatomyMeta) {
    mindAnatomyMeta.textContent = mind ? mind.name : "";
  }

  renderMindRoster();

  // The brain: which model, and whose choice it was. "From your account" is
  // not a hedge — it is the difference between a mind that was configured and
  // one that is simply riding the identity's route.
  if (mindBrainValue) {
    mindBrainValue.textContent = status ? status.model.label : "—";
  }
  if (mindBrainNote) {
    if (!status) {
      mindBrainNote.textContent = "";
    } else if (status.model.source === "mind") {
      mindBrainNote.textContent = `Chosen for this mind · ${status.model.route} route`;
    } else {
      mindBrainNote.textContent = `From your account · ${status.model.route} route`;
    }
  }

  // The cerebellum: the skills this mind practises. They are the machine's
  // agent skills, not a per-mind list — this version teaches knowledge, not
  // skills, and inventing a number here would be the panel's one lie.
  const skillCount = Array.isArray(state.allowedModes) ? state.allowedModes.length : 0;
  if (mindSkillsValue) {
    mindSkillsValue.textContent = skillCount > 0 ? countLabel(skillCount, "skill", "skills") : "—";
  }
  if (mindSkillsNote) {
    mindSkillsNote.textContent =
      skillCount > 0 ? "Practised through this workspace's skills" : "No skills are available yet";
  }

  // The memory: the chunk count is the store's own, and the retrieval line
  // says whether this build can recall any of it at all.
  const chunks = status ? status.memory.chunks : 0;
  if (mindMemoryValue) {
    mindMemoryValue.textContent = status ? countLabel(chunks, "passage", "passages") : "—";
  }
  if (mindMemoryNote) {
    if (!status) {
      mindMemoryNote.textContent = "";
    } else if (status.retrieval === "local") {
      mindMemoryNote.textContent =
        chunks === 0 ? "Nothing taught yet · recalled on this machine" : "Recalled on this machine";
    } else {
      mindMemoryNote.textContent = "Local recall is unavailable on this build";
    }
  }

  renderMindMemoryList(status ? status.memory.sources : []);

  if (mindFeedButton) {
    mindFeedButton.disabled =
      mindState.feeding || !mind || (status !== null && status.retrieval !== "local");
  }
}

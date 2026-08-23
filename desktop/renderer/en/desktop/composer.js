// The composer and the two forms that feed it: attachments, the send/stop
// state machine, the new-thread draft, and the interrupted-turn recovery card.
//
// These are one module because they are one predicate. updateComposerState is
// the single place that decides whether the app can accept input right now,
// and the create form, the recovery card and the attachment chips are all
// inputs to or consequences of that decision; splitting them produces two
// pieces of code that disagree about whether the send button is enabled.
import { fences } from "./fence.js";
import { t } from "./i18n.js";
import {
  agentMode,
  attachButton,
  attachmentChips,
  chatForm,
  chatInput,
  composerStatus,
  emptyDescription,
  emptyNewThreadButton,
  emptyState,
  emptyTitle,
  newThreadButton,
  newThreadCancelButton,
  newThreadError,
  newThreadForm,
  newThreadMode,
  newThreadName,
  newThreadSubmitButton,
  sendButton,
  stopButton,
  threadPanel,
  turnRecoveryCard,
  turnRecoveryDescription,
  turnRecoveryDismissButton,
  turnRecoveryFeedback,
  turnRecoveryPrompt,
  turnRecoveryResumeButton,
} from "./dom.js";
import {
  CANONICAL_V4_UUID,
  MAX_THREAD_NAME_BYTES,
  hasExactKeys,
  isNonNegativeInteger,
  isParseableTimestamp,
  isRecord,
  isSafeAgentMode,
  isSafeProtocolString,
  isSessionChangedPayload,
  isValidChatText,
  isValidThreadName,
  parseDesktopBridgeResult,
  utf8ByteLength,
} from "./protocol.js";
import { selectThread, stashComposerDraft } from "./transcript.js";
import { renderQuickSwitcher, renderThreads } from "./threads.js";
import { renderTaskContext } from "./context-panel.js";
import {
  SessionChangedError,
  ThreadCreateFailure,
  activeLocalAccount,
  defaultLocalAccountLabel,
  desktopAgentBridge,
  desktopAgentCreateBridge,
  handleSessionChanged,
  invalidateActiveTurn,
  renderComposerChips,
  renderLocalAccountArea,
  setStatus,
  state,
} from "./renderer.js";

// The name the form starts with, and the name a home send falls back to
// when the prompt will not make a legal title. "Untitled presentation" was
// a leftover from the PPT-only years: every major desktop agent just says
// New chat and lets the first sentence rename the row. A catalog key —
// every use site wraps it in t(), because the name is data the sidecar
// stores, and it should be stored in the language the window spoke when
// the thread was born.
const DEFAULT_NEW_THREAD_NAME = "New chat";

function parseCreatedThread(value, expectedDraft, created) {
  if (
    !hasExactKeys(value, [
      "uuid",
      "name",
      "agent_mode",
      "message_count",
      "updated_at",
      "cloud_sync_state",
    ]) ||
    value.uuid !== expectedDraft.threadUUID ||
    (created && value.name !== expectedDraft.name) ||
    (created && value.agent_mode !== expectedDraft.agentMode) ||
    !isValidThreadName(value.name) ||
    !isSafeAgentMode(value.agent_mode) ||
    !state.allowedModes.includes(value.agent_mode) ||
    !isNonNegativeInteger(value.message_count) ||
    !isParseableTimestamp(value.updated_at) ||
    // "local" = the signed-out local create branch (L3d). Two other copies
    // of this set (both in the bridge lib) lagged the same way and silently
    // broke every local create — keep all three in step.
    (value.cloud_sync_state !== "synced" &&
      value.cloud_sync_state !== "paused" &&
      value.cloud_sync_state !== "local")
  ) {
    throw new Error("Malformed agent create thread result");
  }
  return {
    uuid: value.uuid,
    name: value.name,
    agent_mode: value.agent_mode,
    message_count: value.message_count,
    updated_at: value.updated_at,
    cloud_sync_state: value.cloud_sync_state,
  };
}

function parseCreateThreadData(value, status, expectedDraft) {
  if (
    hasExactKeys(value, ["state", "created", "thread"]) &&
    value.state === "ready" &&
    typeof value.created === "boolean" &&
    status === (value.created ? 201 : 200)
  ) {
    return {
      state: "ready",
      created: value.created,
      thread: parseCreatedThread(value.thread, expectedDraft, value.created),
    };
  }
  if (
    status === 202 &&
    hasExactKeys(value, ["state", "thread_uuid"]) &&
    value.state === "pending_local_sync" &&
    value.thread_uuid === expectedDraft.threadUUID
  ) {
    return {
      state: "pending_local_sync",
      threadUUID: value.thread_uuid,
    };
  }
  throw new Error("Malformed agent create thread result");
}

// uploadThreadFile uploads one file to the selected thread via the typed bridge
// and tracks it as a pending attachment (chip). Only "ready" attachments are
// sent with the next turn (fileIDs); "uploading" is excluded, "error" flagged.
//
// Each upload carries its own fence — the entry object itself plus the session
// generation — NOT a shared counter. A shared counter meant that attaching
// several files at once invalidated every in-flight upload except the last:
// their completions were dropped and the chips sat on "uploading" forever.
export function uploadThreadFile(file) {
  const agent = desktopAgentBridge();
  if (!agent) {
    setStatus(t("File upload unavailable"), "error");
    return;
  }
  const threadUUID = state.selectedThreadUUID;
  if (!threadUUID) {
    return;
  }
  const entry = {
    id: 0,
    name: file.name,
    size: file.size,
    status: "uploading",
    // Kept for retry: a failed chip re-runs the same file against the thread
    // it was originally attached to, even if the selection moved on.
    threadUUID,
    file,
  };
  state.pendingFiles.push(entry);
  renderAttachments();
  startPendingFileUpload(agent, entry);
}

// startPendingFileUpload runs (or re-runs) one tray entry's upload. The
// completion is fenced per upload: it applies only while the entry is still in
// the tray (not removed, not consumed by a sent turn) and the session has not
// changed — the same condition isCurrentSelection expresses for turns, scoped
// to what an upload can actually outlive.
function startPendingFileUpload(agent, entry) {
  const sessionGeneration = fences.session.snapshot();
  const isLive = () =>
    fences.session.isCurrent(sessionGeneration) &&
    state.pendingFiles.includes(entry);
  agent
    .uploadThreadFile(entry.threadUUID, entry.file)
    .then((result) => {
      if (!isLive()) return;
      if (
        isRecord(result) &&
        result.ok &&
        result.data &&
        typeof result.data.file_id === "number"
      ) {
        entry.id = result.data.file_id;
        entry.status = "ready";
      } else {
        entry.status = "error";
      }
      renderAttachments();
    })
    .catch(() => {
      // Without this, a rejected bridge call became an unhandled rejection
      // and the chip froze on "uploading". A failed upload is a failed
      // attachment: mark it and let the chip offer retry or removal.
      if (!isLive()) return;
      entry.status = "error";
      renderAttachments();
    });
}

function retryPendingFileUpload(entry) {
  const agent = desktopAgentBridge();
  if (!agent) {
    setStatus(t("File upload unavailable"), "error");
    return;
  }
  if (!state.pendingFiles.includes(entry) || entry.status !== "error") return;
  entry.status = "uploading";
  renderAttachments();
  startPendingFileUpload(agent, entry);
}

function removePendingFile(entry) {
  state.pendingFiles = state.pendingFiles.filter((file) => file !== entry);
  renderAttachments();
}

export function renderAttachments() {
  if (!attachmentChips) return;
  attachmentChips.textContent = "";
  for (const file of state.pendingFiles) {
    const chip = document.createElement("span");
    chip.className = `attachment-chip ${file.status}`;
    const label = document.createElement("span");
    label.className = "attachment-chip-name";
    label.textContent =
      file.status === "uploading"
        ? `${file.name}…`
        : file.status === "error"
          ? `${file.name} ✗`
          : file.name;
    chip.appendChild(label);
    if (file.status === "error") {
      // A failed upload is actionable, not terminal: run the same file again,
      // or take the chip out of the tray.
      const retry = document.createElement("button");
      retry.type = "button";
      retry.className = "attachment-chip-retry";
      retry.textContent = t("Retry");
      retry.addEventListener("click", () => {
        retryPendingFileUpload(file);
      });
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "attachment-chip-remove";
      remove.textContent = t("Remove");
      remove.addEventListener("click", () => {
        removePendingFile(file);
      });
      chip.append(retry, remove);
    }
    attachmentChips.appendChild(chip);
  }
  attachmentChips.hidden = state.pendingFiles.length === 0;
  // The composer tray and the Sources panel show the same files; refreshing
  // here keeps them from disagreeing after an upload completes or a turn
  // clears the tray.
  renderTaskContext();
}

export function selectedRecoverableTurn() {
  return state.recoverableTurns.find(
    (turn) => turn.thread_uuid === state.selectedThreadUUID
  ) || null;
}

export function removeRecoverableTurn(turnUUID) {
  state.recoverableTurns = state.recoverableTurns.filter(
    (turn) => turn.turn_uuid !== turnUUID
  );
  state.recoveryFeedback = "";
  state.recoveryFeedbackKind = "default";
  renderThreads();
  updateRecoveryState();
}

function recoveryPromptPreview(value) {
  const characters = Array.from(value);
  const preview = characters.slice(0, 120).join("");
  return t("Prompt: {preview}", {
    preview: `${preview}${characters.length > 120 ? "…" : ""}`,
  });
}

export function updateRecoveryState() {
  const recoverable = selectedRecoverableTurn();
  const visible = recoverable !== null;
  const busy =
    state.recoveryLoading || state.resumingTurn || state.dismissingRecovery;
  turnRecoveryCard.hidden = !visible;
  turnRecoveryCard.setAttribute("aria-busy", String(visible && busy));
  turnRecoveryResumeButton.disabled =
    !visible ||
    busy ||
    state.activeTurn !== null ||
    state.recoveringSession;
  turnRecoveryDismissButton.disabled =
    !visible || busy || state.activeTurn !== null || state.recoveringSession;
  turnRecoveryResumeButton.textContent = state.resumingTurn
    ? t("Resuming...")
    : state.recoveryLoading
      ? t("Checking...")
      : t("Resume");
  turnRecoveryDismissButton.textContent = state.dismissingRecovery
    ? t("Dismissing...")
    : t("Dismiss");
  turnRecoveryDescription.textContent = state.resumingTurn
    ? t("Retrying the interrupted request safely.")
    : t("Retry this interrupted response using the same request.");
  turnRecoveryPrompt.textContent = recoverable
    ? recoveryPromptPreview(recoverable.user_text)
    : "";
  turnRecoveryFeedback.textContent = state.recoveryFeedback;
  turnRecoveryFeedback.classList.toggle(
    "error",
    state.recoveryFeedbackKind === "error"
  );
}

export function updateComposerState() {
  renderLocalAccountArea();
  renderComposerChips();
  // An open palette shows availability-gated commands; when availability
  // changes under it (skills finish loading, a turn starts), the list must
  // follow — otherwise it either hides a now-possible action or offers a
  // now-impossible one.
  const palette = document.querySelector("#quick-switcher");
  if (palette && !palette.hidden) renderQuickSwitcher();
  const canSend = canSendTurn();
  const hasThread = Boolean(state.selectedThreadUUID);
  const hasMode = state.allowedModes.includes(state.selectedMode);
  const active = state.activeTurn !== null;
  const cancelConfirmationPending = state.cancelConfirmationTurnID !== null;
  const recoverable = selectedRecoverableTurn();
  const stage = document.querySelector("#main-stage");
  if (stage) stage.dataset.surface = hasThread ? "thread" : "home";
  // The composer is a sibling of the thread panel so it can sit on the home
  // screen. Hide it only when there is nowhere a prompt could go — no
  // conversation open, and no model plus create path to start one.
  const showComposer =
    hasThread ||
    (canSend && state.createAvailable && state.agentAvailable);
  if (chatForm) chatForm.hidden = !showComposer;
  const homeReady =
    !hasThread &&
    canSend &&
    canOpenNewThread() &&
    hasMode &&
    !state.recoveryLoading &&
    !cancelConfirmationPending &&
    !state.creatingThread;
  const threadReady =
    canSend &&
    hasThread &&
    state.agentAvailable &&
    !state.skillsLoading &&
    !state.recoveryLoading &&
    hasMode &&
    !active &&
    !cancelConfirmationPending &&
    !recoverable &&
    !state.createFormOpen &&
    !state.creatingThread &&
    !state.recoveringSession;
  const ready = threadReady || homeReady;
  const wasDisabled = chatInput.disabled;
  agentMode.disabled = !ready;
  // One allowed skill is not a choice; the composer status already names it.
  // The selector earns its pixels back the day a second skill ships.
  const singleSkill = state.allowedModes.length <= 1;
  agentMode.hidden = singleSkill;
  const modeLabel = document.querySelector("#agent-mode-label");
  if (modeLabel) modeLabel.hidden = singleSkill;
  // A one-option skill is not a choice, and a chip that says PPT on every
  // turn teaches the reader to stop reading the row. The selector comes
  // back the day a second skill ships; until then the row is model + Ask.
  const modeChip = document.querySelector("#mode-chip");
  if (modeChip) modeChip.hidden = true;
  chatInput.disabled = !ready;
  sendButton.disabled = !ready || !isValidChatText(chatInput.value);
  stopButton.hidden = !active;
  stopButton.disabled = !active || state.activeTurn?.stopRequested === true;
  if (attachButton) attachButton.disabled = !ready;
  // The composer just became a place you can type — focus it, the way
  // every agent home does after it finishes booting. Do not steal focus
  // from a dialog or the palette, and do not yank it on every later
  // update: only the locked → open edge.
  if (
    wasDisabled &&
    !chatInput.disabled &&
    !hasThread &&
    document.querySelector("#settings-overlay")?.hidden !== false &&
    document.querySelector("#quick-switcher")?.hidden !== false
  ) {
    chatInput.focus();
  }

  if (state.recoveringSession) {
    composerStatus.textContent = t("Your signed-in account changed. Select a thread again.");
  } else if (!canSend) {
    // Not a locked door: everything else on screen already works. What is
    // missing is a model, and both ways of getting one are named.
    composerStatus.textContent = hasThread
      ? t("This conversation is yours and stays here. To send a prompt, connect a WorkMax account or set a local model under Models.")
      : t("Browse and organize freely. To send a prompt, connect a WorkMax account or set a local model under Models.");
  } else if (!hasThread && ready) {
    // Home is ready to type. The chips already name the model; a second
    // sentence here was the old "select a thread" lobby talking over the box.
    composerStatus.textContent = "";
  } else if (!hasThread) {
    composerStatus.textContent = isLocalOnlySession()
      ? t("Starting a conversation keeps it on this machine.")
      : "";
  } else if (!state.agentAvailable) {
    composerStatus.textContent = t("Agent streaming is unavailable in this Desktop build.");
  } else if (state.skillsLoading) {
    composerStatus.textContent = t("Loading available skills...");
  } else if (state.recoveryLoading) {
    composerStatus.textContent = t("Checking interrupted responses...");
  } else if (!hasMode) {
    composerStatus.textContent = t("No Desktop skill is currently available.");
  } else if (recoverable) {
    composerStatus.textContent = state.resumingTurn
      ? t("Resuming the interrupted response...")
      : t("Resume or dismiss the interrupted response before sending another prompt.");
  } else if (cancelConfirmationPending) {
    composerStatus.textContent = t("Confirming persistent cancellation...");
  } else if (active) {
    composerStatus.textContent = state.activeTurn.stopRequested
      ? t("Stopping this turn...")
      : t("WorkMax Agent is responding...");
  } else if (isLocalOnlySession() && !state.toolLoop) {
    // The one ready-state sentence that is not furniture: the local route
    // falls back to chat without saying so at the dispatch, so the composer
    // has to. Tools-on and cloud say nothing — the chips already named them.
    composerStatus.textContent = t("Chat only — this local route has no tools.");
  } else if (state.skillsDegraded) {
    composerStatus.textContent = t("Live skill details are offline.");
  } else {
    composerStatus.textContent = "";
  }
  updateRecoveryState();
  updateNewThreadState();
}

// Two levels, because there are two different questions.
//
// canSendTurn is "does a prompt have anywhere to go" — a connected account or
// a configured local model. It is a fact about MODELS, and there is no way to
// be polite around it: with neither, a turn cannot run.
//
// The workbench is the other level, and it has no predicate any more because
// the answer is always yes. The sidecar always resolves an identity (this
// machine's local account when no account is connected), so history, drafts,
// settings and account switching are always available. They used to hang off
// canUseAgent, which meant a first-run user with no local model configured
// could not even see their own conversations — the app locked the door to a
// room that was already theirs.
export function canSendTurn() {
  return state.auth?.state === "authenticated" || state.localRoute;
}

// Working as this machine's own identity: no cloud account connected. Worth
// naming because several messages have to say something different here —
// "sign in" is not the answer to anything in this state.
export function isLocalIdentity() {
  return state.auth?.state !== "authenticated";
}

// Local identity AND a local model: the fully offline configuration, where
// the composer can promise that nothing leaves the machine.
export function isLocalOnlySession() {
  return state.localRoute && isLocalIdentity();
}

function canGenerateThreadUUID() {
  return typeof globalThis.crypto?.randomUUID === "function";
}

function generateThreadUUID() {
  if (!canGenerateThreadUUID()) {
    throw new Error("Secure thread identity generation is unavailable.");
  }
  const value = globalThis.crypto.randomUUID();
  if (!CANONICAL_V4_UUID.test(value)) {
    throw new Error("Secure thread identity generation returned an invalid value.");
  }
  return value;
}

// Starting a conversation is workbench work, not model work: the sidecar
// creates it under this machine's identity when no account is connected. You
// can gather sources and name the thing before deciding where the model runs.
export function canOpenNewThread() {
  return (
    state.agentAvailable &&
    state.createAvailable &&
    canGenerateThreadUUID() &&
    !state.skillsLoading &&
    state.allowedModes.length > 0 &&
    !state.activeTurn &&
    !state.creatingThread &&
    !state.recoveringSession &&
    !state.createFormOpen
  );
}

// What a first-time user can actually do here. Three honest starters — each
// is a prompt this agent can genuinely act on, not a leftover from the
// years when the only skill was a slide deck. Clicking one plants the
// words in the home composer; sending is still the user's decision, the
// way every major agent client treats a suggestion chip.
//
// Both halves are catalog keys, translated at render time — the prompt too,
// even though it is MODEL INPUT, not chrome: the system prompt says to reply
// in the language the user writes in, so a zh window planting a zh prompt is
// what makes the answer come back in the window's language.
const STARTER_PROMPTS = [
  {
    title: "Review what I have",
    prompt: "Review the files I attach. Tell me what is wrong, what is missing, and what to do first.",
  },
  {
    title: "Plan the next step",
    prompt: "Help me plan the next step. Ask me the two questions that would change the plan.",
  },
  {
    title: "Explain this simply",
    prompt: "Explain what I have here in plain language, then list the risks I should not ignore.",
  },
];

export function buildStarterCards() {
  const container = document.querySelector("#starter-prompts");
  if (!container) return;
  for (const starter of STARTER_PROMPTS) {
    const card = document.createElement("button");
    card.type = "button";
    card.className = "starter-card";
    // No glyph and no tone colour. Three cards each wearing a different
    // accent — one blue, one violet, one green — said nothing about the three
    // prompts and everything about a template. The prompt is the card.
    const title = document.createElement("strong");
    title.textContent = t(starter.title);
    const preview = document.createElement("span");
    preview.className = "starter-preview";
    preview.textContent = t(starter.prompt);
    card.append(title, preview);
    card.addEventListener("click", () => {
      showHome();
      chatInput.value = t(starter.prompt);
      state.homeDraft = t(starter.prompt);
      updateComposerState();
      if (!chatInput.disabled) chatInput.focus();
    });
    container.appendChild(card);
  }
}

export function renderEmptyState() {
  const canSend = canSendTurn();
  // First run: the question is NOT "who are you" — the machine already knows,
  // and the sidecar has already resolved that identity. The only open question
  // is where the model runs, and the two answers are equal citizens.
  const onboarding = document.querySelector("#onboarding-paths");
  if (onboarding) onboarding.hidden = canSend;
  if (!canSend) {
    const account = activeLocalAccount();
    emptyTitle.textContent =
      account && account.name !== defaultLocalAccountLabel
        ? t("You're working as {name}", { name: account.name })
        : t("You're working on this machine");
    emptyDescription.hidden = false;
    emptyDescription.textContent =
      t("Your conversations and files are already here and already yours. One thing left: where should the model run?");
  } else {
    // One question, Codex-style: the app is usable, so the headline invites
    // work instead of describing machinery. The identity joins the question
    // when it is a real name — "What should we make, Local?" is nobody, and
    // "Local" is exactly the placeholder the sidecar falls back to.
    const account = isLocalOnlySession() ? activeLocalAccount() : null;
    emptyTitle.textContent =
      account && account.name !== defaultLocalAccountLabel
        ? t("What should we make, {name}?", { name: account.name })
        : t("What should we make today?");
    // The composer under the greeting is the next sentence. A subtitle that
    // says "pick a conversation or start fresh" is the lobby talking over it.
    // Keep a line only when there is no create path — then the box cannot
    // start anything, and the reader needs to be told.
    if (state.threads.length === 0 && !state.createAvailable) {
      emptyDescription.hidden = false;
      emptyDescription.textContent =
        t("This Desktop build can continue existing threads after they appear in local history.");
    } else {
      emptyDescription.hidden = true;
      emptyDescription.textContent = "";
    }
  }
  // The empty-state button is the onboarding-only door: once a model exists
  // the composer is how a conversation starts, and a second primary next to
  // it is the old lobby.
  emptyNewThreadButton.hidden = canSend || !state.createAvailable;
  const starters = document.querySelector("#starter-prompts");
  if (starters) {
    // The starters promise a turn. Offering them before there is a model to
    // run one would be a card that fills the composer and then cannot send it.
    starters.hidden = !canSend || !state.createAvailable;
  }
}

export function setCreateFeedback(message, kind = "error") {
  newThreadError.textContent = message;
  newThreadError.hidden = message === "";
  newThreadError.classList.toggle("pending", kind === "pending");
}

export function hasAttemptedCreateDraft() {
  return state.createFormOpen && state.createDraft?.attempted === true;
}

function createFailureCode(value) {
  if (
    !isRecord(value) ||
    typeof value.error !== "string" ||
    !isSafeProtocolString(value.error, 128)
  ) {
    return "";
  }
  return value.error;
}

function classifyCreateThreadFailure(result) {
  const code = createFailureCode(result.error);
  if (result.status === 401 || code === "authentication_required") {
    return new ThreadCreateFailure(
      t("Authentication is required. Cancel this draft, then sign in again."),
      t("Authentication is required before creating a thread."),
      false
    );
  }
  if (code === "thread_uuid_conflict") {
    return new ThreadCreateFailure(
      t("This thread identity is already owned elsewhere. Cancel and start a new draft."),
      t("The generated thread identity cannot be used."),
      false
    );
  }
  if (code === "local_identity_conflict") {
    return new ThreadCreateFailure(
      t("Local history has an identity conflict. Cancel this draft before refreshing."),
      t("Local history could not safely accept this thread."),
      false
    );
  }
  if (
    result.status >= 500 &&
    result.status <= 599 &&
    isRecord(result.error) &&
    result.error.retry_with_same_uuid === true
  ) {
    return new ThreadCreateFailure(
      t("This thread could not be completed. Retry keeps the same identity."),
      t("The presentation thread could not be completed."),
      true
    );
  }
  return new ThreadCreateFailure(
    t("Thread creation was rejected. Cancel this draft before continuing."),
    t("The presentation thread cannot be retried."),
    false
  );
}

export function updateNewThreadState() {
  // No model predicate here, and that is deliberate: the form and the button
  // that opens it must obey ONE condition. They disagreed once already (the
  // form opened, the submit stayed disabled forever — the packaged-app smoke
  // caught it), and the fix is not a second copy of the same test.
  const hasMode = state.allowedModes.includes(newThreadMode.value);
  const attempted = state.createDraft?.attempted === true;
  const pending = state.createDraft?.pending === true;
  const retryable = state.createDraft?.retryable === true;
  const validName = isValidThreadName(newThreadName.value.trim());
  const canSubmit =
    state.createFormOpen &&
    state.agentAvailable &&
    state.createAvailable &&
    !state.skillsLoading &&
    hasMode &&
    validName &&
    (!attempted || retryable) &&
    !state.activeTurn &&
    !state.creatingThread &&
    !state.recoveringSession;

  newThreadButton.disabled = !canOpenNewThread();
  emptyNewThreadButton.disabled = !canOpenNewThread();
  // No refresh control to disable here any more: refresh() still refuses
  // while an attempted draft is open — hasAttemptedCreateDraft() is checked
  // inside it, and says so on the status line — but the guard no longer has
  // a button to grey out.
  newThreadButton.title = state.createAvailable
    ? t("New chat")
    : t("Thread creation is unavailable in this Desktop build");
  newThreadForm.hidden = !state.createFormOpen;
  newThreadForm.setAttribute("aria-busy", String(state.creatingThread));
  newThreadName.disabled = state.creatingThread || attempted;
  newThreadMode.disabled = state.creatingThread || attempted || state.allowedModes.length === 0;
  newThreadSubmitButton.disabled = !canSubmit;
  newThreadCancelButton.disabled = false;
  newThreadSubmitButton.textContent = state.creatingThread
    ? t("Creating...")
    : pending
      ? t("Retry sync")
      : attempted && retryable
        ? t("Retry")
        : attempted
          ? t("Cannot retry")
        : t("Create");
  renderEmptyState();
}

// The first line of a prompt, cut to a legal thread name. Used when a home
// send creates the conversation — ChatGPT / Claude / Codex all name the
// row from what you typed, not from a form you filled first.
export function titleFromPrompt(text) {
  const line = String(text || "").split(/\r?\n/)[0].trim().replace(/\s+/g, " ");
  if (!line) return t(DEFAULT_NEW_THREAD_NAME);
  if (isValidThreadName(line)) return line;
  let cut = line;
  while (cut && utf8ByteLength(cut) > MAX_THREAD_NAME_BYTES) {
    cut = cut.slice(0, -1);
  }
  cut = cut.trim();
  return isValidThreadName(cut) ? cut : t(DEFAULT_NEW_THREAD_NAME);
}

// Leave the open conversation and put the composer on the home screen.
// Already home: just focus the box. Do not wipe what the user has typed —
// New chat from a blank home is a no-op, not a trapdoor under the words.
export function showHome() {
  if (!state.selectedThreadUUID && !state.createFormOpen) {
    updateComposerState();
    if (!chatInput.disabled) chatInput.focus();
    return;
  }
  if (state.createFormOpen) {
    if (state.createDraft?.attempted) {
      const retryable = state.createDraft.retryable === true;
      setStatus(
        retryable
          ? t("Retry or cancel the current thread draft before switching.")
          : t("Cancel the current thread draft before switching."),
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
  stashHomeOrThreadDraft();
  fences.selection.bump();
  state.selectedThreadUUID = null;
  chatInput.value = state.homeDraft || "";
  emptyState.hidden = false;
  threadPanel.hidden = true;
  renderThreads();
  updateComposerState();
  if (!chatInput.disabled) chatInput.focus();
}

export function stashHomeOrThreadDraft() {
  if (state.selectedThreadUUID) {
    stashComposerDraft();
    return;
  }
  state.homeDraft = chatInput.value;
}

// New chat: if a model can answer, go home and type. If it cannot, the
// form is still how a conversation is gathered before a model exists —
// starting one is workbench work, not model work.
export function handleNewChat() {
  state.starterPrompt = null;
  if (canSendTurn()) {
    showHome();
    return;
  }
  openNewThreadForm();
}

export function openNewThreadForm() {
  if (!canOpenNewThread()) {
    updateNewThreadState();
    return;
  }
  let threadUUID;
  try {
    threadUUID = generateThreadUUID();
  } catch {
    setStatus(t("Secure thread identity generation is unavailable."), "error");
    updateNewThreadState();
    return;
  }
  const mode = state.allowedModes.includes(state.selectedMode)
    ? state.selectedMode
    : state.allowedModes[0];
  fences.create.bump();
  state.createFormOpen = true;
  state.createDraft = {
    threadUUID,
    name: t(DEFAULT_NEW_THREAD_NAME),
    agentMode: mode,
    attempted: false,
    pending: false,
    retryable: false,
  };
  newThreadName.value = state.createDraft.name;
  newThreadMode.value = mode;
  setCreateFeedback("");
  updateComposerState();
  newThreadName.focus();
  if (typeof newThreadName.select === "function") {
    newThreadName.select();
  }
}

export function cancelNewThreadDraft(restoreFocus = true) {
  // The starter's prompt belonged to the thread that was not created.
  state.starterPrompt = null;
  const wasAttempted = state.createDraft?.attempted === true;
  fences.create.bump();
  state.createFormOpen = false;
  state.creatingThread = false;
  state.createDraft = null;
  newThreadName.value = t(DEFAULT_NEW_THREAD_NAME);
  setCreateFeedback("");
  updateComposerState();
  if (!restoreFocus) return;
  setStatus(wasAttempted ? t("Thread creation canceled. A late result will be ignored.") : t("New chat canceled."));
  if (state.selectedThreadUUID && !chatInput.disabled) {
    chatInput.focus();
  } else {
    newThreadButton.focus();
  }
}

function isCurrentCreate(context) {
  return (
    fences.session.isCurrent(context.sessionGeneration) &&
    fences.create.isCurrent(context.createGeneration) &&
    context.threadUUID === state.createDraft?.threadUUID
  );
}

function upsertCreatedThread(thread) {
  state.threads = [
    thread,
    ...state.threads.filter((candidate) => candidate.uuid !== thread.uuid),
  ].sort((left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at));
}

export async function submitNewThread(event) {
  event.preventDefault();
  if (state.creatingThread || !state.createFormOpen || !state.createDraft) {
    return;
  }
  const agent = desktopAgentCreateBridge();
  const name = newThreadName.value.trim();
  const mode = newThreadMode.value;
  if (
    !agent ||
    !state.agentAvailable ||
    state.skillsLoading ||
    !isValidThreadName(name) ||
    !state.allowedModes.includes(mode) ||
    state.activeTurn ||
    state.recoveringSession
  ) {
    setCreateFeedback(t("Enter a valid name and choose an available skill."));
    updateNewThreadState();
    return;
  }
  if (state.createDraft.attempted && state.createDraft.retryable !== true) {
    updateNewThreadState();
    return;
  }

  if (!state.createDraft.attempted) {
    state.createDraft = {
      ...state.createDraft,
      name,
      agentMode: mode,
      attempted: true,
      pending: false,
      retryable: false,
    };
    newThreadName.value = name;
  }
  await executeCreateDraft({ skipLoad: false });
}

// A home send creates the conversation from the prompt, then the caller
// starts the turn. Failure opens the existing retry form so the UUID
// contract stays reachable; the happy path never shows that form.
export async function createThreadForHomeSend(userText) {
  if (state.creatingThread || state.activeTurn || state.recoveringSession) {
    return false;
  }
  if (state.createFormOpen && state.createDraft?.attempted) {
    return false;
  }
  if (state.createFormOpen) {
    cancelNewThreadDraft(false);
  }
  let threadUUID;
  try {
    threadUUID = generateThreadUUID();
  } catch {
    setStatus(t("Secure thread identity generation is unavailable."), "error");
    return false;
  }
  const mode = state.allowedModes.includes(state.selectedMode)
    ? state.selectedMode
    : state.allowedModes[0];
  if (!mode || !state.allowedModes.includes(mode)) {
    setStatus(t("No Desktop skill is currently available."), "error");
    return false;
  }
  fences.create.bump();
  state.createFormOpen = false;
  state.createDraft = {
    threadUUID,
    name: titleFromPrompt(userText),
    agentMode: mode,
    attempted: true,
    pending: false,
    retryable: false,
  };
  return executeCreateDraft({ skipLoad: true, revealFormOnFailure: true });
}

async function executeCreateDraft({ skipLoad = false, revealFormOnFailure = false } = {}) {
  const agent = desktopAgentCreateBridge();
  if (!agent || !state.createDraft) {
    return false;
  }
  const draft = {
    threadUUID: state.createDraft.threadUUID,
    name: state.createDraft.name,
    agentMode: state.createDraft.agentMode,
  };
  fences.create.bump();
  const context = {
    sessionGeneration: fences.session.snapshot(),
    createGeneration: fences.create.snapshot(),
    threadUUID: draft.threadUUID,
  };
  state.creatingThread = true;
  setCreateFeedback("");
  setStatus(t("Starting a conversation..."));
  updateComposerState();

  try {
    const result = parseDesktopBridgeResult(
      await agent.createThread(draft),
      "agent create thread result"
    );
    if (!isCurrentCreate(context)) return false;
    if (!result.ok) {
      if (result.status === 409 && isSessionChangedPayload(result.error)) {
        throw new SessionChangedError();
      }
      throw classifyCreateThreadFailure(result);
    }
    const data = parseCreateThreadData(result.data, result.status, draft);
    if (!isCurrentCreate(context)) return false;
    if (data.state === "pending_local_sync") {
      state.createDraft = {
        ...state.createDraft,
        pending: true,
        retryable: true,
      };
      if (revealFormOnFailure) state.createFormOpen = true;
      setCreateFeedback(
        t("The cloud thread is ready. Retry sync with the same identity, or cancel this draft."),
        "pending"
      );
      setStatus(t("Thread created. Local history is still syncing."));
      return false;
    }
    if (data.thread.cloud_sync_state === "paused") {
      throw new ThreadCreateFailure(
        t("This existing thread is paused and cannot be continued here. Cancel this draft before continuing."),
        t("The existing conversation is paused."),
        false
      );
    }

    upsertCreatedThread(data.thread);
    state.creatingThread = false;
    state.createFormOpen = false;
    state.createDraft = null;
    setCreateFeedback("");
    renderThreads();
    selectThread(data.thread, { skipLoad });
    if (state.starterPrompt) {
      // Kept for the form path: a test or the onboarding button can still
      // plant a prompt after a named create. Home starters write the box
      // directly and never set this.
      chatInput.value = state.starterPrompt;
      state.starterPrompt = null;
    }
    if (!skipLoad) chatInput.focus();
    updateComposerState();
    setStatus(data.created ? t("Thread created. Ready for the first prompt.") : t("Thread recovered. Ready for the first prompt."));
    return true;
  } catch (error) {
    if (error instanceof SessionChangedError) {
      await handleSessionChanged();
      return false;
    }
    if (!isCurrentCreate(context)) return false;
    const failure = error instanceof ThreadCreateFailure
      ? error
      : new ThreadCreateFailure(
          t("This thread could not be completed. Retry keeps the same identity, or cancel this draft."),
          t("The conversation could not be started."),
          true
        );
    state.createDraft = {
      ...state.createDraft,
      pending: false,
      retryable: failure.retryable,
    };
    if (revealFormOnFailure) {
      state.createFormOpen = true;
      newThreadName.value = draft.name;
      newThreadMode.value = draft.agentMode;
    }
    setCreateFeedback(failure.feedback);
    setStatus(failure.statusMessage, "error");
    return false;
  } finally {
    if (isCurrentCreate(context)) {
      state.creatingThread = false;
      updateComposerState();
    }
  }
}

// Files arrive however the user's hands bring them: the picker, a drop onto
// the conversation, or a paste. All three land in the same upload path, so
// the chips, the Sources panel, and the send-time union behave identically.
export function attachDroppedFiles(files) {
  const list = Array.from(files || []);
  if (list.length === 0) return;
  if (state.selectedThreadUUID) {
    for (const file of list) uploadThreadFile(file);
    return;
  }
  void attachFilesOnHome(list);
}

function titleFromAttachment(file) {
  const raw = String(file?.name || "").trim();
  if (!raw) return t(DEFAULT_NEW_THREAD_NAME);
  const dot = raw.lastIndexOf(".");
  const base = dot > 0 ? raw.slice(0, dot) : raw;
  return titleFromPrompt(base || raw);
}

// ChatGPT and Claude let you drop a file on the blank screen. The
// conversation is created from what you already typed, or from the file
// name if the box is empty, and the upload follows.
async function attachFilesOnHome(files) {
  if (!canSendTurn() || !canOpenNewThread()) return;
  const named = chatInput.value.trim() || titleFromAttachment(files[0]);
  const created = await createThreadForHomeSend(named);
  if (!created || !state.selectedThreadUUID) return;
  for (const file of files) uploadThreadFile(file);
}

export function canStageAttachments() {
  if (state.activeTurn || state.recoveringSession || state.creatingThread) {
    return false;
  }
  if (state.selectedThreadUUID) return true;
  return canSendTurn() && canOpenNewThread();
}

export function handleAttachmentDragOver(event, el) {
  if (!event.dataTransfer || !el) return;
  event.preventDefault();
  if (canStageAttachments()) el.classList.add("drop-target");
}

export function handleAttachmentDragLeave(el) {
  if (el) el.classList.remove("drop-target");
}

export function handleAttachmentDrop(event, el) {
  if (!el) return;
  event.preventDefault();
  el.classList.remove("drop-target");
  attachDroppedFiles(event.dataTransfer?.files);
}

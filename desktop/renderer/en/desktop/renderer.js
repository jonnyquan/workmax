// The renderer's entry module: shared state, the boot sequence, and the
// wiring that connects the DOM to the modules that own each surface.
//
// Loaded as `<script type="module">`, which the UI origin's CSP allows under
// script-src 'self' — verified inside a real WKWebView against the production
// header, not assumed. Everything below either belongs to no one surface
// (state, the bridge accessors, the session/turn lifecycle, login, refresh) or
// is the listener that hands an event to the module that does own it.
//
// The other modules import from here freely and this file imports from them:
// the cycles are real and they are fine, because every cross-module name is
// either a hoisted function declaration or read inside a callback that runs
// long after every module has been evaluated. The one rule that is NOT
// negotiable is that no module may assign to a binding it imported — see
// fence.js for how the counters that used to do exactly that are shared now.
import { fences } from "./fence.js";
import {
  agentMode,
  appearanceDarkButton,
  appearanceLightButton,
  appearanceSystemButton,
  attachButton,
  chatForm,
  chatInput,
  emptyNewThreadButton,
  emptyState,
  exportThreadButton,
  fileInput,
  localAccountAvatar,
  localAccountBindingState,
  localAccountConnectButton,
  localAccountCreateForm,
  localAccountDisconnectButton,
  localAccountHint,
  localAccountListEl,
  localAccountNameEl,
  localAccountNameInput,
  localAccountPanel,
  localAccountRow,
  localAccountSwitchNote,
  loginCancelButton,
  loginEmail,
  loginForm,
  loginPassword,
  loginSubmitButton,
  messageList,
  modelAPIKey,
  modelBaseURL,
  modelClearAPIKey,
  modelID,
  modelKeyStatus,
  modelLocalFields,
  modelOfficialFields,
  modelOfficialID,
  modelOfficialNote,
  modelPreferredRoute,
  modelProtocol,
  modelSettingsCancelButton,
  modelSettingsError,
  modelSettingsForm,
  modelSettingsSubmitButton,
  newThreadButton,
  newThreadCancelButton,
  newThreadForm,
  newThreadMode,
  newThreadName,
  newThreadSubmitButton,
  onboardingLocal,
  onboardingSignin,
  openWorkspaceButton,
  quickSwitcher,
  renameThreadButton,
  renameThreadCancel,
  renameThreadForm,
  runtimeLabel,
  settingsButton,
  settingsCloseButton,
  settingsOverlay,
  statusBar,
  statusCard,
  statusDismissButton,
  statusRetryButton,
  stopButton,
  threadPanel,
  threadSearchInput,
  threadTitle,
  turnRecoveryDismissButton,
  turnRecoveryResumeButton,
} from "./dom.js";
import {
  AUTH_POLL_INTERVAL_MS,
  AUTH_POLL_TIMEOUT_MS,
  LOGIN_ERROR_MESSAGES,
  MAX_CHAT_TEXT_BYTES,
  MAX_THREAD_NAME_BYTES,
  isRecord,
  isSessionChangedPayload,
  isValidChatText,
  parseAgentModes,
  parseAuthStatus,
  parseCloudBinding,
  parseDesktopBridgeResult,
  parseLocalAccounts,
  parseLoginTransactionResult,
  parseModelRouteSettings,
  parseRecoverableTurns,
  parseSkillsCatalog,
  parseThreads,
  parseTurnCancelResult,
  parseTurnOpenResult,
  sanitizeErrorMessage,
  utf8ByteLength,
  validLoginCredential,
  validLoginEmail,
} from "./protocol.js";
import {
  clearPendingIndicator,
  drainStreamBatch,
  finalizeStreamedAssistant,
  handleParsedTurnEvent,
  handleRawTurnEvent,
  parseAgentTurnEvent,
} from "./events.js";
import {
  appendOptimisticTurn,
  appendRecoveredAssistant,
  attachMessageActions,
  failTurnOpen,
  formatTurnDuration,
  isCurrentSelection,
  loadMessagesForSelection,
  selectionContext,
  setTurnState,
  stashComposerDraft,
  updateSelectedThreadHeading,
} from "./transcript.js";
import {
  attachDroppedFiles,
  buildStarterCards,
  canSendTurn,
  cancelNewThreadDraft,
  hasAttemptedCreateDraft,
  isLocalIdentity,
  isLocalOnlySession,
  openNewThreadForm,
  removeRecoverableTurn,
  renderAttachments,
  renderEmptyState,
  selectedRecoverableTurn,
  setCreateFeedback,
  submitNewThread,
  updateComposerState,
  updateNewThreadState,
  updateRecoveryState,
  uploadThreadFile,
} from "./composer.js";
import {
  DELETE_ARM_MS,
  closeQuickSwitcher,
  openQuickSwitcher,
  renderThreads,
  scheduleContentSearch,
} from "./threads.js";
import {
  attachLastTurnLog,
  contextState,
  loadWorkspaceDeliverables,
  renderTaskContext,
  settleTurnNarration,
} from "./context-panel.js";

// --- Appearance --------------------------------------------------------------
//
// Three states: follow the system (the default), force light, force dark. The
// palette and the cascade that resolves the three live in styles.css; all this
// code does is put the attribute the cascade keys on onto <html>.
//
// The starting value is NOT read here — it is already on the page. The shell
// resolves the stored preference while it serves index.html and writes
// data-theme onto <html> itself (desktop/wails/uiserver.go, withAppearance),
// so the document is painted right the first time instead of being corrected
// a round trip later. All this file does on boot is agree with what it was
// handed, which is why the theme cannot flash: there is no moment at which the
// page has a different opinion from the document it arrived as.
//
// It used to be kept in localStorage, and that never once worked. localStorage
// is scoped to an origin — scheme, host AND port — and the UI origin binds an
// ephemeral port on every launch, so each start was a new origin reading an
// empty store. The preference was written faithfully and could never be read
// back. It lives in SQLite behind the sidecar now, like everything else this
// app remembers, and the renderer holds no storage of its own at all.
export const THEME_CHOICES = ["system", "light", "dark"];
export const THEME_LABELS = {
  system: "match system",
  light: "light",
  dark: "dark",
};
export const THEME_HINTS = {
  system: "Follow the desktop appearance",
  light: "Always light, whatever the system does",
  dark: "Always dark, whatever the system does",
};
export let themeChoice = "system";

// What the document already says. Anything unrecognised is treated as "no
// opinion" rather than trusted onto the page a second time.
function readAppliedTheme() {
  const root = document.documentElement;
  const applied = root ? root.getAttribute("data-theme") : null;
  return THEME_CHOICES.includes(applied) ? applied : "system";
}

function applyTheme(choice) {
  themeChoice = THEME_CHOICES.includes(choice) ? choice : "system";
  const root = document.documentElement;
  if (!root) return;
  // "system" removes the attribute rather than writing data-theme="system":
  // the media query already IS the system answer, and an attribute meaning "no
  // opinion" would have to be excluded by hand from every guard in the cascade.
  if (themeChoice === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", themeChoice);
}

export function setTheme(choice) {
  applyTheme(choice);
  renderAppearanceChoice();
  void persistTheme(themeChoice);
}

// The window changes immediately and the write follows. Nothing waits on the
// sidecar to repaint: a theme that hesitates while a database is written would
// be a worse switch than the one that used to forget.
async function persistTheme(choice) {
  const settings = window.desktopBridge?.settings;
  // A shell too old to have the route is not an error the user can act on —
  // the app still switches, it just will not remember. Saying so on every
  // click would be noise about a build they are not running.
  if (!isRecord(settings) || typeof settings.putAppearance !== "function") return;
  try {
    const result = parseDesktopBridgeResult(
      await settings.putAppearance(choice),
      "settings.putAppearance"
    );
    if (!result.ok) throw new Error("appearance not saved");
  } catch {
    // A failure here is silent by nature — the window already looks right, and
    // the surprise lands on the next launch. Better to say it now.
    setStatus("Appearance changed for now, but it could not be saved.", "error");
  }
}

// The segmented control in Settings. It names all three states and marks the
// live one, rather than being a switch you press until the window looks right.
const APPEARANCE_BUTTONS = () => [
  ["system", appearanceSystemButton],
  ["light", appearanceLightButton],
  ["dark", appearanceDarkButton],
];

function renderAppearanceChoice() {
  for (const [choice, button] of APPEARANCE_BUTTONS()) {
    if (!button) continue;
    button.classList.toggle("active", choice === themeChoice);
    button.setAttribute("aria-pressed", choice === themeChoice ? "true" : "false");
  }
}

applyTheme(readAppliedTheme());

export const state = {
  auth: null,
  threads: [],
  selectedThreadUUID: null,
  skills: [],
  allowedModes: [],
  selectedMode: "",
  skillsLoading: false,
  skillsDegraded: false,
  agentAvailable: false,
  createAvailable: false,
  createFormOpen: false,
  creatingThread: false,
  createDraft: null,
  activeTurn: null,
  cancelConfirmationTurnID: null,
  recoverableTurns: [],
  recoveryLoading: false,
  resumingTurn: false,
  dismissingRecovery: false,
  recoveryFeedback: "",
  recoveryFeedbackKind: "default",
  recoveringSession: false,
  pendingFiles: [],
  threadQuery: "",
  // Half-written prompts, keyed by thread uuid. A thread switch or a refresh
  // stashes the composer here and restores it when the thread is selected
  // again; only a session change (a different signed-in account) clears it.
  // In-memory on purpose: drafts do not outlive the window.
  composerDrafts: new Map(),
  // A starter card's prompt, held while the new-thread form it opened is
  // completed. Consumed exactly once, into the composer of the thread it
  // created; cancelling the form drops it.
  starterPrompt: null,
  // True when a turn sent right now would run on the locally configured model.
  // That is the one condition under which the sidecar serves an unauthenticated
  // turn (localSingleUserUID, L3d/D2), so it is what decides whether this app
  // is usable without an account.
  localRoute: false,
  // Named local identities (登录这块的"本地账户"半边). Always loaded, signed in
  // or not: the active one is who this machine is, and it is what owns local
  // work whenever no cloud account is connected.
  localAccounts: [],
  // Whether this machine's identity currently has a WorkMax account connected
  // to it, and (masked) which one. A binding, not a login state: it grants
  // cloud routing and sync, and connecting or disconnecting moves no data.
  cloudBinding: { state: "unbound", user_id: "" },
  // True while a disconnect (logout) is in flight, so the button cannot be
  // pressed twice into two revocations of the same session.
  disconnecting: false,
  localAccountPanelOpen: false,
  // Whether the credential form is on screen. The connect control and the
  // form are the same invitation; only one of them is shown at a time.
  loginFormOpen: false,
  // The id being renamed inline, or null. While set, background repaints
  // (auth polling → updateComposerState) must NOT rebuild the account list —
  // a repaint that eats the user's half-typed name is data loss in miniature.
  localAccountRenamingID: null,
  // True when /agent/modes answered but this UI could not parse it — version
  // skew between renderer and sidecar. Surfaced instead of the generic
  // signed-out status, which would bury the real problem.
  modesParseSkew: false,
  // Whether a local turn right now runs the L2 tool loop or pure chat. The
  // dispatch falls back silently; the composer must not.
  toolLoop: false,
};

// The sidecar's LAST-RESORT name for this machine's identity — what it writes
// when the operating system will not say who is logged in. A real OS name is a
// real name and gets shown; this one is a placeholder, and repeating it next
// to a "Local" runtime chip would just say Local twice.
export const defaultLocalAccountLabel = "Local";

export class SessionChangedError extends Error {
  constructor() {
    super("The authenticated session changed");
    this.name = "SessionChangedError";
  }
}

export class ThreadCreateFailure extends Error {
  constructor(feedback, statusMessage, retryable) {
    super("Thread creation failed");
    this.name = "ThreadCreateFailure";
    this.feedback = feedback;
    this.statusMessage = statusMessage;
    this.retryable = retryable;
  }
}

export function setStatus(message, kind = "default") {
  statusCard.textContent = sanitizeErrorMessage(message);
  statusCard.classList.toggle("error", kind === "error");
  // The strip around the line carries the tone and the two ways out. An error
  // used to be red text that stayed until something else happened to replace
  // it: no way to act on it, no way to put it down. Dismiss clears it; Retry
  // is the one action that could plausibly fix anything this app reports,
  // because every error here ends at "the sidecar did not answer".
  const isError = kind === "error";
  if (statusBar) {
    statusBar.hidden = statusCard.textContent === "";
    statusBar.classList.toggle("error", isError);
  }
  if (statusRetryButton) statusRetryButton.hidden = !isError;
  if (statusDismissButton) statusDismissButton.hidden = !isError;
}

function bridge() {
  return window.workmaxLocal || null;
}

function desktopAuthBridge() {
  const desktop = window.desktopBridge;
  if (!isRecord(desktop) || !isRecord(desktop.auth)) {
    return null;
  }
  const auth = desktop.auth;
  if (
    typeof auth.beginLogin !== "function" ||
    typeof auth.loginStatus !== "function" ||
    typeof auth.submitLoginPassword !== "function" ||
    typeof auth.cancelLogin !== "function"
  ) {
    return null;
  }
  return auth;
}

export function desktopAgentBridge() {
  const desktop = window.desktopBridge;
  if (!isRecord(desktop) || !isRecord(desktop.agent)) {
    return null;
  }
  const agent = desktop.agent;
  if (
    typeof agent.listSkills !== "function" ||
    typeof agent.startTurn !== "function" ||
    typeof agent.cancelTurn !== "function" ||
    typeof agent.uploadThreadFile !== "function"
  ) {
    return null;
  }
  return agent;
}

function desktopAgentModesBridge() {
  const desktop = window.desktopBridge;
  if (
    !isRecord(desktop) ||
    !isRecord(desktop.agent) ||
    typeof desktop.agent.listModes !== "function"
  ) {
    return null;
  }
  return desktop.agent;
}

export function desktopAgentCreateBridge() {
  const desktop = window.desktopBridge;
  if (
    !isRecord(desktop) ||
    !isRecord(desktop.agent) ||
    typeof desktop.agent.createThread !== "function"
  ) {
    return null;
  }
  return desktop.agent;
}

function desktopAgentRecoveryBridge() {
  const desktop = window.desktopBridge;
  if (
    !isRecord(desktop) ||
    !isRecord(desktop.agent) ||
    typeof desktop.agent.listRecoverableTurns !== "function" ||
    typeof desktop.agent.resumeTurn !== "function" ||
    typeof desktop.agent.cancelTurn !== "function"
  ) {
    return null;
  }
  return desktop.agent;
}

function desktopSettingsBridge() {
  const desktop = window.desktopBridge;
  if (
    !isRecord(desktop) ||
    !isRecord(desktop.settings) ||
    typeof desktop.settings.getModelRoute !== "function" ||
    typeof desktop.settings.putModelRoute !== "function"
  ) {
    return null;
  }
  return desktop.settings;
}

function setModelSettingsError(message) {
  if (!modelSettingsError) return;
  if (!message) {
    modelSettingsError.hidden = true;
    modelSettingsError.textContent = "";
    return;
  }
  modelSettingsError.hidden = false;
  modelSettingsError.textContent = message;
}

// What each protocol actually gets the user — the first-run decision this
// form exists for, answered where it is made rather than after a failed turn.
const MODEL_PROTOCOL_HINTS = {
  openai_compatible: {
    hint: "Works with Ollama, LM Studio, vLLM and similar. Chat only — the tool loop needs the Anthropic protocol.",
    placeholder: "http://127.0.0.1:11434/v1",
  },
  anthropic_compatible: {
    hint: "Works with servers speaking the Anthropic Messages API. Enables the agent tool loop when a Claude CLI is available.",
    placeholder: "http://127.0.0.1:8080",
  },
};

function updateModelProtocolHint() {
  const hintEl = document.querySelector("#model-protocol-hint");
  const protocolEl = document.querySelector("#model-protocol");
  if (!protocolEl) return;
  const info = MODEL_PROTOCOL_HINTS[protocolEl.value];
  if (hintEl) hintEl.textContent = info ? info.hint : "";
  if (modelBaseURL && info) modelBaseURL.placeholder = info.placeholder;
}

// The official model picker belongs to BOTH routes now, and that is not a
// cosmetic change.
//
// Running here and running on an official model used to be the same choice
// asked once. They are two questions: where the turn executes (this machine,
// with a workspace the tools write into — or the cloud agent) and which model
// answers it (yours, or the one your membership pays for). Leaving Base URL
// empty on the local route is how you say "run here, on the official model" —
// the sidecar points the tool loop at its own loopback gateway, so no cloud
// credential is ever handed to the subprocess.
function updateModelLocalFieldsVisibility() {
  if (!modelPreferredRoute || !modelLocalFields) return;
  const local = modelPreferredRoute.value === "local";
  modelLocalFields.hidden = !local;
  if (modelOfficialFields) modelOfficialFields.hidden = false;
  updateModelProtocolHint();
}

// --- Official model catalog -------------------------------------------------
//
// The official route used to offer no choice at all. It offers one now, and
// the whole difficulty is that the choice belongs to the cloud: a membership
// can lapse between opening this form and opening it again, so a model that
// was selectable yesterday may be listed-but-locked today.
//
// Two rules follow, and both are the sidecar's verdict rather than a local
// guess (it answers selection_state alongside the list):
//
//  1. A model the account cannot use is SHOWN, disabled, labelled with the
//     tier it needs. Hiding it would answer "what does upgrading buy me?"
//     with silence.
//  2. A stored choice that stopped being allowed is never quietly swapped for
//     one that works. The form says so and asks for a new choice. Answering
//     on a different model than the user picked is the same betrayal as
//     falling back across routes, one level down.
const modelCatalogState = { state: "unbound", items: [], tier: "", selectionState: "unset" };

function modelCatalogBridgeAvailable() {
  return typeof window.desktopBridge?.settings?.getModelCatalog === "function";
}

function officialModelOptionLabel(item) {
  const name = item.displayName || item.modelId;
  if (item.permissions.includes("use")) {
    return item.default ? `${name} (default)` : name;
  }
  return item.requiredTier ? `${name} — needs ${item.requiredTier}` : `${name} — unavailable`;
}

function renderOfficialModelOptions(selectedModelID) {
  if (!modelOfficialID) return;
  modelOfficialID.textContent = "";
  const accountDefault = document.createElement("option");
  accountDefault.value = "";
  accountDefault.textContent = "Account default";
  modelOfficialID.appendChild(accountDefault);
  for (const item of modelCatalogState.items) {
    const option = document.createElement("option");
    option.value = item.modelId;
    option.textContent = officialModelOptionLabel(item);
    // Visible but not choosable: the upgrade is legible, the mistake is not
    // available to make.
    option.disabled = !item.permissions.includes("use");
    modelOfficialID.appendChild(option);
  }
  // A stored choice the catalog no longer lists still has to be representable,
  // or the select would silently snap to "Account default" and the next save
  // would write that silence back as the user's decision.
  if (
    selectedModelID &&
    !modelCatalogState.items.some((item) => item.modelId === selectedModelID)
  ) {
    const orphan = document.createElement("option");
    orphan.value = selectedModelID;
    orphan.textContent = `${selectedModelID} — no longer offered`;
    orphan.disabled = true;
    modelOfficialID.appendChild(orphan);
  }
  modelOfficialID.value = selectedModelID || "";
}

function officialModelNote() {
  if (!modelCatalogBridgeAvailable()) {
    return "Model choice needs a newer desktop shell.";
  }
  switch (modelCatalogState.state) {
    case "unbound":
      return "Connect an account to choose a model.";
    case "unavailable":
      return "Could not reach WorkMax; the model list may be out of date.";
    default:
      break;
  }
  switch (modelCatalogState.selectionState) {
    case "not_allowed":
      return "Your plan no longer includes the model you picked. Choose another one to continue on the official route.";
    case "unknown":
      return "The model you picked is no longer offered. Choose another one.";
    default:
      return modelCatalogState.tier
        ? `Plan: ${modelCatalogState.tier}.`
        : "";
  }
}

function officialSelectionNeedsAttention() {
  return (
    modelCatalogState.state === "ready" &&
    (modelCatalogState.selectionState === "not_allowed" ||
      modelCatalogState.selectionState === "unknown")
  );
}

function renderOfficialModelSection(selectedModelID) {
  if (modelOfficialID) {
    renderOfficialModelOptions(selectedModelID);
    modelOfficialID.disabled = modelCatalogState.state !== "ready";
  }
  if (modelOfficialNote) {
    modelOfficialNote.textContent = officialModelNote();
    modelOfficialNote.classList.toggle("is-error", officialSelectionNeedsAttention());
  }
}

async function loadModelCatalog(selectedModelID) {
  modelCatalogState.items = [];
  modelCatalogState.tier = "";
  if (!modelCatalogBridgeAvailable()) {
    modelCatalogState.state = "unbound";
    modelCatalogState.selectionState = selectedModelID ? "unverified" : "unset";
    renderOfficialModelSection(selectedModelID);
    return;
  }
  try {
    const result = parseDesktopBridgeResult(
      await window.desktopBridge.settings.getModelCatalog(),
      "settings.getModelCatalog"
    );
    if (!result.ok) {
      throw new Error("catalog unavailable");
    }
    modelCatalogState.state = result.data.state;
    modelCatalogState.items = result.data.items;
    modelCatalogState.tier = result.data.tier;
    modelCatalogState.selectionState = result.data.selection_state;
  } catch {
    // A catalog we could not read is "unavailable", never "you have nothing":
    // the difference decides whether the user thinks their plan lapsed.
    modelCatalogState.state = "unavailable";
    modelCatalogState.selectionState = selectedModelID ? "unverified" : "unset";
  }
  renderOfficialModelSection(selectedModelID);
}

function clearModelAPIKeyField() {
  if (modelAPIKey) modelAPIKey.value = "";
  if (modelClearAPIKey) modelClearAPIKey.checked = false;
}

function fillModelSettingsForm(settings) {
  if (!modelPreferredRoute || !modelProtocol || !modelBaseURL || !modelID) return;
  modelPreferredRoute.value = settings.preferred_route;
  modelProtocol.value =
    settings.local.protocol === "anthropic_compatible"
      ? "anthropic_compatible"
      : "openai_compatible";
  modelBaseURL.value = settings.local.base_url;
  modelID.value = settings.local.model_id;
  clearModelAPIKeyField();
  renderOfficialModelSection(settings.official_model_id);
  if (modelKeyStatus) {
    modelKeyStatus.textContent = settings.local.api_key_configured
      ? "API key: stored in Keychain"
      : "API key: not stored";
  }
  updateModelLocalFieldsVisibility();
  setModelSettingsError("");
}

// The settings panel. Opening it is always safe and never depends on a bridge:
// appearance and the account binding are readable without the sidecar, and the
// model section fills itself in — or says why it cannot — once it is on screen.
export function openSettingsPanel() {
  if (!settingsOverlay) return;
  // The identity popover is dismissed on the way in. It lives in the sidebar,
  // which the modal's backdrop covers but does not close, so leaving it open
  // stranded a menu under the scrim: visible, greyed, and unclickable. The
  // gear sits right next to the row that opens it, so this is a normal way to
  // arrive here rather than a corner case.
  if (state.localAccountPanelOpen) {
    state.localAccountPanelOpen = false;
    state.localAccountRenamingID = null;
    renderLocalAccountArea();
  }
  settingsOverlay.hidden = false;
  renderAppearanceChoice();
  renderLocalAccountBinding();
}

function closeSettingsPanel() {
  if (!settingsOverlay) return;
  settingsOverlay.hidden = true;
  closeModelSettings();
}

export async function openModelSettings() {
  if (!modelSettingsForm) return;
  openSettingsPanel();
  const settings = desktopSettingsBridge();
  if (!settings) {
    setStatus("Model settings require desktopBridge.settings (alpha.7+).", "error");
    return;
  }
  modelSettingsForm.hidden = false;
  if (modelSettingsSubmitButton) modelSettingsSubmitButton.disabled = true;
  setModelSettingsError("");
  try {
    const result = parseDesktopBridgeResult(
      await settings.getModelRoute(),
      "settings.getModelRoute"
    );
    if (!result.ok) {
      throw new Error(sanitizeErrorMessage(result.error) || "Could not load model settings");
    }
    const loaded = parseModelRouteSettings(result.data);
    await loadModelCatalog(loaded.official_model_id);
    fillModelSettingsForm(loaded);
    if (officialSelectionNeedsAttention()) {
      setModelSettingsError(officialModelNote());
    }
  } catch (error) {
    setModelSettingsError(String(error.message || error));
  } finally {
    if (modelSettingsSubmitButton) modelSettingsSubmitButton.disabled = false;
  }
}

function closeModelSettings() {
  if (modelSettingsForm) modelSettingsForm.hidden = true;
  clearModelAPIKeyField();
  setModelSettingsError("");
}

async function submitModelSettings(event) {
  event.preventDefault();
  if (!modelPreferredRoute) return;
  const settings = desktopSettingsBridge();
  if (!settings) {
    setModelSettingsError("Model settings bridge unavailable");
    return;
  }
  if (modelSettingsSubmitButton) modelSettingsSubmitButton.disabled = true;
  setModelSettingsError("");
  const preferred = modelPreferredRoute.value;
  /** @type {{ preferred_route: string, official_model_id?: string, local?: Record<string, unknown> }} */
  const body = { preferred_route: preferred };
  // Sent on either route: on the official route it is the model the cloud
  // agent answers with, and on the local route it is what the tool loop runs
  // when no endpoint of the user's own is filled in.
  if (modelOfficialID && !modelOfficialID.disabled) {
    const chosen = modelOfficialID.value;
    const item = modelCatalogState.items.find((entry) => entry.modelId === chosen);
    if (chosen !== "" && !(item && item.permissions.includes("use"))) {
      // Saving it would store a choice the account cannot run, and the next
      // turn would fail somewhere far from here.
      setModelSettingsError("That model is not available on your plan. Pick one that is.");
      if (modelSettingsSubmitButton) modelSettingsSubmitButton.disabled = false;
      return;
    }
    body.official_model_id = chosen;
  }
  if (preferred === "local") {
    body.local = {
      protocol: modelProtocol ? modelProtocol.value : "openai_compatible",
      base_url: modelBaseURL ? modelBaseURL.value.trim() : "",
      model_id: modelID ? modelID.value.trim() : "",
    };
    if (modelClearAPIKey && modelClearAPIKey.checked) {
      body.local.clear_api_key = true;
    } else if (modelAPIKey && modelAPIKey.value !== "") {
      body.local.api_key = modelAPIKey.value;
    }
  }
  try {
    const result = parseDesktopBridgeResult(
      await settings.putModelRoute(body),
      "settings.putModelRoute"
    );
    clearModelAPIKeyField();
    if (!result.ok) {
      throw new Error(sanitizeErrorMessage(result.error) || "Could not save model settings");
    }
    const saved = parseModelRouteSettings(result.data);
    await loadModelCatalog(saved.official_model_id);
    fillModelSettingsForm(saved);
    // Switching the route changes whether a signed-out user may run a turn at
    // all, so the gate is re-read here rather than left until the next
    // refresh — otherwise saving "local" appears to do nothing.
    await loadLocalModes();
    renderEmptyState();
    setStatus(
      preferred === "local"
        ? isLocalOnlySession()
          ? "Saved. Local model route — you can work without signing in."
          : "Saved local model route."
        : "Saved official model route preference."
    );
  } catch (error) {
    setModelSettingsError(String(error.message || error));
  } finally {
    if (modelAPIKey) modelAPIKey.value = "";
    if (modelSettingsSubmitButton) modelSettingsSubmitButton.disabled = false;
  }
}

export async function sidecarFetch(path, init) {
  const api = bridge();
  if (!api) {
    throw new Error("window.workmaxLocal bridge is unavailable");
  }
  return api.fetch(path, init);
}

export async function readSidecarJSON(response, endpoint) {
  let payload;
  try {
    payload = await response.json();
  } catch {
    if (!response.ok) {
      throw new Error(`${endpoint} HTTP ${response.status}`);
    }
    throw new Error(`Malformed ${endpoint} response`);
  }
  if (!response.ok) {
    if (response.status === 409 && isSessionChangedPayload(payload)) {
      throw new SessionChangedError();
    }
    throw new Error(`${endpoint} HTTP ${response.status}`);
  }
  return payload;
}

async function loadAuthStatus(expectedSessionGeneration = fences.session.snapshot()) {
  const res = await sidecarFetch("/auth/status");
  const auth = parseAuthStatus(await readSidecarJSON(res, "/auth/status"));
  if (!fences.session.isCurrent(expectedSessionGeneration)) {
    return null;
  }
  state.auth = auth;
  return state.auth;
}

export async function loadThreads(expectedSessionGeneration = fences.session.snapshot()) {
  // include_paused=true because pausing sync is now a user action: a thread
  // that vanished from the sidebar the moment you switched it to "local only"
  // would read as deletion, which is the opposite of what the switch does.
  const res = await sidecarFetch("/agent/threads?include_paused=true");
  const threads = parseThreads(await readSidecarJSON(res, "/agent/threads"));
  if (!fences.session.isCurrent(expectedSessionGeneration)) {
    return false;
  }
  state.threads = threads;
  renderThreads();
  updateSelectedThreadHeading();
  return true;
}

async function loadSkills(expectedSessionGeneration = fences.session.snapshot()) {
  const agent = desktopAgentBridge();
  state.agentAvailable = agent !== null;
  state.createAvailable = desktopAgentCreateBridge() !== null;
  if (!agent) {
    if (fences.session.isCurrent(expectedSessionGeneration)) {
      state.skills = [];
      state.allowedModes = [];
      state.selectedMode = "";
      state.skillsLoading = false;
      state.skillsDegraded = false;
      renderSkillOptions();
      updateComposerState();
    }
    return false;
  }
  state.skillsLoading = true;
  updateComposerState();
  let result;
  try {
    result = parseDesktopBridgeResult(
      await agent.listSkills(),
      "agent skills result"
    );
  } finally {
    if (fences.session.isCurrent(expectedSessionGeneration)) {
      state.skillsLoading = false;
    }
  }
  if (!result.ok) {
    if (result.status === 409 && isSessionChangedPayload(result.error)) {
      throw new SessionChangedError();
    }
    throw new Error(`Agent skills HTTP ${result.status}`);
  }
  const catalog = parseSkillsCatalog(result.data);
  if (!fences.session.isCurrent(expectedSessionGeneration)) {
    return false;
  }
  state.skills = catalog.items;
  state.allowedModes = catalog.allowed_modes;
  state.skillsDegraded = catalog.items.length === 0 && catalog.allowed_modes.length > 0;
  renderSkillOptions();
  updateComposerState();
  return true;
}

// Reads the Desktop's own answer to "what may I run, and would it run here".
//
// Never throws outward: this is the call that decides whether a signed-out app
// is usable, and if it fails the honest outcome is the pre-existing behaviour
// (sign in first), not a broken boot.
async function loadLocalModes(expectedSessionGeneration = fences.session.snapshot()) {
  const agent = desktopAgentModesBridge();
  // The same availability facts loadSkills records. They live in both because
  // this is the only loader that runs on the signed-out path, and without them
  // the composer stays disabled for a reason that has nothing to do with the
  // route: "no agent bridge", which is false.
  state.agentAvailable = desktopAgentBridge() !== null;
  state.createAvailable = desktopAgentCreateBridge() !== null;
  if (!agent) return false;
  let result;
  try {
    result = parseDesktopBridgeResult(await agent.listModes(), "agent modes result");
  } catch {
    return false;
  }
  if (!fences.session.isCurrent(expectedSessionGeneration)) return false;
  if (!result.ok) return false;
  let modes;
  try {
    modes = parseAgentModes(result.data);
  } catch {
    // The sidecar ANSWERED — in a shape this UI cannot read. That is version
    // skew (stale binary, newer renderer, or vice versa), and silently
    // falling back to the sign-in wall buries the real problem. The flag is
    // read wherever boot writes its final status, so the message wins.
    state.modesParseSkew = true;
    return false;
  }
  state.modesParseSkew = false;
  state.localRoute = modes.local_route;
  state.toolLoop = modes.tool_loop;
  void loadLocalAccounts(expectedSessionGeneration);
  // The catalog is authoritative when there is a session — it carries the same
  // allowlist plus the live skill details. This only fills the gap the catalog
  // cannot cover, which is having no session at all.
  if (state.auth?.state !== "authenticated") {
    state.allowedModes = modes.allowed_modes;
    if (!state.allowedModes.includes(state.selectedMode)) {
      state.selectedMode = state.allowedModes[0] || "";
    }
    renderSkillOptions();
  }
  updateComposerState();
  return true;
}

function desktopLocalAccountsBridge() {
  const desktop = window.desktopBridge;
  if (
    !isRecord(desktop) ||
    !isRecord(desktop.local) ||
    typeof desktop.local.listAccounts !== "function" ||
    typeof desktop.local.createAccount !== "function" ||
    typeof desktop.local.selectAccount !== "function"
  ) {
    return null;
  }
  return desktop.local;
}

// loadLocalAccounts fills the account switcher. Never throws outward: if the
// sidecar cannot answer, the switcher simply stays hidden and the local route
// keeps running as whatever account is active server-side — accounts are a
// convenience surface, not a gate.
async function loadLocalAccounts(
  expectedSessionGeneration = fences.session.snapshot()
) {
  const local = desktopLocalAccountsBridge();
  if (!local) return false;
  let accounts;
  let binding;
  try {
    const result = parseDesktopBridgeResult(
      await local.listAccounts(),
      "local accounts result"
    );
    if (!result.ok) return false;
    accounts = parseLocalAccounts(result.data);
    binding = parseCloudBinding(result.data);
  } catch {
    return false;
  }
  if (!fences.session.isCurrent(expectedSessionGeneration)) return false;
  state.localAccounts = accounts;
  state.cloudBinding = binding;
  renderLocalAccountArea();
  return true;
}

export function activeLocalAccount() {
  return state.localAccounts.find((account) => account.active) || null;
}

// Who you are on this machine, and what — if anything — that identity is
// connected to. It used to hide itself whenever a cloud session existed, on
// the grounds that the local account was not what turns ran as. True, and
// exactly why it should be visible: "connected to …42" is the fact that
// explains where the work is going, and hiding it made connecting and
// disconnecting feel like signing in and out of the app itself.
export function renderLocalAccountArea() {
  // The binding moved to Settings, which is open or closed independently of
  // this rail, so it is repainted whenever the state behind it changes rather
  // than only when the identity popover happens to be open.
  renderLocalAccountBinding();
  if (!localAccountRow || !localAccountPanel) return;
  const active = activeLocalAccount();
  const visible = active !== null;
  localAccountRow.hidden = !visible;
  if (!visible) {
    state.localAccountPanelOpen = false;
    state.localAccountRenamingID = null;
    localAccountPanel.hidden = true;
    return;
  }
  if (localAccountNameEl) localAccountNameEl.textContent = active.name;
  if (localAccountAvatar) {
    localAccountAvatar.textContent = Array.from(active.name)[0].toUpperCase();
  }
  if (localAccountHint) {
    // The row names the machine's identity; the second line says what that
    // identity's situation IS, never what you could do to it. It read
    // "Switch" before, which made "Local / Switch" parse as a two-word name
    // and left the app with no line anywhere saying where work was running.
    // That line used to be a permanent block above this row on the status
    // strip — "Local model route. No account connected — history stays on
    // this machine." — reserving a paragraph of the rail for a fact that
    // never changes while you work. It is one line here instead, and the
    // promise it carried is still stated in full where a promise belongs:
    // Settings › Account, the onboarding card, and the composer's own hint.
    localAccountHint.textContent =
      effectiveCloudBinding().state === "bound"
        ? "Connected to WorkMax"
        : effectiveCloudBinding().state === "expired"
          ? "Sign-in expired"
          : state.localRoute
            ? "Local model · this machine"
            : "This machine only";
  }
  localAccountRow.setAttribute("aria-expanded", String(state.localAccountPanelOpen === true));
  localAccountPanel.hidden = !state.localAccountPanelOpen;
  if (!state.localAccountPanelOpen) return;
  if (!localAccountListEl) return;
  if (state.localAccountRenamingID !== null) return;
  // Switching identities decides who owns LOCAL work. While an account is
  // connected, new work belongs to that account, so a switch here would be a
  // control that changes nothing — a dead action wearing a label. The list is
  // shown when it means something, and explained when it does not.
  const switchable = isLocalIdentity();
  if (localAccountSwitchNote) {
    localAccountSwitchNote.hidden = switchable;
    localAccountSwitchNote.textContent = switchable
      ? ""
      : "While an account is connected, new conversations belong to it. Disconnect to work as this machine's own identities again.";
  }
  localAccountListEl.hidden = !switchable;
  if (localAccountCreateForm) localAccountCreateForm.hidden = !switchable;
  if (!switchable) return;
  localAccountListEl.textContent = "";
  for (const account of state.localAccounts) {
    localAccountListEl.appendChild(renderLocalAccountItem(account));
  }
}

// Two sources answer "is an account connected", and only one of them is always
// available: the sidecar's binding record needs desktopBridge.local, the
// session status does not. An authenticated session IS a connected account, so
// a missing binding record must not leave the app offering to connect one that
// is already there. The record wins when it has an opinion; the session fills
// the gap when it does not.
function effectiveCloudBinding() {
  const binding = state.cloudBinding || { state: "unbound", user_id: "" };
  if (binding.state !== "unbound") return binding;
  if (state.auth?.state === "authenticated") {
    return { state: "bound", user_id: binding.user_id };
  }
  if (state.auth?.state === "expired") {
    return { state: "expired", user_id: binding.user_id };
  }
  return binding;
}

// The binding line: bound / expired / unbound, and the one action that
// changes it. This is the app's single "connect an account" control — there
// used to be a second one in the rail wearing the same words, which meant two
// buttons, two visibility rules and one intent. Disconnect is the existing
// logout — no data moves, which is why the copy promises exactly that and
// nothing more.
function renderLocalAccountBinding() {
  const binding = effectiveCloudBinding();
  const named = binding.user_id ? " (" + binding.user_id + ")" : "";
  if (localAccountBindingState) {
    localAccountBindingState.textContent =
      binding.state === "bound"
        ? "Connected to a WorkMax account" + named + " — cloud models and sync are available."
        : binding.state === "expired"
          ? "The connected WorkMax account" + named + " needs signing in again. Local work is unaffected."
          : "No WorkMax account connected. Everything here is local to this machine.";
  }
  if (localAccountConnectButton) {
    // While the credential form is open it is the form's own Continue that
    // asks; a second, identical invitation above it would be noise.
    localAccountConnectButton.hidden =
      binding.state === "bound" || state.loginFormOpen === true;
    localAccountConnectButton.textContent =
      binding.state === "expired" ? "Sign in again" : "Connect account";
  }
  if (localAccountDisconnectButton) {
    localAccountDisconnectButton.hidden = binding.state === "unbound";
    localAccountDisconnectButton.textContent = state.disconnecting
      ? "Disconnecting..."
      : "Disconnect";
    localAccountDisconnectButton.disabled = state.disconnecting === true;
  }
}

// The context chips above the input, Codex-style: where the turn will run
// and who it will run as. Facts the dispatch already knows, surfaced where
// the typing happens instead of buried in Models/settings.
export function renderComposerChips() {
  const runtime = document.querySelector("#runtime-chip");
  const accountChip = document.querySelector("#account-chip");
  if (runtime) {
    if (!canSendTurn()) {
      runtime.hidden = true;
    } else if (state.localRoute) {
      runtime.textContent = state.toolLoop ? "⌂ Local · tools" : "⌂ Local · chat";
      runtime.hidden = false;
    } else {
      runtime.textContent = "☁ Cloud";
      runtime.hidden = false;
    }
  }
  if (accountChip) {
    const account = isLocalIdentity() ? activeLocalAccount() : null;
    // See defaultLocalAccountLabel: the placeholder name is not worth a chip.
    if (account && account.name !== defaultLocalAccountLabel) {
      accountChip.textContent = account.name;
      accountChip.hidden = false;
    } else {
      accountChip.hidden = true;
    }
  }
}

function renderLocalAccountItem(account) {
  const item = document.createElement("li");
  item.className = "local-account-entry";
  const button = document.createElement("button");
  button.type = "button";
  button.className = "local-account-item";
  button.classList.toggle("active", account.active);
  button.dataset.accountId = String(account.id);
  button.textContent = account.active ? account.name + " · active" : account.name;
  button.addEventListener("click", () => {
    void selectLocalAccountByID(account.id);
  });
  item.appendChild(button);

  const rename = document.createElement("button");
  rename.type = "button";
  rename.className = "local-account-action";
  rename.textContent = "Rename";
  rename.setAttribute("aria-label", "Rename " + account.name);
  rename.addEventListener("click", () => {
    startLocalAccountRename(item, account);
  });
  item.appendChild(rename);

  // Deleting an identity deletes everything it owns — but never the one you
  // are: switching away first keeps "delete who I am right now" impossible.
  if (!account.active) {
    const del = document.createElement("button");
    del.type = "button";
    del.className = "local-account-action danger";
    del.textContent = "Delete";
    del.setAttribute("aria-label", "Delete " + account.name + " and all its data");
    del.addEventListener("click", () => {
      if (!del.classList.contains("armed")) {
        del.classList.add("armed");
        del.textContent = "Delete all its data?";
        setTimeout(() => {
          del.classList.remove("armed");
          del.textContent = "Delete";
        }, DELETE_ARM_MS);
        return;
      }
      del.disabled = true;
      void deleteLocalAccountByID(account);
    });
    item.appendChild(del);
  }
  return item;
}

// startLocalAccountRename swaps the row into an inline form, mirroring the
// thread rename interaction so there is one editing idiom in the app.
function startLocalAccountRename(item, account) {
  state.localAccountRenamingID = account.id;
  item.textContent = "";
  const form = document.createElement("form");
  form.className = "local-account-rename";
  const input = document.createElement("input");
  input.type = "text";
  input.value = account.name;
  input.maxLength = 64;
  input.setAttribute("aria-label", "New name for " + account.name);
  const save = document.createElement("button");
  save.type = "submit";
  save.className = "primary";
  save.textContent = "Save";
  const cancel = document.createElement("button");
  cancel.type = "button";
  cancel.textContent = "Cancel";
  cancel.addEventListener("click", () => {
    state.localAccountRenamingID = null;
    renderLocalAccountArea();
  });
  form.appendChild(input);
  form.appendChild(save);
  form.appendChild(cancel);
  form.addEventListener("submit", (event) => {
    if (event && typeof event.preventDefault === "function") event.preventDefault();
    void submitLocalAccountRename(account, input.value);
  });
  item.appendChild(form);
  if (typeof input.focus === "function") input.focus();
}

async function submitLocalAccountRename(account, rawName) {
  state.localAccountRenamingID = null;
  const local = desktopLocalAccountsBridge();
  if (!local || typeof local.renameAccount !== "function") return;
  const name = String(rawName || "").trim();
  if (!name || name === account.name) {
    renderLocalAccountArea();
    return;
  }
  try {
    const result = parseDesktopBridgeResult(
      await local.renameAccount(account.id, name),
      "rename local account result"
    );
    if (!result.ok) {
      const raw = isRecord(result.error) ? result.error.error : result.error;
      const reason = sanitizeErrorMessage(raw);
      throw new Error(
        reason === "name_taken"
          ? "An account with that name already exists"
          : reason || "Could not rename account"
      );
    }
  } catch (error) {
    setStatus(String(error.message || error), "error");
    renderLocalAccountArea();
    return;
  }
  setStatus('Renamed to "' + name + '"');
  await loadLocalAccounts();
}

async function deleteLocalAccountByID(account) {
  const local = desktopLocalAccountsBridge();
  if (!local || typeof local.deleteAccount !== "function") return;
  try {
    const result = parseDesktopBridgeResult(
      await local.deleteAccount(account.id),
      "delete local account result"
    );
    if (!result.ok) {
      const raw = isRecord(result.error) ? result.error.error : result.error;
      const reason = sanitizeErrorMessage(raw);
      throw new Error(
        reason === "account_busy"
          ? "That account has a turn running — stop it first"
          : reason === "account_active"
            ? "Switch away from an account before deleting it"
            : reason || "Could not delete account"
      );
    }
    const data = isRecord(result.data) ? result.data : {};
    const threads = Number.isInteger(data.threads) ? data.threads : 0;
    setStatus(
      'Deleted "' + account.name + '"' +
        (threads > 0 ? " and its " + threads + " conversation" + (threads === 1 ? "" : "s") : "")
    );
  } catch (error) {
    setStatus(String(error.message || error), "error");
  }
  await loadLocalAccounts();
}

// Disconnecting an account is the existing logout, said in the words that
// describe what it does: the account stops authorizing cloud work, and the
// local identity that was there the whole time is what remains. No data is
// moved in either direction — the rows an account owns stay its own, and
// reconnecting the same account finds them again.
async function disconnectCloudAccount() {
  const desktop = window.desktopBridge;
  const auth = isRecord(desktop) ? desktop.auth : null;
  if (!isRecord(auth) || typeof auth.logout !== "function") {
    setStatus("Disconnecting is unavailable in this Desktop build", "error");
    return;
  }
  if (state.disconnecting) return;
  state.disconnecting = true;
  renderLocalAccountArea();
  try {
    const result = parseDesktopBridgeResult(await auth.logout(), "logout result");
    if (!result.ok) {
      const raw = isRecord(result.error) ? result.error.error : result.error;
      throw new Error(sanitizeErrorMessage(raw) || "Could not disconnect the account");
    }
  } catch (error) {
    state.disconnecting = false;
    setStatus(String(error.message || error), "error");
    renderLocalAccountArea();
    return;
  }
  state.disconnecting = false;
  state.localAccountPanelOpen = false;
  setStatus("Account disconnected. You are working as this machine's identity again.");
  // A full reload, for the same reason an account switch is: every loaded
  // thread belonged to the identity that just left.
  await refresh();
}

function toggleLocalAccountPanel() {
  state.localAccountPanelOpen = !state.localAccountPanelOpen;
  state.localAccountRenamingID = null;
  renderLocalAccountArea();
  if (state.localAccountPanelOpen && localAccountNameInput) {
    localAccountNameInput.value = "";
  }
}

// Switching accounts is a full session reload: every loaded thread, message
// and mode belongs to the previous uid, so nothing short of refresh() is
// honest about what just happened.
async function selectLocalAccountByID(id) {
  const local = desktopLocalAccountsBridge();
  if (!local) return;
  const current = activeLocalAccount();
  if (current && current.id === id) {
    state.localAccountPanelOpen = false;
    renderLocalAccountArea();
    return;
  }
  try {
    const result = parseDesktopBridgeResult(
      await local.selectAccount(id),
      "select local account result"
    );
    if (!result.ok) {
      const raw = isRecord(result.error) ? result.error.error : result.error;
      throw new Error(sanitizeErrorMessage(raw) || "Could not switch account");
    }
  } catch (error) {
    setStatus(String(error.message || error), "error");
    return;
  }
  state.localAccountPanelOpen = false;
  const chosen = state.localAccounts.find((account) => account.id === id);
  setStatus(chosen ? 'Switched to "' + chosen.name + '"' : "Switched account");
  await refresh();
}

async function submitCreateLocalAccount(event) {
  if (event && typeof event.preventDefault === "function") event.preventDefault();
  const local = desktopLocalAccountsBridge();
  if (!local || !localAccountNameInput) return;
  const name = localAccountNameInput.value.trim();
  if (!name) {
    setStatus("Account name cannot be empty", "error");
    return;
  }
  try {
    const result = parseDesktopBridgeResult(
      await local.createAccount(name),
      "create local account result"
    );
    if (!result.ok) {
      const raw = isRecord(result.error) ? result.error.error : result.error;
      const reason = sanitizeErrorMessage(raw);
      throw new Error(
        reason === "name_taken"
          ? "An account with that name already exists"
          : reason === "account_limit"
            ? "Account limit reached"
            : reason || "Could not create account"
      );
    }
  } catch (error) {
    setStatus(String(error.message || error), "error");
    return;
  }
  localAccountNameInput.value = "";
  setStatus('Created "' + name + '" — click it to switch');
  await loadLocalAccounts();
}

async function loadRecoverableTurns(
  expectedSessionGeneration = fences.session.snapshot()
) {
  const agent = desktopAgentRecoveryBridge();
  if (!agent) {
    if (fences.session.isCurrent(expectedSessionGeneration)) {
      state.recoverableTurns = [];
      state.recoveryLoading = false;
      renderThreads();
      updateComposerState();
    }
    return false;
  }
  state.recoveryLoading = true;
  updateComposerState();
  let result;
  try {
    result = parseDesktopBridgeResult(
      await agent.listRecoverableTurns(),
      "agent recoverable turns result"
    );
  } finally {
    if (fences.session.isCurrent(expectedSessionGeneration)) {
      state.recoveryLoading = false;
      updateComposerState();
    }
  }
  if (!result.ok) {
    if (result.status === 409 && isSessionChangedPayload(result.error)) {
      throw new SessionChangedError();
    }
    if (fences.session.isCurrent(expectedSessionGeneration)) {
      state.recoverableTurns = [];
      renderThreads();
      updateComposerState();
    }
    return false;
  }
  const items = parseRecoverableTurns(result.data);
  if (!fences.session.isCurrent(expectedSessionGeneration)) {
    return false;
  }
  state.recoverableTurns = items;
  renderThreads();
  updateComposerState();
  return true;
}

// Export writes the conversation into the thread's workspace as Markdown,
// then opens the folder: "take your data with you" ends with the file in
// front of the user, not with a path in a status message they must chase.
export async function exportSelectedThread() {
  const agent = window.desktopBridge?.agent;
  if (!agent || typeof agent.exportThread !== "function") return;
  const threadUUID = state.selectedThreadUUID;
  if (!threadUUID) return;
  const exportButton = document.querySelector("#export-thread-button");
  if (exportButton) exportButton.disabled = true;
  try {
    const result = parseDesktopBridgeResult(
      await agent.exportThread(threadUUID),
      "export thread result"
    );
    if (!result.ok) {
      const raw = isRecord(result.error) ? result.error.error : result.error;
      const reason = sanitizeErrorMessage(raw);
      throw new Error(
        reason === "thread_empty"
          ? "Nothing to export yet — this conversation has no messages"
          : reason || "Could not export the conversation"
      );
    }
    const data = isRecord(result.data) ? result.data : {};
    const count = Number.isInteger(data.messages) ? data.messages : 0;
    setStatus(
      "Exported " + count + " message" + (count === 1 ? "" : "s") + " as Markdown — opening the folder"
    );
    if (typeof agent.revealWorkspace === "function") {
      await agent.revealWorkspace(threadUUID);
    }
    // The file is a deliverable; the panel that lists deliverables should
    // know about it without waiting for the next turn.
    void loadWorkspaceDeliverables(threadUUID);
  } catch (error) {
    setStatus(String(error.message || error), "error");
  } finally {
    if (exportButton) exportButton.disabled = false;
  }
}

export function renameThreadBridgeAvailable() {
  return typeof window.desktopBridge?.agent?.renameThread === "function";
}

export function closeRenameForm() {
  const form = document.querySelector("#rename-thread-form");
  const titleRow = document.querySelector("#thread-title");
  if (form) form.hidden = true;
  if (titleRow) titleRow.hidden = false;
}

function openRenameForm() {
  const thread = state.threads.find(
    (candidate) => candidate.uuid === state.selectedThreadUUID
  );
  if (!thread) return;
  const form = document.querySelector("#rename-thread-form");
  const input = document.querySelector("#rename-thread-input");
  if (!form || !input) return;
  input.value = thread.name || "";
  form.hidden = false;
  threadTitle.hidden = true;
  input.focus();
  if (typeof input.select === "function") input.select();
}

async function submitRename() {
  const threadUUID = state.selectedThreadUUID;
  const input = document.querySelector("#rename-thread-input");
  const agent = window.desktopBridge?.agent;
  if (!threadUUID || !input || !agent) return;
  const name = input.value.trim();
  if (name === "" || utf8ByteLength(name) > MAX_THREAD_NAME_BYTES) {
    setStatus("Enter a name up to 200 characters.", "error");
    return;
  }
  let result;
  try {
    result = parseDesktopBridgeResult(
      await agent.renameThread(threadUUID, name),
      "agent rename result"
    );
  } catch {
    setStatus("Could not rename the conversation.", "error");
    return;
  }
  if (!result.ok) {
    setStatus("Could not rename the conversation.", "error");
    return;
  }
  // The selection may have moved while the request was in flight; the rename
  // still happened, but this response must not repaint someone else's title.
  const entry = state.threads.find((t) => t.uuid === threadUUID);
  const serverThread = isRecord(result.data) ? result.data.thread : null;
  if (entry && isRecord(serverThread) && typeof serverThread.name === "string") {
    entry.name = serverThread.name;
    if (typeof serverThread.updated_at === "string") {
      entry.updated_at = serverThread.updated_at;
    }
  }
  renderThreads();
  if (state.selectedThreadUUID === threadUUID) {
    updateSelectedThreadHeading();
  }
  setStatus("Conversation renamed.");
}

export function chooseModeForThread(thread) {
  if (state.allowedModes.includes(thread.agent_mode)) {
    state.selectedMode = thread.agent_mode;
    return;
  }
  if (!state.allowedModes.includes(state.selectedMode)) {
    state.selectedMode = state.allowedModes[0] || "";
  }
}

export function renderSkillOptions() {
  const selectedThread = state.threads.find(
    (thread) => thread.uuid === state.selectedThreadUUID
  );
  if (selectedThread) {
    chooseModeForThread(selectedThread);
  } else if (!state.allowedModes.includes(state.selectedMode)) {
    state.selectedMode = state.allowedModes[0] || "";
  }
  agentMode.textContent = "";
  newThreadMode.textContent = "";
  if (state.allowedModes.length === 0) {
    for (const select of [agentMode, newThreadMode]) {
      const option = document.createElement("option");
      option.value = "";
      option.textContent = state.skillsLoading ? "Loading..." : "Unavailable";
      select.appendChild(option);
      select.value = "";
    }
    agentMode.value = "";
    updateNewThreadState();
    return;
  }
  for (const mode of state.allowedModes) {
    const skill = state.skills.find((item) => item.agentMode === mode);
    for (const select of [agentMode, newThreadMode]) {
      const option = document.createElement("option");
      option.value = mode;
      option.textContent = skill?.name || mode.toUpperCase();
      select.appendChild(option);
    }
  }
  agentMode.value = state.selectedMode;
  const draftMode = state.createDraft?.agentMode;
  newThreadMode.value = state.allowedModes.includes(draftMode)
    ? draftMode
    : state.selectedMode || state.allowedModes[0];
  updateNewThreadState();
}

function isTurnContextCurrent(activeTurn) {
  return (
    fences.session.isCurrent(activeTurn.sessionGeneration) &&
    fences.selection.isCurrent(activeTurn.selectionGeneration) &&
    fences.turn.isCurrent(activeTurn.turnGeneration) &&
    activeTurn.threadUUID === state.selectedThreadUUID
  );
}

export function isCurrentTurn(activeTurn) {
  return state.activeTurn === activeTurn && isTurnContextCurrent(activeTurn);
}

function requestTurnCancellation(turnID) {
  const agent = desktopAgentBridge();
  if (!agent || !turnID) return;
  void agent
    .cancelTurn(turnID)
    .then((result) => {
      parseTurnCancelResult(result);
    })
    .catch(() => {});
}

export function invalidateActiveTurn(requestCancellation) {
  const activeTurn = state.activeTurn;
  if (!activeTurn) return;
  state.activeTurn = null;
  fences.turn.bump();
  if (requestCancellation) {
    requestTurnCancellation(activeTurn.turnID);
  }
  updateComposerState();
}

export async function handleScopedError(error, context) {
  if (error instanceof SessionChangedError) {
    await handleSessionChanged();
    return;
  }
  if (!isCurrentSelection(context)) {
    return;
  }
  setStatus(String(error), "error");
}

async function handleGlobalError(error, expectedSessionGeneration) {
  if (error instanceof SessionChangedError) {
    await handleSessionChanged();
    return;
  }
  if (!fences.session.isCurrent(expectedSessionGeneration)) {
    return;
  }
  setStatus(String(error), "error");
}

function clearWorkbenchForSessionChange() {
  invalidateActiveTurn(true);
  state.cancelConfirmationTurnID = null;
  fences.recovery.bump();
  state.recoverableTurns = [];
  state.recoveryLoading = false;
  state.resumingTurn = false;
  state.dismissingRecovery = false;
  state.recoveryFeedback = "";
  state.recoveryFeedbackKind = "default";
  fences.create.bump();
  state.createFormOpen = false;
  state.creatingThread = false;
  state.createDraft = null;
  newThreadName.value = "Untitled presentation";
  setCreateFeedback("");
  fences.selection.bump();
  state.selectedThreadUUID = null;
  state.threads = [];
  state.skills = [];
  state.allowedModes = [];
  state.selectedMode = "";
  state.skillsLoading = false;
  state.skillsDegraded = false;
  // Deliberately NOT stashed: a session change means a different signed-in
  // account, and the previous account's half-written prompts must not
  // resurface under the new one. Every other path preserves drafts via
  // stashComposerDraft; this one drops them with the rest of the workbench.
  state.composerDrafts.clear();
  chatInput.value = "";
  messageList.textContent = "";
  renderThreads();
  renderSkillOptions();
  emptyState.hidden = false;
  threadPanel.hidden = true;
}

export async function handleSessionChanged() {
  if (state.recoveringSession) {
    return;
  }
  state.recoveringSession = true;
  fences.session.bump();
  fences.loginOperation.bump();
  const generation = fences.session.snapshot();
  clearWorkbenchForSessionChange();
  setTurnState("Session changed");
  renderTaskContext();
  setStatus(
    "Your signed-in account changed. Select a thread again; the previous prompt was not resent and thread creation was not replayed.",
    "error"
  );
  updateComposerState();

  try {
    const auth = await loadAuthStatus(generation);
    if (!fences.session.isCurrent(generation) || !auth) return;
    if (auth.state !== "authenticated") {
      // The account went away rather than changing — a disconnect, or a
      // session that expired. This machine's own identity is still here, so
      // land on it with its workbench loaded instead of an empty shell.
      state.localRoute = false;
      await loadLocalModes(generation);
      if (!fences.session.isCurrent(generation)) return;
      await Promise.allSettled([
        loadThreads(generation),
        loadRecoverableTurns(generation),
      ]);
    }
    if (auth.state === "authenticated") {
      state.localRoute = false;
      void loadLocalAccounts(generation);
      const results = await Promise.allSettled([
        loadThreads(generation),
        loadSkills(generation),
        loadRecoverableTurns(generation),
      ]);
      for (const result of results) {
        if (
          result.status === "rejected" &&
          !(result.reason instanceof SessionChangedError)
        ) {
          // Keep the closed session-change notice. A refresh can retry an
          // independently failed history or catalog request.
          break;
        }
      }
    }
  } catch {
    if (fences.session.isCurrent(generation)) {
      state.auth = null;
    }
  } finally {
    if (fences.session.isCurrent(generation)) {
      state.recoveringSession = false;
      renderSkillOptions();
      updateComposerState();
      setStatus(
        "Your signed-in account changed. Select a thread again; the previous prompt was not resent and thread creation was not replayed.",
        "error"
      );
    }
  }
}

export function submitChat(event) {
  event.preventDefault();
  if (state.activeTurn) {
    return;
  }
  const agent = desktopAgentBridge();
  const thread = state.threads.find(
    (candidate) => candidate.uuid === state.selectedThreadUUID
  );
  const userText = chatInput.value.trim();
  if (
    !canSendTurn() ||
    !agent ||
    !thread ||
    !state.allowedModes.includes(state.selectedMode) ||
    state.createFormOpen ||
    !isValidChatText(userText)
  ) {
    updateComposerState();
    return;
  }

  fences.turn.bump();
  // The previous turn's provenance stops being true the moment a new question
  // is asked. Cleared here rather than when the next retrieval event arrives,
  // because a turn that retrieves nothing sends no event at all. Tool
  // activity follows the same rule.
  contextState.retrieved = [];
  contextState.toolActivity = [];
  const optimistic = appendOptimisticTurn(userText);
  const activeTurn = {
    turnID: "",
    startedAt: Date.now(),
    threadUUID: thread.uuid,
    userText,
    chatMode: state.selectedMode,
    sessionGeneration: fences.session.snapshot(),
    selectionGeneration: fences.selection.snapshot(),
    turnGeneration: fences.turn.snapshot(),
    userNode: optimistic.userNode,
    assistantWrapper: optimistic.assistantNode,
    assistantBubble: optimistic.assistantBubble,
    assistantText: "",
    assistantTextBytes: 0,
    pendingEvents: [],
    stopRequested: false,
    localCancelObserved: false,
    recoveryTurn: null,
  };
  state.activeTurn = activeTurn;
  chatInput.value = "";
  // The draft was consumed by this turn; a stale stash would resurrect an
  // already-sent prompt on the next switch back to this thread.
  state.composerDrafts.delete(thread.uuid);
  // Read the attachments before clearing the tray: startTurn below needs the
  // ids, and clearing first meant every turn was sent with an empty file list
  // — the attachment feature looked like it worked and silently dropped every
  // file.
  // Fresh uploads from the tray plus persisted files checked in the Sources
  // panel, deduped: re-uploading a checked file must not attach it twice.
  const fileIDSet = new Set(
    state.pendingFiles
      .filter((file) => file.status === "ready")
      .map((file) => file.id)
  );
  for (const id of contextState.selectedFileIDs) fileIDSet.add(id);
  const fileIDs = Array.from(fileIDSet);
  state.pendingFiles = [];
  // Cleared with the tray: the label says "next request", and this was it.
  contextState.selectedFileIDs = new Set();
  renderAttachments();
  setTurnState("Working");
  renderTaskContext();
  updateComposerState();

  try {
    const openResult = parseTurnOpenResult(
      agent.startTurn(
        {
          threadUUID: thread.uuid,
          userText,
          chatMode: state.selectedMode,
          fileIDs,
        },
        (rawEvent) => {
          if (!isCurrentTurn(activeTurn)) return;
          if (!activeTurn.turnID) {
            try {
              // Parse before buffering so malformed or legacy-open events
              // never enter renderer state, even transiently.
              activeTurn.pendingEvents.push(parseAgentTurnEvent(rawEvent));
            } catch {
              failActiveTurnProtocol(
                activeTurn,
                "The Agent stream returned an invalid event."
              );
            }
            return;
          }
          handleRawTurnEvent(activeTurn, rawEvent);
        }
      )
    );
    if (!isCurrentTurn(activeTurn)) {
      requestTurnCancellation(openResult.turnID);
      return;
    }
    activeTurn.turnID = openResult.turnID;
    const pendingEvents = activeTurn.pendingEvents;
    activeTurn.pendingEvents = [];
    for (const pendingEvent of pendingEvents) {
      if (!isCurrentTurn(activeTurn)) break;
      handleParsedTurnEvent(activeTurn, pendingEvent);
    }
  } catch {
    failTurnOpen(activeTurn, userText, "The Agent turn could not be started.");
  }
}

function recoveryContext(turnUUID) {
  return {
    sessionGeneration: fences.session.snapshot(),
    selectionGeneration: fences.selection.snapshot(),
    recoveryGeneration: fences.recovery.snapshot(),
    threadUUID: state.selectedThreadUUID,
    turnUUID,
  };
}

function isCurrentRecovery(context) {
  return (
    fences.session.isCurrent(context.sessionGeneration) &&
    fences.selection.isCurrent(context.selectionGeneration) &&
    fences.recovery.isCurrent(context.recoveryGeneration) &&
    context.threadUUID === state.selectedThreadUUID &&
    state.recoverableTurns.some(
      (turn) => turn.turn_uuid === context.turnUUID
    )
  );
}

function resumeRecoverableTurn() {
  const recoverable = selectedRecoverableTurn();
  const agent = desktopAgentRecoveryBridge();
  if (
    !recoverable ||
    !agent ||
    state.activeTurn ||
    state.resumingTurn ||
    state.dismissingRecovery ||
    state.recoveringSession
  ) {
    updateComposerState();
    return;
  }

  fences.recovery.bump();
  const context = recoveryContext(recoverable.turn_uuid);
  fences.turn.bump();
  const activeTurn = {
    turnID: "",
    startedAt: Date.now(),
    threadUUID: recoverable.thread_uuid,
    userText: recoverable.user_text,
    chatMode: recoverable.chat_mode,
    sessionGeneration: fences.session.snapshot(),
    selectionGeneration: fences.selection.snapshot(),
    turnGeneration: fences.turn.snapshot(),
    assistantBubble: appendRecoveredAssistant(),
    assistantText: "",
    assistantTextBytes: 0,
    pendingEvents: [],
    stopRequested: false,
    localCancelObserved: false,
    recoveryTurn: recoverable,
  };
  state.activeTurn = activeTurn;
  state.resumingTurn = true;
  state.recoveryFeedback = "Retrying the interrupted request safely...";
  state.recoveryFeedbackKind = "default";
  setTurnState("Resuming");
  renderTaskContext();
  updateComposerState();

  try {
    const openResult = parseTurnOpenResult(
      agent.resumeTurn(recoverable.turn_uuid, (rawEvent) => {
        if (!isCurrentTurn(activeTurn)) return;
        if (!activeTurn.turnID) {
          try {
            activeTurn.pendingEvents.push(parseAgentTurnEvent(rawEvent));
          } catch {
            failActiveTurnProtocol(
              activeTurn,
              "The recovered Agent stream returned an invalid event."
            );
          }
          return;
        }
        handleRawTurnEvent(activeTurn, rawEvent);
      })
    );
    if (!isCurrentTurn(activeTurn) || !isCurrentRecovery(context)) {
      return;
    }
    if (openResult.turnID !== recoverable.turn_uuid) {
      failActiveTurnProtocol(
        activeTurn,
        "The recovered Agent stream returned an invalid turn identifier."
      );
      return;
    }
    activeTurn.turnID = openResult.turnID;
    setTurnState("Working");
  renderTaskContext();
    const pendingEvents = activeTurn.pendingEvents;
    activeTurn.pendingEvents = [];
    for (const pendingEvent of pendingEvents) {
      if (!isCurrentTurn(activeTurn)) break;
      handleParsedTurnEvent(activeTurn, pendingEvent);
    }
  } catch {
    if (!isCurrentTurn(activeTurn) || !isCurrentRecovery(context)) return;
    state.activeTurn = null;
    fences.turn.bump();
    state.resumingTurn = false;
    state.recoveryFeedback = "Recovery could not connect. Select Resume to try again.";
    state.recoveryFeedbackKind = "error";
    activeTurn.assistantBubble.textContent = "Response recovery is waiting to retry.";
    setTurnState("Interrupted");
    updateComposerState();
    setStatus("The interrupted response could not be resumed yet.", "error");
    turnRecoveryResumeButton.focus();
  }
}

async function dismissRecoverableTurn() {
  const recoverable = selectedRecoverableTurn();
  const agent = desktopAgentRecoveryBridge();
  if (
    !recoverable ||
    !agent ||
    state.activeTurn ||
    state.resumingTurn ||
    state.dismissingRecovery ||
    state.recoveringSession
  ) {
    return;
  }
  fences.recovery.bump();
  const context = recoveryContext(recoverable.turn_uuid);
  state.dismissingRecovery = true;
  state.recoveryFeedback = "Canceling the interrupted response...";
  state.recoveryFeedbackKind = "default";
  updateComposerState();
  try {
    const result = parseTurnCancelResult(
      await agent.cancelTurn(recoverable.turn_uuid)
    );
    if (!isCurrentRecovery(context)) return;
    if (result.turnID !== recoverable.turn_uuid) {
      throw new Error("Mismatched recovery cancel result");
    }
    state.dismissingRecovery = false;
    removeRecoverableTurn(recoverable.turn_uuid);
    setTurnState("Ready");
    setStatus(
      result.canceled
        ? "Interrupted response dismissed."
        : "Interrupted response was already dismissed."
    );
    updateComposerState();
    if (!chatInput.disabled) chatInput.focus();
  } catch {
    if (!isCurrentRecovery(context)) return;
    state.dismissingRecovery = false;
    state.recoveryFeedback = "Dismiss failed. Try again when the Sidecar is available.";
    state.recoveryFeedbackKind = "error";
    updateComposerState();
    setStatus("The interrupted response could not be dismissed.", "error");
    turnRecoveryDismissButton.focus();
  }
}

export function keepRecoverableTurnForRetry(activeTurn, feedback, statusMessage) {
  if (!isCurrentTurn(activeTurn) || !activeTurn.recoveryTurn) return;
  settleTurnNarration(activeTurn);
  state.activeTurn = null;
  fences.turn.bump();
  state.resumingTurn = false;
  state.recoveryFeedback = feedback;
  state.recoveryFeedbackKind = "error";
  if (!activeTurn.assistantText) {
    activeTurn.assistantBubble.textContent = "Response recovery is waiting to retry.";
  }
  setTurnState("Interrupted");
  updateComposerState();
  setStatus(statusMessage, "error");
  turnRecoveryResumeButton.focus();
}

export function isThreadBusyResult(result) {
  return result.code === "THREAD_BUSY" || result.subtype === "thread_busy";
}

function localRecoverableTurn(activeTurn, lastErrorKind = "turn_in_progress") {
  return {
    turn_uuid: activeTurn.turnID,
    thread_uuid: activeTurn.threadUUID,
    user_text: activeTurn.userText,
    chat_mode: activeTurn.chatMode,
    state: "interrupted",
    last_error_kind: lastErrorKind,
    updated_at: new Date().toISOString(),
  };
}

function retainLocalRecoverableTurn(recoverable) {
  state.recoverableTurns = [
    recoverable,
    ...state.recoverableTurns.filter(
      (candidate) => candidate.turn_uuid !== recoverable.turn_uuid
    ),
  ];
  renderThreads();
  updateRecoveryState();
}

async function refreshRecoveryAfterInitialBusy(activeTurn, fallback) {
  const expectedSessionGeneration = activeTurn.sessionGeneration;
  try {
    await loadRecoverableTurns(expectedSessionGeneration);
  } catch (error) {
    if (error instanceof SessionChangedError) {
      await handleSessionChanged();
      return;
    }
  }
  if (!fences.session.isCurrent(expectedSessionGeneration)) return;
  let discovered = state.recoverableTurns.some(
    (turn) => turn.turn_uuid === activeTurn.turnID
  );
  if (!discovered) {
    // The busy terminal is authoritative for this immutable local intent. A
    // temporarily stale list must not reopen the composer and invite a second
    // execution with a new UUID.
    retainLocalRecoverableTurn(fallback);
    discovered = false;
  }
  if (!isTurnContextCurrent(activeTurn)) return;
  state.recoveryFeedback = discovered
    ? "The same request is ready for an explicit recovery attempt."
    : "Persistent recovery was not visible yet; the same request is retained locally.";
  state.recoveryFeedbackKind = discovered ? "default" : "error";
  setStatus(
    discovered
      ? "This request is still busy. Select Resume to retry the same request; nothing was started again."
      : "This request is still busy. Its local recovery intent was retained; select Resume to retry the same request. Nothing was started again.",
    "error"
  );
  updateComposerState();
}

export function handleInitialTurnBusy(activeTurn) {
  if (!isCurrentTurn(activeTurn) || activeTurn.recoveryTurn) return;
  const fallback = localRecoverableTurn(activeTurn);
  settleTurnNarration(activeTurn);
  state.activeTurn = null;
  fences.recovery.bump();
  retainLocalRecoverableTurn(fallback);
  state.recoveryFeedback =
    "This request is still busy. Checking its persistent recovery state...";
  state.recoveryFeedbackKind = "error";
  if (!activeTurn.assistantText) {
    activeTurn.assistantBubble.textContent =
      "Response interrupted before it produced text.";
  }
  setTurnState("Interrupted");
  setStatus(
    "This request is still busy; no new execution was started. Checking recovery state...",
    "error"
  );
  updateComposerState();
  void refreshRecoveryAfterInitialBusy(activeTurn, fallback);
}

export function recoveredTurnErrorMessage(result) {
  const label = [result.code, result.subtype].filter(Boolean).join(" · ");
  return label
    ? `The recovered response failed (${label}).`
    : "The recovered response failed.";
}

export function finishActiveTurn(activeTurn, label, canceled) {
  if (!isCurrentTurn(activeTurn)) return;
  // Buffered words land before the outcome that follows them.
  drainStreamBatch();
  clearPendingIndicator(activeTurn);
  settleTurnNarration(activeTurn);
  state.activeTurn = null;
  if (!activeTurn.assistantText) {
    activeTurn.assistantBubble.textContent = canceled
      ? "Generation stopped."
      : "Response completed without text.";
  } else {
    // Completed blocks were typeset as their closing blank lines streamed
    // past; only the still-raw tail is parsed here. A block is never
    // committed while its shape is ambiguous — an unclosed fence, a list
    // still being written — so nothing on screen changes shape now, and the
    // finish-line cost no longer grows with the answer.
    finalizeStreamedAssistant(activeTurn);
    attachMessageActions(
      activeTurn.assistantBubble.parentNode,
      "assistant",
      activeTurn.assistantText
    );
  }
  // How long the turn took, shown where its outcome is shown. Computed once
  // at the terminal — a live ticker would be noise next to the pulsing pill.
  const duration = activeTurn.startedAt ? formatTurnDuration(Date.now() - activeTurn.startedAt) : "";
  setTurnState(label, duration);
  updateComposerState();
  // The rail's live line is the only thing it says about a run, so a settled
  // turn has to put it down here rather than waiting on the workspace refresh
  // below — which returns early on a bridge with no listWorkspaceFiles and
  // would leave "Step 4 · Write" standing over a finished turn.
  renderTaskContext();
  // Freeze the turn's work log. The in-place reconcile keeps the live strip's
  // nodes, but the fallback full repaint erases them — the survivor copy is
  // what re-hangs the story in that case, and re-folds it in both.
  contextState.lastTurnLog = {
    threadUUID: activeTurn.threadUUID,
    steps: contextState.toolActivity.slice(),
    produced: [],
    duration,
  };
  const before = new Map(
    contextState.deliverables.map((f) => [f.path, f.modified_at])
  );
  const context = selectionContext(activeTurn.threadUUID);
  void reconcileCompletedTurn(activeTurn.threadUUID, context, activeTurn).then(() => {
    attachLastTurnLog();
  });
  // A completed turn is the only time new workspace files can exist. What is
  // new or changed against the pre-turn snapshot is what THIS turn made.
  void loadWorkspaceDeliverables(activeTurn.threadUUID).then(() => {
    const log = contextState.lastTurnLog;
    if (!log || log.threadUUID !== activeTurn.threadUUID) return;
    log.produced = contextState.deliverables.filter(
      (f) => before.get(f.path) !== f.modified_at
    );
    attachLastTurnLog();
    // The same diff marks the rows in the Produced section. It is computed
    // after the listing was painted, so the panel has to be told again — the
    // marks are the only reason a cumulative list stays readable.
    renderTaskContext();
  });
}

export function finishActiveTurnWithError(activeTurn, message) {
  if (!isCurrentTurn(activeTurn)) return;
  drainStreamBatch();
  clearPendingIndicator(activeTurn);
  settleTurnNarration(activeTurn);
  state.activeTurn = null;
  if (activeTurn.recoveryTurn) state.resumingTurn = false;
  const errorDuration = activeTurn.startedAt ? formatTurnDuration(Date.now() - activeTurn.startedAt) : "";
  const safeMessage = sanitizeErrorMessage(message || "The Agent turn failed.");
  if (!activeTurn.assistantText) {
    activeTurn.assistantBubble.textContent = safeMessage;
  } else {
    // A failed turn still delivered text, and that text is as much an answer
    // as a successful one's. Leaving it unformatted would make a partial reply
    // look like a different kind of object from a complete one.
    finalizeStreamedAssistant(activeTurn);
  }
  setTurnState("Error", errorDuration);
  renderTaskContext();
  updateComposerState();
  setStatus(safeMessage, "error");
}

export function failActiveTurnProtocol(activeTurn, message) {
  if (!isCurrentTurn(activeTurn)) return;
  if (activeTurn.recoveryTurn) {
    keepRecoverableTurnForRetry(
      activeTurn,
      "Recovery returned invalid data. Select Resume to try the same request again.",
      message
    );
    return;
  }
  requestTurnCancellation(activeTurn.turnID);
  finishActiveTurnWithError(activeTurn, message);
}

async function reconcileCompletedTurn(threadUUID, context, completedTurn = null) {
  const thread = state.threads.find((candidate) => candidate.uuid === threadUUID) || {
    uuid: threadUUID,
  };
  try {
    await Promise.all([
      loadThreads(context.sessionGeneration),
      loadMessagesForSelection(thread, context, completedTurn),
    ]);
  } catch (error) {
    await handleScopedError(error, context);
  }
}

async function refreshRecoveryAfterUnconfirmedCancel(activeTurn) {
  const expectedSessionGeneration = activeTurn.sessionGeneration;
  const fallback = activeTurn.recoveryTurn
    ? {
        ...activeTurn.recoveryTurn,
        last_error_kind: "cancel_unconfirmed",
        updated_at: new Date().toISOString(),
      }
    : localRecoverableTurn(activeTurn, "cancel_unconfirmed");
  try {
    await loadRecoverableTurns(expectedSessionGeneration);
  } catch (error) {
    if (error instanceof SessionChangedError) {
      await handleSessionChanged();
      return;
    }
  }
  if (!fences.session.isCurrent(expectedSessionGeneration)) return;
  const discovered = state.recoverableTurns.some(
    (turn) => turn.turn_uuid === activeTurn.turnID
  );
  if (!discovered) {
    // The local stream is already fenced, while the persistent cancel outcome
    // is ambiguous. Keep the immutable intent visible until the user makes an
    // explicit follow-up choice; never replay it here.
    retainLocalRecoverableTurn(fallback);
  }
  if (!isTurnContextCurrent(activeTurn)) return;
  state.recoveryFeedback = discovered
    ? "Local stop completed; persistent dismissal was not confirmed."
    : "Persistent dismissal was not confirmed; the request is retained locally.";
  state.recoveryFeedbackKind = "error";
  setStatus(
    "The stream stopped locally, but persistent dismissal was not confirmed. Review the interrupted response; it was not replayed.",
    "error"
  );
  updateComposerState();
}

function clearCancelConfirmation(activeTurn) {
  if (state.cancelConfirmationTurnID === activeTurn.turnID) {
    state.cancelConfirmationTurnID = null;
  }
}

async function stopActiveTurn() {
  const activeTurn = state.activeTurn;
  const agent = desktopAgentBridge();
  if (!activeTurn || !agent || activeTurn.stopRequested) return;
  activeTurn.stopRequested = true;
  state.cancelConfirmationTurnID = activeTurn.turnID;
  setTurnState("Stopping");
  updateComposerState();
  try {
    const result = parseTurnCancelResult(await agent.cancelTurn(activeTurn.turnID));
    if (result.turnID !== activeTurn.turnID) {
      throw new Error("Mismatched Agent cancel result");
    }
    clearCancelConfirmation(activeTurn);
    if (!isCurrentTurn(activeTurn)) {
      updateComposerState();
      return;
    }
    if (result.canceled) {
      finishActiveTurn(activeTurn, "Stopped", true);
      return;
    }
    activeTurn.stopRequested = false;
    setTurnState("Working");
    renderTaskContext();
    updateComposerState();
  } catch {
    clearCancelConfirmation(activeTurn);
    if (
      activeTurn.localCancelObserved &&
      fences.session.isCurrent(activeTurn.sessionGeneration)
    ) {
      if (isTurnContextCurrent(activeTurn)) {
        setTurnState(
          "Stopped locally",
          activeTurn.startedAt ? formatTurnDuration(Date.now() - activeTurn.startedAt) : ""
        );
        setStatus(
          "The stream stopped locally, but persistent dismissal was not confirmed. Checking interrupted responses...",
          "error"
        );
      }
      updateComposerState();
      void refreshRecoveryAfterUnconfirmedCancel(activeTurn);
      return;
    }
    if (!isCurrentTurn(activeTurn)) {
      updateComposerState();
      return;
    }
    activeTurn.stopRequested = false;
    setTurnState("Working");
    renderTaskContext();
    updateComposerState();
    setStatus("The Agent turn could not be stopped yet.", "error");
  }
}

// What the app says when no account is connected. One sentence, in one place,
// because two paths write it (boot, and the login machinery landing on idle)
// and they used to disagree — one described a usable local workbench, the
// other a sign-in wall.
//
// A local route says nothing here. The status strip is for what just happened
// and what to do about it; "you are on a local model and nothing leaves this
// machine" is neither — it is true from launch to quit, and a strip that opens
// with a permanent sentence is a strip nobody reads afterwards. It is the
// identity row's subtitle now (renderLocalAccountArea), one line, next to the
// identity it describes. The empty string is what hides the strip: setStatus
// hides the bar when the line is empty.
//
// The other two branches stay, because they are not ambient — with no route
// and no account you cannot send anything, so naming the two ways out is the
// most useful thing the app can say.
function signedOutStatusMessage(authState = state.auth?.state) {
  if (state.localRoute) {
    return "";
  }
  if (authState === "expired") {
    return "The connected account needs signing in again. Your local work is still here.";
  }
  const identity = activeLocalAccount();
  return identity
    ? `Working as ${identity.name} on this machine. Connect an account or a local model to start a conversation.`
    : "Working on this machine. Connect an account or a local model to start a conversation.";
}

function setLoginFormState(visible, submitting = false) {
  loginForm.hidden = !visible;
  state.loginFormOpen = visible;
  // Signing in is a settings errand and the form lives in the settings panel,
  // so asking for it has to bring the panel with it — otherwise the form is
  // shown inside a dialog nobody opened.
  if (visible) openSettingsPanel();
  renderLocalAccountBinding();
  loginEmail.disabled = submitting;
  loginPassword.disabled = submitting;
  loginSubmitButton.disabled = submitting;
  loginCancelButton.disabled = false;
  if (!visible) {
    loginPassword.value = "";
  }
}

function showLoginFailure(result) {
  const keepForm = result.state === "awaiting_password" || result.state === "submitting";
  setLoginFormState(keepForm, result.state === "submitting");
  setStatus(LOGIN_ERROR_MESSAGES[result.error], "error");
  if (keepForm && result.state === "awaiting_password") {
    loginPassword.focus();
  }
}

async function applyLoginTransactionResult(result, pollSubmitting = false, generation = fences.loginOperation.snapshot()) {
  if (!fences.loginOperation.isCurrent(generation)) {
    return;
  }
  if (result.error) {
    showLoginFailure(result);
    if (result.error === "busy" && result.state === "submitting" && pollSubmitting) {
      await waitForLoginTransaction(generation);
    }
    return;
  }
  switch (result.state) {
    case "idle":
      setLoginFormState(false);
      if (state.modesParseSkew) {
        setStatus("App and sidecar are out of sync: the sidecar's answers no longer match this UI. Restart the app; if this persists, reinstall.", "error");
      } else {
        // The login machinery has the last word on this path, so it must say
        // the same thing boot does: no account connected is a state of the
        // app, not a wall in front of it.
        setStatus(signedOutStatusMessage());
      }
      return;
    case "awaiting_password":
      setLoginFormState(true);
      setStatus("Enter your WorkMax email and password to continue.");
      if (loginEmail.value) {
        loginPassword.focus();
      } else {
        loginEmail.focus();
      }
      return;
    case "submitting":
      setLoginFormState(true, true);
      setStatus("Completing sign-in securely...");
      if (pollSubmitting) {
        await waitForLoginTransaction(generation);
      }
      return;
    case "authenticated":
      setLoginFormState(false);
      setStatus("Sign-in complete. Loading your local history...");
      await refresh();
      return;
  }
}

async function waitForLoginTransaction(generation) {
  const auth = desktopAuthBridge();
  if (!auth) {
    showLoginBridgeUnavailable();
    return;
  }
  const started = Date.now();
  while (
    fences.loginOperation.isCurrent(generation) &&
    Date.now() - started < AUTH_POLL_TIMEOUT_MS
  ) {
    await sleep(AUTH_POLL_INTERVAL_MS);
    let result;
    try {
      result = parseLoginTransactionResult(await auth.loginStatus());
    } catch {
      if (fences.loginOperation.isCurrent(generation)) {
        showLoginBridgeUnavailable();
      }
      return;
    }
    if (!fences.loginOperation.isCurrent(generation)) {
      return;
    }
    if (result.state !== "submitting" || result.error) {
      await applyLoginTransactionResult(result, false, generation);
      return;
    }
  }
  if (!fences.loginOperation.isCurrent(generation)) {
    return;
  }
  setLoginFormState(false);
  setStatus(LOGIN_ERROR_MESSAGES.expired, "error");
}

function showLoginBridgeUnavailable() {
  setLoginFormState(false);
  setStatus(LOGIN_ERROR_MESSAGES.unavailable, "error");
}

async function login() {
  const auth = desktopAuthBridge();
  if (!auth) {
    showLoginBridgeUnavailable();
    return;
  }
  const generation = fences.loginOperation.bump();
  if (localAccountConnectButton) localAccountConnectButton.disabled = true;
  setStatus("Preparing a secure sign-in session...");
  try {
    const result = parseLoginTransactionResult(await auth.beginLogin());
    await applyLoginTransactionResult(result, true, generation);
  } catch {
    if (fences.loginOperation.isCurrent(generation)) {
      showLoginBridgeUnavailable();
    }
  } finally {
    if (localAccountConnectButton) localAccountConnectButton.disabled = false;
  }
}

async function submitLogin(event) {
  event.preventDefault();
  const auth = desktopAuthBridge();
  const email = loginEmail.value.trim();
  let password = loginPassword.value;
  loginPassword.value = "";
  const generation = fences.loginOperation.bump();
  try {
    if (!auth) {
      showLoginBridgeUnavailable();
      return;
    }
    if (!validLoginEmail(email) || !validLoginCredential(password, 1, 1024)) {
      setLoginFormState(true);
      setStatus(LOGIN_ERROR_MESSAGES.invalid_request, "error");
      return;
    }
    loginEmail.value = email;
    setLoginFormState(true, true);
    setStatus("Checking your credentials...");
    let result;
    try {
      result = parseLoginTransactionResult(
        await auth.submitLoginPassword({ email, password })
      );
    } catch {
      if (
        fences.loginOperation.isCurrent(generation) &&
        !(await reconcileAmbiguousLoginOutcome(generation))
      ) {
        showLoginBridgeUnavailable();
      }
      return;
    }
    // Main deliberately collapses transport/protocol failures to the closed
    // public `unavailable` result. The password may nevertheless have reached
    // the Server and committed a session before the local response was lost,
    // so reconcile via the non-secret session status before showing failure.
    // Never replay the credential-bearing command.
    if (
      result.error === "unavailable" &&
      (await reconcileAmbiguousLoginOutcome(generation))
    ) {
      return;
    }
    await applyLoginTransactionResult(result, true, generation);
  } finally {
    password = "";
    loginPassword.value = "";
    if (!loginForm.hidden && !loginSubmitButton.disabled) {
      loginPassword.focus();
    }
  }
}

async function reconcileAmbiguousLoginOutcome(generation) {
  try {
    const sessionGeneration = fences.session.snapshot();
    const current = await loadAuthStatus(sessionGeneration);
    if (
      !fences.loginOperation.isCurrent(generation) ||
      !current ||
      current.state !== "authenticated"
    ) {
      return false;
    }
    setLoginFormState(false);
    setStatus(`Authenticated${current.tier ? ` · ${current.tier}` : ""}. Reading local cache.`);
    await Promise.all([
      loadThreads(sessionGeneration),
      loadSkills(sessionGeneration),
    ]);
    return true;
  } catch {
    return false;
  }
}

async function cancelLogin() {
  const auth = desktopAuthBridge();
  loginPassword.value = "";
  if (!auth) {
    showLoginBridgeUnavailable();
    return;
  }
  const generation = fences.loginOperation.bump();
  loginCancelButton.disabled = true;
  try {
    const result = parseLoginTransactionResult(await auth.cancelLogin());
    if (!fences.loginOperation.isCurrent(generation)) {
      return;
    }
    if (result.error && result.error !== "canceled") {
      showLoginFailure(result);
      return;
    }
    setLoginFormState(false);
    setStatus(LOGIN_ERROR_MESSAGES.canceled);
  } catch {
    if (fences.loginOperation.isCurrent(generation)) {
      showLoginBridgeUnavailable();
    }
  } finally {
    loginPassword.value = "";
    loginCancelButton.disabled = false;
  }
}

async function restoreLoginTransaction() {
  const auth = desktopAuthBridge();
  if (!auth) {
    showLoginBridgeUnavailable();
    return;
  }
  const generation = fences.loginOperation.bump();
  try {
    const result = parseLoginTransactionResult(await auth.loginStatus());
    await applyLoginTransactionResult(result, true, generation);
  } catch {
    if (fences.loginOperation.isCurrent(generation)) {
      showLoginBridgeUnavailable();
    }
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export async function refresh() {
  const api = bridge();
  if (!api) {
    state.agentAvailable = false;
    state.createAvailable = false;
    updateComposerState();
    setStatus("This bundled renderer must run inside WorkMax Desktop.", "error");
    return;
  }
  if (state.recoveringSession) {
    return;
  }
  if (hasAttemptedCreateDraft()) {
    const retryable = state.createDraft.retryable === true;
    setStatus(
      retryable
        ? "Retry or cancel the current thread draft before refreshing."
        : "Cancel the current thread draft before refreshing.",
      "error"
    );
    (retryable ? newThreadSubmitButton : newThreadCancelButton).focus();
    updateNewThreadState();
    return;
  }
  fences.session.bump();
  const generation = fences.session.snapshot();
  invalidateActiveTurn(true);
  state.cancelConfirmationTurnID = null;
  fences.recovery.bump();
  state.recoverableTurns = [];
  state.recoveryLoading = false;
  state.resumingTurn = false;
  state.dismissingRecovery = false;
  state.recoveryFeedback = "";
  state.recoveryFeedbackKind = "default";
  fences.create.bump();
  state.createFormOpen = false;
  state.creatingThread = false;
  state.createDraft = null;
  newThreadName.value = "Untitled presentation";
  setCreateFeedback("");
  // A refresh empties the composer but must not lose its words: stash the
  // draft so re-selecting the thread restores it. Only a session change
  // (clearWorkbenchForSessionChange) drops drafts outright.
  stashComposerDraft();
  fences.selection.bump();
  state.selectedThreadUUID = null;
  state.skills = [];
  state.allowedModes = [];
  state.selectedMode = "";
  state.skillsLoading = false;
  state.skillsDegraded = false;
  chatInput.value = "";
  messageList.textContent = "";
  renderSkillOptions();
  emptyState.hidden = false;
  threadPanel.hidden = true;
  setTurnState("Ready");
  updateComposerState();
  // A source build stamps no version, and "sidecar unknown · app unknown"
  // reads as a fault rather than as the absence of a number. Say nothing
  // instead: the line earns its space only when it carries one.
  //
  // "unknown" is the absence, spelled. The shim defaults both fields to that
  // literal when the document carries no version dataset, so a truthiness test
  // alone let the sentence this comment forbids onto the screen of every
  // source build — which is what a screenshot of the rail showed.
  const stamped = (value) => (value && value !== "unknown" ? value : "");
  const versions = [
    stamped(api.sidecarVersion) ? `sidecar ${api.sidecarVersion}` : "",
    stamped(api.appVersion) ? `app ${api.appVersion}` : "",
  ].filter(Boolean);
  runtimeLabel.textContent = versions.join(" · ");
  runtimeLabel.hidden = versions.length === 0;
  setStatus("Checking auth status...");
  try {
    const auth = await loadAuthStatus(generation);
    if (!auth || !fences.session.isCurrent(generation)) return;
    if (auth.state !== "authenticated") {
      state.threads = [];
      renderThreads();
      emptyState.hidden = false;
      threadPanel.hidden = true;
      // No account connected is not the same as no identity. The sidecar
      // resolves this machine's local account either way, so the workbench —
      // history, interrupted turns, accounts — loads unconditionally. Whether
      // a PROMPT can be sent is a separate question, answered by localRoute
      // below and by the composer.
      await loadLocalModes(generation);
      if (!fences.session.isCurrent(generation)) return;
      await Promise.allSettled([
        loadThreads(generation),
        loadRecoverableTurns(generation),
      ]);
      if (!fences.session.isCurrent(generation)) return;
      renderEmptyState();
      updateComposerState();
      setStatus(signedOutStatusMessage(auth.state));
      await restoreLoginTransaction();
      // Last word on purpose: whatever the login machinery wrote above, a
      // version skew is the thing the user actually needs to know about.
      if (state.modesParseSkew && fences.session.isCurrent(generation)) {
        setStatus(
          "App and sidecar are out of sync: the sidecar's answers no longer match this UI. Restart the app; if this persists, reinstall.",
          "error"
        );
      }
      return;
    }
    state.localRoute = false;
    setLoginFormState(false);
    setStatus(`Authenticated${auth.tier ? ` · ${auth.tier}` : ""}. Reading local cache.`);
    // The local identity is loaded here too, not only on the signed-out path:
    // a connected account is a binding ON that identity, and the sidebar has
    // to be able to say which one it is bound to — and offer to disconnect.
    void loadLocalAccounts(generation);
    await Promise.all([
      loadThreads(generation),
      loadSkills(generation),
      loadRecoverableTurns(generation),
    ]);
  } catch (error) {
    if (error instanceof SessionChangedError) {
      await handleSessionChanged();
      return;
    }
    if (fences.session.isCurrent(generation)) {
      setStatus(String(error), "error");
      updateComposerState();
    }
  }
}

// Retry on the status line is the only control that reloads local history by
// hand, and it appears only where reloading is plausibly the answer: an error.
if (statusRetryButton) {
  statusRetryButton.addEventListener("click", () => {
    void refresh();
  });
}
if (statusDismissButton) {
  statusDismissButton.addEventListener("click", () => {
    // Dismissing clears the line rather than hiding a message that is
    // still there: a strip that reappears with stale text on the next
    // unrelated repaint would be worse than one that never closed.
    setStatus("");
  });
}
if (settingsButton) {
  settingsButton.addEventListener("click", () => {
    void openModelSettings();
  });
}
if (settingsCloseButton) {
  settingsCloseButton.addEventListener("click", () => {
    closeSettingsPanel();
  });
}
if (settingsOverlay) {
  settingsOverlay.addEventListener("click", (event) => {
    // The backdrop dismisses; the panel does not. Same rule the quick
    // switcher already taught.
    if (event.target === settingsOverlay) closeSettingsPanel();
  });
}
for (const [choice, button] of APPEARANCE_BUTTONS()) {
  if (!button) continue;
  button.addEventListener("click", () => {
    setTheme(choice);
  });
}
if (modelPreferredRoute) {
  modelPreferredRoute.addEventListener("change", () => {
    updateModelLocalFieldsVisibility();
  });
}
if (modelProtocol) {
  modelProtocol.addEventListener("change", () => {
    updateModelProtocolHint();
  });
}
if (modelSettingsForm) {
  modelSettingsForm.addEventListener("submit", (event) => {
    void submitModelSettings(event);
  });
}
if (modelSettingsCancelButton) {
  modelSettingsCancelButton.addEventListener("click", () => {
    closeSettingsPanel();
  });
}
if (onboardingSignin) {
  onboardingSignin.addEventListener("click", () => {
    void login();
  });
}
if (onboardingLocal) {
  onboardingLocal.addEventListener("click", () => {
    void openModelSettings();
  });
}
loginForm.addEventListener("submit", (event) => {
  void submitLogin(event);
});
loginCancelButton.addEventListener("click", () => {
  void cancelLogin();
});
newThreadButton.addEventListener("click", () => {
  // A plain "New" is not a starter flow; a stale stash would surprise.
  state.starterPrompt = null;
  openNewThreadForm();
});
emptyNewThreadButton.addEventListener("click", () => {
  openNewThreadForm();
});
newThreadForm.addEventListener("submit", (event) => {
  void submitNewThread(event);
});
newThreadForm.addEventListener("keydown", (event) => {
  if (event.repeat) return;
  if (event.key === "Escape") {
    event.preventDefault();
    cancelNewThreadDraft();
    return;
  }
  if (event.key === "Enter") {
    event.preventDefault();
    void submitNewThread(event);
  }
});
newThreadCancelButton.addEventListener("click", () => {
  cancelNewThreadDraft();
});
newThreadName.addEventListener("input", () => {
  updateNewThreadState();
});
newThreadMode.addEventListener("change", () => {
  updateNewThreadState();
});
attachButton.addEventListener("click", () => {
  fileInput.click();
});
fileInput.addEventListener("change", () => {
  const files = Array.from(fileInput.files || []);
  fileInput.value = "";
  for (const file of files) {
    uploadThreadFile(file);
  }
});

threadPanel.addEventListener("dragover", (event) => {
  if (!event.dataTransfer) return;
  event.preventDefault();
  if (state.selectedThreadUUID) {
    threadPanel.classList.add("drop-target");
  }
});
threadPanel.addEventListener("dragleave", () => {
  threadPanel.classList.remove("drop-target");
});
threadPanel.addEventListener("drop", (event) => {
  event.preventDefault();
  threadPanel.classList.remove("drop-target");
  attachDroppedFiles(event.dataTransfer?.files);
});
chatInput.addEventListener("paste", (event) => {
  const files = event.clipboardData?.files;
  if (files && files.length > 0) {
    event.preventDefault();
    attachDroppedFiles(files);
  }
});
chatForm.addEventListener("submit", (event) => {
  submitChat(event);
});
chatInput.addEventListener("input", () => {
  updateComposerState();
  // The box grows with the words up to its cap, like every composer the
  // user already types into; past the cap it scrolls.
  if (chatInput.style) {
    chatInput.style.height = "auto";
    chatInput.style.height = `${Math.min(chatInput.scrollHeight || 0, 220)}px`;
  }
  const capacity = document.querySelector("#composer-capacity");
  if (capacity) {
    const used = utf8ByteLength(chatInput.value) / MAX_CHAT_TEXT_BYTES;
    if (used >= 0.8) {
      capacity.hidden = false;
      capacity.textContent = `${Math.min(100, Math.round(used * 100))}% of the message limit used`;
    } else {
      capacity.hidden = true;
    }
  }
});
chatInput.addEventListener("keydown", (event) => {
  if (
    event.key === "Enter" &&
    (event.metaKey || event.ctrlKey) &&
    !event.repeat
  ) {
    submitChat(event);
  }
});
agentMode.addEventListener("change", () => {
  if (state.allowedModes.includes(agentMode.value) && !state.activeTurn) {
    state.selectedMode = agentMode.value;
  }
  updateComposerState();
});
stopButton.addEventListener("click", () => {
  void stopActiveTurn();
});
turnRecoveryResumeButton.addEventListener("click", () => {
  resumeRecoverableTurn();
});
turnRecoveryDismissButton.addEventListener("click", () => {
  void dismissRecoverableTurn();
});
if (localAccountRow) {
  localAccountRow.addEventListener("click", () => {
    toggleLocalAccountPanel();
  });
}
if (localAccountCreateForm) {
  localAccountCreateForm.addEventListener("submit", (event) => {
    void submitCreateLocalAccount(event);
  });
}
if (localAccountConnectButton) {
  localAccountConnectButton.addEventListener("click", () => {
    void login();
  });
}
if (localAccountDisconnectButton) {
  localAccountDisconnectButton.addEventListener("click", () => {
    void disconnectCloudAccount();
  });
}

renderAppearanceChoice();
// The account binding is shown in Settings, which can be opened before any
// bridge has answered — so it starts out saying the true thing rather than
// the markup's placeholder.
renderLocalAccountBinding();

void refresh();

buildStarterCards();

// Paint the panel once on load. Without this it keeps whatever static markup
// index.html shipped with — which looks like a rendered panel that is simply
// empty, rather than one that never ran.
renderTaskContext();
if (renameThreadButton) {
  renameThreadButton.addEventListener("click", () => {
    openRenameForm();
  });
}
if (exportThreadButton) {
  exportThreadButton.addEventListener("click", () => {
    void exportSelectedThread();
  });
}
if (renameThreadForm) {
  renameThreadForm.addEventListener("submit", (event) => {
    event.preventDefault();
    void submitRename().finally(() => {
      closeRenameForm();
    });
  });
}
if (renameThreadCancel) {
  renameThreadCancel.addEventListener("click", () => {
    closeRenameForm();
  });
}

// Global keys. ⌘K opens the switcher; Escape stops a streaming turn — the
// same act as the Stop button, reachable without the mouse. Escape prefers
// the switcher when it is open, and never touches a turn from inside form
// fields where it already means "abandon this input".
document.addEventListener("keydown", (event) => {
  if (event.repeat) return;
  if ((event.metaKey || event.ctrlKey) && (event.key === "k" || event.key === "K")) {
    event.preventDefault();
    if (quickSwitcher && !quickSwitcher.hidden) closeQuickSwitcher();
    else openQuickSwitcher();
    return;
  }
  if (event.key === "Escape") {
    if (quickSwitcher && !quickSwitcher.hidden) {
      closeQuickSwitcher();
      return;
    }
    if (settingsOverlay && !settingsOverlay.hidden) {
      closeSettingsPanel();
      return;
    }
    if (state.activeTurn && !state.activeTurn.stopRequested) {
      void stopActiveTurn();
    }
  }
});
if (openWorkspaceButton) {
  openWorkspaceButton.addEventListener("click", () => {
    const threadUUID = state.selectedThreadUUID;
    const agent = window.desktopBridge?.agent;
    if (!threadUUID || typeof agent?.revealWorkspace !== "function") return;
    void agent.revealWorkspace(threadUUID).then(
      (result) => {
        if (!result?.ok) setStatus("Could not open the workspace folder.", "error");
      },
      () => setStatus("Could not open the workspace folder.", "error")
    );
  });
}
if (threadSearchInput) {
  threadSearchInput.addEventListener("input", () => {
    state.threadQuery = threadSearchInput.value;
    renderThreads();
    scheduleContentSearch();
  });
}

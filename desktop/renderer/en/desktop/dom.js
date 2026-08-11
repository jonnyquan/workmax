// The element handles, and nothing else.
//
// This module imports nothing. That is not tidiness, it is the property the
// rest of the split rests on: ES module bodies run depth-first from the entry,
// so a module with no imports is evaluated before any module that names it,
// and a `const` element handle can therefore never be read from another
// module's top-level wiring while it is still in its temporal dead zone. Put a
// single import in here and that guarantee is gone.
//
// Every query is against markup index.html actually ships. A missing id yields
// null rather than throwing, and the callers that can survive that check for
// it — see the `if (renameThreadButton)` guards next to their listeners.
export const statusCard = document.querySelector("#status-card");
// The strip the status line sits in, and its two ways out. An error the user
// cannot put down stops being information and becomes furniture, so it gets a
// dismiss — and the action that would actually fix it, which for every error
// this app raises is "ask the sidecar again".
export const statusBar = document.querySelector("#status-bar");
export const statusRetryButton = document.querySelector("#status-retry");
export const statusDismissButton = document.querySelector("#status-dismiss");
export const localAccountRow = document.querySelector("#local-account-row");
export const localAccountAvatar = document.querySelector("#local-account-avatar");
export const localAccountNameEl = document.querySelector("#local-account-name");
export const localAccountPanel = document.querySelector("#local-account-panel");
export const localAccountListEl = document.querySelector("#local-account-list");
export const localAccountHint = document.querySelector("#local-account-hint");
export const localAccountBindingState = document.querySelector("#local-account-binding-state");
export const localAccountConnectButton = document.querySelector("#local-account-connect");
export const localAccountDisconnectButton = document.querySelector("#local-account-disconnect");
export const localAccountSwitchNote = document.querySelector("#local-account-switch-note");
export const localAccountCreateForm = document.querySelector("#local-account-create-form");
export const localAccountNameInput = document.querySelector("#local-account-name-input");
export const runtimeLabel = document.querySelector("#runtime-label");
export const refreshButton = document.querySelector("#refresh-button");
// Settings is a modal, not a column. A route, a protocol and an API key used
// to live between the conversation list and the account row; the rail is
// navigation, and this is the door out of it.
export const settingsButton = document.querySelector("#settings-button");
export const settingsOverlay = document.querySelector("#settings-overlay");
export const settingsCloseButton = document.querySelector("#settings-close-button");
export const appearanceSystemButton = document.querySelector("#appearance-system");
export const appearanceLightButton = document.querySelector("#appearance-light");
export const appearanceDarkButton = document.querySelector("#appearance-dark");
export const modelSettingsForm = document.querySelector("#model-settings-form");
export const modelPreferredRoute = document.querySelector("#model-preferred-route");
export const modelLocalFields = document.querySelector("#model-local-fields");
export const modelOfficialFields = document.querySelector("#model-official-fields");
export const modelOfficialID = document.querySelector("#model-official-id");
export const modelOfficialNote = document.querySelector("#model-official-note");
export const modelProtocol = document.querySelector("#model-protocol");
export const modelBaseURL = document.querySelector("#model-base-url");
export const modelID = document.querySelector("#model-id");
export const modelAPIKey = document.querySelector("#model-api-key");
export const modelClearAPIKey = document.querySelector("#model-clear-api-key");
export const modelKeyStatus = document.querySelector("#model-key-status");
export const modelSettingsError = document.querySelector("#model-settings-error");
export const modelSettingsSubmitButton = document.querySelector("#model-settings-submit-button");
export const modelSettingsCancelButton = document.querySelector("#model-settings-cancel-button");
export const loginForm = document.querySelector("#login-form");
export const loginEmail = document.querySelector("#login-email");
export const loginPassword = document.querySelector("#login-password");
export const loginSubmitButton = document.querySelector("#login-submit-button");
export const loginCancelButton = document.querySelector("#login-cancel-button");
export const newThreadButton = document.querySelector("#new-thread-button");
export const newThreadForm = document.querySelector("#new-thread-form");
export const newThreadName = document.querySelector("#new-thread-name");
export const newThreadMode = document.querySelector("#new-thread-mode");
export const newThreadError = document.querySelector("#new-thread-error");
export const newThreadSubmitButton = document.querySelector("#new-thread-submit-button");
export const newThreadCancelButton = document.querySelector("#new-thread-cancel-button");
export const threadList = document.querySelector("#thread-list");
export const emptyState = document.querySelector("#empty-state");
export const emptyTitle = document.querySelector("#empty-title");
export const emptyDescription = document.querySelector("#empty-description");
export const emptyNewThreadButton = document.querySelector("#empty-new-thread-button");
export const threadPanel = document.querySelector("#thread-panel");
export const threadTitle = document.querySelector("#thread-title");
export const threadMeta = document.querySelector("#thread-meta");
export const messageList = document.querySelector("#message-list");
export const messageViewport = document.querySelector("#message-viewport");
export const turnRecoveryCard = document.querySelector("#turn-recovery-card");
export const turnRecoveryDescription = document.querySelector("#turn-recovery-description");
export const turnRecoveryPrompt = document.querySelector("#turn-recovery-prompt");
export const turnRecoveryFeedback = document.querySelector("#turn-recovery-feedback");
export const turnRecoveryResumeButton = document.querySelector("#turn-recovery-resume-button");
export const turnRecoveryDismissButton = document.querySelector("#turn-recovery-dismiss-button");
export const chatForm = document.querySelector("#chat-form");
export const agentMode = document.querySelector("#agent-mode");
export const composerStatus = document.querySelector("#composer-status");
export const chatInput = document.querySelector("#chat-input");
export const stopButton = document.querySelector("#stop-button");
export const sendButton = document.querySelector("#send-button");
export const turnState = document.querySelector("#turn-state");
export const fileInput = document.querySelector("#file-input");
export const attachButton = document.querySelector("#attach-button");
export const attachmentChips = document.querySelector("#attachment-chips");
// Cached once: scrollMessagesToEnd runs on every streamed frame, and a
// querySelector per frame is a forced walk the stream never needed.
export const jumpLatestButton = document.querySelector("#jump-latest");
export const onboardingSignin = document.querySelector("#onboarding-signin");
export const onboardingLocal = document.querySelector("#onboarding-local");

// Filtering is local and instant: the thread list is already in memory, so
// there is nothing to debounce and no request to make.
export const renameThreadButton = document.querySelector("#rename-thread-button");
export const exportThreadButton = document.querySelector("#export-thread-button");
export const renameThreadForm = document.querySelector("#rename-thread-form");
export const renameThreadCancel = document.querySelector("#rename-thread-cancel");

// --- Quick switcher (⌘K) ---------------------------------------------------
// The sidebar is where conversations live; the switcher is how you reach one
// without leaving the keyboard. Same data, same filter as the sidebar search.

export const quickSwitcher = document.querySelector("#quick-switcher");
export const quickSwitcherInput = document.querySelector("#quick-switcher-input");
export const quickSwitcherList = document.querySelector("#quick-switcher-list");

export const openWorkspaceButton = document.querySelector("#open-workspace-button");

export const threadSearchInput = document.querySelector("#thread-search");

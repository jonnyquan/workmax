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
// There is no #local-account-panel any more. The identity list used to float
// over the rail in a popover that also carried a rename form and a delete that
// erases everything an identity owns; it lives in Settings › Account now, and
// the row above is what opens it.
export const localAccountListEl = document.querySelector("#local-account-list");
export const localAccountHint = document.querySelector("#local-account-hint");
export const localAccountBindingState = document.querySelector("#local-account-binding-state");
export const localAccountConnectButton = document.querySelector("#local-account-connect");
export const localAccountDisconnectButton = document.querySelector("#local-account-disconnect");
export const localAccountSwitchNote = document.querySelector("#local-account-switch-note");
export const localAccountCreateForm = document.querySelector("#local-account-create-form");
export const localAccountNameInput = document.querySelector("#local-account-name-input");
// The version line, and the About row it sits in. Both hide together: a source
// build stamps no number, and "Version —" is a row that exists to say nothing.
export const runtimeLabel = document.querySelector("#runtime-label");
export const aboutVersionRow = document.querySelector("#about-version-row");
// There is no refresh handle here on purpose. Reloading local history is not
// something the user has to ask for — every mutation repaints the list, and
// the one state where asking again is the answer is an error, which offers
// Retry on the status line and calls the same refresh().
// Settings is a modal, not a column. A route, a protocol and an API key used
// to live between the conversation list and the account row; the rail is
// navigation, and this is the door out of it.
export const settingsButton = document.querySelector("#settings-button");
export const settingsOverlay = document.querySelector("#settings-overlay");
export const settingsCloseButton = document.querySelector("#settings-close-button");
// The section list and the sections it shows, paired by name in renderer.js
// (SETTINGS_SECTIONS). Static handles rather than a built list: the sections
// are markup, not data, and a nav generated from an array would be four
// buttons of JavaScript to avoid four lines of HTML.
export const settingsNavModel = document.querySelector("#settings-nav-model");
export const settingsNavAccount = document.querySelector("#settings-nav-account");
export const settingsNavAppearance = document.querySelector("#settings-nav-appearance");
export const settingsNavAbout = document.querySelector("#settings-nav-about");
export const settingsPanelAccount = document.querySelector("#settings-panel-account");
export const settingsPanelAppearance = document.querySelector("#settings-panel-appearance");
export const settingsPanelAbout = document.querySelector("#settings-panel-about");
export const appearanceSystemButton = document.querySelector("#appearance-system");
export const appearanceLightButton = document.querySelector("#appearance-light");
export const appearanceDarkButton = document.querySelector("#appearance-dark");
export const densityCompactButton = document.querySelector("#density-compact");
export const densityStandardButton = document.querySelector("#density-standard");
export const densityComfortableButton = document.querySelector("#density-comfortable");
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
// The one search in the app. It used to share the job with a permanently
// mounted field in the rail: that one filtered titles in place and asked the
// sidecar for message bodies, this one filtered the same titles and offered
// actions. Neither was complete and both were called "search". The field is
// gone, its message-body half moved in here, and the rail carries an icon that
// opens this — see threads.js.

export const quickSwitcher = document.querySelector("#quick-switcher");
export const quickSwitcherInput = document.querySelector("#quick-switcher-input");
export const quickSwitcherList = document.querySelector("#quick-switcher-list");
export const sidebarSearchButton = document.querySelector("#sidebar-search-button");

export const openWorkspaceButton = document.querySelector("#open-workspace-button");

// --- The mind (心智体) --------------------------------------------------------
// The title-bar icon and the right-column panel it shows. The icon is a
// window-level control like the two folds beside it, which is why it lives up
// there and not in the composer: a mind is not a property of the conversation
// on screen. The panel has no close button of its own for the same reason the
// workspace panel has none — the switch that hides a column cannot live inside
// it, so both switches sit in the title bar.
export const mindButton = document.querySelector("#mind-button");
export const mindPanel = document.querySelector("#mind-panel");
export const mindSubtitle = document.querySelector("#mind-subtitle");
export const mindRoster = document.querySelector("#mind-roster");
export const mindRosterError = document.querySelector("#mind-roster-error");
export const mindCreateForm = document.querySelector("#mind-create-form");
export const mindCreateName = document.querySelector("#mind-create-name");
export const mindCreateButton = document.querySelector("#mind-create-button");
export const mindAnatomyMeta = document.querySelector("#mind-anatomy-meta");
export const mindBrainValue = document.querySelector("#mind-brain-value");
export const mindBrainNote = document.querySelector("#mind-brain-note");
export const mindSkillsValue = document.querySelector("#mind-skills-value");
export const mindSkillsNote = document.querySelector("#mind-skills-note");
export const mindMemoryValue = document.querySelector("#mind-memory-value");
export const mindMemoryNote = document.querySelector("#mind-memory-note");
export const mindMemorySection = document.querySelector("#mind-memory-section");
export const mindMemoryMeta = document.querySelector("#mind-memory-meta");
export const mindMemoryList = document.querySelector("#mind-memory-list");
export const mindFeedForm = document.querySelector("#mind-feed-form");
export const mindFeedTitle = document.querySelector("#mind-feed-title");
export const mindFeedText = document.querySelector("#mind-feed-text");
export const mindFeedError = document.querySelector("#mind-feed-error");
export const mindFeedStatus = document.querySelector("#mind-feed-status");
export const mindFeedButton = document.querySelector("#mind-feed-button");

// --- Folding the columns ----------------------------------------------------
// Both folds are one control each, and both controls live in the window's
// title bar — outside the columns they hide, so the same button that folds a
// column away is still there to bring it back. The rail's fold used to be a
// pair (collapse inside the rail, expand in the main column's chrome); the
// pair collapsed into one toggle when the controls moved up. The right
// column's switch is really two — this one and the brain above — because that
// column has two occupants; see renderer.js, "The right column".
export const titlebar = document.querySelector("#titlebar");
export const sidebarCollapseButton = document.querySelector("#sidebar-collapse-button");
export const contextPanelButton = document.querySelector("#context-panel-button");
// The name and everything attached to it, as one block: renaming replaces the
// line rather than opening a form underneath it.
export const threadTitleRow = document.querySelector("#thread-title-row");

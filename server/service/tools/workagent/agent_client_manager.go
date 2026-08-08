package workagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"server/config"
	"server/globals"
	workagentModel "server/model/workagent"

	"github.com/google/uuid"
	claudecode "github.com/jonnyquan/claude-agent-sdk-go"
)

// AgentClientManager manages singleton Agent client and workspace cache
type AgentClientManager struct {
	client *ClaudeAgentGoClient
	// mu guards both client and config — they are swapped together by
	// ReinitializeWithAccount and read together by chat-path callers.
	mu             sync.RWMutex
	workspaceCache sync.Map // threadUUID -> workspacePath
	config         *config.ClaudeAgent
	workspaceRoot  string
	initOnce       sync.Once
	initError      error
}

var (
	globalManager *AgentClientManager
	managerOnce   sync.Once
)

// GetAgentClientManager returns the singleton AgentClientManager
func GetAgentClientManager() *AgentClientManager {
	managerOnce.Do(func() {
		globalManager = &AgentClientManager{
			workspaceCache: sync.Map{},
		}
	})
	return globalManager
}

// Initialize initializes the Agent client with configuration (called at startup)
func (m *AgentClientManager) Initialize(agentConfig *config.ClaudeAgent) error {
	m.initOnce.Do(func() {
		if agentConfig == nil || !agentConfig.Enabled {
			m.initError = fmt.Errorf("Agent not enabled in configuration")
			globals.Warn("[AgentManager] Claude Agent is not enabled")
			return
		}

		m.config = agentConfig

		// Resolve workspace root directory
		currentDir, err := os.Getwd()
		if err != nil {
			m.initError = fmt.Errorf("failed to get current directory: %w", err)
			globals.Error(fmt.Sprintf("[AgentManager] %v", m.initError))
			return
		}

		workspaceRoot := strings.TrimSpace(agentConfig.WorkspaceRoot)
		if workspaceRoot == "" {
			workspaceRoot = "agent_workspace"
		}
		if !filepath.IsAbs(workspaceRoot) {
			workspaceRoot = filepath.Join(currentDir, workspaceRoot)
		}
		m.workspaceRoot = filepath.Clean(workspaceRoot)

		// Create workspace root directory
		if err := os.MkdirAll(m.workspaceRoot, 0o755); err != nil {
			m.initError = fmt.Errorf("failed to create workspace root: %w", err)
			globals.Error(fmt.Sprintf("[AgentManager] %v", m.initError))
			return
		}

		// Note: Client is NOT created here - it will be created when an account is assigned via ReinitializeWithAccount
		globals.Info(fmt.Sprintf("[AgentManager] Initialized successfully: workspace=%s, max_turns=%d (waiting for account)",
			m.workspaceRoot, agentConfig.MaxTurns))
	})

	return m.initError
}

// WorkspaceRoot returns the configured workspace root directory.
func (m *AgentClientManager) WorkspaceRoot() string {
	return m.workspaceRoot
}

// buildEnvVarsFromAccount builds environment variables from agent account
func (m *AgentClientManager) buildEnvVarsFromAccount(account *workagentModel.AgentAccount) map[string]string {
	envVars := make(map[string]string)

	// Use an isolated HOME for backend Agent runtime to avoid user-level
	// Claude plugins/MCP configs causing startup side effects in headless service.
	if m.workspaceRoot != "" {
		runtimeHome := filepath.Join(m.workspaceRoot, ".claude_runtime_home")
		if err := os.MkdirAll(runtimeHome, 0o755); err != nil {
			globals.Warn(fmt.Sprintf("[AgentManager] Failed to create isolated runtime HOME (%s): %v", runtimeHome, err))
		} else {
			envVars["HOME"] = runtimeHome
			envVars["USERPROFILE"] = runtimeHome // Cross-platform compatibility
			envVars["CLAUDE_HOME"] = runtimeHome // Best-effort (if CLI honors it)
		}
	}

	if strings.TrimSpace(account.BaseURL) != "" {
		envVars["ANTHROPIC_BASE_URL"] = account.BaseURL
		envVars["CLAUDE_API_BASE_URL"] = account.BaseURL
		envVars["CLAUDE_CODE_BASE_URL"] = account.BaseURL
	}

	if account.APIKey != "" {
		envVars["ANTHROPIC_API_KEY"] = account.APIKey
		envVars["ANTHROPIC_AUTH_TOKEN"] = account.APIKey
		envVars["CLAUDE_API_KEY"] = account.APIKey
		envVars["CLAUDE_CODE_AUTH_TOKEN"] = account.APIKey
	}

	// Support skipping CLI version check in development/testing environments
	if skipCheck := os.Getenv("CLAUDE_AGENT_SDK_SKIP_VERSION_CHECK"); skipCheck != "" {
		envVars["CLAUDE_AGENT_SDK_SKIP_VERSION_CHECK"] = skipCheck
	}

	// Set Anthropic SDK logging level from global config
	m.mu.RLock()
	if m.config != nil && m.config.AnthropicLog != "" {
		envVars["ANTHROPIC_LOG"] = m.config.AnthropicLog
	}
	m.mu.RUnlock()

	// Disable Fast mode capability probing in backend service runtime.
	// This avoids non-essential fast-mode status checks and related 401 noise.
	envVars["CLAUDE_CODE_DISABLE_FAST_MODE"] = "1"

	return envVars
}

// GetClient returns the initialized Agent client
func (m *AgentClientManager) GetClient() (*ClaudeAgentGoClient, error) {
	if m.initError != nil {
		return nil, m.initError
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.client == nil {
		return nil, fmt.Errorf("Agent client not initialized")
	}
	return m.client, nil
}

// EnsureThreadWorkspace ensures thread workspace exists and returns the path
// Directory structure: workspace_root/uid/{uid}/YYYYMMDD/thread_{uuid}/
// Uses cache to avoid repeated filesystem operations
func normalizeThreadWorkspaceUUID(threadUUID string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(threadUUID))
	if err != nil {
		return "", fmt.Errorf("invalid thread uuid")
	}
	return parsed.String(), nil
}

func userScopedThreadWorkspaceCacheKey(uid int, threadUUID string) string {
	return fmt.Sprintf("%d:%s", uid, threadUUID)
}

func (m *AgentClientManager) EnsureThreadWorkspace(uid int, threadUUID string) (string, error) {
	normalizedThreadUUID, err := normalizeThreadWorkspaceUUID(threadUUID)
	if err != nil {
		return "", err
	}
	cacheKey := userScopedThreadWorkspaceCacheKey(uid, normalizedThreadUUID)

	// Check cache first
	if cachedPath, ok := m.workspaceCache.Load(cacheKey); ok {
		return cachedPath.(string), nil
	}

	// Cold-cache reattach: a thread created on day N and reopened on day
	// N+K (or after a process restart) must resolve to its existing dated
	// directory, not a freshly-built today-dated one. Without this glob,
	// EnsureThreadWorkspace would MkdirAll an empty parallel workspace at
	// today's date while the real files remained at the creation date —
	// uploads/outputs would silently disappear from the user's view.
	pattern := filepath.Join(m.workspaceRoot, "uid", strconv.Itoa(uid), "*", fmt.Sprintf("thread_%s", normalizedThreadUUID))
	if matches, globErr := filepath.Glob(pattern); globErr == nil && len(matches) > 0 {
		existing := filepath.Clean(matches[0])
		for _, sub := range []string{"uploads", "outputs"} {
			if mkErr := os.MkdirAll(filepath.Join(existing, sub), 0o755); mkErr != nil {
				return "", fmt.Errorf("failed to ensure subdirectory %s: %w", sub, mkErr)
			}
		}
		m.workspaceCache.Store(cacheKey, existing)
		return existing, nil
	}

	// Create workspace directory structure with date prefix
	datePrefix := time.Now().Format("20060102")
	threadWorkspace := filepath.Join(m.workspaceRoot, "uid", strconv.Itoa(uid), datePrefix, fmt.Sprintf("thread_%s", normalizedThreadUUID))

	// Create subdirectories
	dirs := []string{
		threadWorkspace,
		filepath.Join(threadWorkspace, "uploads"),
		filepath.Join(threadWorkspace, "outputs"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Cache the workspace path
	m.workspaceCache.Store(cacheKey, threadWorkspace)

	globals.Info(fmt.Sprintf("[AgentManager] Created thread workspace: %s", threadWorkspace))
	return threadWorkspace, nil
}

// GetWorkspaceRelativePath returns the relative path from workspace root
func (m *AgentClientManager) GetWorkspaceRelativePath(absolutePath string) string {
	relPath, err := filepath.Rel(m.workspaceRoot, absolutePath)
	if err != nil {
		return ""
	}
	return relPath
}

// GetThreadWorkspace returns cached workspace path without creating
func (m *AgentClientManager) GetThreadWorkspace(uid int, threadUUID string) (string, bool) {
	normalizedThreadUUID, err := normalizeThreadWorkspaceUUID(threadUUID)
	if err != nil {
		return "", false
	}
	if cachedPath, ok := m.workspaceCache.Load(userScopedThreadWorkspaceCacheKey(uid, normalizedThreadUUID)); ok {
		return cachedPath.(string), true
	}
	return "", false
}

// InvalidateWorkspace removes workspace from cache (for cleanup)
func (m *AgentClientManager) InvalidateWorkspace(uid int, threadUUID string) {
	normalizedThreadUUID, err := normalizeThreadWorkspaceUUID(threadUUID)
	if err != nil {
		return
	}
	m.workspaceCache.Delete(userScopedThreadWorkspaceCacheKey(uid, normalizedThreadUUID))
}

// GetConfig returns the agent configuration
func (m *AgentClientManager) GetConfig() *config.ClaudeAgent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config
}

// IsEnabled returns whether Agent is enabled
func (m *AgentClientManager) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config != nil && m.config.Enabled && m.initError == nil
}

// BuildClientForAccount constructs a fresh ClaudeAgentGoClient for the
// given account WITHOUT mutating singleton state.
//
// Chat-path callers must use this instead of ReinitializeWithAccount+GetClient:
// concurrent requests rightfully disagree about which account they
// need, and a shared singleton cannot honour all of them without racing.
// The returned client is owned by the caller for the duration of its request.
//
// Note: per-turn config (agent mode, custom prompt, workspace path)
// is supplied separately to ProcessAgentConversationWithRetry; this
// function only handles client-level config (account credentials,
// global limits, sandbox settings).
func (m *AgentClientManager) BuildClientForAccount(account *workagentModel.AgentAccount) (*ClaudeAgentGoClient, *config.ClaudeAgent, error) {
	if account == nil {
		return nil, nil, fmt.Errorf("account is nil")
	}

	globalConfig := globals.GraConf.ClaudeAgent
	if globalConfig == nil {
		return nil, nil, fmt.Errorf("global agent config is nil")
	}

	if err := m.ensureWorkspaceRoot(globalConfig); err != nil {
		return nil, nil, err
	}

	envVars := m.buildEnvVarsFromAccount(account)

	clientCfg := &ClaudeAgentGoConfig{
		Model: "", // Empty - SDK will use default model
		AllowedTools: []string{
			"Read",
			"Write",
			"MultiWrite",
			"Edit",
			"Bash",
			"Search",
			"Browse",
			"WebSearch",
			"WebFetch",
			"Glob",
			"Grep",
			"Python",
		},
		PermissionMode:    claudecode.PermissionModeDefault,
		MaxTurns:          globalConfig.MaxTurns,
		MaxOutputTokens:   globalConfig.MaxOutputTokens,
		MaxThinkingTokens: globalConfig.MaxThinkingTokens,
		MaxBudgetUSD:      globalConfig.MaxBudgetUSD,
		Timeout:           time.Duration(globalConfig.Timeout) * time.Second,
		Sandbox:           translateSandboxConfig(globalConfig.Sandbox),
		SettingSources:    translateSettingSources(globalConfig.SettingSources),
		Env:               envVars,
	}

	if clientCfg.MaxTurns <= 0 {
		clientCfg.MaxTurns = 10
	}

	return NewClaudeAgentGoClient(clientCfg), globalConfig, nil
}

// ensureWorkspaceRoot lazily materialises the workspace root directory.
// Safe to call concurrently — the critical mutation happens under m.mu.
func (m *AgentClientManager) ensureWorkspaceRoot(globalConfig *config.ClaudeAgent) error {
	if m.workspaceRoot != "" {
		return nil
	}

	currentDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	wsRoot := strings.TrimSpace(globalConfig.WorkspaceRoot)
	if wsRoot == "" {
		wsRoot = "agent_workspace"
	}
	if !filepath.IsAbs(wsRoot) {
		wsRoot = filepath.Join(currentDir, wsRoot)
	}
	resolved := filepath.Clean(wsRoot)

	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return fmt.Errorf("failed to create workspace root: %w", err)
	}

	m.mu.Lock()
	if m.workspaceRoot == "" {
		m.workspaceRoot = resolved
	}
	m.mu.Unlock()
	return nil
}

// ReinitializeWithAccount swaps the singleton's cached client/config to match
// the given account. This is appropriate for *serial* operations that want the
// next call to GetClient() to see the new state — e.g. account-switch triggered
// by the pool, or isolated test managers. The chat request path MUST use
// BuildClientForAccount instead; otherwise two concurrent requests can observe
// each other's client.
func (m *AgentClientManager) ReinitializeWithAccount(account *workagentModel.AgentAccount) error {
	newClient, globalConfig, err := m.BuildClientForAccount(account)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.client = newClient
	m.config = globalConfig
	m.mu.Unlock()

	globals.Info(fmt.Sprintf("[AgentManager] Reinitialized with account: %s (ID: %d, Provider: %s)",
		account.Name, account.ID, account.Provider))

	return nil
}

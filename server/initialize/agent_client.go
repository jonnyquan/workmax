package initialize

import (
	"fmt"

	"server/globals"
	"server/service/tools/workagent"
)

// InitAgentClient initializes the Agent Client Manager singleton
func InitAgentClient() {
	// Initialize error notifier with admin email from config
	adminEmail := globals.GraConf.System.AdminEmail
	workagent.InitAgentErrorNotifier(adminEmail)

	agentConfig := globals.GraConf.ClaudeAgent
	if agentConfig == nil || !agentConfig.Enabled {
		globals.Warn("[Initialize] Claude Agent is disabled in config")
		return
	}

	// Step 1: Initialize AgentClientManager with config (sets m.config, m.workspaceRoot, etc.)
	manager := workagent.GetAgentClientManager()
	if err := manager.Initialize(agentConfig); err != nil {
		globals.Error(fmt.Sprintf("[Initialize] Failed to initialize AgentClientManager: %v", err))
		return
	}

	// Step 2: Initialize account pool (load from database)
	pool := workagent.GetAgentAccountPool()
	if err := pool.Initialize(); err != nil {
		globals.Error(fmt.Sprintf("[Initialize] Failed to initialize account pool: %v", err))
		globals.Warn("[Initialize] Agent mode disabled. Please configure accounts in w_workagent_account table")
		return
	}

	// Step 3: Get active account and reinitialize client with it
	activeAccount, err := pool.GetActiveAccount()
	if err != nil {
		globals.Error(fmt.Sprintf("[Initialize] No active account available: %v", err))
		globals.Warn("[Initialize] Agent mode disabled. Please add at least one account via admin API")
		return
	}

	if err := manager.ReinitializeWithAccount(activeAccount); err != nil {
		globals.Error(fmt.Sprintf("[Initialize] Failed to initialize with active account: %v", err))
		return
	}

	globals.Info(fmt.Sprintf("[Initialize] Agent initialized successfully, active account: %s (ID: %d, Priority: %d)",
		activeAccount.Name, activeAccount.ID, activeAccount.Priority))
}


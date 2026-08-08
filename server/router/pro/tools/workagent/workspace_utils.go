package workagent

import (
	workagentService "server/service/tools/workagent"
)

// workspaceRoot delegates to the service-layer resolver so the router and
// service layers never disagree about which directory is the workspace.
func workspaceRoot() string {
	return workagentService.ResolveWorkspaceRoot()
}

func workspaceRoutePrefix() string {
	return workagentService.WorkspaceRoutePrefix()
}

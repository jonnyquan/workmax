package workagent

import (
	service "server/service/tools/workagent"
)

// workspaceRoot delegates to the service-layer resolver so the api and
// service layers never disagree about which directory is the workspace.
//
// The four public-shaped shims (WorkspaceRoutePrefix /
// SanitizeWorkspacePath / ReplaceWorkspacePaths / SanitizePathForClient)
// that used to live here were collapsed — every consumer in this
// package already imports `workagentService` for other reasons, so an
// extra api-side hop with identical signature added no value and
// drifted between the two layers if a parameter ever changed.
// Consumers now call `workagentService.X(...)` directly.
func workspaceRoot() string {
	return service.ResolveWorkspaceRoot()
}

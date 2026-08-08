package canvas

import workagentService "server/service/tools/workagent"

// init registers the canvas surface with workagent's global
// SurfaceRegistry so canvas_agent_api.go::HandleCanvasAgentChat
// can look it up by AgentMode="canvas" at request time (see the
// LookupSurface call there). Side-effect import is the idiomatic
// Go pattern for surface plugins; the surface itself is stateless
// so registering Surface{} (a value, not a pointer) is safe.
func init() {
	workagentService.RegisterSurface(Surface{})
}

package tools

import (
	"server/service/tools/workagent"
)

// ToolsServiceGroup is the canonical singleton container for tool-level
// services. The CanvasAgent surface used to live here as a Service Group;
// after the handler relocation (api/pro/tools/canvas_agent_api.go), the
// canvasagent package only exports domain helpers — no service struct
// to wire up — so it no longer needs a slot here.
type ToolsServiceGroup struct {
	WorkAgentService workagent.ServiceGroup
	GeneratorService GeneratorService
}

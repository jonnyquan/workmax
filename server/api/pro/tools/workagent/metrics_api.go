package workagent

import (
	"server/model/common/response"
	workagentService "server/service/tools/workagent"
	"server/utils"

	"github.com/gin-gonic/gin"
)

// GetRenderRunnerStatus handles
// GET /api/work-agent/metrics/render-runners.
func (api *AIChatApiNew) GetRenderRunnerStatus(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	response.OkWithData(workagentService.DefaultArtifactRenderRunnerStatuses(), c)
}

// RunRenderRunnerSmoke handles
// POST /api/work-agent/metrics/render-runners/smoke.
func (api *AIChatApiNew) RunRenderRunnerSmoke(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	response.OkWithData(workagentService.RunDefaultArtifactRenderSmoke(c.Request.Context()), c)
}

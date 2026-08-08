package tools

import (
	"path"
	"server/api"
	"server/globals"
	"strings"

	"github.com/gin-gonic/gin"
)

type VideoPromptRewriterRouter struct{}

func (r *VideoPromptRewriterRouter) InitVideoPromptRewriterRouter(router *gin.RouterGroup) {
	vprApi := api.ApiGroupApp.ToolsApiGroup.VideoPromptRewriterApi
	vpcApi := api.ApiGroupApp.ToolsApiGroup.VideoPromptConfigApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	Router := router.Group(path.Join(prefix, "video-prompts"))
	{
		// LLM rewriter
		Router.POST("/rewrite", vprApi.RewriteVideoPrompt)

		// CRUD — saved configurations
		Router.GET("/configs", vpcApi.ListConfigs)
		Router.GET("/configs/:id", vpcApi.GetConfig)
		Router.POST("/configs", vpcApi.SaveConfig)
		Router.PUT("/configs/:id", vpcApi.UpdateConfig)
		Router.DELETE("/configs/:id", vpcApi.DeleteConfig)
	}
}

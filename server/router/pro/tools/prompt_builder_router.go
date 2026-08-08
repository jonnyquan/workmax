package tools

import (
	"path"
	"server/api"
	"server/globals"
	"strings"

	"github.com/gin-gonic/gin"
)

type PromptBuilderRouter struct{}

func (r *PromptBuilderRouter) InitPromptBuilderRouter(router *gin.RouterGroup) {
	pbApi := api.ApiGroupApp.ToolsApiGroup.PromptBuilderApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	Router := router.Group(path.Join(prefix, "image-prompt-builder"))
	{
		// CRUD — saved configurations
		Router.GET("/configs", pbApi.ListConfigs)
		Router.GET("/configs/:id", pbApi.GetConfig)
		Router.POST("/configs", pbApi.SaveConfig)
		Router.PUT("/configs/:id", pbApi.UpdateConfig)
		Router.DELETE("/configs/:id", pbApi.DeleteConfig)

		// AI features
		Router.POST("/enhance", pbApi.EnhancePrompt)
		Router.POST("/parse", pbApi.ParsePrompt)
		Router.POST("/parse-image", pbApi.ParsePromptImage)
		Router.POST("/suggest", pbApi.SuggestTags)
	}

	// Backward-compatible route for generator prompt translator stream proxy.
	toolsRouter := router.Group(path.Join(prefix, "tools"))
	{
		toolsRouter.POST("/translate-prompt-stream", pbApi.TranslateStream)
	}
}

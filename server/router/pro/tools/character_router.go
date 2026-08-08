package tools

import (
	"path"
	"server/api"
	"server/globals"
	"strings"

	"github.com/gin-gonic/gin"
)

type CharacterRouter struct{}

func (r *CharacterRouter) InitCharacterRouter(router *gin.RouterGroup) {
	characterApi := api.ApiGroupApp.ToolsApiGroup.CharacterApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	// Group: /api/character
	group := router.Group(path.Join(prefix, "character"))
	{
		group.POST("", characterApi.Create)
		group.GET("", characterApi.List)
		group.GET("/:id", characterApi.Get)
		group.PUT("/:id", characterApi.Update)
		group.DELETE("/:id", characterApi.Delete)

		group.POST("/:id/reference", characterApi.AddReference)
		group.PUT("/:id/reference/reorder", characterApi.ReorderReferences)
		group.DELETE("/reference/:refId", characterApi.DeleteReference)
	}
}

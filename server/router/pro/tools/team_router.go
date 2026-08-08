package tools

import (
	"path"
	"server/api"
	"server/globals"
	"strings"

	"github.com/gin-gonic/gin"
)

type TeamRouter struct{}

func (r *TeamRouter) InitTeamRouter(router *gin.RouterGroup) {
	teamApi := api.ApiGroupApp.ToolsApiGroup.TeamApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	group := router.Group(path.Join(prefix, "team"))
	{
		group.GET("", teamApi.List)
		group.POST("", teamApi.Create)
		group.GET("/:id", teamApi.Get)
		group.PUT("/:id", teamApi.Update)
		group.DELETE("/:id", teamApi.Delete)

		group.POST("/:id/member", teamApi.AddMember)
		group.DELETE("/:id/member/:mid", teamApi.RemoveMember)
	}
}

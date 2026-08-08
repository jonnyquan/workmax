package tools

import (
	"path"
	"server/api"
	"server/globals"
	"server/middleware"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type CanvasRouter struct{}

func (r *CanvasRouter) InitCanvasPublicRouter(router *gin.RouterGroup) {
	canvasApi := api.ApiGroupApp.ToolsApiGroup.CanvasApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	Router := router.Group(path.Join(prefix, "tools", "canvas", "public"))
	Router.Use(middleware.RateLimit(120, time.Minute))
	{
		Router.GET("/projects/uuid/:uuid", canvasApi.GetSharedProjectByUUID)
	}
}

func (r *CanvasRouter) InitCanvasRouter(router *gin.RouterGroup) {
	canvasApi := api.ApiGroupApp.ToolsApiGroup.CanvasApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	// Canvas rate-limit registry. Built once at router init — buckets
	// live for the process lifetime so repeated requests from the same
	// user stay in the same state machine.
	limits := middleware.NewCanvasRateLimitRegistry()

	Router := router.Group(path.Join(prefix, "tools", "canvas"))
	// L1 IP-level guard. Tuned for a Canvas session: a single user can
	// easily burst 20 reads while flipping project screens.
	Router.Use(middleware.RateLimit(240, time.Minute))
	{
		Router.POST("/quote", limits.Limiter("quote"), canvasApi.Quote)
		// /chat handler retired 2026-05-15 (Task #15) — was orphaned
		// (no FE caller). Agent surface at /agent/chat is the live
		// canvas conversation path.
		Router.POST("/agent/chat", limits.Limiter("agent"), canvasApi.AgentChat)
		Router.GET("/agent/threads", canvasApi.AgentThreads)
		Router.GET("/agent/threads/:threadId/messages", canvasApi.AgentMessages)
		Router.POST("/agent/messages", limits.Limiter("chat"), canvasApi.AppendAgentMessage)

		Router.POST("/projects", canvasApi.CreateProject)
		Router.GET("/projects", canvasApi.ListProjects)
		Router.GET("/projects/uuid/:uuid", canvasApi.GetProjectByUUID)
		Router.GET("/projects/:id", canvasApi.GetProject)
		Router.PATCH("/projects/:id", canvasApi.UpdateProject)
		Router.POST("/projects/:id/publish", canvasApi.PublishProject)
		Router.PATCH("/projects/:id/elements", canvasApi.UpdateElements)
		Router.POST("/projects/:id/shots/sync", canvasApi.SyncShots)

		Router.POST("/projects/:id/shots/:shotId/lock", canvasApi.AcquireShotLock)
		Router.POST("/projects/:id/shots/:shotId/heartbeat", canvasApi.HeartbeatShotLock)
		Router.POST("/projects/:id/shots/:shotId/unlock", canvasApi.ReleaseShotLock)

		Router.DELETE("/projects/:id", canvasApi.DeleteProject)

		// P1 #6 slice 3 — per-project credit budget.
		// GET returns {cap, used, remaining, exceeded};
		// PUT body {cap: int | null} sets / clears the cap.
		Router.GET("/projects/:id/budget", canvasApi.GetProjectBudget)
		Router.PUT("/projects/:id/budget", canvasApi.SetProjectBudget)

		// Decision log — read-only projection over the project's
		// task history (model / prompt / credits / status / duration).
		// Capped at canvas.MaxDecisionLogEntries; `truncated: true`
		// in the body when the cap is hit.
		Router.GET("/projects/:id/decision-log", canvasApi.GetProjectDecisionLog)

		// ShotBoard → PDF — printable shot board for client / director
		// review. Streams application/pdf inline; falls back to a
		// placeholder cell when an image fetch fails so a single
		// broken URL doesn't tank the whole export.
		Router.GET("/projects/:id/export/shot-board-pdf", canvasApi.ExportShotBoardPDF)

		// Pre-render consistency check — soft warn before video
		// submit. Returns {ok, summary, warnings, ...} based on
		// the project's recent shots + the proposed new prompt.
		// Always 200; FE decides whether to surface a modal.
		Router.POST("/projects/:id/preflight-consistency", canvasApi.PreflightConsistency)

		Router.POST("/projects/:id/versions", canvasApi.CreateVersion)
		Router.GET("/projects/:id/versions", canvasApi.ListVersions)
		Router.GET("/projects/:id/versions/:version", canvasApi.GetVersion)
		Router.PATCH("/projects/:id/versions/:version", canvasApi.UpdateVersion)
		// C-09 (2026-05-15): restore a historical snapshot as the
		// live document. Atomic — source snapshot stays intact, a new
		// audit snapshot is allocated at LatestVersion+1, the project
		// row is repointed. See canvas_version_restore_api.go for the
		// body contract (expectedProjectUpdatedAt + danglingAssets
		// policy).
		Router.POST("/projects/:id/versions/:version/restore", canvasApi.RestoreVersion)
		// C-11 (2026-05-15): rotate the share-link token. Owner-only.
		// Generates a fresh 256-bit token and returns it; any
		// previously distributed share URL drops to 404 on next read
		// because the public lookup at /public/projects/uuid/:uuid
		// now requires ?t=<token> match.
		Router.POST("/projects/:id/share/rotate", canvasApi.RotateShareToken)

		Router.POST("/projects/:id/assets", canvasApi.CreateAsset)
		Router.POST("/projects/:id/assets/upload", canvasApi.UploadAsset)
		Router.GET("/projects/:id/assets", canvasApi.ListAssets)

		// AI Operations — all share the generation bucket so a user
		// can't bypass the limit by rotating through img2img → outpaint
		// → inpaint.
		gen := limits.Limiter("generation")
		Router.POST("/img2img", gen, canvasApi.Img2Img)
		Router.POST("/outpaint", gen, canvasApi.Outpaint)
		Router.POST("/inpaint", gen, canvasApi.Inpaint)
		Router.POST("/mockup", gen, canvasApi.Mockup)
		Router.POST("/edit-text", gen, canvasApi.EditText)
		Router.POST("/split-layers", gen, canvasApi.SplitLayers)
		Router.POST("/upscale", gen, canvasApi.Upscale)
		Router.POST("/remove-bg", gen, canvasApi.RemoveBg)
		Router.POST("/extract-keyframe", gen, canvasApi.ExtractKeyframe)
		Router.POST("/optimize-prompt", limits.Limiter("chat"), canvasApi.OptimizePrompt)
		Router.GET("/task/:taskId", canvasApi.GetTaskStatus)
		// M2-W4-01: task-recovery list. Reads only — uses the quote
		// bucket because it has the right burst profile for a client
		// that calls this on every project focus.
		Router.GET("/tasks", limits.Limiter("quote"), canvasApi.ListCanvasTasks)

		// B6 PR 3 (2026-05-15): admin-only counter snapshot for
		// PromptGuard + the consistency judge. Gated with AdminAuth so
		// it isn't reachable by every authenticated user (mirrors the
		// /sse/stats posture in aichat_router.go). Cheap by design —
		// each Load is a single atomic read — but no reason to expose
		// rejection / truncation rates to arbitrary callers.
		Router.GET("/admin/stats", middleware.AdminAuth(), canvasApi.AdminStats)
		// D-01 hot-reload endpoint (2026-05-15): lets trust & safety
		// push an updated sensitive-words list without restarting the
		// server. AdminAuth-gated for the same reason /admin/stats is
		// — the rejection contract is sensitive ops surface.
		Router.POST("/admin/prompt-guard/reload", middleware.AdminAuth(), canvasApi.AdminReloadPromptGuard)
		// C-06 Stage 1 (2026-05-15): read-only orphan-element-asset
		// preview. Walks every active canvas project, computes the
		// per-project orphan count, returns distribution stats +
		// top-K worst offenders. Read-only — no DB writes, no
		// filesystem mutations. The numbers it surfaces are the
		// gating signal for Stage 2 (admin sweep + storage GC).
		Router.GET("/admin/orphan-element-assets/preview", middleware.AdminAuth(), canvasApi.AdminPreviewOrphanElementAssets)
	}
}

package workagent

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"server/api/pro/tools/workagent"
	"server/middleware"
	"server/utils"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// publicShareReadRateLimit caps the per-IP fetch rate against the
// unauthenticated /conversations/:threadId and /messages/:msgId
// endpoints. These routes serve up to ~MB-scale payloads (500-message
// share cap) and are reachable to anyone with a known UUID, so a hot
// share link or a deliberate scraper could amplify into real load
// without a cap. 60/min is well above any human flow (one preview +
// occasional reload) but tight enough to stop an enumeration burst.
const publicShareReadRateLimit = 60
const publicShareReadRateWindow = time.Minute

// publicShareReadPerUUIDRateLimit complements the per-IP cap above:
// even when each individual scraper IP stays under the per-IP bucket,
// a distributed scrape across many IPs aimed at ONE leaked share URL
// would otherwise sail through. 240 reads/min on one UUID still
// supports a CDN-fronted high-traffic preview (Slack/Twitter unfurl,
// search-engine refresh) but caps an aggregate scrape at ~4/sec —
// enough that a determined attacker doesn't amplify a leaked URL
// into hot-row DB pressure.
const publicShareReadPerUUIDRateLimit = 240
const publicShareReadPerUUIDRateWindow = time.Minute

// activeContentExtensions lists file extensions the browser will
// happily render as active content under the SPA's same origin —
// i.e. any executable script context. agent_workspace serves under
// the same origin as the SPA, so an HTML/SVG/XML file the agent
// emits would otherwise execute JavaScript with the user's session.
//
// We can't refuse the type at write time: agent OUTPUT generation
// legitimately produces HTML (Plotly reports), and the upload path
// already refuses HTML uploads in file_service.go. The mitigation
// is at the response boundary — force `Content-Disposition:
// attachment` so the browser downloads instead of rendering. Any
// future inline-preview UX for these types must move to a
// sandboxed origin (e.g. *.sandbox.<host>) before this list is
// trimmed.
var activeContentExtensions = map[string]struct{}{
	".html":  {},
	".htm":   {},
	".xhtml": {},
	".xht":   {},
	".svg":   {},
	".svgz":  {},
	".xml":   {},
	".xsl":   {},
	".xslt":  {},
	".mhtml": {},
	".mht":   {},
}

type AIChatRouter struct{}

// InitAIChatRouter initializes AIChat routes (Cleaned Version)
func (cr *AIChatRouter) InitAIChatRouter(Router *gin.RouterGroup) {
	aichatRouter := Router.Group("api/work-agent")

	// 使用新的AIChat API实现
	apiNew := workagent.NewAIChatApiNew()

	// File serving for agent_workspace.
	// Expected path layout: /agent_workspace/uid/{uid}/{YYYYMMDD}/thread_{uuid}/...
	// JWTAuth is attached explicitly here (not just inherited from the parent
	// group) so that if InitAIChatRouter is ever called on a less-privileged
	// group by mistake, this route still rejects unauthenticated callers
	// instead of silently exposing every user's generated artifacts.
	// serveWorkspaceFile additionally enforces that the uid segment matches
	// the caller's uid so an authenticated user cannot read another user's
	// workspace files.
	{
		Router.GET("/"+workspaceRoutePrefix()+"/*filepath", middleware.JWTAuth(), serveWorkspaceFile)
	}

	// Chat related routes - 核心聊天功能
	//
	// NOTE: POST /chat/agent and GET /skills are NOT registered here. They are
	// the two Agent routes the Desktop client calls with a Desktop OAuth token,
	// so they live on their own credential group — see
	// InitDesktopSharedAIChatRouter below.
	{
		aichatRouter.POST("/chat/conversation", apiNew.CreateConversation)
		aichatRouter.GET("/chat/conversations", apiNew.GetConversations)
		// Single-conversation lookup by UUID. Used to land on a deep-history
		// thread URL without paging through the conversation list. Note:
		// path is `by-uuid/:uuid` (not `:uuid`) to avoid collision with the
		// numeric-id `:id` routes immediately below.
		aichatRouter.GET("/chat/conversation/by-uuid/:uuid", apiNew.GetConversationByUuid)
		aichatRouter.GET("/chat/conversation/:id/settings", apiNew.GetConversationSettings)
		aichatRouter.PUT("/chat/conversation/:id", apiNew.UpdateConversationSettings)
		aichatRouter.PUT("/chat/conversation/:id/share", apiNew.SetConversationVisibility)
		aichatRouter.DELETE("/chat/conversation/:id", apiNew.DeleteConversation)
		aichatRouter.GET("/chat/conversation/:id/files", apiNew.GetConversationFiles)
		aichatRouter.GET("/chat/conversation/:id/artifacts", apiNew.GetConversationArtifacts)
		aichatRouter.PATCH("/chat/conversation/:id/artifacts/:artifactId/status", apiNew.UpdateArtifactStatus)
		aichatRouter.PATCH("/chat/conversation/:id/artifacts/:artifactId/comparison", apiNew.UpdateArtifactComparison)
		aichatRouter.POST("/chat/conversation/:id/artifacts/:artifactId/visual-diff-report", apiNew.CreateArtifactVisualDiffReport)
		aichatRouter.PATCH("/chat/conversation/:id/artifacts/:artifactId/html-preview-diagnostics", apiNew.UpdateArtifactHTMLPreviewDiagnostics)
		aichatRouter.POST("/chat/conversation/:id/artifacts/:artifactId/html-browser-validation", apiNew.RunArtifactHTMLBrowserValidation)
		aichatRouter.POST("/chat/conversation/:id/artifacts/:artifactId/export", apiNew.ExportArtifact)
		aichatRouter.POST("/chat/conversation/:id/artifacts/:artifactId/export-jobs", apiNew.CreateArtifactExportJob)
		aichatRouter.GET("/chat/conversation/:id/artifacts/:artifactId/export-jobs/:jobId", apiNew.GetArtifactExportJob)
		aichatRouter.POST("/chat/conversation/:id/artifacts/:artifactId/decision", apiNew.ApplyArtifactDecision)
		aichatRouter.POST("/chat/conversation/:id/artifacts/:artifactId/asset-candidate", apiNew.CreateArtifactAssetCandidate)
		aichatRouter.GET("/chat/conversation/:id/asset-candidates", apiNew.ListArtifactAssetCandidates)
		aichatRouter.PATCH("/chat/conversation/:id/asset-candidates/:candidateId/status", apiNew.UpdateArtifactAssetCandidateStatus)
		aichatRouter.POST("/chat/conversation/:id/clear", apiNew.ClearConversation)
		aichatRouter.POST("/chat/conversation/:id/clear-messages", apiNew.ClearMessages)
		aichatRouter.GET("/chat/history", apiNew.GetChatHistory)
		aichatRouter.DELETE("/chat/message/:id", apiNew.DeleteMessage)
		// P0 #3 critique loop. POST { rating: -1|0|1, feedback?: string }
		// records the user's 👍/👎 on a specific assistant message. A
		// thumbs-down + non-empty feedback is consumed by the next-turn
		// preflight composer as a <previous-critique> block.
		aichatRouter.POST("/chat/message/:id/rate", apiNew.RateMessage)

		aichatRouter.POST("/skills/:agentMode/access-requests", apiNew.RequestSkillAccess)
		aichatRouter.GET("/skills/access-requests", middleware.AdminAuth(), apiNew.ListSkillAccessRequests)
		aichatRouter.PATCH("/skills/access-requests/:requestId/status", middleware.AdminAuth(), apiNew.UpdateSkillAccessRequestStatus)
		aichatRouter.GET("/design-systems", apiNew.ListDesignSystems)
		// Render-runner state and smoke execution are operational controls, not
		// end-user Agent features. Keep both behind the Admin/Ops policy even
		// while the surrounding legacy Agent surface still uses generic JWTAuth.
		aichatRouter.GET("/metrics/render-runners", middleware.AdminAuth(), apiNew.GetRenderRunnerStatus)
		aichatRouter.POST("/metrics/render-runners/smoke", middleware.AdminAuth(), apiNew.RunRenderRunnerSmoke)
		aichatRouter.PATCH("/projects/:projectId/design-systems/:designSystemId/status", apiNew.UpdateProjectDesignSystemStatus)
		aichatRouter.GET("/projects/:projectId/design-systems/:designSystemId/history", apiNew.GetProjectDesignSystemHistory)
		aichatRouter.POST("/projects/:projectId/design-systems/fork", apiNew.ForkOfficialDesignSystem)
		aichatRouter.POST("/projects/:projectId/design-systems/:designSystemId/fork", apiNew.ForkProjectDesignSystem)

		// §1 Project/Workspace 模型升级 Stage 1 (2026-05-15):
		// expose the project layer to the workagent FE. On first GET
		// the user's Personal Workspace is auto-created and any
		// grandfathered (project_id=0) threads get lazily migrated
		// into it. POST creates additional user-named projects. PUT
		// rebinds an existing thread.
		aichatRouter.GET("/projects", apiNew.ListProjects)
		aichatRouter.POST("/projects", apiNew.CreateProject)
		// Param name must match the existing :projectId wildcard further
		// up in this router (design-systems routes) — gin rejects siblings
		// at the same position that disagree on the param name.
		aichatRouter.GET("/projects/:projectId/settings", apiNew.GetProjectSettings)
		aichatRouter.PATCH("/projects/:projectId/settings", apiNew.UpdateProjectSettings)
		aichatRouter.GET("/projects/:projectId/budget", apiNew.GetProjectBudget)
		aichatRouter.PUT("/projects/:projectId/budget", apiNew.SetProjectBudget)
		aichatRouter.DELETE("/projects/:projectId", apiNew.DeleteProject)
		aichatRouter.PUT("/chat/conversation/:id/project", apiNew.BindThreadProject)

		// DS-2 visual direction selection. POST { direction_id, source? }
		// persists a synthetic visual_direction_selected metadata row on
		// the thread; subsequent turns' preflight reads it back. IDOR
		// defence: thread ownership checked via LoadByIDForOwner.
		aichatRouter.POST("/threads/:id/direction", apiNew.SubmitDirectionSelection)

		// M1 question-form submission. POST { form_id, answers,
		// skip_reason? } persists a synthetic discovery_form_result row.
		// Mirrors the direction submission's IDOR posture + fail-soft
		// persist + readback verification.
		aichatRouter.POST("/threads/:id/discovery", apiNew.SubmitDiscoveryAnswers)

		// P3 generic pass-mode transition. Used by UI gates such as
		// variations_picker when the transition is workflow-level rather
		// than tied to a richer domain object.
		aichatRouter.POST("/threads/:id/pass-mode", apiNew.SubmitPassMode)

		// Backlog #11 4/n — brand-library REST surface. Minimum CRUD
		// for the Sprint-C brand-library UI: list / show / confirm /
		// soft-delete. Create + edit ride through the M4 extraction
		// path today; explicit POST/PATCH can be added when an
		// editor UI ships.
		aichatRouter.GET("/brand-assets", apiNew.ListBrandAssets)
		aichatRouter.GET("/brand-assets/:id", apiNew.GetBrandAsset)
		aichatRouter.POST("/brand-assets/:id/confirm", apiNew.ConfirmBrandAsset)
		aichatRouter.POST("/brand-assets/:id/restore", apiNew.RestoreBrandAsset)
		aichatRouter.DELETE("/brand-assets/:id", apiNew.DeleteBrandAsset)

		// Backlog #11 11/n — character-library REST surface. Same
		// shape as brand: list / show / confirm / soft-delete.
		aichatRouter.GET("/character-assets", apiNew.ListCharacterAssets)
		aichatRouter.GET("/character-assets/:id", apiNew.GetCharacterAsset)
		aichatRouter.POST("/character-assets/:id/confirm", apiNew.ConfirmCharacterAsset)
		aichatRouter.POST("/character-assets/:id/restore", apiNew.RestoreCharacterAsset)
		aichatRouter.DELETE("/character-assets/:id", apiNew.DeleteCharacterAsset)

		// Backlog #11 12/n — director-style-library REST surface.
		// Same shape as brand/character with one extra: list
		// supports `?genre=` to hit the (genre) index.
		aichatRouter.GET("/director-style-assets", apiNew.ListDirectorStyleAssets)
		aichatRouter.GET("/director-style-assets/:id", apiNew.GetDirectorStyleAsset)
		aichatRouter.POST("/director-style-assets/:id/confirm", apiNew.ConfirmDirectorStyleAsset)
		aichatRouter.POST("/director-style-assets/:id/restore", apiNew.RestoreDirectorStyleAsset)
		aichatRouter.DELETE("/director-style-assets/:id", apiNew.DeleteDirectorStyleAsset)

		// Phase B4a — MCP connector registry CRUD. List view returns
		// metadata only (env / header values scrubbed); detail view
		// returns full plaintext for the owner. Enabled toggle is a
		// dedicated PATCH for the UI's per-row switch.
		aichatRouter.GET("/mcp-connectors", apiNew.ListMCPConnectors)
		aichatRouter.GET("/mcp-connectors/:id", apiNew.GetMCPConnector)
		aichatRouter.POST("/mcp-connectors", apiNew.CreateMCPConnector)
		aichatRouter.PUT("/mcp-connectors/:id", apiNew.UpdateMCPConnector)
		aichatRouter.PATCH("/mcp-connectors/:id/enabled", apiNew.SetMCPConnectorEnabled)
		aichatRouter.DELETE("/mcp-connectors/:id", apiNew.DeleteMCPConnector)

		// MCP connector live-validation (2026-05-16). POST a probe at
		// the connector's URL + headers and report reachable/unreachable
		// so the UI can show a green/red badge without spending a full
		// agent turn to discover broken config. Stdio transports are
		// not probed (disabled at the bridge boundary).
		aichatRouter.POST("/mcp-connectors/:id/test", apiNew.TestMCPConnector)
	}

	// File related routes - 文件功能
	{
		aichatRouter.POST("/file/upload", apiNew.UploadFile)
		aichatRouter.DELETE("/file/:id", apiNew.DeleteFile)
		aichatRouter.GET("/file/:id/download", apiNew.DownloadFile)
		aichatRouter.GET("/file/:id/preview", apiNew.PreviewFile)
		aichatRouter.POST("/files/sync", apiNew.SyncFiles) // Sync output files with database
	}

	// SSE Monitoring routes - SSE连接监控
	//
	// Stats are operational/admin info (global byte counts, oldest
	// connection age, per-user breakdowns). Until the rollback they
	// were reachable to every authenticated user via the broad
	// PrivateGroup mount; nothing in the frontend consumes them. Gate
	// with AdminAuth so only role=manager can pull them.
	{
		aichatRouter.GET("/sse/stats", middleware.AdminAuth(), apiNew.GetSSEConnectionStats)
	}
}

// InitDesktopSharedAIChatRouter registers the only two Agent routes the
// Desktop client calls without a /api/desktop prefix:
//
//   - POST /api/work-agent/chat/agent  (cloud_proxy.CloudRouteChatAgent)
//   - GET  /api/work-agent/skills      (cloud_proxy.CloudRouteSkillsList)
//
// They are split out of InitAIChatRouter because they need a credential the
// rest of the Agent surface must NOT have: a group whose JWT middleware
// accepts the Desktop resource audience. Everything else on
// /api/work-agent/** stays on plain JWTAuth, which rejects Desktop tokens.
//
// Pass an unauthenticated group; this router applies its own middleware, the
// same way DesktopSyncRouter does.
//
// Adding a route here widens what a Desktop OAuth token can reach — the
// decision belongs in this file, not in a group mount somewhere else.
func (cr *AIChatRouter) InitDesktopSharedAIChatRouter(Router *gin.RouterGroup) {
	aichatRouter := Router.Group("api/work-agent")
	aichatRouter.Use(middleware.JWTAuthAcceptingDesktopAudience())

	apiNew := workagent.NewAIChatApiNew()

	aichatRouter.POST("/chat/agent", apiNew.HandleAgentChat) // Agent mode with message/block streaming

	// F3 (2026-05-17) — skill catalog. Returns structural
	// metadata (version + has-questionForm / has-directions
	// / has-postScripts booleans) for every user-facing skill.
	// Complements the FE's typed AGENT_MODES union with runtime
	// capability flags; unblocks future skill-discovery UX
	// (e.g. "browse skills" pages, paid-skill gating).
	aichatRouter.GET("/skills", apiNew.ListSkills)
}

// serveWorkspaceFile serves files under agent_workspace only when the caller's
// uid matches the `uid/{N}/` segment of the requested path. This prevents a
// logged-in user from reading another user's generated artifacts by URL-guessing.
//
// Wrapper around serveWorkspaceFileWithRoot that resolves the workspace root
// from the live config; the inner function takes the root explicitly so unit
// tests can inject a temporary directory.
func serveWorkspaceFile(c *gin.Context) {
	serveWorkspaceFileWithRoot(c, workspaceRoot())
}

func serveWorkspaceFileWithRoot(c *gin.Context, root string) {
	raw := c.Param("filepath")
	// Gin's *filepath catches the leading slash; normalize and clean.
	rel := strings.TrimPrefix(raw, "/")
	cleaned := filepath.ToSlash(filepath.Clean(rel))
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "../") {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Expect "uid/{N}/..." layout. Legacy paths without uid/ prefix are no
	// longer served — all current writes go through the uid-scoped layout.
	parts := strings.SplitN(cleaned, "/", 3)
	if len(parts) < 3 || parts[0] != "uid" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	callerUID := utils.GetUserID(c)
	if callerUID == 0 {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	if parts[1] != fmt.Sprint(callerUID) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	fullPath := filepath.Join(root, filepath.FromSlash(cleaned))
	// Defense in depth: ensure the resolved path is still inside root.
	// EvalSymlinks resolves through symlinks before the prefix check so
	// an agent that smuggled `ln -s /etc/passwd <workspace>/outputs/x`
	// past the workspace-confinement boundary can't make us serve the
	// target. Without this, the previous filepath.Abs-only prefix
	// check passed because absFull was the symlink's own path, not the
	// resolved destination.
	//
	// EvalSymlinks errors when the file doesn't exist; fall through to
	// the open below (clients see 404). On a symlink whose target IS
	// inside the workspace (the legitimate case for the uploads/
	// symlinks PrepareFilesForAgent installs), the resolved path stays
	// under absRoot and the check passes.
	absRoot, err := filepath.Abs(root)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if eval, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = eval
	}
	absFull, err := filepath.Abs(fullPath)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if eval, err := filepath.EvalSymlinks(absFull); err == nil {
		absFull = eval
	}
	if !strings.HasPrefix(absFull+string(filepath.Separator), absRoot+string(filepath.Separator)) && absFull != absRoot {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// TOCTOU defense: open the EvalSymlinks-resolved canonical path
	// with O_NOFOLLOW. The prior `c.File(fullPath)` re-resolved through
	// the kernel at serve time — between our prefix check and the
	// kernel's open, an in-process agent goroutine could `rm <canonical>
	// && ln -s /etc/passwd <canonical>` and we'd serve the new target.
	//
	// We open `absFull` (post-EvalSymlinks) rather than `fullPath`
	// because `uploads/<filename>` is itself a symlink that
	// PrepareFilesForAgent installs (workspace_file_manager.go:179).
	// O_NOFOLLOW on the user-requested path would refuse those legit
	// symlinks; O_NOFOLLOW on the resolved canonical path only refuses
	// a symlink that was swapped in *after* we resolved. The legitimate
	// canonical target is always a regular file — we resolved through
	// every symlink in EvalSymlinks above.
	f, err := os.OpenFile(absFull, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Defense against stored XSS via agent-emitted active-content
	// files. agent_workspace serves under the SPA's same origin so any
	// HTML / SVG / XML the agent writes would otherwise execute JS
	// with the caller's session cookies — full account takeover from
	// any agent that can be steered to write a file. Two layers:
	//
	//   1. X-Content-Type-Options: nosniff blocks the browser from
	//      promoting an extension-less or mistyped file to text/html
	//      via content sniffing.
	//   2. For active-content extensions, force Content-Disposition:
	//      attachment so the browser downloads instead of rendering.
	//      Inline preview of those types must move to a sandboxed
	//      origin before this restriction is relaxed.
	c.Header("X-Content-Type-Options", "nosniff")
	ext := strings.ToLower(filepath.Ext(absFull))
	if _, isActive := activeContentExtensions[ext]; isActive {
		c.Header("Content-Disposition", workagent.ContentDispositionAttachment(filepath.Base(absFull)))
	}

	http.ServeContent(c.Writer, c.Request, filepath.Base(absFull), stat.ModTime(), f)
}

func (cr *AIChatRouter) InitPublicAIChatRouter(Router *gin.RouterGroup) {
	aichatRouter := Router.Group("api/work-agent")

	// 使用新的AIChat API实现
	apiNew := workagent.NewAIChatApiNew()

	// One shared per-IP rate-limit handler is constructed here and
	// applied to BOTH share endpoints. Sharing the bucket across
	// endpoints is intentional: an attacker probing /conversations
	// shouldn't be able to "reset their bucket" by pivoting to
	// /messages after burning through their quota on the first
	// endpoint.
	shareIPRateLimit := middleware.RateLimit(publicShareReadRateLimit, publicShareReadRateWindow)

	// Per-UUID buckets are SEPARATE per route because the param name
	// differs (:threadId vs :msgId) and we want each UUID to have its
	// own allotment. Caps a distributed scrape on a single leaked URL
	// even when the per-IP buckets aren't hit.
	shareThreadUUIDRateLimit := middleware.RateLimitByPathParam(
		"threadId", publicShareReadPerUUIDRateLimit, publicShareReadPerUUIDRateWindow,
	)
	shareMsgUUIDRateLimit := middleware.RateLimitByPathParam(
		"msgId", publicShareReadPerUUIDRateLimit, publicShareReadPerUUIDRateWindow,
	)

	// Chat related routes - 核心聊天功能
	{
		// Shared routes for public access - 分享功能路由
		aichatRouter.GET("/conversations/:threadId", shareIPRateLimit, shareThreadUUIDRateLimit, apiNew.GetConversationDetail)
		aichatRouter.GET("/messages/:msgId", shareIPRateLimit, shareMsgUUIDRateLimit, apiNew.GetMessageDetail)
	}
}

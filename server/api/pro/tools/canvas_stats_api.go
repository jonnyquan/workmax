package tools

// canvas_stats_api.go · admin-only JSON dump of the in-process atomic
// counters added in B6 (PromptGuard.Stats() + VideoConsistencyJudgeStats).
// Lets ops see allow / truncate / reject / skip rates without grepping
// logs, and without waiting on the C-01 metrics-backbone (Prometheus /
// Grafana) build-out.
//
// The endpoint is intentionally cheap: each Load() is a single atomic
// read and the response is one small struct. No DB calls, no allocations
// beyond the JSON envelope. Safe to scrape every few seconds; a future
// /metrics scraper can run this on any cadence.

import (
	"net/http"

	canvasService "server/service/tools/canvas"

	"github.com/gin-gonic/gin"
)

// CanvasStatsResponse is the JSON envelope. Field names are camelCase
// to match the rest of the canvas API surface.
type CanvasStatsResponse struct {
	PromptGuard      canvasPromptGuardStatsJSON      `json:"promptGuard"`
	ConsistencyJudge canvasConsistencyJudgeStatsJSON `json:"consistencyJudge"`
}

type canvasPromptGuardStatsJSON struct {
	Checks            uint64 `json:"checks"`
	Allowed           uint64 `json:"allowed"`
	TruncatedPrompt   uint64 `json:"truncatedPrompt"`
	TruncatedNegative uint64 `json:"truncatedNegative"`
	RejectedSensitive uint64 `json:"rejectedSensitive"`
	WarnedInjection   uint64 `json:"warnedInjection"`
}

type canvasConsistencyJudgeStatsJSON struct {
	OK      uint64 `json:"ok"`
	Warned  uint64 `json:"warned"`
	Skipped uint64 `json:"skipped"`
}

// AdminStats godoc
// @Summary  Canvas admin counters snapshot
// @Description Returns a point-in-time snapshot of the in-process atomic
// @Description counters for PromptGuard and the video consistency judge.
// @Description Each field is a lifetime monotonic count; clients compute
// @Description deltas across polls if they want rate signals.
// @Tags Tools:Canvas (Admin)
// @Produce json
// @Router /api/tools/canvas/admin/stats [get]
func (a *CanvasApi) AdminStats(c *gin.Context) {
	guard := canvasService.Default().Stats()
	judge := canvasService.VideoConsistencyJudgeStats()
	c.JSON(http.StatusOK, CanvasStatsResponse{
		PromptGuard: canvasPromptGuardStatsJSON{
			Checks:            guard.Checks,
			Allowed:           guard.Allowed,
			TruncatedPrompt:   guard.TruncatedPrompt,
			TruncatedNegative: guard.TruncatedNegative,
			RejectedSensitive: guard.RejectedSensitive,
			WarnedInjection:   guard.WarnedInjection,
		},
		ConsistencyJudge: canvasConsistencyJudgeStatsJSON{
			OK:      judge.OK,
			Warned:  judge.Warned,
			Skipped: judge.Skipped,
		},
	})
}

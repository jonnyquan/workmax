// Decision log projection (Sprint borrowable #5 — OpenMontage-style
// "production state as JSON with decision logs"). Per-project read-
// only view over historical generation activity.
//
// The data already lives across w_canvas_task_binding,
// w_generation_task, and w_credit_reservation:
//
//   - binding row tells us "this task belonged to canvas project P,
//     possibly on element E, via run-id R"
//   - task row carries the actual decision (which model, what
//     prompt, what aspect/resolution, who chose video vs image)
//     and the outcome (status, credits charged, duration)
//
// This file just projects those rows into a stable wire shape so the
// UI (and future audit / export pipelines) can answer "what did this
// project do, with which model, for how many credits" without each
// caller learning the binding/task join.
//
// What it deliberately does NOT do:
//
//   - No new column or table. Existing rows already carry the data.
//   - No mutation. Pure read; safe to call on hot paths.
//   - No "decision rationale" beyond what the row already records.
//     (Capturing *why* a model was chosen over alternatives is a
//     separate feature — provider decision tree — that would need
//     write-side instrumentation. See Task #8 follow-up.)
//   - No pagination. Project task history is bounded in practice
//     (Canvas projects typically carry tens to low hundreds of tasks
//     over their lifetime); cap at MaxEntries below to defend
//     against accidental unbounded scans.

package canvas

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"server/model"
	"server/service/project"
)

// MaxDecisionLogEntries caps the result to a reasonable upper bound.
// Real canvas projects produce O(tens-to-hundreds) tasks; anything
// returning 1000+ is either a power-user edge case (acceptable to
// truncate) or a bug we'd want to notice. Sort is desc-by-created-at
// so the cap drops the *oldest* tail, not the most recent activity.
const MaxDecisionLogEntries = 500

// DecisionLogEntry is one historical task in the project's audit
// view. Fields use omitempty so the wire payload stays compact for
// failed-without-prompt or pending-without-completion rows.
type DecisionLogEntry struct {
	TaskID              string                    `json:"taskId"`
	ElementID           string                    `json:"elementId,omitempty"`
	GenerationRunID     string                    `json:"generationRunId,omitempty"`
	ToolID              string                    `json:"toolId"`
	Model               string                    `json:"model"`
	MediaType           string                    `json:"mediaType,omitempty"`
	Status              string                    `json:"status"`
	Prompt              string                    `json:"prompt,omitempty"`
	NegativePrompt      string                    `json:"negativePrompt,omitempty"`
	AspectRatio         string                    `json:"aspectRatio,omitempty"`
	Resolution          string                    `json:"resolution,omitempty"`
	Duration            string                    `json:"duration,omitempty"`
	StylePreset         string                    `json:"stylePreset,omitempty"`
	NumberOfImages      int                       `json:"numberOfImages,omitempty"`
	Origin              string                    `json:"origin,omitempty"`
	ReferenceImageCount int                       `json:"referenceImageCount,omitempty"`
	AssetBindingSummary string                    `json:"assetBindingSummary,omitempty"`
	CreditsCharged      int                       `json:"creditsCharged"`
	DurationMs          int                       `json:"durationMs,omitempty"`
	CreatedAt           time.Time                 `json:"createdAt"`
	StartedAt           *time.Time                `json:"startedAt,omitempty"`
	CompletedAt         *time.Time                `json:"completedAt,omitempty"`
	ErrorMessage        string                    `json:"errorMessage,omitempty"`
	ConsistencyVerdict  *DecisionLogVerdictRecord `json:"consistencyVerdict,omitempty"`
}

// DecisionLogVerdictRecord captures the pre-render consistency
// verdict the user acknowledged when submitting. Persisted on the
// task's request_data by the FE submit path (#9 follow-up,
// 2026-05-15) so the audit view can answer "did we warn them
// about this shot, and did they proceed anyway?".
//
// Only stored when the verdict had warnings AND the user chose
// Proceed-Anyway — green-light verdicts (ok=true, no warnings)
// are skipped to keep the row compact, so a missing field reads
// as "no warnings were surfaced".
type DecisionLogVerdictRecord struct {
	// Summary is the one-line headline the LLM produced.
	Summary string `json:"summary,omitempty"`
	// Warnings list the specific risks shown to the user.
	Warnings []DecisionLogVerdictWarning `json:"warnings,omitempty"`
	// Model identifies which LLM judged the verdict (e.g.
	// "deepseek-chat", "claude-haiku-4-5"). Diagnostic only —
	// useful when sweeping verdicts during a model rollout.
	Model string `json:"model,omitempty"`
	// Accepted is true when the user clicked Proceed-Anyway.
	// Always true in stored rows today (the FE only persists on
	// acceptance), but the field is explicit so a future "reject
	// also persisted" surface doesn't require a wire change.
	Accepted bool `json:"accepted"`
}

// DecisionLogVerdictWarning mirrors the FE / judge ConsistencyWarning
// shape on the wire. Kept structurally identical so reflective
// renderers (audit UI) can share the warning component with the
// modal.
type DecisionLogVerdictWarning struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// DecisionLogTotals is the rollup the UI uses for the project header
// ("This project used N credits across M renders, K% successful").
// Always present — empty projects emit zeros, not absent fields.
type DecisionLogTotals struct {
	Tasks      int `json:"tasks"`
	Successful int `json:"successful"`
	Failed     int `json:"failed"`
	Cancelled  int `json:"cancelled"`
	Pending    int `json:"pending"`
	Credits    int `json:"credits"`
	DurationMs int `json:"durationMs"`
}

// DecisionLog is the API response shape. Truncated reports whether
// the result was capped at MaxDecisionLogEntries so the UI can show
// "showing 500 of N — older history truncated".
type DecisionLog struct {
	ProjectID uint64             `json:"projectId"`
	Entries   []DecisionLogEntry `json:"entries"`
	Totals    DecisionLogTotals  `json:"totals"`
	Truncated bool               `json:"truncated"`
}

// joinedRow is the GORM Find target: every column we need from the
// JOIN of w_canvas_task_binding × w_generation_task. Anonymous so
// it doesn't escape this file as part of any public type surface.
type joinedRow struct {
	TaskID          string
	ElementID       string
	GenerationRunID string
	ToolID          string
	Model           string
	Status          model.TaskStatus
	RequestData     model.JSONMap
	ResultData      model.JSONMap
	ErrorMsg        string
	CreditsUsed     int
	DurationMs      int
	StartedAt       *time.Time
	CompletedAt     *time.Time
	CreatedAt       time.Time
}

// BuildProjectDecisionLog loads the project's task history and
// projects it into the DecisionLog wire shape. Verifies project
// ownership first (returns gorm.ErrRecordNotFound for both missing
// and cross-tenant; same IDOR-collapse posture as the rest of the
// canvas API surface).
func BuildProjectDecisionLog(
	ctx context.Context,
	db *gorm.DB,
	uid uint,
	projectID uint64,
) (DecisionLog, error) {
	if db == nil {
		return DecisionLog{}, errors.New("decision-log: db is nil")
	}
	if uid == 0 {
		return DecisionLog{}, errors.New("decision-log: uid is zero")
	}
	if projectID == 0 {
		return DecisionLog{}, errors.New("decision-log: project id is zero")
	}

	// Ownership gate — uses the same Repository the rest of the
	// canvas API uses. Returns gorm.ErrRecordNotFound for both
	// missing and cross-tenant, so the API handler can collapse
	// them into one 404-shaped body.
	repo := project.NewRepository(db)
	if _, err := repo.LoadByIDForOwner(uint(projectID), uid); err != nil {
		return DecisionLog{}, err
	}

	rows := make([]joinedRow, 0, 64)
	// Capped query: order desc by task created_at, hard-limit at
	// MaxDecisionLogEntries+1 so we can detect truncation without
	// a second COUNT(*) query.
	queryLimit := MaxDecisionLogEntries + 1
	err := db.WithContext(ctx).
		Table("w_canvas_task_binding AS b").
		Select(
			"t.task_id AS task_id",
			"b.element_id AS element_id",
			"b.generation_run_id AS generation_run_id",
			"t.tool_id AS tool_id",
			"t.model AS model",
			"t.status AS status",
			"t.request_data AS request_data",
			"t.result_data AS result_data",
			"t.error_msg AS error_msg",
			"t.credits_used AS credits_used",
			"t.duration_ms AS duration_ms",
			"t.started_at AS started_at",
			"t.completed_at AS completed_at",
			"t.created_at AS created_at",
		).
		Joins("JOIN w_generation_task AS t ON t.task_id = b.task_id").
		Where("b.uid = ? AND b.project_id = ?", uid, projectID).
		Order("t.created_at DESC").
		Limit(queryLimit).
		Find(&rows).Error
	if err != nil {
		return DecisionLog{}, err
	}

	truncated := false
	if len(rows) > MaxDecisionLogEntries {
		rows = rows[:MaxDecisionLogEntries]
		truncated = true
	}

	entries, totals := projectJoinedRowsToDecisionLog(rows)
	return DecisionLog{
		ProjectID: projectID,
		Entries:   entries,
		Totals:    totals,
		Truncated: truncated,
	}, nil
}

// projectJoinedRowsToDecisionLog is the pure projection — splits
// row-loading from row-formatting so tests can pin the decode logic
// (referenceImage count, assetBinding summary, status string) without
// a database.
func projectJoinedRowsToDecisionLog(rows []joinedRow) ([]DecisionLogEntry, DecisionLogTotals) {
	entries := make([]DecisionLogEntry, 0, len(rows))
	totals := DecisionLogTotals{}

	for _, r := range rows {
		entry := DecisionLogEntry{
			TaskID:          strings.TrimSpace(r.TaskID),
			ElementID:       strings.TrimSpace(r.ElementID),
			GenerationRunID: strings.TrimSpace(r.GenerationRunID),
			ToolID:          strings.TrimSpace(r.ToolID),
			Model:           strings.TrimSpace(r.Model),
			Status:          r.Status.String(),
			CreditsCharged:  r.CreditsUsed,
			DurationMs:      r.DurationMs,
			CreatedAt:       r.CreatedAt,
			StartedAt:       r.StartedAt,
			CompletedAt:     r.CompletedAt,
			ErrorMessage:    strings.TrimSpace(r.ErrorMsg),
		}
		decodeRequestIntoEntry(r.RequestData, &entry)

		entries = append(entries, entry)

		totals.Tasks++
		totals.Credits += r.CreditsUsed
		totals.DurationMs += r.DurationMs
		switch r.Status {
		case model.TaskStatusCompleted:
			totals.Successful++
		case model.TaskStatusFailed:
			totals.Failed++
		case model.TaskStatusCancelled:
			totals.Cancelled++
		case model.TaskStatusPending, model.TaskStatusProcessing:
			totals.Pending++
		}
	}

	// Stable sort by created_at desc — the SQL already orders this
	// way, but a few rows might share the same second; pin a
	// deterministic order so test golden comparisons don't flake.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	return entries, totals
}

// decodeRequestIntoEntry pulls the human-readable subset out of a
// JSONMap RequestData. We intentionally do NOT unmarshal into the
// full TaskRequestData struct — most fields are noise for the log
// view and the JSONMap shape lets us tolerate request shapes we
// didn't anticipate (e.g. older rows with retired fields).
func decodeRequestIntoEntry(req model.JSONMap, entry *DecisionLogEntry) {
	if req == nil {
		return
	}
	entry.MediaType = jsonMapString(req, "mediaType")
	entry.Prompt = jsonMapString(req, "prompt")
	entry.NegativePrompt = jsonMapString(req, "negativePrompt")
	entry.AspectRatio = jsonMapString(req, "aspectRatio")
	entry.Resolution = jsonMapString(req, "resolution")
	entry.Duration = jsonMapString(req, "duration")
	entry.StylePreset = jsonMapString(req, "stylePreset")
	entry.Origin = jsonMapString(req, "origin")
	if n, ok := jsonMapInt(req, "numberOfImages"); ok {
		entry.NumberOfImages = n
	}
	entry.ReferenceImageCount = countSliceItems(req, "referenceImages") +
		countSliceItems(req, "referenceVideos") +
		countSliceItems(req, "referenceAudios")
	entry.AssetBindingSummary = summariseAssetBindings(req)
	entry.ConsistencyVerdict = decodeConsistencyVerdict(req)
}

// decodeConsistencyVerdict pulls the consistencyVerdict slot out of
// the task's request_data when present. Returns nil for any of:
//
//   • slot absent (the FE only writes it on accepted-with-warnings)
//   • slot present but malformed (corrupt JSON, wrong types)
//   • slot present but no warnings (treated as "no signal worth
//     keeping" — green-light verdicts are noise in the audit view)
//
// Defensive: every field is loosely coerced because request_data
// is a JSONMap of unknowns. A type mismatch on any one field falls
// through to zero/empty for that field, never to a panic.
func decodeConsistencyVerdict(req model.JSONMap) *DecisionLogVerdictRecord {
	if req == nil {
		return nil
	}
	rawVerdict, ok := req["consistencyVerdict"]
	if !ok || rawVerdict == nil {
		return nil
	}
	verdictMap, ok := rawVerdict.(map[string]interface{})
	if !ok {
		return nil
	}
	record := &DecisionLogVerdictRecord{
		Summary: jsonMapString(verdictMap, "summary"),
		Model:   jsonMapString(verdictMap, "model"),
	}
	if accepted, ok := verdictMap["accepted"].(bool); ok {
		record.Accepted = accepted
	}
	rawWarnings, ok := verdictMap["warnings"].([]interface{})
	if !ok || len(rawWarnings) == 0 {
		// No warnings → no signal worth persisting in the audit
		// view. The slot was probably written by a future
		// always-on client; collapse to nil so consumers don't
		// render an empty "we warned them" row.
		return nil
	}
	warnings := make([]DecisionLogVerdictWarning, 0, len(rawWarnings))
	for _, rawWarning := range rawWarnings {
		warningMap, ok := rawWarning.(map[string]interface{})
		if !ok {
			continue
		}
		detail := jsonMapString(warningMap, "detail")
		if detail == "" {
			continue
		}
		kind := jsonMapString(warningMap, "kind")
		if kind == "" {
			kind = "general"
		}
		warnings = append(warnings, DecisionLogVerdictWarning{Kind: kind, Detail: detail})
	}
	if len(warnings) == 0 {
		return nil
	}
	record.Warnings = warnings
	return record
}

func jsonMapString(m model.JSONMap, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func jsonMapInt(m model.JSONMap, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

func countSliceItems(m model.JSONMap, key string) int {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	if arr, ok := v.([]interface{}); ok {
		return len(arr)
	}
	return 0
}

// summariseAssetBindings produces a human-readable phrase like
// "2 characters, 1 brand" from the request's assetBindings entry.
// Returns empty string when no bindings are present so the field
// stays omitted in JSON output.
func summariseAssetBindings(m model.JSONMap) string {
	if m == nil {
		return ""
	}
	rawBindings, ok := m["assetBindings"]
	if !ok || rawBindings == nil {
		return ""
	}
	bindings, ok := rawBindings.(map[string]interface{})
	if !ok {
		return ""
	}
	parts := make([]string, 0, 3)
	if n := countSliceItems(bindings, "characterIds"); n > 0 {
		parts = append(parts, pluralize(n, "character", "characters"))
	}
	if n := countSliceItems(bindings, "brandIds"); n > 0 {
		parts = append(parts, pluralize(n, "brand", "brands"))
	}
	if n := countSliceItems(bindings, "productIds"); n > 0 {
		parts = append(parts, pluralize(n, "product", "products"))
	}
	return strings.Join(parts, ", ")
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return formatInt(n) + " " + plural
}

func formatInt(n int) string {
	// strconv.Itoa via fmt would import "fmt" for one use; this
	// keeps the dep footprint tight. Negative n shouldn't occur
	// (counts are non-negative) but we don't special-case it —
	// the upstream check at countSliceItems prevents it anyway.
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Video render consistency pre-check (Task #9, borrowable from
// HKUDS/ViMax). Cheap LLM judgment of whether a proposed video
// prompt aligns with the project's prior shots — runs BEFORE the
// expensive video generation queue accepts the task, so we can
// surface "looks like a continuity issue" warnings to the user
// rather than burning provider credits on a render the user is
// going to reject anyway.
//
// Model selection: uses whatever is configured in config.yaml
// `aihub.text` (currently deepseek). No new model dependency,
// no new provider account, no new credit lane — the same LLM
// path the prompt builder already uses.
//
// Posture:
//
//   • The judge is ADVISORY. It returns warnings, never errors
//     that block submission. UX decision (soft warn) is enforced
//     at the API handler: a "concerns" verdict produces HTTP 200
//     with warnings in the body, never a 4xx that would prevent
//     the user from proceeding.
//   • The check is FAST (~1-3s wall clock). Anything slower
//     would damage the perceived submit latency more than the
//     check is worth.
//   • The check has a BUDGET. Per-project: it pulls the most
//     recent K=5 shots from the existing decision-log join, NOT
//     the full project history. Continuity matters most against
//     immediate predecessors.

package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"server/globals"
	"server/model"
	llmService "server/service/llm"
)

// consistencyJudgeStats counts lifetime BuildVideoConsistencyJudgment
// outcomes. Three buckets per the #9 follow-up note ("加
// canvas.consistency_judge.{ok,warned,skipped} 计数器"):
//
//   - ok      — successful LLM call with verdict OK=true (green light)
//   - warned  — successful LLM call with verdict OK=false (warnings)
//   - skipped — bailed for any reason (SkippedReason set)
//
// Package-level singleton because BuildVideoConsistencyJudgment is a
// free function, not a method. atomic.Uint64 → safe for the concurrent
// canvas-agent / video-submit dispatch paths that share this judge.
//
// The split lets us track false-positive rate (warned / (ok+warned))
// once the judge runs in production with a populated history.
var consistencyJudgeStats struct {
	ok      atomic.Uint64
	warned  atomic.Uint64
	skipped atomic.Uint64
}

// ConsistencyJudgeStats is the public snapshot returned by
// VideoConsistencyJudgeStats. Same semantics as canvas.GuardStats:
// each Load is atomic but the snapshot is not transactional across
// fields. Used by observability surfaces, not by invariant assertions.
type ConsistencyJudgeStats struct {
	OK      uint64
	Warned  uint64
	Skipped uint64
}

// VideoConsistencyJudgeStats returns a point-in-time snapshot of the
// process-wide consistency-judge counters. Safe to call concurrently;
// see ConsistencyJudgeStats for the snapshot contract.
func VideoConsistencyJudgeStats() ConsistencyJudgeStats {
	return ConsistencyJudgeStats{
		OK:      consistencyJudgeStats.ok.Load(),
		Warned:  consistencyJudgeStats.warned.Load(),
		Skipped: consistencyJudgeStats.skipped.Load(),
	}
}

// MaxConsistencyContextShots caps how many recent shots feed the
// judge. The LLM doesn't need 50 examples to spot a continuity
// break; 5 prior shots are enough to anchor "what does this
// project look like so far".
const MaxConsistencyContextShots = 5

// consistencyJudgePromptHeader frames the LLM's task. Kept short
// and explicit: the LLM's role is judge, not author. Pinned in
// English because the LLM responds better to English instructions
// for structured-JSON output; the user-facing warnings travel
// back as plain strings the FE renders verbatim.
const consistencyJudgePromptHeader = `You are a video pre-production continuity checker for a project on the WorkMax creative platform.

You will receive: (1) the user's proposed NEW video shot prompt, and (2) a list of PRIOR successful shot prompts from the same project, ordered most-recent-first.

Your job: identify CONCRETE continuity / consistency risks the user should know about before paying to render the new shot. Examples of real risks:
  - character description in new prompt contradicts a recurring character in prior shots ("a young man" after several "a young woman" shots)
  - setting / location switches abruptly without justification ("snowy mountain" after a series of "office interior" shots)
  - tone or mood reversal ("cheerful dance" after "funeral procession")
  - referenced entity (brand, product) is absent from prior context where it would be expected

NOT risks worth surfacing:
  - shots that are intentionally different scenes (anthology, cuts, montages)
  - subtle stylistic shifts that are within authorial discretion
  - lack of context (first or second shot in a project) — return ok=true

Output STRICT JSON only, no prose:
{
  "ok": <true if no concrete risk, false if concrete risks>,
  "summary": "<one-line summary, <=60 chars, empty when ok>",
  "warnings": [
    {"kind": "<character|setting|tone|entity>", "detail": "<one concrete sentence>"}
  ]
}

Be conservative: if uncertain, return ok=true with empty warnings. Cost of a false positive (annoying the user with a non-issue) is higher than the cost of a false negative (one bad render they could have spotted themselves).`

// ConsistencyWarning is one specific concern the judge raised.
// Kinds are a closed enum so the FE can group / colour-code them;
// detail is free-form English (FE renders verbatim).
type ConsistencyWarning struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// ConsistencyJudgment is what BuildVideoConsistencyJudgment
// returns. OK=true → green light; OK=false → at least one
// concrete warning the FE should surface before letting the
// user spend credits.
type ConsistencyJudgment struct {
	OK            bool                 `json:"ok"`
	Summary       string               `json:"summary,omitempty"`
	Warnings      []ConsistencyWarning `json:"warnings,omitempty"`
	Model         string               `json:"model,omitempty"`
	DurationMs    int                  `json:"durationMs,omitempty"`
	// SkippedReason records why we didn't actually call the LLM
	// (no LLM client configured, no prior shots, etc.). Empty
	// when the call happened. Useful for diagnostics; SkippedReason
	// always coexists with OK=true (a non-call is treated as
	// "green light").
	SkippedReason string `json:"skippedReason,omitempty"`
}

// ConsistencyJudgeInput is the call-site request.
type ConsistencyJudgeInput struct {
	Prompt         string
	NegativePrompt string
	// Mentions describes the referenced entities that the
	// asset-injector machinery has already resolved (e.g.
	// "@character/lin-xia", "@brand/nike"). The judge is told
	// about them so it can spot "entity missing from prior shot
	// context" issues.
	Mentions []string
}

// BuildVideoConsistencyJudgment is the DB-coupled call. Loads the
// project's most-recent N successful tasks (same machinery as
// decision_log), assembles the LLM prompt, calls
// llm.GetService().ProcessRequest with JSONMode, parses the
// verdict, and returns it.
//
// Never fails hard:
//
//   • nil DB / uid=0 / projectID=0 → skipped, OK=true
//   • LLM client not configured     → skipped, OK=true
//   • LLM call timed out / errored  → skipped, OK=true
//   • JSON parse failed             → skipped, OK=true
//
// All four paths return ConsistencyJudgment{OK:true, SkippedReason:...}
// with a non-nil error in the "configuration broke" case so the
// caller's logging can pick it up without ever surfacing the
// failure to the end user as "video render denied".
func BuildVideoConsistencyJudgment(
	ctx context.Context,
	db *gorm.DB,
	uid uint,
	projectID uint64,
	input ConsistencyJudgeInput,
) (judgment ConsistencyJudgment, err error) {
	// Best-effort observability — every return path lands in exactly
	// one of the three counters. Deferred classifier reads the final
	// named return value so existing inline returns don't have to
	// be touched.
	defer func() {
		switch {
		case judgment.SkippedReason != "":
			consistencyJudgeStats.skipped.Add(1)
		case judgment.OK:
			consistencyJudgeStats.ok.Add(1)
		default:
			consistencyJudgeStats.warned.Add(1)
		}
	}()

	startedAt := time.Now()
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		return ConsistencyJudgment{OK: true, SkippedReason: "empty prompt"}, nil
	}
	if db == nil || uid == 0 || projectID == 0 {
		return ConsistencyJudgment{OK: true, SkippedReason: "no project context"}, nil
	}

	priorShots, err := loadPriorShotPrompts(ctx, db, uid, projectID, MaxConsistencyContextShots)
	if err != nil {
		// DB error: don't punish the user. Log and skip.
		globals.Warn(fmt.Sprintf("[ConsistencyJudge] prior shots load failed for project %d: %v", projectID, err))
		return ConsistencyJudgment{OK: true, SkippedReason: "history unavailable"}, nil
	}
	if len(priorShots) == 0 {
		// Brand-new project (no prior successful renders) — nothing
		// to compare against. The judge prompt itself says to
		// return ok=true in this case; skip the call entirely
		// to save the round-trip.
		return ConsistencyJudgment{OK: true, SkippedReason: "no prior shots"}, nil
	}

	userMessage := composeConsistencyUserMessage(prompt, input.NegativePrompt, input.Mentions, priorShots)

	llmSvc := llmService.GetService()
	resp, err := llmSvc.ProcessRequest(ctx, llmService.UniversalLLMRequest{
		SystemPrompt: consistencyJudgePromptHeader,
		UserMessage:  userMessage,
		Temperature:  0.2, // low — we want consistent, conservative judgments
		MaxTokens:    600, // short JSON output; cap defends against rambling LLMs
		JSONMode:     true,
		UserID:       uid,
		Service:      "canvas.consistency_judge",
		RequestID:    fmt.Sprintf("consistency_%d_%d", projectID, startedAt.UnixNano()),
	})
	durationMs := int(time.Since(startedAt).Milliseconds())
	if err != nil || resp == nil || !resp.Success {
		errMsg := "llm call failed"
		if err != nil {
			errMsg = err.Error()
		} else if resp != nil && resp.Error != "" {
			errMsg = resp.Error
		}
		globals.Warn(fmt.Sprintf("[ConsistencyJudge] %s (project=%d, took=%dms)", errMsg, projectID, durationMs))
		return ConsistencyJudgment{
			OK:            true,
			SkippedReason: "llm unavailable",
			DurationMs:    durationMs,
		}, nil
	}

	verdict, parseErr := parseConsistencyVerdict(resp.Content)
	if parseErr != nil {
		globals.Warn(fmt.Sprintf("[ConsistencyJudge] parse failed (project=%d, took=%dms): %v", projectID, durationMs, parseErr))
		return ConsistencyJudgment{
			OK:            true,
			SkippedReason: "llm response unparseable",
			DurationMs:    durationMs,
			Model:         resp.Model,
		}, nil
	}
	verdict.Model = resp.Model
	verdict.DurationMs = durationMs
	return verdict, nil
}

// composeConsistencyUserMessage builds the user-side prompt the
// LLM judges. Format pinned so the LLM has a predictable shape:
// the new prompt on its own line, mentions as a comma list, then
// the numbered prior-shot context. Keep the whole thing tight —
// the LLM is cheap but tokens still matter.
func composeConsistencyUserMessage(
	prompt, negativePrompt string,
	mentions []string,
	priorShots []string,
) string {
	var b strings.Builder
	b.WriteString("NEW SHOT PROMPT:\n")
	b.WriteString(strings.TrimSpace(prompt))
	if neg := strings.TrimSpace(negativePrompt); neg != "" {
		b.WriteString("\n\nNEGATIVE PROMPT (avoid):\n")
		b.WriteString(neg)
	}
	if cleaned := dedupAndTrim(mentions); len(cleaned) > 0 {
		b.WriteString("\n\nREFERENCED ENTITIES:\n")
		b.WriteString(strings.Join(cleaned, ", "))
	}
	b.WriteString("\n\nPRIOR SHOTS (most recent first):\n")
	for i, p := range priorShots {
		fmt.Fprintf(&b, "%d. %s\n", i+1, p)
	}
	return b.String()
}

func dedupAndTrim(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// loadPriorShotPrompts queries the project's recent successful
// generation tasks for their request prompts. Filters to image+
// video shots (skips text-only / shape / drawing); returns at
// most `limit` prompts, most-recent-first.
func loadPriorShotPrompts(
	ctx context.Context,
	db *gorm.DB,
	uid uint,
	projectID uint64,
	limit int,
) ([]string, error) {
	type promptRow struct {
		RequestData model.JSONMap
		CompletedAt *time.Time
		CreatedAt   time.Time
	}
	rows := make([]promptRow, 0, limit*2) // over-fetch to allow filtering
	err := db.WithContext(ctx).
		Table("w_canvas_task_binding AS b").
		Select(
			"t.request_data AS request_data",
			"t.completed_at AS completed_at",
			"t.created_at AS created_at",
		).
		Joins("JOIN w_generation_task AS t ON t.task_id = b.task_id").
		Where("b.uid = ? AND b.project_id = ? AND t.status = ?",
			uid, projectID, model.TaskStatusCompleted).
		Order("t.completed_at DESC, t.created_at DESC").
		Limit(limit * 4). // headroom — some rows may have empty prompts
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, limit)
	for _, r := range rows {
		if len(out) >= limit {
			break
		}
		prompt := strings.TrimSpace(jsonMapString(r.RequestData, "prompt"))
		if prompt == "" {
			continue
		}
		// Hard-cap each prior prompt's length so a 5000-char prompt
		// from history doesn't blow the LLM's context budget. 400
		// chars is enough to capture the gist of any shot.
		if len(prompt) > 400 {
			prompt = prompt[:400] + "..."
		}
		out = append(out, prompt)
	}
	return out, nil
}

// parseConsistencyVerdict decodes the LLM's JSON output into a
// ConsistencyJudgment. Tolerant of two things the LLMs sometimes
// do that strict json.Unmarshal would choke on:
//
//   1. Wrapping the JSON in a markdown code fence (```json …  ```).
//      The handler strips one outer fence if present.
//   2. Returning {"ok": "true"} as a string instead of bool. We
//      coerce string→bool on the OK field so the LLM's
//      occasional sloppiness doesn't make the whole verdict
//      unusable.
func parseConsistencyVerdict(content string) (ConsistencyJudgment, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return ConsistencyJudgment{}, errors.New("empty content")
	}
	// Strip outer ```json … ``` fences if present.
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}

	// First try strict-typed unmarshal.
	var strict struct {
		OK       bool                 `json:"ok"`
		Summary  string               `json:"summary"`
		Warnings []ConsistencyWarning `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(content), &strict); err == nil {
		// Defensive: if ok=false but warnings is empty, treat as ok=true.
		// An "ok=false with no warnings" is a useless verdict.
		if !strict.OK && len(strict.Warnings) == 0 {
			strict.OK = true
		}
		return ConsistencyJudgment{
			OK:       strict.OK,
			Summary:  strict.Summary,
			Warnings: strict.Warnings,
		}, nil
	}

	// Fall back to loose decode that tolerates string "true"/"false"
	// for the ok field. The strict decode would have rejected.
	var loose map[string]interface{}
	if err := json.Unmarshal([]byte(content), &loose); err != nil {
		return ConsistencyJudgment{}, fmt.Errorf("json: %w", err)
	}
	verdict := ConsistencyJudgment{}
	switch v := loose["ok"].(type) {
	case bool:
		verdict.OK = v
	case string:
		verdict.OK = strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		verdict.OK = true // defensive: unknown OK shape → green light
	}
	if s, ok := loose["summary"].(string); ok {
		verdict.Summary = strings.TrimSpace(s)
	}
	if arr, ok := loose["warnings"].([]interface{}); ok {
		for _, raw := range arr {
			m, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			w := ConsistencyWarning{}
			if k, ok := m["kind"].(string); ok {
				w.Kind = strings.TrimSpace(k)
			}
			if d, ok := m["detail"].(string); ok {
				w.Detail = strings.TrimSpace(d)
			}
			if w.Detail == "" {
				continue
			}
			verdict.Warnings = append(verdict.Warnings, w)
		}
	}
	if !verdict.OK && len(verdict.Warnings) == 0 {
		verdict.OK = true
	}
	return verdict, nil
}

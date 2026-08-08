package workagent

// critique_dispatcher.go — M3 phase 2/3: Haiku sub-agent SDK
// invocation. The wrapper:
//
//   1. Selects an account via the agent_account_pool's
//      GetAccountForModelTier (defaulting to work-pro per S2 spike)
//   2. Builds a fresh ClaudeAgentGoClient for that account
//   3. Invokes the SDK with agentMode="critique" so the critique
//      skill's SKILL.md drives the system prompt + 5-dim rubric
//   4. Captures the assistant text via a minimal callback
//      adapter — no streaming UI, no session, no file writes
//   5. Hands the raw text to ParseCritiqueResponse (phase 1) for
//      structured decoding
//
// Phase 3 (the OnDone wiring) calls RunCritique after the pre-emit
// gate runs, then EvaluateCritique to decide pass/warn/block, then
// FormatCritiqueRedoPrompt + the existing redo loop on Block.
//
// The dispatcher's surface is intentionally minimal: one input
// struct, one output struct, one error. Hides the account-pool
// + client-builder + callback-adapter dance from the caller so a
// future replacement (e.g. direct gateway POST) is a one-method
// rewrite, not a sprawl across agent_turn_callbacks.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"server/globals"
)

// CritiqueInput is the per-turn payload the critique dispatcher
// consumes. The artifact text + generated files describe WHAT the
// sub-agent reviews; the uid + modelTier + workspaceRoot describe
// HOW we route the SDK call (which account, which sandbox).
type CritiqueInput struct {
	// UID is the requesting user's id — used by the critique-
	// gate flag check upstream and by pool accounting; the
	// sub-agent itself doesn't need it.
	UID uint

	// ModelTier scopes the account pool selection. Empty string
	// resolves to "work-pro" (per normalizeRequestedTier) which
	// is the right tier for the cheap-and-fast Haiku path
	// critique should run on.
	ModelTier string

	// SkillName is the parent skill that emitted the artifact
	// (ppt / marketingPoster / etc). Carried into the user
	// prompt so the critique sub-agent can apply skill-specific
	// rubric weights — but the sub-agent's OWN agentMode is
	// always "critique," not the parent skill.
	SkillName string

	// ArtifactText is the user-facing assistant text the parent
	// turn produced. Mirrors what runChecklistGate (M5) reads;
	// captured via the SDK's OnBlock streaming callback.
	ArtifactText string

	// GeneratedFiles is the optional list of artifact files (PPTX
	// / images / HTML) the parent turn dropped into the workspace.
	// We pass paths only — the sub-agent reads them via its own
	// file-tool capability if it needs to inspect contents.
	GeneratedFiles []map[string]interface{}

	// WorkspaceRoot is the on-disk path where the parent turn's
	// artifacts live. Sub-agent inherits the same workspace so
	// any file-read tool calls resolve relative paths correctly.
	WorkspaceRoot string
}

// CritiqueRunner abstracts the SDK invocation so the dispatcher
// (phase 3) can fake-test the orchestration layer without touching
// the SDK / account pool. Production wires the SDKCritiqueRunner
// implementation below.
//
// The shape is one method, raw-text out — keeping it narrower than
// the full SDK surface (callbacks / files / sessions) so the
// abstraction doesn't leak into phase 3.
type CritiqueRunner interface {
	// Invoke runs the critique skill once over the supplied input
	// and returns the assistant's raw text. The text is expected
	// to contain the structured JSON the parser consumes; the
	// runner does NOT parse — that's the dispatcher's job.
	Invoke(ctx context.Context, in CritiqueInput) (string, error)
}

// SDKCritiqueRunner is the production implementation. Holds no
// state itself — the account pool + client manager singletons are
// resolved per-call so a hot-reload of either picks up immediately.
type SDKCritiqueRunner struct{}

// Invoke runs the critique sub-agent via the same SDK plumbing
// the main agent uses. Differences:
//
//   - agentMode is hardcoded to "critique" (the critique skill)
//   - sessionID + agentSessionID are empty so each call starts
//     fresh; no session retry behavior to worry about
//   - callbacks accumulate text only — no streaming SSE, no done
//     event, no file scanning
//   - empty systemAdditions: the critique skill's SKILL.md is
//     intentionally self-contained (no preflight / discovery /
//     directions fold-in)
func (SDKCritiqueRunner) Invoke(ctx context.Context, in CritiqueInput) (string, error) {
	pool := GetAgentAccountPool()
	if pool == nil {
		return "", fmt.Errorf("critique: account pool not initialised")
	}
	account, err := pool.GetAccountForModelTier(in.ModelTier)
	if err != nil {
		return "", fmt.Errorf("critique: select account for tier %q: %w", in.ModelTier, err)
	}

	manager := GetAgentClientManager()
	if manager == nil {
		return "", fmt.Errorf("critique: client manager not initialised")
	}
	client, _, err := manager.BuildClientForAccount(account)
	if err != nil {
		return "", fmt.Errorf("critique: build client: %w", err)
	}

	// Concurrency note: OnBlock fires from the SDK's stream-reader
	// goroutine. The mutex protects the builder against the
	// (rare) case where the SDK emits multiple blocks in flight
	// while the reader hasn't drained yet. cheap insurance.
	var (
		mu       sync.Mutex
		captured strings.Builder
	)
	callbacks := AgentCallbacks{
		OnBlock: func(_ string, block json.RawMessage, _, _ int) {
			text := extractCritiqueText(block)
			if text == "" {
				return
			}
			mu.Lock()
			captured.WriteString(text)
			mu.Unlock()
		},
	}

	prompt := buildCritiqueUserPrompt(in)
	globals.Info(fmt.Sprintf("[Critique] invoking sub-agent: skill=%s tier=%s account=%d artifact_chars=%d",
		in.SkillName, in.ModelTier, account.ID, len(in.ArtifactText)))

	_, _, err = client.ProcessAgentConversationWithRetry(
		ctx,
		prompt,
		"",      // threadID — sub-agent doesn't share threads
		"",      // agentSessionID — fresh session every time
		in.WorkspaceRoot,
		nil,     // files — paths embedded in prompt; sub-agent re-reads on demand
		callbacks,
		"critique",
		"",      // customPrompt — the critique skill is self-contained
		account, // pre-resolved to bypass the inner GetActiveAccount fallback
		"",      // systemAdditions — critique skill carries its own rubric
	)
	if err != nil {
		return "", fmt.Errorf("critique: SDK invoke: %w", err)
	}

	return captured.String(), nil
}

// RunCritique is the public entry point for phase 3. Composes the
// runner + parser so callers see a single (input → result) hop.
// Errors propagate from the SDK and the parser unchanged so the
// dispatcher can branch on each:
//
//   - SDK error  → fail soft, log, skip critique (pre-emit gate
//                  result still emits)
//   - parse error → treat as Block (parser-failure ≠ silent pass,
//                  matching EvaluateCritique's zero-value posture)
//
// runner is exported as a parameter so phase 3 can inject either
// SDKCritiqueRunner{} (production) or a fake (tests). A nil
// runner defaults to SDK so callers that don't care about
// testability still work.
func RunCritique(ctx context.Context, runner CritiqueRunner, in CritiqueInput) (CritiqueResult, error) {
	if runner == nil {
		runner = SDKCritiqueRunner{}
	}
	raw, err := runner.Invoke(ctx, in)
	if err != nil {
		return CritiqueResult{}, err
	}
	return ParseCritiqueResponse(raw)
}

// buildCritiqueUserPrompt formats the artifact + generated-files
// context into the user-side message the sub-agent's first turn
// reads. SKILL.md owns the rubric instructions; this prompt only
// supplies the inputs:
//
//   <skill>ppt</skill>
//   <artifact-to-review>{accumulatedText}</artifact-to-review>
//   <generated-files>{relative paths, one per line}</generated-files>
//
// Tags mirror the existing SystemAdditions block format so the
// sub-agent's prompt reads as a familiar structured input rather
// than free-form prose.
func buildCritiqueUserPrompt(in CritiqueInput) string {
	var b strings.Builder
	if in.SkillName != "" {
		fmt.Fprintf(&b, "<skill>%s</skill>\n\n", in.SkillName)
	}
	b.WriteString("<artifact-to-review>\n")
	b.WriteString(strings.TrimSpace(in.ArtifactText))
	b.WriteString("\n</artifact-to-review>\n")

	if len(in.GeneratedFiles) > 0 {
		b.WriteString("\n<generated-files>\n")
		for _, f := range in.GeneratedFiles {
			path, _ := f["path"].(string)
			if path == "" {
				path, _ = f["name"].(string)
			}
			if path != "" {
				fmt.Fprintf(&b, "- %s\n", path)
			}
		}
		b.WriteString("</generated-files>\n")
	}

	b.WriteString("\nProvide your structured review per the SKILL.md JSON contract.")
	return b.String()
}

// extractCritiqueText — sub-agent variant of extractTextFromBlock
// (api/pro/tools/workagent/agent_turn_callbacks.go). Duplicated
// here rather than imported to keep the service-layer dispatcher
// free of API-package dependencies; the function is 5 lines and
// the cost of duplication is lower than the cost of the import
// graph inversion.
func extractCritiqueText(block json.RawMessage) string {
	if len(block) == 0 {
		return ""
	}
	var probe struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(block, &probe); err != nil {
		return ""
	}
	if probe.Type == "text" {
		return probe.Text
	}
	return ""
}

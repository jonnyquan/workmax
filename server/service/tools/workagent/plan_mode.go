package workagent

// plan_mode.go — system-prompt fragment appended when the user
// has Plan mode on for this turn. Lives in the workagent service
// package (not in surfaces/canvas) because /work-agent's plan
// vocabulary is different from canvas's: the canvas surface
// emits a `propose_plan` tool_use carrying canvas-tool steps,
// while /work-agent reuses the existing TodoWrite mechanism
// (latest_plan + plan_history columns) as the plan carrier.
//
// Design notes:
//
//   - TodoWrite is already the standard plan-tracking artefact in
//     /work-agent. Adding a parallel "propose_plan" tool just for
//     Plan mode would fork the data model; the ProgressSection
//     already renders TodoWrite snapshots, so the user-facing UI
//     stays the same in Plan mode and outside it.
//
//   - The protocol is therefore "emit ONE TodoWrite as the first
//     and only action, then halt for approval". The agent's next
//     turn (post-approval) executes the steps. The TodoWrite
//     rendered as a plan card IS the approval surface — the user
//     edits items, marks them in_progress / done / cancelled, and
//     the next turn picks up where the plan says.
//
//   - Per-turn flag, not persisted: matches the canvas surface's
//     PlanMode in CanvasAgentContext and avoids the "I forgot
//     Plan mode was on three weeks ago" failure mode. The FE
//     toggle resets on page reload.
//
// Tone of the protocol is imperative ("you MUST", "STOP") so it
// reads as a hard constraint rather than a suggestion — Anthropic
// models honor explicit prohibitions better than soft preferences.

// WorkAgentPlanModeProtocol is the markdown fragment the
// SystemAdditionsComposer slots into the system prompt's
// additions tail when ChatStreamRequest.PlanMode == true.
//
// Exported (capitalized) so the preflight composer in the same
// package — and tests in adjacent packages — can reference the
// const by name rather than re-pasting the text. Modifying this
// const is a model-behavior change; the test
// TestBuildPreflightAdditionsForThreadWithOptions_PlanModeInjected
// pins the protocol's presence so a typo can't silently delete it.
const WorkAgentPlanModeProtocol = `## Plan Mode (active)

The user has enabled **Plan mode** for this turn. You MUST:

1. Decide what sequence of production-tool calls would fulfill the user's request.
2. Emit a SINGLE ` + "`TodoWrite`" + ` tool call carrying that plan as the todo list (one todo per step).
3. STOP. Do NOT call any other tools in this turn (no script_generator, shotboard_generator, shot_prompt_generator, render_shot, lookup_asset, etc.).

The frontend will render your TodoWrite as a plan card. The user will approve, edit, or reject the plan. On approval the user's next message will re-enter the loop, and you will then execute each step using the corresponding tool calls in order.

### Plan content rules

- Each todo describes ONE concrete production-tool call (or a single thinking / lookup step). Keep the description ≤120 chars.
- Order steps so dependencies are satisfied — e.g. ` + "`lookup_asset`" + ` before ` + "`shot_prompt_generator`" + ` if the prompt needs the asset name.
- DO NOT inline the tool's full arguments in the todo description; the next turn fills those in. The plan is a roadmap, not the actual tool call.
- Mark every step ` + "`status: \"pending\"`" + ` — the user changes statuses on approval.
- If the user's request is a one-line micro-task ("rename this draft", "add one more shot"), prefer a single-step plan over refusing — Plan mode is opt-in, the user knows what they're getting.

If you cannot form a plan without more information, ask ONE clarifying question — do not emit a partial TodoWrite that hides the gap.`

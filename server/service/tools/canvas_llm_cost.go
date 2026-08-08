package tools

// Canvas LLM cost formulas are Server authoritative so the Desktop quote must
// match what the Server reserves. When a signed quote endpoint lands, Desktop
// should consume it rather than duplicate these formulas.

// CanvasChatCost computes the credit cost for a Canvas Chat submission. The
// Formula semantics: thinking
// replaces the base (6 instead of 2), and fast/web/attachments stack on top.
func CanvasChatCost(isThinking, isFast, useWeb bool, attachmentCount int) int {
	base := 2
	if isThinking {
		base = 6
	}
	if isFast {
		base += 1
	}
	if useWeb {
		base += 2
	}
	clamped := attachmentCount
	if clamped < 0 {
		clamped = 0
	}
	if clamped > 5 {
		clamped = 5
	}
	total := base + clamped
	if total < 1 {
		return 1
	}
	return total
}

// CanvasOptimizePromptCost is the flat cost for a single optimize-prompt call.
// Matches the PromptBuilder enhance path (2 credits).
func CanvasOptimizePromptCost() int {
	return 2
}

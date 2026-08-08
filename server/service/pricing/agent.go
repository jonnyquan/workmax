// Package pricing centralizes credit-cost formulas shared by multiple tools.
// Kept as a leaf package so any caller (workagent, canvasagent, …) can import
// it without risking the import cycle that plagued the service/tools root.
package pricing

import "math"

// Agent base cost for a single Claude Agent SDK run. Attachments stack on top
// up to the cap to match the UI estimator's clamp.
const (
	AgentBaseCost      = 6
	AgentAttachmentCap = 5

	// CreditsPerUSD converts the SDK-reported total_cost_usd into
	// credit units used by the project's billing service. 1 credit
	// = $0.005 (i.e. 200 credits per $1). Picked so a typical
	// turn (~$0.03 input/output via Anthropic) costs ~6 credits,
	// matching AgentBaseCost — the legacy file-count-based
	// ceiling and the new token-based actuals end up in the same
	// order of magnitude.
	//
	// Bumping this changes effective unit economics; never bump
	// without an updated pricing review.
	CreditsPerUSD = 200.0

	// AgentMinTokenCredits is the floor we charge for a successful
	// turn even when total_cost_usd is missing or pathologically
	// low (e.g. cache-only hits). 1 credit so the legitimate
	// "I asked one question, got one short answer" path doesn't
	// round to zero (which would let users grind unlimited free
	// queries via small turns).
	AgentMinTokenCredits = 1
)

// AgentCost returns credits consumed by one agent submission with the given
// attachment count. Negative values are treated as zero; counts above the
// cap are clamped.
//
// This is the LEGACY pre-turn ceiling. Reserve uses it as the upper bound
// of how many credits a turn can possibly consume; the post-turn settle
// step calls AgentCostFromUSD with the SDK's actual total_cost_usd and
// finalises at that lower value (with the delta refunded via
// service/account.CreditReservationService).
func AgentCost(attachmentCount int) int {
	clamped := attachmentCount
	if clamped < 0 {
		clamped = 0
	}
	if clamped > AgentAttachmentCap {
		clamped = AgentAttachmentCap
	}
	return AgentBaseCost + clamped
}

// AgentCostFromUSD converts the SDK-reported per-turn USD cost to the
// integer credit count we charge the user. Rounds UP via math.Ceil so we
// don't lose fractional credits to integer truncation — at $0.005 per
// credit, even a sub-half-credit underbill matters at scale.
//
// Clamped to AgentMinTokenCredits on the low end so a cache-hit turn
// can't grind to zero charge. Negative / NaN / Inf inputs fall back to
// the legacy ceiling-style price for safety; that should never happen
// (the SDK contract is non-negative finite) but a billing path should
// fail closed.
//
// Note: the returned credits may EXCEED the reservation ceiling for
// outlier turns. The caller is responsible for clamping to the
// reservation cap before passing to Finalize (which rejects
// used > reserved). The clamp lives at the call site because the cap
// depends on what was reserved, not on pricing math.
func AgentCostFromUSD(usd float64) int {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return AgentBaseCost
	}
	credits := int(math.Ceil(usd * CreditsPerUSD))
	if credits < AgentMinTokenCredits {
		return AgentMinTokenCredits
	}
	return credits
}

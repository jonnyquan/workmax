package canvas

import "server/service/pricing"

// AgentCost is the per-submission credit cost for a Canvas Agent run. Shared
// with workagent via pricing.AgentCost so there's a single pricing source.
func AgentCost(attachmentCount int) int {
	return pricing.AgentCost(attachmentCount)
}

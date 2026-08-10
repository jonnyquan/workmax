//go:build desktop

package local_agent

import (
	"log"

	"gorm.io/gorm"
)

// Durable "always allow" rules (migration 0008). Same degradation stance as
// session refs: a bookkeeping failure costs a grant (the user gets asked
// again), never the turn.

// loadAlwaysAllowed returns the uid's permanently allowed tools.
func loadAlwaysAllowed(db *gorm.DB, uid uint64) map[string]bool {
	var tools []string
	if err := db.Raw(
		`SELECT tool FROM w_desktop_agent_permission_rule WHERE uid = ? AND decision = 'allow'`,
		uid,
	).Scan(&tools).Error; err != nil {
		log.Printf("local agent: load permission rules for uid %d: %v", uid, err)
		return map[string]bool{}
	}
	set := make(map[string]bool, len(tools))
	for _, t := range tools {
		set[t] = true
	}
	return set
}

// storeAlwaysAllow records an "always allow" answer.
func storeAlwaysAllow(db *gorm.DB, uid uint64, tool string) {
	if err := db.Exec(
		`INSERT INTO w_desktop_agent_permission_rule (uid, tool, decision) VALUES (?, ?, 'allow')
		 ON CONFLICT(uid, tool) DO NOTHING`,
		uid, tool,
	).Error; err != nil {
		log.Printf("local agent: store permission rule %s for uid %d: %v", tool, uid, err)
	}
}

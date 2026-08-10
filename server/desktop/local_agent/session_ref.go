//go:build desktop

package local_agent

import (
	"log"

	"gorm.io/gorm"
)

// Session continuity bookkeeping (migration 0007). The ref is advisory:
// every helper here degrades to "" / a log line, because a bookkeeping
// failure must cost one turn of continuity, never the turn itself.

// loadSessionRef returns the runtime's continuity handle for a thread, or ""
// when none is stored.
func loadSessionRef(db *gorm.DB, threadUUID, runtime string) string {
	var ref string
	err := db.Raw(
		`SELECT session_ref FROM w_desktop_agent_session WHERE thread_uuid = ? AND runtime = ?`,
		threadUUID, runtime,
	).Scan(&ref).Error
	if err != nil {
		log.Printf("local agent: load session ref for %s: %v", threadUUID, err)
		return ""
	}
	return ref
}

// storeSessionRef records the handle the runtime reported this turn.
func storeSessionRef(db *gorm.DB, threadUUID, runtime, ref string) {
	if ref == "" {
		return
	}
	err := db.Exec(
		`INSERT INTO w_desktop_agent_session (thread_uuid, runtime, session_ref, updated_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(thread_uuid, runtime) DO UPDATE
		 SET session_ref = excluded.session_ref, updated_at = CURRENT_TIMESTAMP`,
		threadUUID, runtime, ref,
	).Error
	if err != nil {
		log.Printf("local agent: store session ref for %s: %v", threadUUID, err)
	}
}

// clearSessionRef drops the stored handle. Called when a turn that tried to
// resume failed: whether the session file is gone or the error was transient,
// the safe next state is the fallback path (history flattened into the
// prompt), which always works.
func clearSessionRef(db *gorm.DB, threadUUID, runtime string) {
	err := db.Exec(
		`DELETE FROM w_desktop_agent_session WHERE thread_uuid = ? AND runtime = ?`,
		threadUUID, runtime,
	).Error
	if err != nil {
		log.Printf("local agent: clear session ref for %s: %v", threadUUID, err)
	}
}

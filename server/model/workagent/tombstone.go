package workagent

import "time"

// Tombstone is a "this row got deleted" marker for desktop sync.
// Inserted in the same transaction that hard-deletes the underlying
// row (server/service/tools/workagent/thread_lifecycle_service.go
// DeleteThread; agent_other_endpoints.go DeleteMessage).
//
// The desktop sidecar's pull-sync (P1.A.2 + P1.A.4) merges these
// into the delta response items[] as action="delete" so the
// sidecar's UpsertThreads / UpsertMessages can remove the
// corresponding local row.
//
// EntityType: closed enum — currently "thread" and "message".
// Adding "render_job" / "thread_file" later requires:
//  1. Add a new TombstoneEntityType… const below.
//  2. Hook the corresponding delete handler to call
//     desktopsync.InsertTombstone in its transaction.
//  3. Merge tombstones for that type into the corresponding
//     /sync/<entity> endpoint.
//
// No schema change needed; the table is entity-type-agnostic.
type Tombstone struct {
	Id         uint      `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UID        int       `gorm:"column:uid;not null;index:idx_w_workagent_tombstone_uid_deleted_at,priority:1" json:"uid"`
	EntityType string    `gorm:"column:entity_type;type:varchar(32);not null;index:idx_w_workagent_tombstone_entity,priority:1" json:"entity_type"`
	EntityID   uint      `gorm:"column:entity_id;not null;index:idx_w_workagent_tombstone_entity,priority:2" json:"entity_id"`
	EntityUUID string    `gorm:"column:entity_uuid;type:varchar(64);not null" json:"entity_uuid"`
	DeletedAt  time.Time `gorm:"column:deleted_at;not null;default:CURRENT_TIMESTAMP;index:idx_w_workagent_tombstone_uid_deleted_at,priority:2" json:"deleted_at"`
}

func (Tombstone) TableName() string { return "w_workagent_tombstone" }

// TombstoneEntityType is the closed enum of valid `entity_type`
// values. Functions that write to or query w_workagent_tombstone
// should validate against IsValidTombstoneEntityType so a typo
// (e.g. "thrad") fails at insert time rather than silently
// breaking delete-propagation for desktop sync.
//
// The underlying type is string so existing call sites and DB
// queries continue to work; the typed-const form just gives
// new callers a way to declare intent unambiguously.
type TombstoneEntityType = string

// Closed enum of EntityType values. Use these consts at every
// callsite — string-typed mistakes would silently fail the sync
// query (no tombstones returned).
const (
	TombstoneEntityTypeThread  TombstoneEntityType = "thread"
	TombstoneEntityTypeMessage TombstoneEntityType = "message"
)

// IsValidTombstoneEntityType reports whether v is one of the known
// tombstone entity types. Returns false for empty strings, typos,
// and any value not yet in the closed enum. Callers that take
// untrusted strings (HTTP handlers, sync DB writes) must check this
// before passing the value to InsertTombstone / ListTombstonesDelta.
func IsValidTombstoneEntityType(v string) bool {
	switch v {
	case TombstoneEntityTypeThread, TombstoneEntityTypeMessage:
		return true
	}
	return false
}

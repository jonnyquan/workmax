package model

import (
	"time"
)

// CommerceProviderEvent is the durable inbox owner for one signature-verified
// payment-provider event. Raw payload bytes are retained so a reconciler can
// deterministically rebuild provider facts after an HTTP worker crash.
//
// The production table is migration-owned. GORM tags document the runtime
// shape and support typed queries; application startup must not AutoMigrate it.
// Keep the three owner fields explicit: globals.GraMODEL carries a process-time
// auto-update tag and second-precision datetime type, while this state machine
// requires database-authoritative datetime(6) values with no ON UPDATE clause.
type CommerceProviderEvent struct {
	Id                    uint       `json:"id" gorm:"column:id;type:bigint unsigned;primaryKey;autoIncrement"`
	CreatedAt             time.Time  `json:"createdAt" gorm:"column:created_at;type:datetime(6);not null;default:CURRENT_TIMESTAMP(6);autoCreateTime:false"`
	UpdatedAt             time.Time  `json:"updatedAt" gorm:"column:updated_at;type:datetime(6);not null;default:CURRENT_TIMESTAMP(6);autoUpdateTime:false"`
	Provider              string     `json:"provider" gorm:"column:provider;type:varchar(32) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	ProviderAccountID     string     `json:"providerAccountId" gorm:"column:provider_account_id;type:varchar(255) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	ProviderAPIVersion    string     `json:"providerApiVersion" gorm:"column:provider_api_version;type:varchar(32) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	EventID               string     `json:"eventId" gorm:"column:event_id;type:varchar(255) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	EventType             string     `json:"eventType" gorm:"column:event_type;type:varchar(128) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	ObjectID              string     `json:"objectId" gorm:"column:object_id;type:varchar(255) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	LiveMode              bool       `json:"liveMode" gorm:"column:live_mode;type:tinyint unsigned;not null"`
	ProviderCreatedAt     *time.Time `json:"providerCreatedAt" gorm:"column:provider_created_at;type:datetime(6)"`
	VerificationKeyDigest string     `json:"-" gorm:"column:verification_key_digest;type:char(71) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	PayloadDigest         string     `json:"payloadDigest" gorm:"column:payload_digest;type:char(64) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	PayloadJSON           []byte     `json:"-" gorm:"column:payload_json;type:mediumblob;not null"`
	Status                string     `json:"status" gorm:"column:status;type:varchar(32) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	AttemptCount          int        `json:"attemptCount" gorm:"column:attempt_count;type:int unsigned;not null"`
	ProcessingVersion     int64      `json:"processingVersion" gorm:"column:processing_version;type:bigint unsigned;not null"`
	LeaseOwnerID          string     `json:"-" gorm:"column:lease_owner_id;type:varchar(128) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	LeaseExpiresAt        *time.Time `json:"leaseExpiresAt" gorm:"column:lease_expires_at;type:datetime(6)"`
	NextAttemptAt         *time.Time `json:"nextAttemptAt" gorm:"column:next_attempt_at;type:datetime(6)"`
	ProcessedAt           *time.Time `json:"processedAt" gorm:"column:processed_at;type:datetime(6)"`
	OutcomeKind           string     `json:"outcomeKind" gorm:"column:outcome_kind;type:varchar(64) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	ResultDigest          string     `json:"resultDigest" gorm:"column:result_digest;type:char(64) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	LastErrorCode         string     `json:"lastErrorCode" gorm:"column:last_error_code;type:varchar(64) CHARACTER SET ascii COLLATE ascii_bin;not null"`
}

func (CommerceProviderEvent) TableName() string {
	return "w_commerce_provider_event"
}

const (
	CommerceProviderEventStatusReceived     = "received"
	CommerceProviderEventStatusProcessing   = "processing"
	CommerceProviderEventStatusRetryWait    = "retry_wait"
	CommerceProviderEventStatusProcessed    = "processed"
	CommerceProviderEventStatusIgnored      = "ignored"
	CommerceProviderEventStatusManualReview = "manual_review"
)

func (event CommerceProviderEvent) IsTerminal() bool {
	return event.Status == CommerceProviderEventStatusProcessed ||
		event.Status == CommerceProviderEventStatusIgnored
}

// CommerceOutbox is the transactional handoff created in the same database
// transaction that applies the paid Order/User/Pack mutation and terminalizes
// its Provider Event. A later dispatcher may deliver it without re-running the
// financial transaction.
//
// Its owner fields deliberately repeat the Inbox definition for the same
// database-clock reason; do not replace them with globals.GraMODEL.
type CommerceOutbox struct {
	Id               uint       `json:"id" gorm:"column:id;type:bigint unsigned;primaryKey;autoIncrement"`
	CreatedAt        time.Time  `json:"createdAt" gorm:"column:created_at;type:datetime(6);not null;default:CURRENT_TIMESTAMP(6);autoCreateTime:false"`
	UpdatedAt        time.Time  `json:"updatedAt" gorm:"column:updated_at;type:datetime(6);not null;default:CURRENT_TIMESTAMP(6);autoUpdateTime:false"`
	ProviderEventID  uint       `json:"providerEventId" gorm:"column:provider_event_id;type:bigint unsigned;not null"`
	Ordinal          int        `json:"ordinal" gorm:"column:ordinal;type:int unsigned;not null"`
	Topic            string     `json:"topic" gorm:"column:topic;type:varchar(128) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	DedupeKey        string     `json:"dedupeKey" gorm:"column:dedupe_key;type:char(64) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	PayloadDigest    string     `json:"payloadDigest" gorm:"column:payload_digest;type:char(64) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	PayloadJSON      []byte     `json:"-" gorm:"column:payload_json;type:mediumblob;not null"`
	Status           string     `json:"status" gorm:"column:status;type:varchar(32) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	AvailableAt      time.Time  `json:"availableAt" gorm:"column:available_at;type:datetime(6);not null"`
	DeliveryAttempts int64      `json:"deliveryAttempts" gorm:"column:delivery_attempts;type:bigint unsigned;not null"`
	DispatchVersion  int64      `json:"dispatchVersion" gorm:"column:dispatch_version;type:bigint unsigned;not null"`
	LeaseOwnerID     string     `json:"-" gorm:"column:lease_owner_id;type:varchar(128) CHARACTER SET ascii COLLATE ascii_bin;not null"`
	LeaseExpiresAt   *time.Time `json:"leaseExpiresAt" gorm:"column:lease_expires_at;type:datetime(6)"`
	DeliveredAt      *time.Time `json:"deliveredAt" gorm:"column:delivered_at;type:datetime(6)"`
	LastErrorCode    string     `json:"lastErrorCode" gorm:"column:last_error_code;type:varchar(64) CHARACTER SET ascii COLLATE ascii_bin;not null"`
}

func (CommerceOutbox) TableName() string {
	return "w_commerce_outbox"
}

const (
	CommerceOutboxStatusPending    = "pending"
	CommerceOutboxStatusDelivering = "delivering"
	CommerceOutboxStatusDelivered  = "delivered"
	CommerceOutboxStatusDeadLetter = "dead_letter"
)

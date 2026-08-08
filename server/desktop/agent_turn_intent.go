//go:build desktop

package desktop

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

const (
	agentTurnIntentStarting    = "starting"
	agentTurnIntentStreaming   = "streaming"
	agentTurnIntentInterrupted = "interrupted"
	agentTurnIntentCompleted   = "completed"
	agentTurnIntentCanceled    = "canceled"
	maxAgentTurnErrorKindBytes = 128
)

var (
	errAgentTurnIntentNotFound            = errors.New("agent turn intent not found")
	errAgentTurnIntentOwnerConflict       = errors.New("agent turn intent belongs to another user")
	errAgentTurnIntentIdempotencyConflict = errors.New("agent turn intent request conflicts")
	errAgentTurnIntentCanceled            = errors.New("agent turn intent is canceled")
	errAgentTurnIntentThreadNotFound      = errors.New("agent turn intent thread not found")
)

type agentTurnIntent struct {
	UID           uint64
	TurnUUID      string
	ThreadID      uint64
	ThreadUUID    string
	UserText      string
	ChatMode      string
	RequestDigest string
	State         string
	LastErrorKind string
	CreatedAt     string
	UpdatedAt     string
}

type agentTurnThread struct {
	ID            uint64
	UUID          string
	CloudThreadID string
}

type agentTurnDigestInput struct {
	ThreadUUID string `json:"thread_uuid"`
	UserText   string `json:"user_text"`
	ChatMode   string `json:"chat_mode"`
}

func digestAgentTurnIntent(threadUUID, userText, chatMode string) (string, error) {
	canonical, err := json.Marshal(agentTurnDigestInput{
		ThreadUUID: threadUUID,
		UserText:   userText,
		ChatMode:   chatMode,
	})
	if err != nil {
		return "", fmt.Errorf("encode agent turn digest input: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func validateAgentTurnThreadUUID(value string) (string, error) {
	validated, err := validateLocalHistoryThreadUUID(value)
	if err != nil || strings.ContainsAny(value, "/?#\\") {
		return "", fmt.Errorf("agent turn thread uuid is malformed")
	}
	return validated, nil
}

// validateFrozenAgentTurnIntent treats SQLite as durable but not infallible.
// Recovery must re-apply every renderer-facing and routing constraint before
// exposing a row or rebuilding an upstream request; the digest is an
// integrity cross-check, not an authentication primitive.
func validateFrozenAgentTurnIntent(intent agentTurnIntent) (time.Time, error) {
	if _, err := cloudproxy.DesktopTurnRequestID(intent.TurnUUID); err != nil {
		return time.Time{}, fmt.Errorf("invalid frozen turn uuid: %w", err)
	}
	if _, err := validateAgentTurnThreadUUID(intent.ThreadUUID); err != nil {
		return time.Time{}, err
	}
	if !utf8.ValidString(intent.UserText) || strings.TrimSpace(intent.UserText) == "" ||
		len(intent.UserText) > maxAgentTurnUserTextBytes || strings.ContainsRune(intent.UserText, '\x00') {
		return time.Time{}, fmt.Errorf("invalid frozen user text")
	}
	normalizedMode, allowed := NormalizeDesktopAgentMode(intent.ChatMode)
	if !allowed || normalizedMode != intent.ChatMode {
		return time.Time{}, fmt.Errorf("invalid frozen chat mode")
	}
	if len(intent.RequestDigest) != sha256.Size*2 {
		return time.Time{}, fmt.Errorf("invalid frozen request digest")
	}
	digest, err := digestAgentTurnIntent(intent.ThreadUUID, intent.UserText, intent.ChatMode)
	if err != nil || digest != intent.RequestDigest {
		return time.Time{}, fmt.Errorf("frozen request digest mismatch")
	}
	if !utf8.ValidString(intent.LastErrorKind) || len(intent.LastErrorKind) > maxAgentTurnErrorKindBytes || containsControl(intent.LastErrorKind) {
		return time.Time{}, fmt.Errorf("invalid frozen error kind")
	}
	updatedAt := parseSQLiteTime(intent.UpdatedAt)
	if updatedAt.IsZero() {
		return time.Time{}, fmt.Errorf("invalid frozen updated_at")
	}
	return updatedAt, nil
}

// ensureAgentTurnIntent applies first-writer-wins ownership before comparing
// request content. A cross-account UUID collision therefore returns one
// generic conflict without revealing whether the frozen text/digest matched.
func ensureAgentTurnIntent(
	db *gorm.DB,
	lease cloudproxy.SessionLease,
	uid uint64,
	turnUUID, threadUUID, userText, chatMode, requestDigest string,
) (agentTurnIntent, agentTurnThread, bool, error) {
	var (
		intent  agentTurnIntent
		thread  agentTurnThread
		created bool
	)
	if db == nil || uid == 0 {
		return intent, thread, false, fmt.Errorf("agent turn intent store is unavailable")
	}
	err := lease.WithCurrent(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			existing, found, err := selectAgentTurnIntent(tx, turnUUID)
			if err != nil {
				return err
			}
			if found {
				if err := validateAgentTurnIntentReplay(existing, uid, threadUUID, userText, chatMode, requestDigest); err != nil {
					return err
				}
				thread, err = selectOwnedAgentTurnThread(tx, uid, existing.ThreadUUID)
				if err != nil {
					return err
				}
				intent = existing
				return nil
			}

			thread, err = selectOwnedAgentTurnThread(tx, uid, threadUUID)
			if err != nil {
				return err
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			res := tx.Exec(`
				INSERT INTO w_desktop_agent_turn_intent
					(uid, turn_uuid, thread_id, thread_uuid, user_text, chat_mode,
					 request_digest, state, last_error_kind, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, 'starting', '', ?, ?)
				ON CONFLICT(turn_uuid) DO NOTHING`,
				uid, turnUUID, thread.ID, thread.UUID, userText, chatMode,
				requestDigest, now, now,
			)
			if res.Error != nil {
				return fmt.Errorf("insert agent turn intent: %w", res.Error)
			}
			intent, found, err = selectAgentTurnIntent(tx, turnUUID)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("insert agent turn intent did not produce a row")
			}
			if err := validateAgentTurnIntentReplay(intent, uid, threadUUID, userText, chatMode, requestDigest); err != nil {
				return err
			}
			created = res.RowsAffected == 1
			return nil
		})
	})
	return intent, thread, created, err
}

func validateAgentTurnIntentReplay(
	intent agentTurnIntent,
	uid uint64,
	threadUUID, userText, chatMode, requestDigest string,
) error {
	if intent.UID != uid {
		return errAgentTurnIntentOwnerConflict
	}
	if intent.ThreadUUID != threadUUID || intent.UserText != userText ||
		intent.ChatMode != chatMode || intent.RequestDigest != requestDigest {
		return errAgentTurnIntentIdempotencyConflict
	}
	if intent.State == agentTurnIntentCanceled {
		return errAgentTurnIntentCanceled
	}
	return nil
}

// checkAgentTurnIntentOwner performs the privacy-sensitive first lookup
// without comparing frozen request content. Callers use it before consulting
// the process-local lock so a replacement account cannot distinguish an
// active foreign turn from any other foreign UUID collision.
func checkAgentTurnIntentOwner(
	db *gorm.DB,
	lease cloudproxy.SessionLease,
	uid uint64,
	turnUUID string,
) (bool, error) {
	var ownerUID uint64
	err := lease.WithCurrent(func() error {
		err := db.Raw(`
			SELECT uid
			  FROM w_desktop_agent_turn_intent
			 WHERE turn_uuid = ?
			 LIMIT 1`, turnUUID,
		).Row().Scan(&ownerUID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("select agent turn intent owner: %w", err)
		}
		if ownerUID != uid {
			return errAgentTurnIntentOwnerConflict
		}
		return nil
	})
	return ownerUID == uid && ownerUID != 0, err
}

func loadOwnedAgentTurnIntentAndThread(
	db *gorm.DB,
	lease cloudproxy.SessionLease,
	uid uint64,
	turnUUID string,
) (agentTurnIntent, agentTurnThread, error) {
	var (
		intent agentTurnIntent
		thread agentTurnThread
	)
	err := lease.WithCurrent(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			var found bool
			var err error
			intent, found, err = selectAgentTurnIntent(tx, turnUUID)
			if err != nil {
				return err
			}
			if !found || intent.UID != uid {
				return errAgentTurnIntentNotFound
			}
			thread, err = selectOwnedAgentTurnThread(tx, uid, intent.ThreadUUID)
			return err
		})
	})
	return intent, thread, err
}

func selectAgentTurnIntent(db *gorm.DB, turnUUID string) (agentTurnIntent, bool, error) {
	var intent agentTurnIntent
	err := db.Raw(`
		SELECT uid, turn_uuid, thread_id, thread_uuid, user_text, chat_mode,
		       request_digest, state, last_error_kind, created_at, updated_at
		  FROM w_desktop_agent_turn_intent
		 WHERE turn_uuid = ?
		 LIMIT 1`, turnUUID,
	).Row().Scan(
		&intent.UID, &intent.TurnUUID, &intent.ThreadID, &intent.ThreadUUID,
		&intent.UserText, &intent.ChatMode, &intent.RequestDigest, &intent.State,
		&intent.LastErrorKind, &intent.CreatedAt, &intent.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return agentTurnIntent{}, false, nil
	}
	if err != nil {
		return agentTurnIntent{}, false, fmt.Errorf("select agent turn intent: %w", err)
	}
	return intent, true, nil
}

func selectOwnedAgentTurnThread(db *gorm.DB, uid uint64, threadUUID string) (agentTurnThread, error) {
	var thread agentTurnThread
	err := db.Raw(`
		SELECT id, uuid, COALESCE(cloud_thread_id, '')
		  FROM w_workagent_thread
		 WHERE uuid = ? AND uid = ?
		 LIMIT 1`, threadUUID, uid,
	).Row().Scan(&thread.ID, &thread.UUID, &thread.CloudThreadID)
	if errors.Is(err, sql.ErrNoRows) {
		return agentTurnThread{}, errAgentTurnIntentThreadNotFound
	}
	if err != nil {
		return agentTurnThread{}, fmt.Errorf("select agent turn thread: %w", err)
	}
	if thread.ID == 0 {
		return agentTurnThread{}, errAgentTurnIntentThreadNotFound
	}
	return thread, nil
}

func updateAgentTurnIntentState(
	db *gorm.DB,
	lease cloudproxy.SessionLease,
	uid uint64,
	turnUUID, state, lastErrorKind string,
) error {
	return lease.WithCurrent(func() error {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		res := db.Exec(`
			UPDATE w_desktop_agent_turn_intent
			   SET state = ?, last_error_kind = ?, updated_at = ?
			 WHERE uid = ? AND turn_uuid = ? AND state <> 'canceled'`,
			state, lastErrorKind, now, uid, turnUUID,
		)
		if res.Error != nil {
			return fmt.Errorf("update agent turn intent: %w", res.Error)
		}
		return nil
	})
}

// enterAgentTurnIntentState is the pre-upstream transition. Unlike terminal
// bookkeeping, it must observe one live non-canceled row: a concurrent cancel
// is authoritative and must prevent this request from reaching Cloud.
func enterAgentTurnIntentState(
	db *gorm.DB,
	lease cloudproxy.SessionLease,
	uid uint64,
	turnUUID, state string,
) error {
	return lease.WithCurrent(func() error {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		res := db.Exec(`
			UPDATE w_desktop_agent_turn_intent
			   SET state = ?, last_error_kind = '', updated_at = ?
			 WHERE uid = ? AND turn_uuid = ? AND state <> 'canceled'`,
			state, now, uid, turnUUID,
		)
		if res.Error != nil {
			return fmt.Errorf("enter agent turn intent state: %w", res.Error)
		}
		if res.RowsAffected != 1 {
			return errAgentTurnIntentCanceled
		}
		return nil
	})
}

// finalizeAgentTurnIntentOutcome is the narrowly-scoped terminal write used
// after a legacy stream has already produced its outcome. It intentionally
// does not require the now-current session lease: a replacement login must not
// leave the old, frozen uid/turn row stuck in streaming after the Renderer has
// seen done. The exact old owner is part of the UPDATE predicate, and canceled
// remains authoritative.
func finalizeAgentTurnIntentOutcome(
	db *gorm.DB,
	uid uint64,
	turnUUID, state, lastErrorKind string,
) error {
	if db == nil || uid == 0 {
		return fmt.Errorf("agent turn terminal store is unavailable")
	}
	if state != agentTurnIntentCompleted && state != agentTurnIntentInterrupted {
		return fmt.Errorf("invalid agent turn terminal state %q", state)
	}
	if _, err := cloudproxy.DesktopTurnRequestID(turnUUID); err != nil {
		return fmt.Errorf("invalid terminal turn uuid: %w", err)
	}
	if !utf8.ValidString(lastErrorKind) || len(lastErrorKind) > maxAgentTurnErrorKindBytes || containsControl(lastErrorKind) {
		return fmt.Errorf("invalid terminal error kind")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res := db.Exec(`
		UPDATE w_desktop_agent_turn_intent
		   SET state = ?, last_error_kind = ?, updated_at = ?
		 WHERE uid = ? AND turn_uuid = ? AND state <> 'canceled'`,
		state, lastErrorKind, now, uid, turnUUID,
	)
	if res.Error != nil {
		return fmt.Errorf("finalize agent turn intent: %w", res.Error)
	}
	if res.RowsAffected == 1 {
		return nil
	}
	var ownerUID uint64
	var currentState string
	err := db.Raw(`
		SELECT uid, state
		  FROM w_desktop_agent_turn_intent
		 WHERE turn_uuid = ?
		 LIMIT 1`, turnUUID,
	).Row().Scan(&ownerUID, &currentState)
	if err != nil {
		return fmt.Errorf("verify agent turn terminal ownership: %w", err)
	}
	if ownerUID == uid && currentState == agentTurnIntentCanceled {
		return nil
	}
	return fmt.Errorf("agent turn terminal ownership changed")
}

type recoverableAgentTurnIntent struct {
	TurnUUID      string    `json:"turn_uuid"`
	ThreadUUID    string    `json:"thread_uuid"`
	UserText      string    `json:"user_text"`
	ChatMode      string    `json:"chat_mode"`
	State         string    `json:"state"`
	LastErrorKind string    `json:"last_error_kind"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func listRecoverableAgentTurnIntents(
	db *gorm.DB,
	lease cloudproxy.SessionLease,
	uid uint64,
	limit int,
) ([]recoverableAgentTurnIntent, error) {
	items := make([]recoverableAgentTurnIntent, 0, limit)
	err := lease.WithCurrent(func() error {
		rows, err := db.Raw(`
			SELECT turn_uuid, thread_uuid, user_text, chat_mode, request_digest,
			       state, last_error_kind, updated_at
			  FROM w_desktop_agent_turn_intent
			 WHERE uid = ? AND state = 'interrupted'
			 ORDER BY updated_at DESC, turn_uuid
			 LIMIT ?`, uid, limit,
		).Rows()
		if err != nil {
			return fmt.Errorf("list recoverable agent turn intents: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var intent agentTurnIntent
			if err := rows.Scan(
				&intent.TurnUUID, &intent.ThreadUUID, &intent.UserText, &intent.ChatMode,
				&intent.RequestDigest, &intent.State, &intent.LastErrorKind, &intent.UpdatedAt,
			); err != nil {
				return fmt.Errorf("scan recoverable agent turn intent: %w", err)
			}
			updatedAt, err := validateFrozenAgentTurnIntent(intent)
			if err != nil {
				return fmt.Errorf("recoverable agent turn is invalid: %w", err)
			}
			items = append(items, recoverableAgentTurnIntent{
				TurnUUID: intent.TurnUUID, ThreadUUID: intent.ThreadUUID,
				UserText: intent.UserText, ChatMode: intent.ChatMode,
				State: intent.State, LastErrorKind: intent.LastErrorKind,
				UpdatedAt: updatedAt,
			})
		}
		return rows.Err()
	})
	return items, err
}

func cancelAgentTurnIntent(
	db *gorm.DB,
	lease cloudproxy.SessionLease,
	uid uint64,
	turnUUID string,
) (bool, error) {
	var canceled bool
	err := lease.WithCurrent(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			intent, found, err := selectAgentTurnIntent(tx, turnUUID)
			if err != nil {
				return err
			}
			if !found || intent.UID != uid {
				return errAgentTurnIntentNotFound
			}
			if intent.State == agentTurnIntentCanceled || intent.State == agentTurnIntentCompleted {
				return nil
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			res := tx.Exec(`
				UPDATE w_desktop_agent_turn_intent
				   SET state = 'canceled', last_error_kind = '', updated_at = ?
				 WHERE uid = ? AND turn_uuid = ? AND state NOT IN ('canceled', 'completed')`,
				now, uid, turnUUID,
			)
			if res.Error != nil {
				return fmt.Errorf("cancel agent turn intent: %w", res.Error)
			}
			canceled = res.RowsAffected == 1
			return nil
		})
	})
	return canceled, err
}

func cleanupAbandonedAgentTurnIntents(db *gorm.DB) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res := db.Exec(`
		UPDATE w_desktop_agent_turn_intent
		   SET state = 'interrupted',
		       last_error_kind = 'sidecar_restarted',
		       updated_at = ?
		 WHERE state IN ('starting', 'streaming')`, now)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCreditReservationStateHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        string
		terminal      bool
		held          bool
		refundPending bool
		activeDebited bool
	}{
		{name: "reserved", status: CreditReservationStatusReserved, activeDebited: true},
		{name: "review hold", status: CreditReservationStatusReviewHold, held: true, activeDebited: true},
		{name: "refund pending", status: CreditReservationStatusRefundPending, refundPending: true, activeDebited: true},
		{name: "finalized", status: CreditReservationStatusFinalized, terminal: true},
		{name: "released", status: CreditReservationStatusReleased, terminal: true},
		{name: "expired", status: CreditReservationStatusExpired, terminal: true},
		{name: "empty", status: ""},
		// metered_held belongs to the Agent settlement review state machine. It
		// must never silently become a recognized Credits state.
		{name: "agent metered hold is not a credit state", status: "metered_held"},
		{name: "unknown", status: "unknown"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reservation := CreditReservation{Status: tt.status}
			if got := reservation.IsTerminal(); got != tt.terminal {
				t.Errorf("IsTerminal() = %v, want %v", got, tt.terminal)
			}
			if got := reservation.IsHeld(); got != tt.held {
				t.Errorf("IsHeld() = %v, want %v", got, tt.held)
			}
			if got := reservation.IsRefundPending(); got != tt.refundPending {
				t.Errorf("IsRefundPending() = %v, want %v", got, tt.refundPending)
			}
			if got := reservation.IsActiveDebited(); got != tt.activeDebited {
				t.Errorf("IsActiveDebited() = %v, want %v", got, tt.activeDebited)
			}
		})
	}
}

func TestCreditReservationP0046GORMTypesMirrorMigration(t *testing.T) {
	t.Parallel()
	typeOf := reflect.TypeOf(CreditReservation{})
	wants := map[string][]string{
		"RequestDigest":       {"varchar(64)", "ascii_bin", "default:null"},
		"HoldReviewID":        {"varchar(256)", "ascii_bin", "default:null"},
		"HoldSettlementKey":   {"varchar(256)", "ascii_bin", "default:null"},
		"HoldRequestDigest":   {"varchar(128)", "ascii_bin", "default:null"},
		"ReviewHeldAt":        {"datetime(6)"},
		"RefundTargetStatus":  {"varchar(16)", "ascii_bin", "default:null"},
		"RefundTargetUsed":    {"int unsigned"},
		"RefundDue":           {"int unsigned"},
		"RefundAttempts":      {"bigint unsigned"},
		"NextRefundAt":        {"datetime(6)"},
		"LastRefundErrorCode": {"varchar(64)", "ascii_bin", "default:null"},
		"StateChangedAt":      {"datetime(6)"},
		"StateVersion":        {"bigint unsigned"},
	}
	for fieldName, fragments := range wants {
		field, ok := typeOf.FieldByName(fieldName)
		if !ok {
			t.Fatalf("missing field %s", fieldName)
		}
		tag := strings.ToLower(field.Tag.Get("gorm"))
		for _, fragment := range fragments {
			if !strings.Contains(tag, fragment) {
				t.Errorf("%s gorm tag %q missing %q", fieldName, tag, fragment)
			}
		}
	}
	refundAttempts, ok := typeOf.FieldByName("RefundAttempts")
	if !ok || refundAttempts.Type.Kind() != reflect.Uint64 {
		t.Fatal("RefundAttempts must be uint64 for bigint unsigned")
	}
}

func TestCreditReservationLegacyJSONFieldNamesRemainCompatible(t *testing.T) {
	t.Parallel()

	expiresAt := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	encoded, err := json.Marshal(CreditReservation{
		UID:            42,
		Tool:           "workagent",
		IdempotencyKey: "request-1",
		QuoteID:        "quote-1",
		Reserved:       10,
		Used:           3,
		Status:         CreditReservationStatusReserved,
		ExpiresAt:      expiresAt,
		Remark:         "legacy",
		ProjectID:      9,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{
		"uid", "tool", "idempotencyKey", "quoteId", "reserved", "used",
		"status", "expiresAt", "finalizedAt", "releasedAt", "remark", "projectId",
	} {
		if _, ok := object[key]; !ok {
			t.Errorf("legacy JSON key %q is missing from %s", key, encoded)
		}
	}
	for _, key := range []string{
		"requestDigest", "holdReviewId", "holdSettlementKey", "holdRequestDigest",
		"reviewHeldAt", "refundTargetStatus", "refundTargetUsed", "refundDue",
		"refundAttempts", "nextRefundAt", "lastRefundErrorCode", "stateChangedAt",
		"stateVersion",
	} {
		if _, ok := object[key]; ok {
			t.Errorf("zero-value P0-046 JSON key %q must be omitted from %s", key, encoded)
		}
	}
}

func TestCreditReservationRefundTargetUsedPreservesNullableZero(t *testing.T) {
	t.Parallel()

	zero := 0
	encoded, err := json.Marshal(CreditReservation{
		RefundTargetStatus: CreditReservationStatusFinalized,
		RefundTargetUsed:   &zero,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got, ok := object["refundTargetUsed"]; !ok || got != float64(0) {
		t.Fatalf("refundTargetUsed = %#v, present=%v; want explicit numeric zero", got, ok)
	}
}

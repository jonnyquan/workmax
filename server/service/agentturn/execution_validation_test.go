package agentturn

import (
	"strings"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
)

func TestTurnAttemptValidateLifecycleUpdateOrdering(t *testing.T) {
	base := validTurnAttemptForValidation()

	t.Run("updatedAt may equal lastHeartbeatAt", func(t *testing.T) {
		attempt := base
		attempt.UpdatedAt = attempt.LastHeartbeatAt
		if err := attempt.Validate(); err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
	})

	t.Run("updatedAt must not precede lastHeartbeatAt", func(t *testing.T) {
		attempt := base
		attempt.UpdatedAt = attempt.LastHeartbeatAt.Add(-time.Nanosecond)
		if err := attempt.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want lifecycle ordering error")
		}
	})

	t.Run("terminal updatedAt may equal finishedAt", func(t *testing.T) {
		attempt := terminalTurnAttemptForValidation(base)
		attempt.UpdatedAt = *attempt.FinishedAt
		if err := attempt.Validate(); err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
	})

	t.Run("terminal updatedAt must not precede finishedAt", func(t *testing.T) {
		attempt := terminalTurnAttemptForValidation(base)
		attempt.UpdatedAt = attempt.FinishedAt.Add(-time.Nanosecond)
		if err := attempt.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want finishedAt ordering error")
		}
	})
}

func TestValidEffectOutboxStateLeaseAndErrorBounds(t *testing.T) {
	base := validDeliveringEffectOutboxRow()

	tests := []struct {
		name   string
		mutate func(*sqlEffectOutboxRow)
		want   bool
	}{
		{
			name: "valid boundary lengths",
			mutate: func(row *sqlEffectOutboxRow) {
				leaseOwnerID := strings.Repeat("w", MaxWorkerIDBytes)
				lastErrorCode := strings.Repeat("e", MaxEffectErrorCodeBytes)
				row.LeaseOwnerID = &leaseOwnerID
				row.LastErrorCode = &lastErrorCode
			},
			want: true,
		},
		{
			name: "lease owner exceeds bound",
			mutate: func(row *sqlEffectOutboxRow) {
				leaseOwnerID := strings.Repeat("w", MaxWorkerIDBytes+1)
				row.LeaseOwnerID = &leaseOwnerID
			},
			want: false,
		},
		{
			name: "lease owner is not printable ASCII",
			mutate: func(row *sqlEffectOutboxRow) {
				leaseOwnerID := "worker\nsecondary"
				row.LeaseOwnerID = &leaseOwnerID
			},
			want: false,
		},
		{
			name: "error code exceeds bound",
			mutate: func(row *sqlEffectOutboxRow) {
				lastErrorCode := strings.Repeat("e", MaxEffectErrorCodeBytes+1)
				row.LastErrorCode = &lastErrorCode
			},
			want: false,
		},
		{
			name: "error code is not printable ASCII",
			mutate: func(row *sqlEffectOutboxRow) {
				lastErrorCode := "provider\nerror"
				row.LastErrorCode = &lastErrorCode
			},
			want: false,
		},
		{
			name: "lease expiry follows update",
			mutate: func(row *sqlEffectOutboxRow) {
				leaseExpiresAt := row.UpdatedAt.Add(time.Nanosecond)
				row.LeaseExpiresAt = &leaseExpiresAt
			},
			want: true,
		},
		{
			name: "lease expiry equals update",
			mutate: func(row *sqlEffectOutboxRow) {
				leaseExpiresAt := row.UpdatedAt
				row.LeaseExpiresAt = &leaseExpiresAt
			},
			want: false,
		},
		{
			name: "lease expiry precedes update",
			mutate: func(row *sqlEffectOutboxRow) {
				leaseExpiresAt := row.UpdatedAt.Add(-time.Nanosecond)
				row.LeaseExpiresAt = &leaseExpiresAt
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := base
			test.mutate(&row)
			if got := validEffectOutboxState(row); got != test.want {
				t.Fatalf("validEffectOutboxState() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestValidEffectOutboxStateTerminalTimestampOrdering(t *testing.T) {
	createdAt := time.Date(2026, time.August, 3, 8, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		status    string
		timestamp time.Time
		want      bool
	}{
		{name: "delivered may equal createdAt", status: "delivered", timestamp: createdAt, want: true},
		{name: "delivered must not precede createdAt", status: "delivered", timestamp: createdAt.Add(-time.Nanosecond), want: false},
		{name: "dead letter may equal createdAt", status: "dead_letter", timestamp: createdAt, want: true},
		{name: "dead letter must not precede createdAt", status: "dead_letter", timestamp: createdAt.Add(-time.Nanosecond), want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := sqlEffectOutboxRow{
				Status:               test.status,
				DeliveryAttempts:     1,
				DispatchFencingToken: 1,
				CreatedAt:            createdAt,
				UpdatedAt:            createdAt,
			}
			switch test.status {
			case "delivered":
				row.DeliveredAt = &test.timestamp
			case "dead_letter":
				row.DeadLetteredAt = &test.timestamp
			}
			if got := validEffectOutboxState(row); got != test.want {
				t.Fatalf("validEffectOutboxState() = %t, want %t", got, test.want)
			}
		})
	}
}

func validTurnAttemptForValidation() TurnAttempt {
	claimedAt := time.Date(2026, time.August, 3, 8, 0, 0, 0, time.UTC)
	lastHeartbeatAt := claimedAt.Add(time.Second)
	return TurnAttempt{
		ID:                "attempt_validation",
		TurnID:            agentv1.TurnID("turn_validation"),
		FencingToken:      1,
		Status:            AttemptStatusRunning,
		WorkerID:          "worker_validation",
		WorkerBuildDigest: "build_validation",
		LeaseExpiresAt:    lastHeartbeatAt.Add(DefaultAttemptLeaseTTL),
		ClaimedAt:         claimedAt,
		LastHeartbeatAt:   lastHeartbeatAt,
		CreatedAt:         claimedAt,
		UpdatedAt:         lastHeartbeatAt.Add(time.Second),
	}
}

func terminalTurnAttemptForValidation(base TurnAttempt) TurnAttempt {
	attempt := base
	finishedAt := attempt.LastHeartbeatAt.Add(time.Second)
	attempt.Status = AttemptStatusCompleted
	attempt.FinishedAt = &finishedAt
	attempt.UpdatedAt = finishedAt.Add(time.Second)
	return attempt
}

func validDeliveringEffectOutboxRow() sqlEffectOutboxRow {
	createdAt := time.Date(2026, time.August, 3, 8, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Second)
	leaseOwnerID := "worker_validation"
	leaseExpiresAt := updatedAt.Add(DefaultEffectLeaseTTL)
	return sqlEffectOutboxRow{
		Status:               "delivering",
		DeliveryAttempts:     1,
		DispatchFencingToken: 1,
		LeaseOwnerID:         &leaseOwnerID,
		LeaseExpiresAt:       &leaseExpiresAt,
		CreatedAt:            createdAt,
		UpdatedAt:            updatedAt,
	}
}

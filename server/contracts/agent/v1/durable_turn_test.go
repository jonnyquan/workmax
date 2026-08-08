package v1

import (
	"encoding/json"
	"testing"
)

func TestTurnStatusVocabularyAndTerminalBoundary(t *testing.T) {
	tests := []struct {
		status   TurnStatus
		terminal bool
	}{
		{TurnStatusQueued, false},
		{TurnStatusRunning, false},
		{TurnStatusCompleted, true},
		{TurnStatusStopped, true},
		{TurnStatusFailed, true},
		{TurnStatusTimeout, true},
	}
	for _, test := range tests {
		if !test.status.Valid() {
			t.Errorf("documented status %q is invalid", test.status)
		}
		if got := test.status.Terminal(); got != test.terminal {
			t.Errorf("status %q Terminal() = %v, want %v", test.status, got, test.terminal)
		}
	}
	if TurnStatus("cancelled").Valid() {
		t.Fatal("undocumented cancelled alias must not replace stopped")
	}
}

func TestStartContractsRequireDurableIdentity(t *testing.T) {
	request := StartRequest{ThreadID: "thread_1", IdempotencyKey: "idem_1"}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid StartRequest rejected: %v", err)
	}
	for _, invalid := range []StartRequest{
		{IdempotencyKey: "idem_1"},
		{ThreadID: "thread_1"},
		{ThreadID: " thread_1", IdempotencyKey: "idem_1"},
	} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("invalid StartRequest accepted: %+v", invalid)
		}
	}

	result := StartAdmissionResult{TurnID: "turn_1", StreamURL: "/api/v1/agent/threads/thread_1/turns/turn_1/stream"}
	if err := result.Validate(); err != nil {
		t.Fatalf("valid StartAdmissionResult rejected: %v", err)
	}
	if err := (StartAdmissionResult{TurnID: "turn_1"}).Validate(); err == nil {
		t.Fatal("admission without streamUrl was accepted")
	}
}

func TestReplayCursorSupportsOneTransportForm(t *testing.T) {
	after := Sequence(42)
	for _, cursor := range []ReplayCursor{
		{},
		{LastEventID: "turn_1:42"},
		{AfterSequence: &after},
		{LastEventID: "turn_1:42", AfterSequence: &after},
	} {
		if err := cursor.Validate(); err != nil {
			t.Errorf("valid cursor %+v rejected: %v", cursor, err)
		}
	}

	if err := (ReplayCursor{LastEventID: " turn_1:42"}).Validate(); err == nil {
		t.Fatal("cursor with surrounding whitespace was accepted")
	}
	if err := (AttachRequest{TurnID: "turn_1", Cursor: ReplayCursor{AfterSequence: &after}}).Validate(); err != nil {
		t.Fatalf("valid AttachRequest rejected: %v", err)
	}
}

func TestReplayWindowBounds(t *testing.T) {
	if empty := (ReplayWindow{}); !empty.Empty() || empty.Validate() != nil {
		t.Fatalf("zero replay window must be a valid empty window: %+v", empty)
	}
	if err := (ReplayWindow{OldestSequence: 4, LatestSequence: 9}).Validate(); err != nil {
		t.Fatalf("valid replay window rejected: %v", err)
	}
	for _, invalid := range []ReplayWindow{
		{OldestSequence: 1},
		{LatestSequence: 1},
		{OldestSequence: 9, LatestSequence: 4},
	} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("invalid replay window accepted: %+v", invalid)
		}
	}
}

func TestEventEnvelopeValidationKeepsDomainTypesOpen(t *testing.T) {
	event := validEvent(42, "turn_1:42")
	event.Type = "writer.document.proposed"
	if err := event.Validate(); err != nil {
		t.Fatalf("valid namespaced domain event rejected: %v", err)
	}

	mutations := []func(*EventEnvelope){
		func(e *EventEnvelope) { e.SchemaVersion = 0 },
		func(e *EventEnvelope) { e.FrameKind = "heartbeat" },
		func(e *EventEnvelope) { e.EventID = "" },
		func(e *EventEnvelope) { e.TurnID = "" },
		func(e *EventEnvelope) { e.Sequence = 0 },
		func(e *EventEnvelope) { e.Plugin.ReleaseDigest = "" },
		func(e *EventEnvelope) { e.Type = "" },
		func(e *EventEnvelope) { e.Visibility = "internal" },
		func(e *EventEnvelope) { e.Data = json.RawMessage(`{`) },
		func(e *EventEnvelope) { e.ResourceRefs = []string{""} },
	}
	for i, mutate := range mutations {
		candidate := event
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Errorf("invalid event mutation %d was accepted: %+v", i, candidate)
		}
	}
}

func TestValidateEventSequenceRequiresMonotonicSingleTurnEvents(t *testing.T) {
	events := []EventEnvelope{
		validEvent(1, "turn_1:1"),
		validEvent(3, "turn_1:3"), // gaps are legal; order is the invariant
		validEvent(8, "turn_1:8"),
	}
	if err := ValidateEventSequence(events); err != nil {
		t.Fatalf("valid non-contiguous sequence rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func([]EventEnvelope)
	}{
		{name: "sequence regression", mutate: func(events []EventEnvelope) { events[2].Sequence = 2 }},
		{name: "duplicate event id", mutate: func(events []EventEnvelope) { events[2].EventID = events[1].EventID }},
		{name: "mixed turn", mutate: func(events []EventEnvelope) { events[2].TurnID = "turn_2" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := append([]EventEnvelope(nil), events...)
			test.mutate(candidate)
			if err := ValidateEventSequence(candidate); err == nil {
				t.Fatalf("invalid sequence was accepted: %+v", candidate)
			}
		})
	}
}

func TestCancelIntentAndTerminalReconciliationStaySeparate(t *testing.T) {
	if err := (CancelRequest{TurnID: "turn_1"}).Validate(); err != nil {
		t.Fatalf("valid CancelRequest rejected: %v", err)
	}
	if err := (CancelRequest{}).Validate(); err == nil {
		t.Fatal("CancelRequest without TurnID was accepted")
	}

	for _, status := range []TurnStatus{
		TurnStatusCompleted,
		TurnStatusStopped,
		TurnStatusFailed,
		TurnStatusTimeout,
	} {
		if err := (TerminalState{TurnID: "turn_1", Status: status}).Validate(); err != nil {
			t.Errorf("valid TerminalState %q rejected: %v", status, err)
		}
	}
	for _, status := range []TurnStatus{TurnStatusQueued, TurnStatusRunning, "cancelled"} {
		if err := (TerminalState{TurnID: "turn_1", Status: status}).Validate(); err == nil {
			t.Errorf("non-terminal TerminalState %q was accepted", status)
		}
	}
}

func validEvent(sequence Sequence, eventID string) EventEnvelope {
	return EventEnvelope{
		SchemaVersion: EventEnvelopeSchemaVersion,
		FrameKind:     EventFrameEvent,
		EventID:       eventID,
		TurnID:        "turn_1",
		Sequence:      sequence,
		Plugin: EventPluginRef{
			ID:            "workmax.writer",
			Version:       "1.2.0",
			ReleaseDigest: "sha256:plugin",
		},
		Type:         EventCoreTurnStatus,
		Visibility:   EventVisibilityUser,
		ResourceRefs: []string{"wm:workmax.writer:document:doc_1@7"},
		Data:         json.RawMessage(`{}`),
	}
}

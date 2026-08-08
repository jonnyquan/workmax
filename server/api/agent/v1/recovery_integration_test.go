package agentv1api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	agentcontract "server/contracts/agent/v1"
	"server/service/agentturn"
)

func TestCandidateCompositionRecoversStatusAndReplayAfterSQLiteReopen(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "candidate-recovery.db")
	firstDB := openCandidateRecoveryDB(t, databasePath)
	installCandidateRecoverySchema(t, firstDB)

	firstStore, err := agentturn.NewSQLStore(firstDB)
	if err != nil {
		t.Fatalf("NewSQLStore(first): %v", err)
	}
	firstService, err := agentturn.NewService(agentturn.ServiceConfig{
		Store: firstStore,
		NewTurnID: func() (agentcontract.TurnID, error) {
			return "turn_recovery", nil
		},
		Now: func() time.Time { return candidateTestTime },
	})
	if err != nil {
		t.Fatalf("NewService(first): %v", err)
	}

	ctx := context.Background()
	started, err := firstService.Start(ctx, agentturn.StartCommand{
		PrincipalID: "principal_1",
		Request: agentcontract.StartRequest{
			ThreadID:       "thread_1",
			IdempotencyKey: "idem-candidate-recovery",
		},
		CommandDigest: "sha256:candidate-recovery",
		Plugin: agentcontract.EventPluginRef{
			ID:            "workmax.writer",
			Version:       "1.0.0",
			ReleaseDigest: "sha256:plugin-recovery",
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := firstService.Transition(ctx, started.Turn.ID, agentcontract.TurnStatusRunning); err != nil {
		t.Fatalf("Transition(running): %v", err)
	}
	if _, err := firstService.AppendDomainEvent(ctx, started.Turn.ID, agentturn.EventDraft{
		Type: agentcontract.EventAssistantTextDelta,
		Data: json.RawMessage(`{"text":"persisted before restart"}`),
	}); err != nil {
		t.Fatalf("AppendDomainEvent: %v", err)
	}
	if _, err := firstService.Transition(ctx, started.Turn.ID, agentcontract.TurnStatusCompleted); err != nil {
		t.Fatalf("Transition(completed): %v", err)
	}

	firstSQLDB, err := firstDB.DB()
	if err != nil {
		t.Fatalf("first sql.DB: %v", err)
	}
	if err := firstSQLDB.Close(); err != nil {
		t.Fatalf("close first SQLite handle: %v", err)
	}

	reopenedDB := openCandidateRecoveryDB(t, databasePath)
	reopenedSQLDB, err := reopenedDB.DB()
	if err != nil {
		t.Fatalf("reopened sql.DB: %v", err)
	}
	t.Cleanup(func() { _ = reopenedSQLDB.Close() })
	reopenedStore, err := agentturn.NewSQLStore(reopenedDB)
	if err != nil {
		t.Fatalf("NewSQLStore(reopened): %v", err)
	}
	reopenedService, err := agentturn.NewService(agentturn.ServiceConfig{Store: reopenedStore})
	if err != nil {
		t.Fatalf("NewService(reopened): %v", err)
	}
	turnStream, err := agentturn.NewTurnEventStream(reopenedStore, agentturn.EventStreamOptions{
		PollInterval: time.Millisecond,
		PageLimit:    1,
	})
	if err != nil {
		t.Fatalf("NewTurnEventStream: %v", err)
	}
	durableStream, err := NewDurableEventStream(turnStream)
	if err != nil {
		t.Fatalf("NewDurableEventStream: %v", err)
	}
	handler := mustCandidateHandler(t, reopenedService, startResolverFunc(defaultStartResolver), durableStream)
	router := candidateRouter(handler)

	statusResponse := httptest.NewRecorder()
	router.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/api/v1/agent/threads/thread_1/turns/turn_recovery", nil))
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("Status after reopen = %d, body=%s", statusResponse.Code, statusResponse.Body.String())
	}
	var status turnResponse
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode recovered status: %v", err)
	}
	if status.TurnID != "turn_recovery" || status.Status != agentcontract.TurnStatusCompleted || status.FinishedAt == nil {
		t.Fatalf("recovered status = %+v", status)
	}

	streamRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent/threads/thread_1/turns/turn_recovery/stream?after=1", nil)
	streamRequest.Header.Set("Last-Event-ID", "turn_recovery:2")
	streamResponse := httptest.NewRecorder()
	router.ServeHTTP(streamResponse, streamRequest)
	if streamResponse.Code != http.StatusOK {
		t.Fatalf("Stream after reopen = %d, body=%s", streamResponse.Code, streamResponse.Body.String())
	}
	events := decodeCandidateSSEEvents(t, streamResponse.Body.String())
	if len(events) != 2 {
		t.Fatalf("replayed events = %d, want 2; body=%s", len(events), streamResponse.Body.String())
	}
	if events[0].Sequence != 3 || events[0].Type != agentcontract.EventAssistantTextDelta || events[0].EventID != "turn_recovery:3" {
		t.Fatalf("first recovered event = %+v", events[0])
	}
	if events[1].Sequence != 4 || events[1].Type != agentcontract.EventCoreTurnStatus || events[1].EventID != "turn_recovery:4" {
		t.Fatalf("terminal recovered event = %+v", events[1])
	}
}

func openCandidateRecoveryDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open candidate recovery SQLite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("candidate recovery sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatalf("enable candidate recovery foreign keys: %v", err)
	}
	return db
}

// This deliberately narrow fixture mirrors the columns and uniqueness
// contracts SQLStore reads and writes. SQLStore still never auto-migrates;
// installing schema remains an explicit test-composition responsibility.
func installCandidateRecoverySchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE w_agent_turn (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			turn_id TEXT NOT NULL COLLATE BINARY,
			principal_id TEXT NOT NULL COLLATE BINARY,
			thread_id TEXT NOT NULL COLLATE BINARY,
			idempotency_key TEXT NOT NULL COLLATE BINARY,
			command_digest TEXT NOT NULL COLLATE BINARY,
			plugin_snapshot_json TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'queued',
			last_event_sequence INTEGER NOT NULL DEFAULT 1,
			active_attempt_id TEXT COLLATE BINARY,
			fencing_token INTEGER NOT NULL DEFAULT 0,
			cancel_requested_at DATETIME,
			started_at DATETIME,
			finished_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL,
			UNIQUE(turn_id),
			UNIQUE(principal_id, thread_id, idempotency_key)
		)`,
		`CREATE TABLE w_agent_turn_event (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			turn_id TEXT NOT NULL COLLATE BINARY,
			sequence INTEGER NOT NULL,
			event_id TEXT NOT NULL COLLATE BINARY,
			schema_version INTEGER NOT NULL,
			event_type TEXT NOT NULL COLLATE BINARY,
			event_json TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(turn_id, sequence),
			UNIQUE(turn_id, event_id),
			FOREIGN KEY(turn_id) REFERENCES w_agent_turn(turn_id) ON DELETE RESTRICT ON UPDATE RESTRICT
		)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("install candidate recovery schema: %v", err)
		}
	}
}

func decodeCandidateSSEEvents(t *testing.T, body string) []agentcontract.EventEnvelope {
	t.Helper()
	var events []agentcontract.EventEnvelope
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event agentcontract.EventEnvelope
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("decode SSE event: %v; line=%s", err, line)
		}
		events = append(events, event)
	}
	return events
}

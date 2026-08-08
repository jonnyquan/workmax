package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	systemrequest "server/model/system/request"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	testThreadUUID       = "123e4567-e89b-42d3-a456-426614174000"
	testSecondThreadUUID = "123e4567-e89b-42d3-a456-426614174001"
)

func newThreadAPITestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testutil.NewTestDB(t)
	if err := db.Exec(`CREATE UNIQUE INDEX uk_thread_api_uuid ON w_workagent_thread(uuid)`).Error; err != nil {
		t.Fatalf("create thread UUID unique index: %v", err)
	}
	return db
}

func threadAPITestRouter(api *ThreadApi, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/api/desktop/agent/threads/:uuid", func(c *gin.Context) {
		if uid != 0 {
			c.Set("claims", &systemrequest.CustomClaims{
				BaseClaims: systemrequest.BaseClaims{Id: uid},
			})
		}
		api.PutThread(c)
	})
	return router
}

func putThreadAPIRequest(router http.Handler, threadUUID string, body []byte, contentTypes ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/desktop/agent/threads/"+threadUUID,
		bytes.NewReader(body),
	)
	for _, contentType := range contentTypes {
		request.Header.Add("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestCanonicalV4UUIDRequiresLowercaseRFC4122Form(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "canonical RFC4122 v4", value: testThreadUUID, valid: true},
		{name: "uppercase", value: strings.ToUpper(testThreadUUID)},
		{name: "surrounding whitespace", value: " " + testThreadUUID},
		{name: "version one", value: "123e4567-e89b-12d3-a456-426614174000"},
		{name: "Microsoft variant with version four", value: "123e4567-e89b-42d3-c456-426614174000"},
		{name: "compact encoding", value: strings.ReplaceAll(testThreadUUID, "-", "")},
		{name: "empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalV4UUID(test.value)
			if test.valid {
				if err != nil || got != test.value {
					t.Fatalf("canonicalV4UUID(%q) = %q, %v", test.value, got, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("canonicalV4UUID(%q) unexpectedly accepted %q", test.value, got)
			}
		})
	}
}

func TestPutThreadEnforcesStrictFourKiBJSON(t *testing.T) {
	db := newThreadAPITestDB(t)
	router := threadAPITestRouter(NewThreadApi(db), 42)
	valid := []byte(`{"name":"Deck","agent_mode":"ppt"}`)
	exactLimit := append([]byte(nil), valid...)
	exactLimit = append(exactLimit, bytes.Repeat([]byte(" "), maxPutThreadBodyBytes-len(exactLimit))...)
	overLimit := append(append([]byte(nil), exactLimit...), ' ')

	tests := []struct {
		name         string
		threadUUID   string
		body         []byte
		contentTypes []string
		wantStatus   int
	}{
		{
			name:         "exactly four KiB is accepted",
			threadUUID:   testThreadUUID,
			body:         exactLimit,
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusCreated,
		},
		{
			name:         "one byte over limit is rejected",
			threadUUID:   testSecondThreadUUID,
			body:         overLimit,
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:         "unknown field",
			threadUUID:   testSecondThreadUUID,
			body:         []byte(`{"name":"Deck","agent_mode":"ppt","uid":99}`),
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:         "duplicate field",
			threadUUID:   testSecondThreadUUID,
			body:         []byte(`{"name":"Deck","name":"Other","agent_mode":"ppt"}`),
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:         "non canonical field spelling",
			threadUUID:   testSecondThreadUUID,
			body:         []byte(`{"name":"Deck","agentMode":"ppt"}`),
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:         "second JSON value",
			threadUUID:   testSecondThreadUUID,
			body:         []byte(`{"name":"Deck","agent_mode":"ppt"}{}`),
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:         "top level array",
			threadUUID:   testSecondThreadUUID,
			body:         []byte(`[{"name":"Deck","agent_mode":"ppt"}]`),
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:       "missing content type",
			threadUUID: testSecondThreadUUID,
			body:       valid,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:         "duplicate content type",
			threadUUID:   testSecondThreadUUID,
			body:         valid,
			contentTypes: []string{"application/json", "application/json"},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:         "unsupported content type",
			threadUUID:   testSecondThreadUUID,
			body:         valid,
			contentTypes: []string{"text/plain"},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:         "UTF8 charset is accepted",
			threadUUID:   testSecondThreadUUID,
			body:         valid,
			contentTypes: []string{"application/json; charset=UTF-8"},
			wantStatus:   http.StatusCreated,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := putThreadAPIRequest(router, test.threadUUID, test.body, test.contentTypes...)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestPutThreadValidatesCanonicalFields(t *testing.T) {
	db := newThreadAPITestDB(t)
	router := threadAPITestRouter(NewThreadApi(db), 42)

	tests := []struct {
		name       string
		uuid       string
		body       string
		wantStatus int
		wantError  string
	}{
		{name: "uppercase UUID", uuid: strings.ToUpper(testThreadUUID), body: `{"name":"Deck","agent_mode":"ppt"}`, wantStatus: 400, wantError: "invalid_thread_uuid"},
		{name: "non RFC variant", uuid: "123e4567-e89b-42d3-c456-426614174000", body: `{"name":"Deck","agent_mode":"ppt"}`, wantStatus: 400, wantError: "invalid_thread_uuid"},
		{name: "empty name", uuid: testThreadUUID, body: `{"name":"  ","agent_mode":"ppt"}`, wantStatus: 400, wantError: "invalid_name"},
		{name: "control character after decoding", uuid: testThreadUUID, body: `{"name":"bad\nname","agent_mode":"ppt"}`, wantStatus: 400, wantError: "invalid_name"},
		{name: "name over 200 UTF8 bytes", uuid: testThreadUUID, body: `{"name":"` + strings.Repeat("界", 67) + `","agent_mode":"ppt"}`, wantStatus: 400, wantError: "invalid_name"},
		{name: "unknown mode", uuid: testThreadUUID, body: `{"name":"Deck","agent_mode":"unknown"}`, wantStatus: 400, wantError: "invalid_agent_mode"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := putThreadAPIRequest(router, test.uuid, []byte(test.body), "application/json")
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body["error"] != test.wantError {
				t.Fatalf("error = %q, want %q", body["error"], test.wantError)
			}
		})
	}
}

func TestPutThreadFirstWriterWinsAndOwnerReplayUsesHTTP200(t *testing.T) {
	db := newThreadAPITestDB(t)
	api := NewThreadApi(db)
	ownerRouter := threadAPITestRouter(api, 42)

	first := putThreadAPIRequest(
		ownerRouter,
		testThreadUUID,
		[]byte(`{"name":"  Original Deck  ","agent_mode":"ppt"}`),
		"application/json",
	)
	if first.Code != http.StatusCreated {
		t.Fatalf("first PUT status = %d, want 201; body=%s", first.Code, first.Body.String())
	}
	var created putThreadResponse
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if !created.Created || created.Thread.UUID != testThreadUUID || created.Thread.Name != "Original Deck" || created.Thread.AgentMode != "ppt" {
		t.Fatalf("first response = %+v", created)
	}
	if created.Thread.CloudThreadID == "" || created.Thread.CloudThreadID == "0" {
		t.Fatalf("cloud_thread_id = %q", created.Thread.CloudThreadID)
	}
	if _, err := strconv.ParseUint(created.Thread.CloudThreadID, 10, 64); err != nil {
		t.Fatalf("cloud_thread_id is not numeric: %v", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, created.Thread.CreatedAt); err != nil {
		t.Fatalf("created_at is not RFC3339Nano: %q (%v)", created.Thread.CreatedAt, err)
	}

	replay := putThreadAPIRequest(
		ownerRouter,
		testThreadUUID,
		[]byte(`{"name":"Conflicting Replay","agent_mode":"flashCard"}`),
		"application/json",
	)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay PUT status = %d, want 200; body=%s", replay.Code, replay.Body.String())
	}
	var existing putThreadResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &existing); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if existing.Created || existing.Thread.CloudThreadID != created.Thread.CloudThreadID || existing.Thread.Name != "Original Deck" || existing.Thread.AgentMode != "ppt" {
		t.Fatalf("replay changed first-writer resource: first=%+v replay=%+v", created, existing)
	}

	crossOwner := putThreadAPIRequest(
		threadAPITestRouter(api, 99),
		testThreadUUID,
		[]byte(`{"name":"Other Owner","agent_mode":"ppt"}`),
		"application/json",
	)
	if crossOwner.Code != http.StatusConflict || crossOwner.Body.String() != `{"error":"thread_uuid_conflict"}` {
		t.Fatalf("cross-owner response = status %d body %s", crossOwner.Code, crossOwner.Body.String())
	}

	var count int64
	if err := db.Table("w_workagent_thread").Where("uuid = ?", testThreadUUID).Count(&count).Error; err != nil {
		t.Fatalf("count thread rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("thread rows = %d, want 1", count)
	}
}

func TestPutThreadConcurrentRequestsReturnOneCreatedResource(t *testing.T) {
	db := newThreadAPITestDB(t)
	router := threadAPITestRouter(NewThreadApi(db), 42)
	const callers = 16

	start := make(chan struct{})
	statuses := make(chan int, callers)
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			response := putThreadAPIRequest(
				router,
				testThreadUUID,
				[]byte(`{"name":"Deck","agent_mode":"ppt"}`),
				"application/json",
			)
			statuses <- response.Code
		}()
	}
	close(start)
	group.Wait()
	close(statuses)

	created := 0
	replayed := 0
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			replayed++
		default:
			t.Fatalf("concurrent PUT status = %d, want 201 or 200", status)
		}
	}
	if created != 1 || replayed != callers-1 {
		t.Fatalf("concurrent statuses: created=%d replayed=%d, want 1/%d", created, replayed, callers-1)
	}

	var count int64
	if err := db.Table("w_workagent_thread").Where("uuid = ?", testThreadUUID).Count(&count).Error; err != nil {
		t.Fatalf("count concurrent thread rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("concurrent thread rows = %d, want 1", count)
	}
}

func TestPutThreadFailsClosedWithoutAPIOrClaims(t *testing.T) {
	validBody := []byte(`{"name":"Deck","agent_mode":"ppt"}`)

	missingAPI := putThreadAPIRequest(threadAPITestRouter(NewThreadApi(nil), 42), testThreadUUID, validBody, "application/json")
	if missingAPI.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil DB status = %d, want 503", missingAPI.Code)
	}

	missingClaims := putThreadAPIRequest(threadAPITestRouter(NewThreadApi(newThreadAPITestDB(t)), 0), testThreadUUID, validBody, "application/json")
	if missingClaims.Code != http.StatusUnauthorized {
		t.Fatalf("missing claims status = %d, want 401", missingClaims.Code)
	}
}

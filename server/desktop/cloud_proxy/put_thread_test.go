//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

const putThreadTestUUID = "de305d54-75b4-431b-adb2-eb6b9e546014"

func putThreadTestInput() PutThreadInput {
	return PutThreadInput{
		UUID:      putThreadTestUUID,
		Name:      "Design deck",
		AgentMode: "ppt",
	}
}

func putThreadTestResource(threadUUID string) string {
	return fmt.Sprintf(`{"cloud_thread_id":"42","uuid":%q,"name":"Design deck","agent_mode":"ppt","agent_type":"general_agent","model":"work-pro","message_count":0,"msg_preview":"","file_count":0,"is_public":false,"updated_at":"2026-08-06T10:00:00Z","created_at":"2026-08-06T09:00:00Z"}`, threadUUID)
}

func putThreadTestResponse(threadUUID string, created bool) string {
	return fmt.Sprintf(`{"thread":%s,"created":%t}`, putThreadTestResource(threadUUID), created)
}

func TestPutThread_RequestAndCreatedResponseContract(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPut || r.URL.EscapedPath() != "/api/desktop/agent/threads/"+putThreadTestUUID {
			t.Errorf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Cookie"); got != "" {
			t.Errorf("credential request inherited Cookie = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		if got, want := string(body), `{"name":"Design deck","agent_mode":"ppt"}`; got != want {
			t.Errorf("body = %s, want %s", got, want)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, putThreadTestResponse(putThreadTestUUID, true))
	}))
	t.Cleanup(upstream.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(parsed, []*http.Cookie{{Name: "ambient", Value: "authority"}})
	client := NewClient(upstream.URL)
	client.HTTPClient = &http.Client{Transport: upstream.Client().Transport, Jar: jar}

	result, err := client.PutThread(context.Background(), "access-token", putThreadTestInput())
	if err != nil {
		t.Fatalf("PutThread: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	if !result.Created || result.Thread.Action != "upsert" ||
		result.Thread.UUID != putThreadTestUUID || result.Thread.CloudThreadID != "42" {
		t.Fatalf("result = %+v", result)
	}
}

func TestPutThread_RejectsInvalidInputsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)
	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()

	tests := []struct {
		name  string
		token string
		input PutThreadInput
	}{
		{name: "missing token", input: putThreadTestInput()},
		{name: "padded token", token: " access-token", input: putThreadTestInput()},
		{name: "non-v4 UUID", token: "access-token", input: PutThreadInput{UUID: "de305d54-75b4-11d3-adb2-eb6b9e546014", Name: "N", AgentMode: "ppt"}},
		{name: "Microsoft variant UUID", token: "access-token", input: PutThreadInput{UUID: "de305d54-75b4-431b-c456-eb6b9e546014", Name: "N", AgentMode: "ppt"}},
		{name: "uppercase UUID", token: "access-token", input: PutThreadInput{UUID: strings.ToUpper(putThreadTestUUID), Name: "N", AgentMode: "ppt"}},
		{name: "blank name", token: "access-token", input: PutThreadInput{UUID: putThreadTestUUID, Name: " ", AgentMode: "ppt"}},
		{name: "name too large", token: "access-token", input: PutThreadInput{UUID: putThreadTestUUID, Name: strings.Repeat("n", putThreadMaxNameBytes+1), AgentMode: "ppt"}},
		{name: "name control", token: "access-token", input: PutThreadInput{UUID: putThreadTestUUID, Name: "bad\nname", AgentMode: "ppt"}},
		{name: "blank mode", token: "access-token", input: PutThreadInput{UUID: putThreadTestUUID, Name: "N"}},
		{name: "padded mode", token: "access-token", input: PutThreadInput{UUID: putThreadTestUUID, Name: "N", AgentMode: " ppt"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.PutThread(context.Background(), test.token, test.input); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid inputs reached HTTP %d time(s)", calls.Load())
	}
}

func TestPutThread_RejectsMalformedCloudProtocol(t *testing.T) {
	validResource := putThreadTestResource(putThreadTestUUID)
	tests := []struct {
		name         string
		status       int
		contentTypes []string
		body         string
	}{
		{name: "missing MIME", status: http.StatusCreated, body: putThreadTestResponse(putThreadTestUUID, true)},
		{name: "wrong MIME", status: http.StatusCreated, contentTypes: []string{"text/plain"}, body: putThreadTestResponse(putThreadTestUUID, true)},
		{name: "non UTF-8 charset", status: http.StatusCreated, contentTypes: []string{"application/json; charset=iso-8859-1"}, body: putThreadTestResponse(putThreadTestUUID, true)},
		{name: "duplicate MIME", status: http.StatusCreated, contentTypes: []string{"application/json", "application/json"}, body: putThreadTestResponse(putThreadTestUUID, true)},
		{name: "unknown envelope field", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: fmt.Sprintf(`{"thread":%s,"created":true,"extra":1}`, validResource)},
		{name: "duplicate envelope field", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: fmt.Sprintf(`{"thread":%s,"created":true,"created":true}`, validResource)},
		{name: "unknown thread field", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: fmt.Sprintf(`{"thread":%s,"created":true}`, strings.Replace(validResource, `"model":"work-pro"`, `"model":"work-pro","extra":1`, 1))},
		{name: "duplicate thread field", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: fmt.Sprintf(`{"thread":%s,"created":true}`, strings.Replace(validResource, `"model":"work-pro"`, `"model":"work-pro","model":"work-pro"`, 1))},
		{name: "action is not a wire field", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: fmt.Sprintf(`{"thread":%s,"created":true}`, strings.Replace(validResource, `"model":"work-pro"`, `"model":"work-pro","action":"upsert"`, 1))},
		{name: "missing thread field", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: fmt.Sprintf(`{"thread":%s,"created":true}`, strings.Replace(validResource, `,"model":"work-pro"`, ``, 1))},
		{name: "negative count", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: strings.Replace(putThreadTestResponse(putThreadTestUUID, true), `"message_count":0`, `"message_count":-1`, 1)},
		{name: "noncanonical cloud id", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: strings.Replace(putThreadTestResponse(putThreadTestUUID, true), `"cloud_thread_id":"42"`, `"cloud_thread_id":"042"`, 1)},
		{name: "wrong UUID", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: putThreadTestResponse("756e8c12-e612-4a16-8f2d-90d9a4f64154", true)},
		{name: "wrong agent type", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: strings.Replace(putThreadTestResponse(putThreadTestUUID, true), `"agent_type":"general_agent"`, `"agent_type":"admin_agent"`, 1)},
		{name: "empty model", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: strings.Replace(putThreadTestResponse(putThreadTestUUID, true), `"model":"work-pro"`, `"model":""`, 1)},
		{name: "timestamp order", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: strings.Replace(putThreadTestResponse(putThreadTestUUID, true), `"updated_at":"2026-08-06T10:00:00Z"`, `"updated_at":"2026-08-06T08:00:00Z"`, 1)},
		{name: "created flag conflicts with status", status: http.StatusOK, contentTypes: []string{"application/json"}, body: putThreadTestResponse(putThreadTestUUID, true)},
		{name: "trailing JSON", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: putThreadTestResponse(putThreadTestUUID, true) + `{}`},
		{name: "oversized body", status: http.StatusCreated, contentTypes: []string{"application/json"}, body: strings.Repeat("x", putThreadMaxResponseBodyBytes+1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, contentType := range test.contentTypes {
					w.Header().Add("Content-Type", contentType)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			t.Cleanup(upstream.Close)
			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()
			if _, err := client.PutThread(context.Background(), "access-token", putThreadTestInput()); err == nil {
				t.Fatal("expected closed protocol error")
			}
		})
	}
}

func TestDecodePutThreadResponse_RejectsNullAndWrongTypeForEveryDeclaredField(t *testing.T) {
	valid := putThreadTestResponse(putThreadTestUUID, true)
	tests := []struct {
		name      string
		validPair string
		wrongPair string
	}{
		{name: "thread", validPair: `"thread":` + putThreadTestResource(putThreadTestUUID), wrongPair: `"thread":[]`},
		{name: "created", validPair: `"created":true`, wrongPair: `"created":"true"`},
		{name: "cloud_thread_id", validPair: `"cloud_thread_id":"42"`, wrongPair: `"cloud_thread_id":42`},
		{name: "uuid", validPair: `"uuid":"` + putThreadTestUUID + `"`, wrongPair: `"uuid":42`},
		{name: "name", validPair: `"name":"Design deck"`, wrongPair: `"name":{}`},
		{name: "agent_mode", validPair: `"agent_mode":"ppt"`, wrongPair: `"agent_mode":false`},
		{name: "agent_type", validPair: `"agent_type":"general_agent"`, wrongPair: `"agent_type":[]`},
		{name: "model", validPair: `"model":"work-pro"`, wrongPair: `"model":1`},
		{name: "message_count", validPair: `"message_count":0`, wrongPair: `"message_count":"0"`},
		{name: "msg_preview", validPair: `"msg_preview":""`, wrongPair: `"msg_preview":false`},
		{name: "file_count", validPair: `"file_count":0`, wrongPair: `"file_count":0.5`},
		{name: "is_public", validPair: `"is_public":false`, wrongPair: `"is_public":0`},
		{name: "updated_at", validPair: `"updated_at":"2026-08-06T10:00:00Z"`, wrongPair: `"updated_at":true`},
		{name: "created_at", validPair: `"created_at":"2026-08-06T09:00:00Z"`, wrongPair: `"created_at":[]`},
	}
	for _, test := range tests {
		t.Run(test.name+" null", func(t *testing.T) {
			nullPair := `"` + test.name + `":null`
			body := strings.Replace(valid, test.validPair, nullPair, 1)
			if body == valid {
				t.Fatalf("test did not replace %q", test.validPair)
			}
			if _, err := decodePutThreadResponse([]byte(body)); err == nil {
				t.Fatal("declared null field was accepted")
			}
		})
		t.Run(test.name+" wrong type", func(t *testing.T) {
			body := strings.Replace(valid, test.validPair, test.wrongPair, 1)
			if body == valid {
				t.Fatalf("test did not replace %q", test.validPair)
			}
			if _, err := decodePutThreadResponse([]byte(body)); err == nil {
				t.Fatal("wrongly typed declared field was accepted")
			}
		})
	}
}

func TestPutThread_StatusSentinelsAndNoRedirect(t *testing.T) {
	t.Run("401", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, strings.Repeat("not-json", putThreadMaxResponseBodyBytes))
		}))
		t.Cleanup(upstream.Close)
		client := NewClient(upstream.URL)
		client.HTTPClient = upstream.Client()
		_, err := client.PutThread(context.Background(), "access-token", putThreadTestInput())
		if !errors.Is(err, ErrPutThreadAuthExpired) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("409", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
		}))
		t.Cleanup(upstream.Close)
		client := NewClient(upstream.URL)
		client.HTTPClient = upstream.Client()
		_, err := client.PutThread(context.Background(), "access-token", putThreadTestInput())
		if !errors.Is(err, ErrPutThreadConflict) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("redirect", func(t *testing.T) {
		var targetCalls atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			targetCalls.Add(1)
		}))
		t.Cleanup(target.Close)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(http.StatusTemporaryRedirect)
		}))
		t.Cleanup(upstream.Close)
		client := NewClient(upstream.URL)
		client.HTTPClient = upstream.Client()
		if _, err := client.PutThread(context.Background(), "access-token", putThreadTestInput()); err == nil {
			t.Fatal("expected redirect refusal")
		}
		if targetCalls.Load() != 0 {
			t.Fatalf("credential request followed redirect %d time(s)", targetCalls.Load())
		}
	})
}

func TestPutThread_SessionChangedContextWinsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(upstream.Close)
	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrSessionChanged)
	_, err := client.PutThread(ctx, "access-token", putThreadTestInput())
	if !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("error = %v, want ErrSessionChanged", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("retired session reached HTTP %d time(s)", calls.Load())
	}
}

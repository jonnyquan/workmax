package common

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestWorkerPoolProcessRedisTask_DispatchesHandler(t *testing.T) {
	wp := NewWorkerPool(1, 1)

	called := false
	var got []byte
	wp.RegisterPersistentTaskHandler("echo", func(_ context.Context, payload []byte) error {
		called = true
		got = append([]byte(nil), payload...)
		return nil
	})

	want := []byte("hello")
	env := persistentTaskEnvelope{
		Type:    "echo",
		Payload: base64.StdEncoding.EncodeToString(want),
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if err := wp.processRedisTask(string(b)); err != nil {
		t.Fatalf("process: %v", err)
	}
	if !called {
		t.Fatalf("handler not called")
	}
	if string(got) != string(want) {
		t.Fatalf("payload mismatch: got=%q want=%q", string(got), string(want))
	}
}

func TestWorkerPoolProcessRedisTask_ErrorsOnUnknownType(t *testing.T) {
	wp := NewWorkerPool(1, 1)

	env := persistentTaskEnvelope{
		Type:    "missing",
		Payload: base64.StdEncoding.EncodeToString([]byte("x")),
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if err := wp.processRedisTask(string(b)); err == nil {
		t.Fatalf("expected error")
	}
}

func TestWorkerPoolProcessRedisTask_ErrorsOnBadJSON(t *testing.T) {
	wp := NewWorkerPool(1, 1)
	if err := wp.processRedisTask("{"); err == nil {
		t.Fatalf("expected error")
	}
}

package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Integration tests against an in-process fake server. Validates the
// request the provider emits and exercises the error-path classification
// callers rely on.

func TestOpenAIProvider_Success(t *testing.T) {
	var gotBody openaiRequestBody
	var gotAuth, gotContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake-audio-bytes"))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("test-key", srv.URL, "tts-1", nil)
	resp, err := p.Synthesize(context.Background(), &SynthesizeRequest{
		Text:  "hello world",
		Voice: "alloy",
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}

	if gotAuth != "Bearer test-key" {
		t.Errorf("auth header: got %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type: got %q", gotContentType)
	}
	if gotBody.Model != "tts-1" {
		t.Errorf("model: got %q, want tts-1", gotBody.Model)
	}
	if gotBody.Input != "hello world" {
		t.Errorf("input: got %q", gotBody.Input)
	}
	if gotBody.Voice != "alloy" {
		t.Errorf("voice: got %q", gotBody.Voice)
	}
	if gotBody.ResponseFormat != "mp3" {
		t.Errorf("default format should be mp3, got %q", gotBody.ResponseFormat)
	}
	if gotBody.Speed != 1.0 {
		t.Errorf("default speed should be 1.0, got %v", gotBody.Speed)
	}

	if string(resp.AudioBytes) != "fake-audio-bytes" {
		t.Errorf("audio bytes not passed through")
	}
	if resp.MIMEType != "audio/mpeg" {
		t.Errorf("MIME type: got %q", resp.MIMEType)
	}
	if resp.Provider != "openai" {
		t.Errorf("provider: got %q", resp.Provider)
	}
	if resp.ResponseTime <= 0 {
		t.Errorf("response time should be positive")
	}
}

func TestOpenAIProvider_PropagatesErrorBody(t *testing.T) {
	// Providers rate-limit or reject invalid voices with descriptive
	// JSON bodies. The error should carry enough of that body for
	// callers to grep for known failure modes without parsing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_exceeded","message":"slow down"}}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("k", srv.URL, "tts-1", nil)
	_, err := p.Synthesize(context.Background(), &SynthesizeRequest{
		Text: "hi", Voice: "alloy",
	})
	if err == nil {
		t.Fatal("expected error on 429")
	}
	msg := err.Error()
	if !strings.Contains(msg, "429") || !strings.Contains(msg, "rate_limit") {
		t.Errorf("error should include status + body prefix: %q", msg)
	}
}

func TestOpenAIProvider_RejectsEmptyText(t *testing.T) {
	p := NewOpenAIProvider("k", "http://localhost:0", "tts-1", nil)
	cases := []string{"", "   ", "\t\n"}
	for _, text := range cases {
		_, err := p.Synthesize(context.Background(), &SynthesizeRequest{
			Text: text, Voice: "alloy",
		})
		if err == nil {
			t.Errorf("expected error for empty text %q", text)
		}
	}
}

func TestOpenAIProvider_RejectsMissingVoice(t *testing.T) {
	p := NewOpenAIProvider("k", "http://localhost:0", "tts-1", nil)
	_, err := p.Synthesize(context.Background(), &SynthesizeRequest{Text: "hi"})
	if err == nil {
		t.Error("expected error when voice is empty")
	}
}

func TestOpenAIProvider_RejectsOversizedText(t *testing.T) {
	p := NewOpenAIProvider("k", "http://localhost:0", "tts-1", nil)
	huge := strings.Repeat("a", MaxTextChars+1)
	_, err := p.Synthesize(context.Background(), &SynthesizeRequest{
		Text: huge, Voice: "alloy",
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("expected exceeds-limit error, got %v", err)
	}
}

func TestOpenAIProvider_ContextCancellation(t *testing.T) {
	// Hold the server response long enough that a cancelled context
	// aborts the client side before the body arrives. Provider must
	// return an error rather than hanging.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			_, _ = io.WriteString(w, "too-late")
		}
	}))
	defer srv.Close()

	p := NewOpenAIProvider("k", srv.URL, "tts-1", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := p.Synthesize(ctx, &SynthesizeRequest{Text: "hi", Voice: "alloy"})
	if err == nil {
		t.Fatal("expected context-cancel error")
	}
}

func TestOpenAIProvider_CustomFormatAndSpeed(t *testing.T) {
	var body openaiRequestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("wav"))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("k", srv.URL, "tts-1", nil)
	resp, err := p.Synthesize(context.Background(), &SynthesizeRequest{
		Text: "hi", Voice: "alloy", Format: "wav", Speed: 1.25,
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if body.ResponseFormat != "wav" {
		t.Errorf("format: got %q", body.ResponseFormat)
	}
	if body.Speed != 1.25 {
		t.Errorf("speed: got %v", body.Speed)
	}
	if resp.MIMEType != "audio/wav" {
		t.Errorf("MIME: got %q", resp.MIMEType)
	}
}

func TestOpenAIProvider_Name(t *testing.T) {
	p := NewOpenAIProvider("k", "", "tts-1-hd", nil)
	if p.Name() != "openai-tts-1-hd" {
		t.Errorf("name: got %q", p.Name())
	}
}

func TestMimeForFormat(t *testing.T) {
	cases := map[string]string{
		"mp3":     "audio/mpeg",
		"MP3":     "audio/mpeg",
		"wav":     "audio/wav",
		"opus":    "audio/opus",
		"aac":     "audio/aac",
		"flac":    "audio/flac",
		"unknown": "audio/mpeg",
		"":        "audio/mpeg",
	}
	for format, want := range cases {
		if got := mimeForFormat(format); got != want {
			t.Errorf("mimeForFormat(%q) = %q, want %q", format, got, want)
		}
	}
}

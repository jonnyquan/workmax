// Package tts defines a provider-agnostic text-to-speech interface
// and ships one concrete implementation (OpenAI tts-1). Short-drama
// export uses this to synthesise per-panel dialogue audio before the
// ffmpeg concat step; other callers can reuse the interface directly.
//
// Design intent:
//   - Request / Response shapes abstract away per-provider quirks so
//     swapping providers is a one-line wiring change at construction.
//   - Voice is a caller-supplied string (e.g. "alloy", "onyx") — the
//     voice catalogue lives in a separate file so adding presets
//     doesn't touch this type.
//   - Context cancellation flows through to the underlying HTTP call
//     so a worker shutdown or timeout propagates cleanly.
package tts

import (
	"context"
	"time"
)

// SynthesizeRequest is the single-shot input to a TTS provider.
type SynthesizeRequest struct {
	// Text is the spoken line. Providers typically cap around 4096
	// chars; callers are responsible for chunking longer inputs.
	Text string

	// Voice is a provider-specific voice identifier (e.g. "alloy",
	// "onyx", "zh-female-01"). The voice catalogue defined in this
	// package maps provider-agnostic character presets to concrete
	// voices.
	Voice string

	// Language is an optional BCP-47 code ("en", "zh-CN", etc.).
	// When empty, providers fall back to their default language
	// detection. Short-drama passes the project locale here.
	Language string

	// Speed is a playback-rate multiplier. 1.0 is natural; 0.75 is
	// slow, 1.5 is fast. Providers clamp to their supported range;
	// 0 means "use provider default".
	Speed float64

	// Format is the output audio format. "mp3" (default) is the
	// smallest; "wav" is uncompressed; "opus" streams well.
	Format string
}

// SynthesizeResponse carries the raw audio bytes and provider
// metadata. Callers typically hand AudioBytes straight to ffmpeg via
// a temp file.
type SynthesizeResponse struct {
	AudioBytes   []byte
	MIMEType     string        // "audio/mpeg", "audio/wav", etc.
	ResponseTime time.Duration // round-trip wall clock
	Provider     string        // "openai", "azure", …
	Model        string        // "tts-1", "tts-1-hd", …
}

// Provider is the interface every TTS implementation satisfies.
// Keep it minimal — streaming variants can extend this later.
type Provider interface {
	// Synthesize converts text → audio bytes. Context cancellation
	// must abort the underlying HTTP call; callers pass a deadline
	// to protect the export pipeline from a runaway TTS request.
	Synthesize(ctx context.Context, req *SynthesizeRequest) (*SynthesizeResponse, error)

	// Name returns a short identifier ("openai-tts-1") for log /
	// metadata tagging. Mirrors the LLM service's pattern.
	Name() string
}

// Defaults match what the short-drama pipeline picks when a caller
// doesn't specify. Centralising them here means "what does our TTS
// sound like today?" has one answer.
const (
	DefaultFormat   = "mp3"
	DefaultSpeed    = 1.0
	DefaultLanguage = ""   // let provider autodetect
	DefaultTimeout  = 30   // seconds
	MaxTextChars    = 4096 // OpenAI + Azure both cap here
)

package modelgateway

import (
	"strings"
	"testing"
)

// Metering is what a later settlement pass will bill from, so the arithmetic
// has to be right on the first pass — there is no second copy of the stream
// to recount.

// Anthropic reports input once and then a GROWING output figure in every
// message_delta. Summing would multiply the bill by the number of deltas;
// max is the only rule that reads a growing counter correctly.
func TestUsageScanner_AnthropicDeltasAreMaximaNotSums(t *testing.T) {
	scanner := NewUsageScanner()
	stream := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"usage":{"input_tokens":120,"cache_read_input_tokens":40,"cache_creation_input_tokens":5}}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","usage":{"output_tokens":10}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","usage":{"output_tokens":25}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","usage":{"output_tokens":60}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	if _, err := scanner.Write([]byte(stream)); err != nil {
		t.Fatalf("write: %v", err)
	}

	usage := scanner.Usage()
	if usage.InputTokens != 120 {
		t.Errorf("input = %d, want 120", usage.InputTokens)
	}
	if usage.OutputTokens != 60 {
		t.Errorf("output = %d, want 60 (the last, largest delta — not 10+25+60)", usage.OutputTokens)
	}
	if usage.CacheReadTokens != 40 || usage.CacheCreationTokens != 5 {
		t.Errorf("cache tokens = %d/%d, want 40/5", usage.CacheReadTokens, usage.CacheCreationTokens)
	}
	if usage.Total() != 225 {
		t.Errorf("total = %d, want 225", usage.Total())
	}
}

// The chunk boundaries a stream arrives on are arbitrary. Splitting mid-frame,
// mid-token, or mid-newline must not change the metering.
func TestUsageScanner_IsIndependentOfChunkBoundaries(t *testing.T) {
	stream := `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":7}}}

data: {"type":"message_delta","usage":{"output_tokens":9}}

`
	for _, chunkSize := range []int{1, 3, 17, 64, len(stream)} {
		scanner := NewUsageScanner()
		for i := 0; i < len(stream); i += chunkSize {
			end := i + chunkSize
			if end > len(stream) {
				end = len(stream)
			}
			_, _ = scanner.Write([]byte(stream[i:end]))
		}
		usage := scanner.Usage()
		if usage.InputTokens != 7 || usage.OutputTokens != 9 {
			t.Errorf("chunk size %d: usage = %+v, want input 7 / output 9", chunkSize, usage)
		}
	}
}

// OpenAI sends a single cumulative usage object, usually on the final chunk.
func TestUsageScanner_OpenAIStreamUsage(t *testing.T) {
	scanner := NewUsageScanner()
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hi"}}]}`,
		"",
		`data: {"choices":[],"usage":{"prompt_tokens":31,"completion_tokens":12,"total_tokens":43}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	_, _ = scanner.Write([]byte(stream))

	usage := scanner.Usage()
	if usage.InputTokens != 31 || usage.OutputTokens != 12 {
		t.Fatalf("usage = %+v, want input 31 / output 12", usage)
	}
	if usage.Total() != 43 {
		t.Errorf("total = %d, want the upstream's own 43", usage.Total())
	}
}

// A truncated stream still has to yield the numbers it did see. Losing all
// metering because the last frame was cut off would under-bill exactly the
// calls most likely to have been expensive.
func TestUsageScanner_TruncatedStreamKeepsWhatItSaw(t *testing.T) {
	scanner := NewUsageScanner()
	_, _ = scanner.Write([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":50}}}` + "\n\n"))
	_, _ = scanner.Write([]byte(`data: {"type":"message_delta","usage":{"output_to`))

	usage := scanner.Usage()
	if usage.InputTokens != 50 {
		t.Errorf("input = %d, want 50 from the complete frame", usage.InputTokens)
	}
	if usage.OutputTokens != 0 {
		t.Errorf("output = %d, want 0 — a half-parsed JSON object has no trustworthy numbers", usage.OutputTokens)
	}
}

// A stream without newlines must not become an unbounded buffer. Losing a
// metering figure is a reporting gap; holding megabytes per in-flight stream
// is an outage.
func TestUsageScanner_OverlongLineIsDroppedNotBuffered(t *testing.T) {
	scanner := NewUsageScanner()
	giant := "data: {\"padding\":\"" + strings.Repeat("x", maxUsageScanLine+1024) + "\"}"
	_, _ = scanner.Write([]byte(giant))
	_, _ = scanner.Write([]byte("\n\n"))
	// Recovery: the scanner must resume on the next well-formed frame.
	_, _ = scanner.Write([]byte(`data: {"usage":{"input_tokens":3}}` + "\n\n"))

	if len(scanner.buf) > maxUsageScanLine {
		t.Fatalf("scanner buffered %d bytes past its cap", len(scanner.buf))
	}
	if scanner.Usage().InputTokens != 3 {
		t.Errorf("scanner did not recover after an over-long line: %+v", scanner.Usage())
	}
}

// Non-data SSE lines carry no JSON; parsing them would be wasted work and a
// source of spurious matches.
func TestUsageScanner_IgnoresNonDataLines(t *testing.T) {
	scanner := NewUsageScanner()
	_, _ = scanner.Write([]byte(": keepalive\nevent: ping\nid: 42\nretry: 1000\n\n"))
	if usage := scanner.Usage(); usage.Total() != 0 {
		t.Errorf("usage = %+v, want zero", usage)
	}
}

func TestParseUsageJSON(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantInput    int
		wantOutput   int
		wantTotal    int
		wantCacheHit int
	}{
		{
			name:         "anthropic non-stream",
			body:         `{"id":"msg_1","usage":{"input_tokens":80,"output_tokens":12,"cache_read_input_tokens":6}}`,
			wantInput:    80,
			wantOutput:   12,
			wantTotal:    98,
			wantCacheHit: 6,
		},
		{
			name:       "openai non-stream",
			body:       `{"id":"chatcmpl","usage":{"prompt_tokens":15,"completion_tokens":5,"total_tokens":20}}`,
			wantInput:  15,
			wantOutput: 5,
			wantTotal:  20,
		},
		{
			// Unparseable is a reporting gap, never a failed request: the
			// call already happened and the user already has the answer.
			name: "unparseable body meters zero rather than erroring",
			body: `<html>gateway timeout</html>`,
		},
		{
			name: "no usage field",
			body: `{"content":[]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usage := ParseUsageJSON([]byte(tc.body))
			if usage.InputTokens != tc.wantInput || usage.OutputTokens != tc.wantOutput {
				t.Errorf("usage = %+v, want input %d / output %d", usage, tc.wantInput, tc.wantOutput)
			}
			if usage.CacheReadTokens != tc.wantCacheHit {
				t.Errorf("cache read = %d, want %d", usage.CacheReadTokens, tc.wantCacheHit)
			}
			if usage.Total() != tc.wantTotal {
				t.Errorf("total = %d, want %d", usage.Total(), tc.wantTotal)
			}
		})
	}
}

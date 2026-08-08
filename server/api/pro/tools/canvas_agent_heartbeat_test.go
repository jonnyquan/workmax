// canvas_agent_heartbeat_test.go pins the SSE heartbeat wire format.
//
// SSE clients, including the Desktop renderer, ignore comment lines whose
// first character is ":" — that is the contract this format hooks into.
// A regression that ships (say)
// "data: hb 1700000000\n\n" would pass type-checks on both sides but
// surface as a JSON parse failure on every 25s heartbeat in
// production logs. This test exists so that drift is loud.

package tools

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFormatCanvasAgentHeartbeat_ExactWireBytes(t *testing.T) {
	// 1700000000 is a stable epoch (~2023-11-14). Hardcoding it keeps
	// the assertion deterministic without time mocking.
	at := time.Unix(1700000000, 0)
	got := formatCanvasAgentHeartbeat(at)
	want := []byte(": hb 1700000000\n\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("heartbeat bytes mismatch:\n  want: %q\n  got:  %q", want, got)
	}
}

func TestFormatCanvasAgentHeartbeat_LineStartsWithColon(t *testing.T) {
	// The Desktop SSE parser's comment-drop predicate is a leading colon.
	// A leading colon on the FIRST line of the SSE frame is what makes
	// the heartbeat invisible. If a future refactor reorders fields or
	// adds a prefix, this assertion catches it before the dropped-frame
	// behaviour changes.
	got := formatCanvasAgentHeartbeat(time.Now())
	if !bytes.HasPrefix(got, []byte(":")) {
		t.Fatalf("heartbeat must start with ':', got %q", got)
	}
}

func TestFormatCanvasAgentHeartbeat_TerminatesWithBlankLine(t *testing.T) {
	// SSE frame terminator is "\n\n". Without it, the Desktop buffer would
	// never flush the heartbeat line — and worse, the next real "data:"
	// frame would merge into it.
	got := formatCanvasAgentHeartbeat(time.Now())
	if !bytes.HasSuffix(got, []byte("\n\n")) {
		t.Fatalf("heartbeat must end with blank-line terminator, got %q", got)
	}
}

func TestFormatCanvasAgentHeartbeat_NoEmbeddedNewlinesBeforeTerminator(t *testing.T) {
	// Belt-and-braces: ensure the format is a single SSE-comment line.
	// A future change that injected (e.g.) `: hb 17000\nfoo` would
	// split into two SSE lines and the Desktop parser would treat "foo" as
	// a real data line.
	at := time.Unix(1700000000, 0)
	got := formatCanvasAgentHeartbeat(at)
	body := strings.TrimSuffix(string(got), "\n\n")
	if strings.Contains(body, "\n") {
		t.Fatalf("heartbeat body must be a single line, got %q", body)
	}
}

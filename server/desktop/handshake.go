//go:build desktop

package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Handshake is the JSON payload emitted as the first line of stdout
// after the sidecar's HTTP server is bound but before any other stdout
// activity. The Electron sidecar-manager parses this single line to
// learn which port to talk to.
//
// Stable wire format — see sidecar-protocol.md §3.2. Add fields, never
// rename or remove. Bump V on any incompatible change.
type Handshake struct {
	V              int    `json:"v"`
	Port           int    `json:"port"`
	PID            int    `json:"pid"`
	StartedAt      string `json:"started_at"`
	SidecarVersion string `json:"sidecar_version"`
}

// CurrentHandshakeVersion is the protocol version Electron expects to
// see in the `v` field of the handshake JSON.
const CurrentHandshakeVersion = 1

// WriteHandshake emits the first-line stdout JSON. Must be called
// exactly once, before any other writer touches stdout. Caller is
// responsible for switching subsequent logs to stderr (the main
// binary configures `log` to stderr via log.SetOutput).
func WriteHandshake(port int, sidecarVersion string) error {
	h := Handshake{
		V:              CurrentHandshakeVersion,
		Port:           port,
		PID:            os.Getpid(),
		StartedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		SidecarVersion: sidecarVersion,
	}
	b, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("marshal handshake: %w", err)
	}
	// Single line + newline + sync so Electron's line reader sees it
	// immediately even if stdout is pipe-buffered.
	if _, err := fmt.Fprintf(os.Stdout, "%s\n", b); err != nil {
		return fmt.Errorf("write handshake: %w", err)
	}
	// os.Stdout.Sync() returns ENOTSUP on pipes/PTYs which is fine for
	// our purposes — the newline alone is enough for line-buffered
	// consumers. We ignore the error rather than gating boot on it.
	_ = os.Stdout.Sync()
	return nil
}

//go:build desktop

// Package buildinfo holds compile-time identifiers for the
// workagent-desktop sidecar binary. Centralized here so the
// handshake JSON, the cloud-bound X-WorkMax-Client-Version header,
// and ops log lines all stay in lockstep.
//
// CI builds can override via -ldflags:
//
//	go build -tags desktop -ldflags="-X server/desktop/buildinfo.Version=$(git describe --tags)" ./cmd/workagent-desktop
//
// Without an override the default below ships; bump it when cutting
// a release. Keep the form semver-ish so version-gated cloud
// behavior (if we ever need it) has a parseable handle.
package buildinfo

// Version is the sidecar version string. Emitted in:
//   - the handshake JSON on stdout to Electron
//   - the X-WorkMax-Client-Version header on every cloud-bound request
//   - the startup log line
//
// var (not const) so -ldflags -X can override it at link time.
var Version = "0.1.0-p1-ea"

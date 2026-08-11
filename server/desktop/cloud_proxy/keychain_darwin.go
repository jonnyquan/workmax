//go:build desktop && darwin

package cloud_proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const darwinKeychainCommandTimeout = 5 * time.Second

// DarwinKeychain implements Keychain by shelling out to the macOS
// `security` CLI. Avoids the CGO dependency that `keybase/go-keychain`
// would pull in — keeps the build pure-Go and lets us cross-compile
// from a Linux host (GOOS=darwin go build without a C toolchain).
//
// Tradeoffs vs CGO library:
//   - zero deps, easy to debug (the same commands work in a terminal)
//   - no Apple framework version coupling
//   - shell-out overhead (~50ms per call, negligible for OAuth flow)
//   - command stderr parsing for "not found" detection
//
// Tests stub via the Keychain interface instead of mocking exec.
type DarwinKeychain struct {
	// Command name override, for tests that need to swap in a fake
	// binary. Empty string → "security" (the real CLI).
	commandName string
	// commandTimeout is a test seam. Production always uses the bounded
	// default so a stuck Keychain CLI cannot block login cancellation or
	// Sidecar shutdown indefinitely.
	commandTimeout time.Duration
}

// NewDarwinKeychain returns a real-Keychain-backed implementation.
func NewDarwinKeychain() *DarwinKeychain {
	return &DarwinKeychain{}
}

func (k *DarwinKeychain) cmd() string {
	if k.commandName == "" {
		return "security"
	}
	return k.commandName
}

func (k *DarwinKeychain) timeout() time.Duration {
	if k != nil && k.commandTimeout > 0 {
		return k.commandTimeout
	}
	return darwinKeychainCommandTimeout
}

// Write upserts the entry. macOS Keychain doesn't have an "upsert"
// primitive; we have to delete-then-add. The delete is best-effort
// (NoEntry is fine; permission denied bubbles up).
//
// `security add-generic-password -U` exists for upsert in some
// versions but isn't universally available — explicit delete+add
// avoids version sniffing.
func (k *DarwinKeychain) Write(service, account string, value []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), k.timeout())
	defer cancel()
	// Best-effort delete (ignore NoEntry).
	if err := k.delete(ctx, service, account); err != nil {
		return fmt.Errorf("keychain write: pre-delete: %w", err)
	}
	// A final -w without an argv value selects security(1)'s documented
	// password-prompt mode. Feed that prompt over stdin so the serialized
	// TokenPair never appears in the process list.
	//
	// The prompt is TWO reads, not one: "password data for new item:" then
	// "retype password for new item:". Sending the value once made them
	// mismatch, and security(1) then created the item with an EMPTY password
	// and exited 0 — a silent, total credential loss that Read reported back
	// as a present-but-empty entry (measured on macOS 26 via the pi L2 smoke:
	// the user's configured api_key never reached the model endpoint, and
	// api_key_configured still answered true). So the value goes twice.
	//
	// A value containing a newline cannot survive prompt mode at all — the
	// first line would be taken as the whole password — so it is refused here
	// rather than silently truncated. Every caller's payload (JSON token
	// pairs, API keys) is newline-free by construction.
	if bytes.ContainsAny(value, "\n\r") {
		return errors.New("keychain write: value must not contain a newline")
	}
	cmd := exec.CommandContext(ctx, k.cmd(),
		"add-generic-password",
		"-s", service,
		"-a", account,
		"-w",
	)
	cmd.Stdin = io.MultiReader(
		bytes.NewReader(value), strings.NewReader("\n"),
		bytes.NewReader(value), strings.NewReader("\n"),
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if err != nil {
		return darwinKeychainCommandError("write", ctx, err)
	}
	return nil
}

// Read returns the secret as bytes. Uses `-w` to print only the
// password (no metadata noise).
func (k *DarwinKeychain) Read(service, account string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), k.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, k.cmd(),
		"find-generic-password",
		"-s", service,
		"-a", account,
		"-w",
	)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, darwinKeychainCommandError("read", ctx, err)
		}
		if darwinKeychainEntryNotFound(err, out) {
			return nil, ErrKeychainNoEntry
		}
		return nil, darwinKeychainCommandError("read", ctx, err)
	}
	// `-w` prints the secret followed by a trailing newline.
	return bytes.TrimRight(out, "\n"), nil
}

// Delete removes the entry. Idempotent per the interface contract —
// "not found" maps to nil here, not ErrKeychainNoEntry.
func (k *DarwinKeychain) Delete(service, account string) error {
	ctx, cancel := context.WithTimeout(context.Background(), k.timeout())
	defer cancel()
	return k.delete(ctx, service, account)
}

func (k *DarwinKeychain) delete(ctx context.Context, service string, account string) error {
	cmd := exec.CommandContext(ctx, k.cmd(),
		"delete-generic-password",
		"-s", service,
		"-a", account,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return darwinKeychainCommandError("delete", ctx, err)
		}
		if darwinKeychainEntryNotFound(err, out) {
			return nil // idempotent
		}
		return darwinKeychainCommandError("delete", ctx, err)
	}
	return nil
}

func darwinKeychainEntryNotFound(err error, output []byte) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	// security(1) uses exit status 44 for a missing generic-password item.
	// Keep the legacy diagnostic match as a compatibility fallback, but never
	// copy command output into a returned error.
	return exitErr.ExitCode() == 44 ||
		strings.Contains(string(output), "could not be found") ||
		strings.Contains(string(exitErr.Stderr), "could not be found")
}

func darwinKeychainCommandError(operation string, ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("keychain %s: security command timed out: %w", operation, context.DeadlineExceeded)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("keychain %s: security command canceled: %w", operation, context.Canceled)
	}
	// exec.ExitError.Error contains only the exit status. In particular, do not
	// attach stdout/stderr: security implementations and test doubles may echo
	// credential material in diagnostics.
	return fmt.Errorf("keychain %s: security command failed: %w", operation, err)
}

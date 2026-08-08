//go:build desktop && darwin

package cloud_proxy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	fakeKeychainCaptureDirEnv = "WORKMAX_TEST_KEYCHAIN_CAPTURE_DIR"
	fakeKeychainTargetEnv     = "WORKMAX_TEST_KEYCHAIN_TARGET"
	fakeKeychainModeEnv       = "WORKMAX_TEST_KEYCHAIN_MODE"
	fakeKeychainReadValueEnv  = "WORKMAX_TEST_KEYCHAIN_READ_VALUE"
	fakeKeychainErrorMarker   = "RAW_FAKE_KEYCHAIN_SECRET_OUTPUT"
)

func TestDarwinKeychainWriteUsesPromptStdinWithoutSecretArgv(t *testing.T) {
	keychain, captureDir := newFakeDarwinKeychain(t, time.Second)
	secret := []byte(`{"access_token":"argv-access-secret","refresh_token":"argv-refresh-secret"}`)

	if err := keychain.Write(KeychainService, KeychainAccount, secret); err != nil {
		t.Fatalf("Write: %v", err)
	}

	args := readFakeKeychainArgs(t, captureDir, "add-generic-password")
	wantArgs := []string{"-s", KeychainService, "-a", KeychainAccount, "-w"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("add argv = %#v, want %#v", args, wantArgs)
	}
	joinedArgs := strings.Join(args, " ")
	for _, forbidden := range []string{string(secret), "argv-access-secret", "argv-refresh-secret"} {
		if strings.Contains(joinedArgs, forbidden) {
			t.Fatalf("secret entered argv: %q", joinedArgs)
		}
	}

	stdin := readFakeKeychainCapture(t, captureDir, "add-generic-password.stdin")
	wantStdin := append(append([]byte(nil), secret...), '\n')
	if !bytes.Equal(stdin, wantStdin) {
		t.Fatalf("add stdin = %q, want prompt value plus newline", stdin)
	}
	if got := readFakeKeychainCapture(t, captureDir, "delete-generic-password.stdin"); len(got) != 0 {
		t.Fatalf("delete unexpectedly received stdin: %q", got)
	}
}

func TestDarwinKeychainReadAndMissingEntryContracts(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		keychain, captureDir := newFakeDarwinKeychain(t, time.Second)
		const secret = `{"access_token":"read-secret"}`
		t.Setenv(fakeKeychainReadValueEnv, secret)

		got, err := keychain.Read(KeychainService, KeychainAccount)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if string(got) != secret {
			t.Fatalf("Read = %q, want %q", got, secret)
		}
		wantArgs := []string{"-s", KeychainService, "-a", KeychainAccount, "-w"}
		if args := readFakeKeychainArgs(t, captureDir, "find-generic-password"); !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("find argv = %#v, want %#v", args, wantArgs)
		}
	})

	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "documented exit status", mode: "not-found"},
		{name: "legacy diagnostic fallback", mode: "not-found-message"},
	} {
		t.Run(test.name, func(t *testing.T) {
			keychain, _ := newFakeDarwinKeychain(t, time.Second)
			t.Setenv(fakeKeychainTargetEnv, "find-generic-password")
			t.Setenv(fakeKeychainModeEnv, test.mode)
			if _, err := keychain.Read(KeychainService, KeychainAccount); !errors.Is(err, ErrKeychainNoEntry) {
				t.Fatalf("Read error = %v, want ErrKeychainNoEntry", err)
			}
		})
	}

	t.Run("delete remains idempotent", func(t *testing.T) {
		keychain, _ := newFakeDarwinKeychain(t, time.Second)
		t.Setenv(fakeKeychainTargetEnv, "delete-generic-password")
		t.Setenv(fakeKeychainModeEnv, "not-found")
		if err := keychain.Delete(KeychainService, KeychainAccount); err != nil {
			t.Fatalf("Delete missing entry: %v", err)
		}
	})
}

func TestDarwinKeychainErrorsNeverExposeCommandOutput(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		operation func(*DarwinKeychain) error
	}{
		{
			name:   "write pre-delete",
			target: "delete-generic-password",
			operation: func(keychain *DarwinKeychain) error {
				return keychain.Write(KeychainService, KeychainAccount, []byte("write-input-secret"))
			},
		},
		{
			name:   "write",
			target: "add-generic-password",
			operation: func(keychain *DarwinKeychain) error {
				return keychain.Write(KeychainService, KeychainAccount, []byte("write-input-secret"))
			},
		},
		{
			name:   "read",
			target: "find-generic-password",
			operation: func(keychain *DarwinKeychain) error {
				_, err := keychain.Read(KeychainService, KeychainAccount)
				return err
			},
		},
		{
			name:   "delete",
			target: "delete-generic-password",
			operation: func(keychain *DarwinKeychain) error {
				return keychain.Delete(KeychainService, KeychainAccount)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			keychain, _ := newFakeDarwinKeychain(t, time.Second)
			t.Setenv(fakeKeychainTargetEnv, test.target)
			t.Setenv(fakeKeychainModeEnv, "error")
			err := test.operation(keychain)
			if err == nil {
				t.Fatal("operation unexpectedly succeeded")
			}
			for _, forbidden := range []string{fakeKeychainErrorMarker, "write-input-secret"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error leaked %q: %v", forbidden, err)
				}
			}
			if !strings.Contains(err.Error(), "security command failed") {
				t.Fatalf("error is not closed/stable: %v", err)
			}
		})
	}
}

func TestDarwinKeychainCommandsHaveBoundedTimeouts(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		operation func(*DarwinKeychain) error
	}{
		{
			name:   "write pre-delete",
			target: "delete-generic-password",
			operation: func(keychain *DarwinKeychain) error {
				return keychain.Write(KeychainService, KeychainAccount, []byte("timeout-secret"))
			},
		},
		{
			name:   "write",
			target: "add-generic-password",
			operation: func(keychain *DarwinKeychain) error {
				return keychain.Write(KeychainService, KeychainAccount, []byte("timeout-secret"))
			},
		},
		{
			name:   "read",
			target: "find-generic-password",
			operation: func(keychain *DarwinKeychain) error {
				_, err := keychain.Read(KeychainService, KeychainAccount)
				return err
			},
		},
		{
			name:   "delete",
			target: "delete-generic-password",
			operation: func(keychain *DarwinKeychain) error {
				return keychain.Delete(KeychainService, KeychainAccount)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			keychain, _ := newFakeDarwinKeychain(t, 40*time.Millisecond)
			t.Setenv(fakeKeychainTargetEnv, test.target)
			t.Setenv(fakeKeychainModeEnv, "timeout")
			started := time.Now()
			err := test.operation(keychain)
			elapsed := time.Since(started)
			if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
				t.Fatalf("timeout error = %v", err)
			}
			if elapsed > 2*time.Second {
				t.Fatalf("command timeout was not bounded: %s", elapsed)
			}
			if strings.Contains(err.Error(), "timeout-secret") || strings.Contains(err.Error(), fakeKeychainErrorMarker) {
				t.Fatalf("timeout error leaked sensitive value: %v", err)
			}
		})
	}
}

func newFakeDarwinKeychain(t *testing.T, timeout time.Duration) (*DarwinKeychain, string) {
	t.Helper()
	captureDir := t.TempDir()
	scriptPath := filepath.Join(captureDir, "fake-security")
	if err := os.WriteFile(scriptPath, []byte(fakeDarwinSecurityScript), 0o700); err != nil {
		t.Fatalf("write fake security command: %v", err)
	}
	t.Setenv(fakeKeychainCaptureDirEnv, captureDir)
	t.Setenv(fakeKeychainTargetEnv, "")
	t.Setenv(fakeKeychainModeEnv, "")
	t.Setenv(fakeKeychainReadValueEnv, "")
	return &DarwinKeychain{commandName: scriptPath, commandTimeout: timeout}, captureDir
}

func readFakeKeychainArgs(t *testing.T, captureDir string, operation string) []string {
	t.Helper()
	raw := readFakeKeychainCapture(t, captureDir, operation+".argv")
	trimmed := strings.TrimSuffix(string(raw), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func readFakeKeychainCapture(t *testing.T, captureDir string, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(captureDir, name))
	if err != nil {
		t.Fatalf("read fake security capture %s: %v", name, err)
	}
	return raw
}

const fakeDarwinSecurityScript = `#!/bin/sh
set -eu

capture_dir="${WORKMAX_TEST_KEYCHAIN_CAPTURE_DIR:?}"
operation="$1"
shift

: > "${capture_dir}/${operation}.argv"
for argument in "$@"; do
  printf '%s\n' "$argument" >> "${capture_dir}/${operation}.argv"
done
cat > "${capture_dir}/${operation}.stdin"

if [ "${WORKMAX_TEST_KEYCHAIN_TARGET-}" = "$operation" ]; then
  case "${WORKMAX_TEST_KEYCHAIN_MODE-}" in
    timeout)
      while :; do :; done
      ;;
    not-found)
      printf '%s\n' 'security: item could not be found' >&2
      exit 44
      ;;
    not-found-message)
      printf '%s\n' 'security: item could not be found' >&2
      exit 9
      ;;
    error)
      printf '%s\n' '` + fakeKeychainErrorMarker + `'
      printf '%s\n' '` + fakeKeychainErrorMarker + `' >&2
      exit 9
      ;;
  esac
fi

if [ "$operation" = 'find-generic-password' ]; then
  printf '%s\n' "${WORKMAX_TEST_KEYCHAIN_READ_VALUE-}"
fi
`

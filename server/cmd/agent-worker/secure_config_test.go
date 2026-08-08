package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSecureWorkerConfigReadsOwnerOnlyRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.yaml")
	want := []byte("mysql_system:\n  password: test-only-secret\n")
	writeSecureConfigFixture(t, path, want, 0o600)

	got, err := readSecureWorkerConfig(path)
	if err != nil {
		t.Fatalf("readSecureWorkerConfig(): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("readSecureWorkerConfig() returned %q, want %q", got, want)
	}
}

func TestReadSecureWorkerConfigRejectsGroupOrWorldAccess(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o666} {
		mode := mode
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "SECRET_CONFIG_PATH.yaml")
			const secret = "SECRET_CONFIG_CONTENT_password=hunter2"
			writeSecureConfigFixture(t, path, []byte(secret), mode)

			assertSecureConfigRejected(t, path, path, filepath.Base(path), secret)
		})
	}
}

func TestReadSecureWorkerConfigRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.yaml")
	link := filepath.Join(directory, "SECRET_SYMLINK_PATH.yaml")
	const secret = "SECRET_SYMLINK_CONTENT"
	writeSecureConfigFixture(t, target, []byte(secret), 0o600)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("os.Symlink(): %v", err)
	}

	assertSecureConfigRejected(t, link, link, filepath.Base(link), target, secret)
}

func TestReadSecureWorkerConfigRejectsNonRegularFiles(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "SECRET_CONFIG_DIRECTORY")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("os.Mkdir(): %v", err)
		}

		assertSecureConfigRejected(t, path, path, filepath.Base(path))
	})

	t.Run("device", func(t *testing.T) {
		assertSecureConfigRejected(t, os.DevNull, os.DevNull)
	})
}

func TestReadSecureWorkerConfigRejectsFilesLargerThanLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SECRET_OVERSIZED_CONFIG.yaml")
	raw := make([]byte, int(maxWorkerConfigBytes)+1)
	const secret = "SECRET_OVERSIZED_CONTENT"
	copy(raw, secret)
	writeSecureConfigFixture(t, path, raw, 0o600)

	assertSecureConfigRejected(t, path, path, filepath.Base(path), secret)
}

func TestReadSecureWorkerConfigErrorsAreRedacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SECRET_MISSING_CONFIG_password=hunter2.yaml")
	raw, err := readSecureWorkerConfig(path)
	if raw != nil {
		t.Fatalf("readSecureWorkerConfig() returned bytes for a missing file: %q", raw)
	}
	if !errors.Is(err, errWorkerConfigFileUnsafe) {
		t.Fatalf("error = %v, want errWorkerConfigFileUnsafe", err)
	}
	for _, forbidden := range []string{path, filepath.Base(path), "hunter2", "no such file"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error %q disclosed %q", err, forbidden)
		}
	}
}

func writeSecureConfigFixture(t *testing.T, path string, raw []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("os.WriteFile(): %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("os.Chmod(%#o): %v", mode, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("os.Lstat(): %v", err)
	}
	if got := info.Mode().Perm(); got != mode.Perm() {
		t.Fatalf("fixture mode = %#o, want %#o", got, mode.Perm())
	}
}

func assertSecureConfigRejected(t *testing.T, path string, forbidden ...string) {
	t.Helper()
	raw, err := readSecureWorkerConfig(path)
	if raw != nil {
		t.Fatalf("readSecureWorkerConfig() returned rejected bytes: %q", raw)
	}
	if !errors.Is(err, errWorkerConfigFileUnsafe) {
		t.Fatalf("error = %v, want errWorkerConfigFileUnsafe", err)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatalf("error %q disclosed %q", err, value)
		}
	}
}

//go:build desktop && !darwin

package cloud_proxy

import (
	"errors"
	"testing"
)

func TestStubKeychainReturnsNotImplemented(t *testing.T) {
	kc := NewDarwinKeychain()

	if err := kc.Write(KeychainService, KeychainAccount, []byte("secret")); !errors.Is(err, errNotImplemented) {
		t.Fatalf("Write error: got %v, want errNotImplemented", err)
	}
	if _, err := kc.Read(KeychainService, KeychainAccount); !errors.Is(err, errNotImplemented) {
		t.Fatalf("Read error: got %v, want errNotImplemented", err)
	}
	if err := kc.Delete(KeychainService, KeychainAccount); !errors.Is(err, errNotImplemented) {
		t.Fatalf("Delete error: got %v, want errNotImplemented", err)
	}
}

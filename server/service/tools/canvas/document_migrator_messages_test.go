package canvas

import (
	"strings"
	"testing"

	"server/model"
)

// document_migrator_messages_test.go pins the diagnosability of the
// SchemaError message. The reject-newer test in document_migrator_test.go
// only checks the SchemaError code; a refactor that kept the code but
// lost the version number in the message would leave ops without a
// diagnosable string. Pin that the message names the observed version
// (e.g. "v99"). This is the contract that makes server logs useful
// without reading source.

func TestEnsureSchemaVersion_RejectFutureVersion_MessageNamesObservedVersion(t *testing.T) {
	future := model.JSONMap{
		"schemaVersion": float64(99),
		"elements":      []interface{}{},
		"viewport":      map[string]interface{}{"x": float64(0), "y": float64(0), "scale": float64(1)},
	}
	_, err := EnsureSchemaVersion(future)
	if err == nil {
		t.Fatal("expected SchemaError for v99 doc; got nil")
	}
	se, ok := IsSchemaError(err)
	if !ok {
		t.Fatalf("expected SchemaError wrap; got %T: %v", err, err)
	}
	if se.Code != ErrCodeSchemaUnsupported {
		t.Errorf("code = %q, want %q", se.Code, ErrCodeSchemaUnsupported)
	}
	if !strings.Contains(se.Message, "v99") {
		t.Errorf("message should name the observed version; got %q", se.Message)
	}
}

func TestEnsureSchemaVersion_InputMapIsStampedInPlace(t *testing.T) {
	// Go passes maps by reference, so the gate stamps the input map
	// directly. Call-sites that discard the return value rely on this.
	// A copy-out refactor that built a fresh map and returned it would
	// leave the caller's reference at v0 (no schemaVersion) while the
	// returned map holds v2. Pin by asserting the input map's
	// schemaVersion reflects the stamped value AFTER the call returns,
	// regardless of whether the caller uses `out`.
	input := model.JSONMap{
		"elements": []interface{}{},
		"viewport": map[string]interface{}{
			"x": float64(0), "y": float64(0), "scale": float64(1),
		},
	}
	if _, err := EnsureSchemaVersion(input); err != nil {
		t.Fatalf("stamp on missing schemaVersion failed: %v", err)
	}
	if input["schemaVersion"] != CurrentSchemaVersion {
		t.Errorf("input map was not stamped in place: %v (want %d)",
			input["schemaVersion"], CurrentSchemaVersion)
	}
}

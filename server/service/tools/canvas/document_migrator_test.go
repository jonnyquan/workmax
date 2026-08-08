package canvas

import (
	"testing"

	"server/model"
)

func TestEnsureSchemaVersion_BackfillsMissing(t *testing.T) {
	// Legacy rows written before B5 have no schemaVersion. They must
	// be stamped to the current version rather than rejected.
	doc := model.JSONMap{"elements": []interface{}{}}
	out, err := EnsureSchemaVersion(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["schemaVersion"] != CurrentSchemaVersion {
		t.Fatalf("expected schemaVersion=%d, got %v", CurrentSchemaVersion, out["schemaVersion"])
	}
}

func TestEnsureSchemaVersion_PassesCurrent(t *testing.T) {
	doc := model.JSONMap{"schemaVersion": CurrentSchemaVersion}
	out, err := EnsureSchemaVersion(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["schemaVersion"] != CurrentSchemaVersion {
		t.Fatalf("schemaVersion should remain %d, got %v", CurrentSchemaVersion, out["schemaVersion"])
	}
}

func TestEnsureSchemaVersion_AcceptsJSONFloat(t *testing.T) {
	// encoding/json decodes numbers into float64 when the target is a
	// map[string]interface{}, so we must accept that shape.
	doc := model.JSONMap{"schemaVersion": float64(CurrentSchemaVersion)}
	if _, err := EnsureSchemaVersion(doc); err != nil {
		t.Fatalf("float64(%d) should pass: %v", CurrentSchemaVersion, err)
	}
}

func TestEnsureSchemaVersion_RejectsNewer(t *testing.T) {
	doc := model.JSONMap{"schemaVersion": CurrentSchemaVersion + 5}
	_, err := EnsureSchemaVersion(doc)
	if err == nil {
		t.Fatal("newer schema should be rejected")
	}
	se, ok := IsSchemaError(err)
	if !ok {
		t.Fatalf("expected SchemaError, got %T", err)
	}
	if se.Code != ErrCodeSchemaUnsupported {
		t.Fatalf("expected code %q, got %q", ErrCodeSchemaUnsupported, se.Code)
	}
}

func TestEnsureSchemaVersion_RejectsNonNumeric(t *testing.T) {
	doc := model.JSONMap{"schemaVersion": "one"}
	_, err := EnsureSchemaVersion(doc)
	if err == nil {
		t.Fatal("non-numeric schemaVersion should be rejected")
	}
	if _, ok := IsSchemaError(err); !ok {
		t.Fatalf("expected SchemaError, got %T", err)
	}
}

func TestEnsureSchemaVersion_NilDoc(t *testing.T) {
	// Defensive: handlers may pass a nil doc on unusual code paths. We
	// should produce a freshly stamped empty document rather than
	// panicking.
	out, err := EnsureSchemaVersion(nil)
	if err != nil {
		t.Fatalf("nil doc should not error: %v", err)
	}
	if out["schemaVersion"] != CurrentSchemaVersion {
		t.Fatalf("nil doc should be stamped to v%d", CurrentSchemaVersion)
	}
}

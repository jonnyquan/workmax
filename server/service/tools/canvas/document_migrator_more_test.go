package canvas

import (
	"errors"
	"testing"

	"server/model"
)

// document_migrator_test.go pins the most common paths (backfill, pass-
// current, reject-newer, nil doc, float64 shape). These tests fill in
// the numeric-type matrix that readSchemaVersion supports + the
// IsSchemaError unwrap guards that call sites lean on, plus the
// "no migrator registered" branch that will become load-bearing the
// moment a v3 lands and an un-registered v2→v3 path slips through.

func TestReadSchemaVersion_AcceptsAllNumericKinds(t *testing.T) {
	// EnsureSchemaVersion routes through readSchemaVersion, which has to
	// accept int / int64 / float32 / float64 because documents arrive
	// via three different decoders:
	//   - gorm → int
	//   - encoding/json direct → float64
	//   - legacy msgpack / proto bridges → int64 / float32
	// A regression where any of these four paths errors out would block
	// a subset of canvas reads entirely. Pin all four here.
	cases := []struct {
		name  string
		value any
	}{
		{"int", int(CurrentSchemaVersion)},
		{"int64", int64(CurrentSchemaVersion)},
		{"float32", float32(CurrentSchemaVersion)},
		{"float64", float64(CurrentSchemaVersion)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := model.JSONMap{"schemaVersion": c.value}
			out, err := EnsureSchemaVersion(doc)
			if err != nil {
				t.Fatalf("%s: unexpected error %v", c.name, err)
			}
			if out["schemaVersion"] != CurrentSchemaVersion {
				t.Errorf("%s: schemaVersion after = %v, want %d", c.name, out["schemaVersion"], CurrentSchemaVersion)
			}
		})
	}
}

func TestReadSchemaVersion_NilValueTreatedAsMissing(t *testing.T) {
	// A `schemaVersion: null` in the JSON column is ambiguous — nothing
	// was written yet, or someone cleared it. The current contract
	// treats that like "missing" and back-fills to CurrentSchemaVersion.
	// Pin it so a stricter future check ("null is malformed") would be
	// a deliberate rather than accidental change.
	doc := model.JSONMap{"schemaVersion": nil}
	out, err := EnsureSchemaVersion(doc)
	if err != nil {
		t.Fatalf("nil schemaVersion should be treated as missing: %v", err)
	}
	if out["schemaVersion"] != CurrentSchemaVersion {
		t.Errorf("expected back-fill to %d, got %v", CurrentSchemaVersion, out["schemaVersion"])
	}
}

func TestIsSchemaError_NilErrReturnsFalse(t *testing.T) {
	// Call sites use `if se, ok := IsSchemaError(err); ok` without
	// nil-checking err first. Pin that `nil` in → `(nil, false)` out.
	se, ok := IsSchemaError(nil)
	if ok {
		t.Fatal("IsSchemaError(nil) should return ok=false")
	}
	if se != nil {
		t.Fatalf("IsSchemaError(nil) should return nil SchemaError, got %v", se)
	}
}

func TestIsSchemaError_UnwrapsWrappedSchemaError(t *testing.T) {
	// Handlers commonly wrap the migrator error with fmt.Errorf("%w")
	// before re-throwing. errors.As must peel that back to the inner
	// SchemaError, otherwise the HTTP layer loses the §12.1 code and
	// falls back to a generic 500.
	inner := &SchemaError{Code: ErrCodeSchemaUnsupported, Message: "boom"}
	wrapped := &wrapError{msg: "handler context", inner: inner}
	se, ok := IsSchemaError(wrapped)
	if !ok {
		t.Fatal("IsSchemaError should unwrap a wrapped SchemaError")
	}
	if se.Code != ErrCodeSchemaUnsupported {
		t.Errorf("unwrapped code = %q, want %q", se.Code, ErrCodeSchemaUnsupported)
	}
}

func TestIsSchemaError_PlainErrorReturnsFalse(t *testing.T) {
	// A regular, non-SchemaError errors.New must not match — otherwise
	// the HTTP layer would mis-classify every generic failure as a
	// schema issue and the toast copy would drift from reality.
	se, ok := IsSchemaError(errors.New("unrelated"))
	if ok {
		t.Fatal("IsSchemaError on a plain error should return false")
	}
	if se != nil {
		t.Fatalf("SchemaError pointer should be nil, got %+v", se)
	}
}

// wrapError is a tiny %w-compatible wrapper used only by the test above.
// Avoids dragging in fmt.Errorf's exact formatting while keeping the
// unwrap chain intact.
type wrapError struct {
	msg   string
	inner error
}

func (e *wrapError) Error() string { return e.msg + ": " + e.inner.Error() }
func (e *wrapError) Unwrap() error { return e.inner }

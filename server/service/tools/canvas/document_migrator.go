package canvas

import (
	"errors"
	"fmt"

	"server/model"
)

// document_migrator.go — schema-version enforcement gate.
//
// All canvas documents must carry schemaVersion = 2. The pre-2026-05
// build supported a v1→v2 in-place upgrader through a `migrators` map
// + walk loop; that machinery was retired when the project committed to
// "v2 is the only schema we speak" (no real users yet, so no production
// data needed migrating). If a future v3 lands, re-introduce the map +
// walk here. Until then, this file is intentionally tiny: stamp on
// missing, accept on match, reject on anything else.

// CurrentSchemaVersion is the only schemaVersion this server build will
// accept on read or stamp on write.
const CurrentSchemaVersion = 2

// ErrCodeSchemaUnsupported is returned to clients when the persisted
// document schema doesn't match what this server build understands.
const ErrCodeSchemaUnsupported = "CANVAS_SCHEMA_VERSION_UNSUPPORTED"

// SchemaError wraps a migrator failure so handlers can surface the
// exact §12.1 error code to the client instead of a generic message.
type SchemaError struct {
	Code    string
	Message string
}

func (e *SchemaError) Error() string { return e.Message }

// EnsureSchemaVersion stamps a missing schemaVersion as the current
// version and rejects anything else. Handlers call this on every read
// and write so unknown schema versions surface a structured error
// instead of being silently processed.
func EnsureSchemaVersion(doc model.JSONMap) (model.JSONMap, error) {
	if doc == nil {
		doc = model.JSONMap{}
	}

	version, err := readSchemaVersion(doc)
	if err != nil {
		return doc, &SchemaError{Code: ErrCodeSchemaUnsupported, Message: err.Error()}
	}

	if version == 0 {
		// Missing or null schemaVersion → stamp current. Lets clients that
		// don't include the tag still write through the gate.
		doc["schemaVersion"] = CurrentSchemaVersion
		return doc, nil
	}

	if version != CurrentSchemaVersion {
		return doc, &SchemaError{
			Code:    ErrCodeSchemaUnsupported,
			Message: fmt.Sprintf("Document schema v%d is not supported (server speaks v%d only)", version, CurrentSchemaVersion),
		}
	}

	// Re-stamp with the canonical int type. Documents arriving via JSON
	// decoders carry float64 (encoding/json) or int64 (legacy bridges);
	// downstream consumers that compare against CurrentSchemaVersion (an
	// int) fail equality on type mismatch even when values agree.
	doc["schemaVersion"] = CurrentSchemaVersion
	return doc, nil
}

// readSchemaVersion accepts int / int64 / float64 (JSON numbers round-
// trip through float64 when decoded into a map) and reports version 0
// when the key is absent. Any other shape is a client mistake.
func readSchemaVersion(doc model.JSONMap) (int, error) {
	raw, ok := doc["schemaVersion"]
	if !ok || raw == nil {
		return 0, nil
	}
	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case float32:
		return int(v), nil
	default:
		return 0, errors.New("schemaVersion must be a number")
	}
}

// IsSchemaError lets call sites decide whether to surface the code to
// the client without importing errors.As everywhere.
func IsSchemaError(err error) (*SchemaError, bool) {
	if err == nil {
		return nil, false
	}
	var se *SchemaError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}

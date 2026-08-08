package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"server/service/secrets"
)

// EncryptedJSONMap is a JSONMap that transparently encrypts on
// write (Value) and decrypts on read (Scan). The on-disk shape
// is the secrets.Encrypt envelope string (v1:nonce:ciphertext)
// stored in a JSON or TEXT column; the in-memory shape is the
// same map[string]interface{} as JSONMap so the rest of the
// codebase reads / writes it identically.
//
// Used for fields that carry user secrets — MCP connector env
// (stdio child-process environment) + headers (sse/http auth).
// Plain JSONMap is fine for non-secret JSON; switch to this
// type only when the column might hold API keys / Bearer
// tokens / OAuth refresh tokens.
//
// Decryption failures during Scan produce a nil map + a logged
// error rather than failing the whole row read. A row with a
// broken ciphertext (key rotation incident, corrupted backup)
// should still show up in the list view; only its secret
// payload is unreadable. The caller (bridge / handler) treats
// nil here as "no secrets configured" which downgrades the
// row to anonymous-mode rather than crashing the turn.
type EncryptedJSONMap map[string]interface{}

// GormDataType — TEXT column on the DB; the encryption
// envelope is a base64+colon string, not native JSON. Storing
// in JSON column type works too but TEXT is more honest about
// the contents (DB can't introspect the ciphertext anyway).
func (EncryptedJSONMap) GormDataType() string { return "text" }

// Value encrypts the map into the on-disk envelope. nil / empty
// short-circuits to SQL NULL — no point storing an envelope
// for "no secrets configured."
func (e EncryptedJSONMap) Value() (driver.Value, error) {
	if len(e) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(map[string]interface{}(e))
	if err != nil {
		return nil, fmt.Errorf("encrypted json marshal: %w", err)
	}
	envelope, err := secrets.Encrypt(raw)
	if err != nil {
		return nil, fmt.Errorf("encrypted json seal: %w", err)
	}
	return envelope, nil
}

// Scan decrypts the on-disk envelope. Three input shapes are
// tolerated:
//
//   - nil / empty → empty map (no secrets)
//   - secrets envelope (v1:...) → decrypt + unmarshal
//   - raw JSON ({...}) → unmarshal directly (back-compat for
//     rows written before this type existed; the next Save()
//     will rewrite as encrypted)
//
// The plain-JSON fallback exists because Phase B1 wrote env /
// headers as unencrypted JSONMap. Phase B3 doesn't ship a
// backfill migration — the next user-initiated update on each
// row re-encrypts naturally.
func (e *EncryptedJSONMap) Scan(value interface{}) error {
	if value == nil {
		*e = nil
		return nil
	}
	var raw string
	switch v := value.(type) {
	case []byte:
		raw = string(v)
	case string:
		raw = v
	default:
		return nil
	}
	if raw == "" {
		*e = nil
		return nil
	}

	// Back-compat: if the stored value looks like raw JSON
	// (Phase-B1 rows), unmarshal directly.
	if len(raw) > 0 && (raw[0] == '{' || raw[0] == '[') {
		var legacy map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &legacy); err == nil {
			*e = EncryptedJSONMap(legacy)
			return nil
		}
		// fall through to envelope path on parse failure
	}

	if !secrets.IsEnvelope(raw) {
		// Unknown shape — log via the error path. Returning
		// nil here would silently lose data; returning the
		// error propagates up so the call site sees something
		// is wrong with the row.
		return fmt.Errorf("encrypted json: unrecognized envelope shape")
	}

	plaintext, err := secrets.Decrypt(raw)
	if err != nil {
		return fmt.Errorf("encrypted json decrypt: %w", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(plaintext, &decoded); err != nil {
		return fmt.Errorf("encrypted json unmarshal: %w", err)
	}
	*e = EncryptedJSONMap(decoded)
	return nil
}

// AsJSONMap is a typed cast helper. The bridge layer
// (jsonMapToStringMap) takes a JSONMap; this lets the caller
// avoid scattering explicit `model.JSONMap(c.Env)` everywhere.
func (e EncryptedJSONMap) AsJSONMap() JSONMap {
	if e == nil {
		return nil
	}
	return JSONMap(e)
}

package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// QuoteTTL is how long an issued credit quote remains valid before the client
// must request a new one. Keep short so pricing drift is bounded; Reservation
// re-validates fingerprint anyway.
const QuoteTTL = 5 * time.Minute

// QuoteRecord captures the server-side truth about a credit quote. The
// fingerprint is a hash over the canonical {mode, model, params} tuple so the
// reservation step can detect client-side tampering without trusting the raw
// params echo.
type QuoteRecord struct {
	ID            string                 `json:"id"`
	UID           uint                   `json:"uid"`
	Mode          string                 `json:"mode"`
	Model         string                 `json:"model"`
	Fingerprint   string                 `json:"fingerprint"`
	Credits       int                    `json:"credits"`
	PricingStatus string                 `json:"pricingStatus,omitempty"`
	Params        map[string]interface{} `json:"params,omitempty"`
	ExpiresAt     time.Time              `json:"expiresAt"`
}

var (
	quoteStoreOnce sync.Once
	quoteStoreMu   sync.RWMutex
	quoteStore     = map[string]*QuoteRecord{}
)

// IssueQuote registers a new quote and returns the persisted record. A fresh
// UUID is generated server-side so clients cannot forge IDs.
func IssueQuote(uid uint, mode, modelID string, params map[string]interface{}, credits int, pricingStatus string) QuoteRecord {
	startQuoteJanitor()
	record := QuoteRecord{
		ID:            uuid.New().String(),
		UID:           uid,
		Mode:          mode,
		Model:         modelID,
		Fingerprint:   fingerprintQuote(mode, modelID, params),
		Credits:       credits,
		PricingStatus: pricingStatus,
		Params:        cloneQuoteParams(params),
		ExpiresAt:     time.Now().Add(QuoteTTL),
	}
	quoteStoreMu.Lock()
	quoteStore[record.ID] = &record
	quoteStoreMu.Unlock()
	return record
}

// LookupQuote returns a non-expired quote by ID. It does not consume the quote
// — reservation is responsible for atomically consuming and deducting credits.
func LookupQuote(id string) (QuoteRecord, bool) {
	quoteStoreMu.RLock()
	rec, ok := quoteStore[id]
	quoteStoreMu.RUnlock()
	if !ok {
		return QuoteRecord{}, false
	}
	if time.Now().After(rec.ExpiresAt) {
		return QuoteRecord{}, false
	}
	return *rec, true
}

// VerifyQuote confirms the quote matches the caller + current request shape.
// Returns the record if valid so callers can read the authoritative credits
// value without having to trust request-side fields.
func VerifyQuote(id string, uid uint, mode, modelID string, params map[string]interface{}) (QuoteRecord, bool) {
	rec, ok := LookupQuote(id)
	if !ok {
		return QuoteRecord{}, false
	}
	if rec.UID != uid || rec.Mode != mode || rec.Model != modelID {
		return QuoteRecord{}, false
	}
	if rec.Fingerprint != fingerprintQuote(mode, modelID, params) {
		return QuoteRecord{}, false
	}
	return rec, true
}

// ConsumeQuote removes a quote after a successful reservation. It is safe to
// call even if the quote was already swept; the boolean signals whether a
// live quote was actually consumed.
func ConsumeQuote(id string) bool {
	quoteStoreMu.Lock()
	_, ok := quoteStore[id]
	if ok {
		delete(quoteStore, id)
	}
	quoteStoreMu.Unlock()
	return ok
}

// CanonicalImageQuoteParams builds the canonical param map used for image
// quote fingerprints. Callers on both the quote and reservation side must go
// through this helper so the SHA hash matches byte-for-byte.
func CanonicalImageQuoteParams(numberOfImages int, resolution string, quality string) map[string]interface{} {
	if numberOfImages <= 0 {
		numberOfImages = 1
	}
	out := map[string]interface{}{"numberOfImages": numberOfImages}
	if resolution != "" {
		out["resolution"] = resolution
	}
	if quality != "" {
		out["quality"] = quality
	}
	return out
}

// CanonicalVideoQuoteParams builds the canonical param map used for video
// quote fingerprints. opts is expected to already be normalized.
func CanonicalVideoQuoteParams(opts VideoQuoteOptions) map[string]interface{} {
	out := map[string]interface{}{"duration": opts.Duration}
	if opts.Resolution != "" {
		out["resolution"] = opts.Resolution
	}
	if opts.AspectRatio != "" {
		out["aspectRatio"] = opts.AspectRatio
	}
	if opts.MotionMode != "" {
		out["motionMode"] = opts.MotionMode
	}
	if opts.MotionOrientation != "" {
		out["motionOrientation"] = opts.MotionOrientation
	}
	if opts.HasStartFrame {
		out["hasStartFrame"] = true
	}
	if opts.HasEndFrame {
		out["hasEndFrame"] = true
	}
	return out
}

func fingerprintQuote(mode, modelID string, params map[string]interface{}) string {
	payload := struct {
		Mode   string          `json:"mode"`
		Model  string          `json:"model"`
		Params json.RawMessage `json:"params"`
	}{
		Mode:   mode,
		Model:  modelID,
		Params: canonicalJSON(params),
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// canonicalJSON emits map keys in sorted order so fingerprints are stable
// across Go map iteration randomness and client-side key reordering.
func canonicalJSON(v interface{}) json.RawMessage {
	switch val := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf := []byte{'{'}
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			keyJSON, _ := json.Marshal(k)
			buf = append(buf, keyJSON...)
			buf = append(buf, ':')
			buf = append(buf, canonicalJSON(val[k])...)
		}
		buf = append(buf, '}')
		return buf
	case []interface{}:
		buf := []byte{'['}
		for i, item := range val {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = append(buf, canonicalJSON(item)...)
		}
		buf = append(buf, ']')
		return buf
	default:
		raw, _ := json.Marshal(val)
		return raw
	}
}

func cloneQuoteParams(params map[string]interface{}) map[string]interface{} {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}

func startQuoteJanitor() {
	quoteStoreOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for now := range ticker.C {
				sweepExpiredQuotes(now)
			}
		}()
	})
}

func sweepExpiredQuotes(now time.Time) {
	quoteStoreMu.Lock()
	defer quoteStoreMu.Unlock()
	for id, rec := range quoteStore {
		if now.After(rec.ExpiresAt) {
			delete(quoteStore, id)
		}
	}
}

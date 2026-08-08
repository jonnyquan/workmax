package workagent

import (
	"encoding/json"
	"testing"
)

// extractTotalCostUSDFromResult feeds the cost-drift log line. A
// false negative (returning ok=false when the field IS present)
// silently disables cost-drift observability for one of the gateways
// — pin every branch.
func TestExtractTotalCostUSDFromResult(t *testing.T) {
	cases := []struct {
		name    string
		raw     json.RawMessage
		wantOK  bool
		wantVal float64 // only checked when wantOK = true
	}{
		{
			name:   "empty bytes → not found",
			raw:    json.RawMessage{},
			wantOK: false,
		},
		{
			name:   "nil bytes → not found",
			raw:    nil,
			wantOK: false,
		},
		{
			name:   "malformed JSON → not found (don't crash)",
			raw:    json.RawMessage("not json"),
			wantOK: false,
		},
		{
			name:   "no field → not found",
			raw:    json.RawMessage(`{"type":"result","session_id":"abc"}`),
			wantOK: false,
		},
		{
			name:    "positive value → reported",
			raw:     json.RawMessage(`{"type":"result","total_cost_usd":0.0123}`),
			wantOK:  true,
			wantVal: 0.0123,
		},
		{
			name:    "zero is a valid value (free turn) → reported",
			raw:     json.RawMessage(`{"type":"result","total_cost_usd":0}`),
			wantOK:  true,
			wantVal: 0,
		},
		{
			name:    "very small value preserved (subscription tier with discount)",
			raw:     json.RawMessage(`{"type":"result","total_cost_usd":0.00001}`),
			wantOK:  true,
			wantVal: 0.00001,
		},
		{
			name:    "large turn cost preserved",
			raw:     json.RawMessage(`{"type":"result","total_cost_usd":12.345678}`),
			wantOK:  true,
			wantVal: 12.345678,
		},
		{
			name:   "explicit JSON null → not found (parsed pointer is nil)",
			raw:    json.RawMessage(`{"type":"result","total_cost_usd":null}`),
			wantOK: false,
		},
		{
			name:    "field present with extra siblings → reported (parser ignores unknown)",
			raw:     json.RawMessage(`{"type":"result","total_cost_usd":2.5,"num_turns":5,"is_error":false,"session_id":"abc"}`),
			wantOK:  true,
			wantVal: 2.5,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPtr, gotOK := extractTotalCostUSDFromResult(tc.raw)
			if gotOK != tc.wantOK {
				t.Fatalf("ok = %v, want %v\ninput: %s", gotOK, tc.wantOK, string(tc.raw))
			}
			if !tc.wantOK {
				if gotPtr != nil {
					t.Errorf("expected nil pointer when not-found, got %v", *gotPtr)
				}
				return
			}
			if gotPtr == nil {
				t.Fatalf("ok=true but pointer is nil\ninput: %s", string(tc.raw))
			}
			if *gotPtr != tc.wantVal {
				t.Errorf("value = %v, want %v\ninput: %s", *gotPtr, tc.wantVal, string(tc.raw))
			}
		})
	}
}

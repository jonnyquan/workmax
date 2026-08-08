package utils

import (
	"testing"
	"time"
)

// ParseDuration extends time.ParseDuration with day-suffix support so JWT
// expiry / buffer times in config can be written as "7d" or "7d12h" —
// values that Go's stdlib parser rejects outright. Drifts here shorten or
// lengthen token lifetimes silently (middleware/jwt.go, initialize/outer.go,
// utils/JWT.go all read through this helper), so the contract is pinned
// here with table-driven coverage of every branch.

func TestParseDuration(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		// Standard time.ParseDuration delegations.
		{"simple hours", "3h", 3 * time.Hour, false},
		{"minutes and seconds", "1m30s", time.Minute + 30*time.Second, false},
		{"leading whitespace trimmed", "  2h  ", 2 * time.Hour, false},

		// Day-suffix cases — the whole point of this wrapper.
		{"days only", "2d", 48 * time.Hour, false},
		{"days plus hours", "2d3h", 48*time.Hour + 3*time.Hour, false},
		{"zero days", "0d", 0, false},

		// Invalid trailer after `d` swallows the parse error and returns
		// just the day portion. Callers rely on this: a malformed tail
		// shouldn't discard the parsed-days work.
		{"days with garbage trailer", "2dxxx", 48 * time.Hour, false},

		// `d` alone — Atoi("") fails silently, trailer ParseDuration
		// fails silently, so 0 is returned with nil error. Not a great
		// contract but it IS the current one; callers treat 0 + nil as
		// "use a default" elsewhere.
		{"bare d", "d", 0, false},

		// Pure integer → ParseInt path → treated as nanoseconds (time.Duration
		// underlying unit). Used for raw tick counts from some config.
		{"pure int is nanoseconds", "1000", 1000 * time.Nanosecond, false},

		// Failure path: empty string bails through both branches.
		{"empty string errors", "", 0, true},

		// Failure path: completely invalid non-numeric string with no `d`
		// falls to ParseInt and errors there.
		{"garbage errors", "abc", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDuration(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Cross-call consistency: "7d" and "168h" must resolve to the same
// duration. If a future refactor changed the day-multiplier (e.g. to
// 25h for DST, which would be wrong here — this is a wall-clock span),
// this test would catch it without needing to chase every caller.
func TestParseDuration_DayEqualsTwentyFourHours(t *testing.T) {
	d1, err := ParseDuration("7d")
	if err != nil {
		t.Fatalf("7d: %v", err)
	}
	d2, err := ParseDuration("168h")
	if err != nil {
		t.Fatalf("168h: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("7d (%v) != 168h (%v)", d1, d2)
	}
}

package workagent

import (
	"reflect"
	"testing"
)

// translateSettingSources is the sole gate between operator config
// and the SDK's --setting-sources flag. Drift here directly affects
// which settings files the CLI loads at startup — every branch must
// be pinned.
func TestTranslateSettingSources(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string // nil means caller should skip the option
	}{
		{
			name: "nil → nil (don't pass option, SDK default)",
			in:   nil,
			want: nil,
		},
		{
			name: "empty slice → nil",
			in:   []string{},
			want: nil,
		},
		{
			name: "single valid entry passes through",
			in:   []string{"user"},
			want: []string{"user"},
		},
		{
			name: "multiple valid entries preserved in order",
			in:   []string{"user", "project"},
			want: []string{"user", "project"},
		},
		{
			name: "all three valid entries",
			in:   []string{"user", "project", "local"},
			want: []string{"user", "project", "local"},
		},
		{
			name: "whitespace stripped",
			in:   []string{"  user  ", "\tproject\n"},
			want: []string{"user", "project"},
		},
		{
			name: "case normalized to lower",
			in:   []string{"USER", "Project", "LoCaL"},
			want: []string{"user", "project", "local"},
		},
		{
			name: "empty string entries dropped (stray YAML \"- \")",
			in:   []string{"", "user", "  ", "project"},
			want: []string{"user", "project"},
		},
		{
			name: "all-empty input → nil (don't disable lockdown by collapsing)",
			in:   []string{"", "  ", "\t"},
			want: nil,
		},
		{
			name: "invalid entry dropped (typo)",
			in:   []string{"user", "globals"},
			want: []string{"user"},
		},
		{
			name: "all-invalid → nil (operator typo throughout, SDK default takes over)",
			in:   []string{"globals", "userr"},
			want: nil,
		},
		{
			name: "whitespace + invalid + valid — only valid survives",
			in:   []string{"  ", "user", "junk", " project "},
			want: []string{"user", "project"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateSettingSources(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("translateSettingSources(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// Defensive copy: the translator must not alias the caller's slice.
// A future config-reload flow that mutates the slice in place would
// otherwise corrupt the live SDK options for the in-flight turn.
func TestTranslateSettingSources_NoAliasing(t *testing.T) {
	src := []string{"user", "project"}
	got := translateSettingSources(src)
	if got == nil {
		t.Fatal("translateSettingSources returned nil for valid input")
	}

	// Mutate src and confirm got is untouched.
	src[0] = "MUTATED"
	if got[0] != "user" {
		t.Errorf("translator aliased the input slice: got[0] = %q after src mutation", got[0])
	}
}

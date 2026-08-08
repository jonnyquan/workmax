package model

import "testing"

func TestJSONMapScan(t *testing.T) {
	cases := []struct {
		name  string
		input any
	}{
		{name: "nil", input: nil},
		{name: "empty-bytes", input: []byte("")},
		{name: "empty-string", input: ""},
		{name: "null-bytes", input: []byte("null")},
		{name: "object-bytes", input: []byte(`{"a":1,"b":"x"}`)},
		{name: "wrapped-json-string", input: []byte(`"{\"a\":1}"`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m JSONMap
			if err := (&m).Scan(tc.input); err != nil {
				t.Fatalf("Scan returned error: %v", err)
			}
		})
	}
}

package canvas

import "testing"

func TestAgentCost(t *testing.T) {
	cases := []struct {
		name            string
		attachmentCount int
		want            int
	}{
		{"no attachments", 0, 6},
		{"three attachments", 3, 9},
		{"attachments capped at five", 12, 11},
		{"negative treated as zero", -3, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AgentCost(tc.attachmentCount)
			if got != tc.want {
				t.Fatalf("AgentCost(%d) = %d, want %d", tc.attachmentCount, got, tc.want)
			}
		})
	}
}

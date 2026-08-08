package tools

import "testing"

func TestClassifyTaskErrorCode(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "video provider missing",
			msg:  "No available video generation provider. Ask the Server operator to configure media_type=video.",
			want: TaskErrorCodeVideoProvider,
		},
		{
			name: "generic provider missing",
			msg:  "No available image generation provider. Ask the Server operator to configure one.",
			want: TaskErrorCodeProviderMissing,
		},
		{
			name: "rate limited",
			msg:  "rate limit exceeded",
			want: TaskErrorCodeRateLimited,
		},
		{
			name: "fallback unknown",
			msg:  "some unexpected backend failure",
			want: TaskErrorCodeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyTaskErrorCode(tt.msg)
			if got != tt.want {
				t.Fatalf("ClassifyTaskErrorCode() = %s, want %s", got, tt.want)
			}
		})
	}
}

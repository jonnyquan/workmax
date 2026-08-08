package workagent

import (
	"math"
	"testing"
	"time"
)

func TestResolveAgentExecutionTimeout(t *testing.T) {
	fallback := 30 * time.Minute
	tests := []struct {
		name       string
		configured int
		want       time.Duration
	}{
		{name: "zero uses fallback", configured: 0, want: fallback},
		{name: "negative uses fallback", configured: -1, want: fallback},
		{name: "positive seconds", configured: 1800, want: 30 * time.Minute},
		{name: "overflow saturates", configured: math.MaxInt, want: time.Duration(math.MaxInt64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ResolveAgentExecutionTimeout(test.configured, fallback); got != test.want {
				t.Fatalf("timeout = %s, want %s", got, test.want)
			}
		})
	}
}

func TestReservationTTLForExecutionAddsSettlementMarginWithoutOverflow(t *testing.T) {
	if got := ReservationTTLForExecution(30 * time.Minute); got != 35*time.Minute {
		t.Fatalf("TTL = %s, want 35m", got)
	}
	if got := ReservationTTLForExecution(time.Duration(math.MaxInt64)); got != time.Duration(math.MaxInt64) {
		t.Fatalf("overflow TTL = %s, want saturation", got)
	}
	canvasTimeout := ResolveAgentExecutionTimeout(0, 10*time.Minute)
	if got := ReservationTTLForExecution(canvasTimeout); got != 15*time.Minute {
		t.Fatalf("Canvas fallback TTL = %s, want 15m", got)
	}
}

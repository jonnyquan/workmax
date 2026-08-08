//go:build !windows

package core

import (
	"testing"
	"time"
)

func TestPublicServerHeaderBudgetsStayBounded(t *testing.T) {
	if serverReadHeaderTimeout <= 0 || serverReadHeaderTimeout > 30*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want a positive value no greater than 30s", serverReadHeaderTimeout)
	}
	if serverMaxHeaderBytes <= 0 || serverMaxHeaderBytes > 64<<10 {
		t.Fatalf("MaxHeaderBytes = %d, want a positive value no greater than 64KiB", serverMaxHeaderBytes)
	}
}

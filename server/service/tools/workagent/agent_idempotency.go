package workagent

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// MintIdempotencyKey produces the credit-reservation idempotency key
// for one agent turn. Returns the surface-prefixed client header
// when X-{Surface}-Request-Id is present (so a retry collapses onto
// the same reservation row → no double-charge); otherwise mints a
// fallback key with a UnixNano + atomic counter against same-
// nanosecond collisions on the (uid, idempotency_key) unique index.
//
// The fallback shape "{tool}_{uid}_{nano}_{seq}" matches the legacy
// per-handler patterns this helper consolidates — canvas's
// "canvas_agent_…" and workagent's "workagent_…" both fit because
// AgentSurface.Tool() returns the right tool name per surface.
//
// Each surface's handler holds its own atomic.Uint64 fallback counter
// (the counter is process-wide hot path — one shared counter across
// surfaces would serialize unrelated handlers needlessly). Pass it
// in via fallbackSeq.
func MintIdempotencyKey(
	c *gin.Context,
	surface AgentSurface,
	uid uint,
	fallbackSeq *atomic.Uint64,
) string {
	header := strings.TrimSpace(c.GetHeader(surface.IdempotencyHeaderName()))
	if header != "" {
		if prefix := surface.IdempotencyKeyPrefix(); prefix != "" {
			return prefix + header
		}
		return header
	}
	return fmt.Sprintf(
		"%s_%d_%d_%d",
		surface.Tool(),
		uid,
		time.Now().UnixNano(),
		fallbackSeq.Add(1),
	)
}

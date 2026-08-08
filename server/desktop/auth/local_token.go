//go:build desktop

// Package auth implements the X-Local-Token middleware that protects
// the sidecar's loopback HTTP server from non-renderer callers on the
// same machine.
//
// The token is generated fresh on each sidecar startup (the shell
// passes it via env var; renderer receives it via contextBridge).
// Renderer requests must include it as a header; everyone else (other
// browser tabs, other local processes that happened to guess the port)
// is rejected with 403.
//
// See sidecar-protocol.md §3.3 for the threat model and §3.4 for the
// attack surface analysis.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"
)

// HeaderLocalToken is the request header that carries the per-sidecar
// rotating token. Renderer's preload.fetch wrapper injects it.
const HeaderLocalToken = "X-Local-Token"

// RequireLocalToken returns a Gin middleware that rejects requests
// missing, duplicating, or carrying the wrong X-Local-Token. The final
// equality check compares fixed-length digests, avoiding the
// length-dependent early return in subtle.ConstantTimeCompare.
//
// The expected token must be non-empty; passing an empty expected
// value would let every blank/missing header through. We panic at
// boot in that case (programmer error, not a runtime branch).
func RequireLocalToken(expected string) gin.HandlerFunc {
	if expected == "" {
		panic("auth.RequireLocalToken: expected token is empty")
	}
	expectedDigest := sha256.Sum256([]byte(expected))
	return func(c *gin.Context) {
		values := c.Request.Header.Values(HeaderLocalToken)
		if len(values) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "missing or invalid " + HeaderLocalToken,
			})
			return
		}
		got := values[0]
		gotDigest := sha256.Sum256([]byte(got))
		if subtle.ConstantTimeCompare(gotDigest[:], expectedDigest[:]) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "missing or invalid " + HeaderLocalToken,
			})
			return
		}
		c.Next()
	}
}

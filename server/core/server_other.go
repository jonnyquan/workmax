//go:build !windows
// +build !windows

package core

import (
	"time"

	"github.com/fvbock/endless"
	"github.com/gin-gonic/gin"
)

const (
	serverReadHeaderTimeout = 15 * time.Second
	serverMaxHeaderBytes    = 64 << 10
)

// initServer 初始化服务器
func initServer(address string, router *gin.Engine) server {
	s := endless.NewServer(address, router)
	// Header parsing is never a long-running Agent operation. Keep this short
	// so a slow or oversized unauthenticated request cannot retain a connection
	// for the one-hour execution window reserved for response streaming.
	s.ReadHeaderTimeout = serverReadHeaderTimeout
	// Increased WriteTimeout to 60 minutes to support long-running operations
	// - Agent operations can take 15-30 minutes for complex content creation
	// - Long-form writing and analysis tasks may need extended processing time
	s.WriteTimeout = 3600 * time.Second // 60 minutes (1 hour)
	s.MaxHeaderBytes = serverMaxHeaderBytes

	// Agent message queue removed - using PureLLM approach
	// setupGracefulShutdown() // removed

	return s
}

// setupGracefulShutdown removed - message queue system deleted

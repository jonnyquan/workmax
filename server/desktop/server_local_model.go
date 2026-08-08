//go:build desktop

package desktop

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

const maxModelSettingsBodyBytes = 8 << 10

// handleGetModelRoute returns preferred route + local profile without secrets.
func (s *Server) handleGetModelRoute(c *gin.Context) {
	if s.cfg.ModelSettings == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "model settings unavailable"})
		return
	}
	dto, err := s.cfg.ModelSettings.Get()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "model settings read failed"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, dto)
}

// handlePutModelRoute updates route preference and optional local profile/key.
func (s *Server) handlePutModelRoute(c *gin.Context) {
	if s.cfg.ModelSettings == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "model settings unavailable"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxModelSettingsBodyBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if int64(len(raw)) > maxModelSettingsBodyBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "model settings body too large"})
		return
	}
	in, err := DecodeLocalModelSettingsPut(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid model settings json"})
		return
	}
	dto, err := s.cfg.ModelSettings.Put(in)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, dto)
}

//go:build desktop

package desktop

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

const maxAppearanceSettingsBodyBytes = 1 << 10

// handleGetAppearance returns the machine's appearance preference.
//
// Two callers, and the second is the interesting one: the renderer's Settings
// panel, and the shell itself, which reads this while serving index.html so
// the document arrives with data-theme already on <html>. That is why the
// route takes no identity — see migration 0010 — and why it is no-store: a
// cached answer here is a wrong first frame after the next change.
func (s *Server) handleGetAppearance(c *gin.Context) {
	if s.cfg.UIPreferences == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ui preferences unavailable"})
		return
	}
	dto, err := s.cfg.UIPreferences.Get()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ui preference read failed"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, dto)
}

// handlePutAppearance stores one of the three appearance states.
func (s *Server) handlePutAppearance(c *gin.Context) {
	if s.cfg.UIPreferences == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ui preferences unavailable"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxAppearanceSettingsBodyBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if int64(len(raw)) > maxAppearanceSettingsBodyBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "appearance settings body too large"})
		return
	}
	in, err := DecodeAppearanceSettingsPut(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid appearance settings json"})
		return
	}
	dto, err := s.cfg.UIPreferences.Put(in)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, dto)
}

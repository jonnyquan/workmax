//go:build desktop

package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// /local/accounts — the local half of sign-in.
//
// Remote authorization (cloud OAuth/password) authenticates you to
// workmax.app; these routes manage who you are on THIS machine when no cloud
// session is involved. They never touch the network and require no session —
// only the local token perimeter, like every loopback route.

type localAccountsResponse struct {
	Items []LocalAccount `json:"items"`
	Count int            `json:"count"`
}

func (s *Server) handleListLocalAccounts(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "local_accounts_unavailable"})
		return
	}
	items, err := listLocalAccounts(s.cfg.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "local_accounts_unavailable"})
		return
	}
	c.JSON(http.StatusOK, localAccountsResponse{Items: items, Count: len(items)})
}

const maxLocalAccountBodyBytes = 2 << 10

func (s *Server) handleCreateLocalAccount(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "local_accounts_unavailable"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxLocalAccountBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxLocalAccountBodyBytes || !utf8.Valid(body) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	var payload struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.More() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	account, err := createLocalAccount(s.cfg.DB, payload.Name)
	switch {
	case errors.Is(err, errLocalAccountName):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name"})
	case errors.Is(err, errLocalAccountTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "name_taken"})
	case errors.Is(err, errLocalAccountLimit):
		c.JSON(http.StatusConflict, gin.H{"error": "account_limit"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "local_accounts_unavailable"})
	default:
		c.JSON(http.StatusCreated, account)
	}
}

func (s *Server) handleSelectLocalAccount(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "local_accounts_unavailable"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_account_id"})
		return
	}
	if err := selectLocalAccount(s.cfg.DB, id); err != nil {
		if errors.Is(err, errLocalAccountNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "account_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "local_accounts_unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"selected": true})
}

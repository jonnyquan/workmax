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

func (s *Server) handleRenameLocalAccount(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "local_accounts_unavailable"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_account_id"})
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
	account, err := renameLocalAccount(s.cfg.DB, id, payload.Name)
	switch {
	case errors.Is(err, errLocalAccountName):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name"})
	case errors.Is(err, errLocalAccountTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "name_taken"})
	case errors.Is(err, errLocalAccountNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "account_not_found"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "local_accounts_unavailable"})
	default:
		c.JSON(http.StatusOK, account)
	}
}

// deleteLocalAccountResponse reports what left the machine with the identity.
type deleteLocalAccountResponse struct {
	Deleted  bool  `json:"deleted"`
	Threads  int   `json:"threads"`
	Messages int64 `json:"messages"`
	Files    int   `json:"files"`
}

// DELETE /local/accounts/:id — remove an identity AND everything it owns.
//
// Keeping the data would be worse than deleting it: the uid can never be
// issued again (AUTOINCREMENT), so orphaned rows would be permanently
// unreachable — invisible to every account, undeletable forever, sitting in
// a database the user believes they cleaned. An account delete is therefore
// a full cascade over the identity's threads, reusing the exact per-thread
// purge the thread-delete route runs.
//
// Two refusals, both about consent:
//   - the ACTIVE account cannot be deleted — the renderer switches first, so
//     "delete who I am right now" is never a single ambiguous action;
//   - an account with a turn in flight cannot be deleted — the turn would
//     write into rows this handler is removing.
func (s *Server) handleDeleteLocalAccount(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "local_accounts_unavailable"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_account_id"})
		return
	}
	var active int
	row := s.cfg.DB.Raw(`SELECT is_active FROM w_desktop_local_account WHERE id = ?`, id).Row()
	if err := row.Scan(&active); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "account_not_found"})
		return
	}
	if active == 1 {
		c.JSON(http.StatusConflict, gin.H{"error": "account_active"})
		return
	}
	uid := localAccountUID(id)
	var running int64
	if err := s.cfg.DB.Raw(
		`SELECT COUNT(*) FROM w_desktop_agent_turn_intent
		  WHERE uid = ? AND state IN ('starting','streaming')`, uid,
	).Row().Scan(&running); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "account_delete_failed"})
		return
	}
	if running > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "account_busy"})
		return
	}

	rows, err := s.cfg.DB.Raw(
		`SELECT id, uuid FROM w_workagent_thread WHERE uid = ?`, uid,
	).Rows()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "account_delete_failed"})
		return
	}
	type threadRef struct {
		id   uint64
		uuid string
	}
	var threads []threadRef
	for rows.Next() {
		var ref threadRef
		if err := rows.Scan(&ref.id, &ref.uuid); err != nil {
			rows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "account_delete_failed"})
			return
		}
		threads = append(threads, ref)
	}
	rows.Close()

	response := deleteLocalAccountResponse{Deleted: true, Threads: len(threads)}
	for _, ref := range threads {
		purge, err := s.purgeLocalThread(uid, ref.id, ref.uuid)
		if err != nil {
			// Stop honestly: some threads are gone, this one is not, and the
			// account row still exists so nothing became unreachable.
			c.JSON(http.StatusInternalServerError, gin.H{"error": "account_delete_failed"})
			return
		}
		response.Messages += purge.messages
		response.Files += purge.files
	}
	// Leftover sweep: rows keyed by uid that no thread cascade touched
	// (orphans from crashes, messages whose thread row was already gone).
	if err := s.cfg.DB.Exec(`DELETE FROM w_workagent_message WHERE uid = ?`, uid).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "account_delete_failed"})
		return
	}
	if err := s.cfg.DB.Exec(`DELETE FROM w_desktop_agent_turn_intent WHERE uid = ?`, uid).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "account_delete_failed"})
		return
	}
	if err := s.cfg.DB.Exec(`DELETE FROM w_desktop_local_account WHERE id = ?`, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "account_delete_failed"})
		return
	}
	c.JSON(http.StatusOK, response)
}

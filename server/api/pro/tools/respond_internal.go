package tools

import (
	"fmt"

	"server/globals"
	"server/model/common/response"
	"server/utils"

	"github.com/gin-gonic/gin"
)

// respondInternal logs the underlying error with operational
// context (HTTP method + path + uid) and returns a stable user-
// facing message that does NOT leak driver / network / filesystem
// detail.
//
// Why: prior convention was
//
//	response.FailWithMessage("Update failed: "+err.Error(), c)
//
// which surfaces error tails like "Error 1062: Duplicate entry
// '...' for key 'foo'" or "dial tcp [::1]:3306: connect: connection
// refused" directly to the API consumer. That's a small but real
// info-disclosure surface — driver internals, schema fragments,
// network topology — and it grows fast across hundreds of CRUD
// sites.
//
// The helper splits the two concerns: ops gets the full error in
// the log (with enough context to find the request), the caller
// gets a stable phrase like "Update failed". Validation errors
// (bind failures) are NOT routed through here — those still want
// the field-level detail.
//
// Usage:
//
//	if err := db.Create(&row).Error; err != nil {
//	    respondInternal(c, "Create failed", err)
//	    return
//	}
//
// Cross-cutting handlers (canvas / character / brand / product)
// follow the same convention. New handlers should reach for this
// helper instead of inlining +err.Error().
func respondInternal(c *gin.Context, userMessage string, err error) {
	uid := utils.GetUserID(c)
	globals.Error(fmt.Sprintf(
		"[%s %s uid=%d] %s: %v",
		c.Request.Method,
		c.Request.URL.Path,
		uid,
		userMessage,
		err,
	))
	response.FailWithMessage(userMessage, c)
}

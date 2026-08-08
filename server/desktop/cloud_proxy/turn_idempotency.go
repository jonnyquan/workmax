//go:build desktop

package cloud_proxy

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const desktopTurnRequestIDPrefix = "desktop-turn:"

// DesktopTurnRequestID validates the renderer-generated stable turn UUID and
// returns the exact legacy Cloud idempotency key. The value is safe for the
// X-Agent-Request-Id header and remains well below the Cloud's 128-byte cap.
func DesktopTurnRequestID(turnUUID string) (string, error) {
	if turnUUID == "" || strings.TrimSpace(turnUUID) != turnUUID {
		return "", fmt.Errorf("turn uuid is required")
	}
	parsed, err := uuid.Parse(turnUUID)
	if err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 || parsed.String() != turnUUID {
		return "", fmt.Errorf("turn uuid must be canonical RFC 4122 v4")
	}
	requestID := desktopTurnRequestIDPrefix + turnUUID
	if len(requestID) > 128 {
		return "", fmt.Errorf("turn request id exceeds 128 bytes")
	}
	return requestID, nil
}

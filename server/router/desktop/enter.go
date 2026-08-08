// Package desktop wires the /api/desktop/* HTTP routes onto Gin
// groups. Mirrors server/router/admin/.
package desktop

// RouterGroup composes the individual desktop route registrars so
// server/router/enter.go can reference a single `Desktop` field.
type RouterGroup struct {
	DesktopAgentRouter
	DesktopLoginRouter
	DesktopOauthRouter
	DesktopSyncRouter
	DesktopVersionRouter
}

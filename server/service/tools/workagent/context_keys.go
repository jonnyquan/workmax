package workagent

import (
	"context"
	"fmt"
)

// ctxKey is a private type used to scope context.Value keys to this package
// so they can't collide with keys defined elsewhere. Using a string literal
// like "user_id" triggers a `go vet` warning and is silently overwritable
// by any other package that happens to choose the same name.
type ctxKey int

const (
	userIDKey ctxKey = iota
	threadIDKey
	projectIDKey
)

// WithUserID returns a context that carries the requesting user's ID.
// Use this in handlers that pass ctx down into the agent pipeline so
// downstream code (error notifier, monitoring) can attribute work.
func WithUserID(ctx context.Context, uid uint) context.Context {
	return context.WithValue(ctx, userIDKey, uid)
}

// UserIDFromContext extracts the requesting user's ID from ctx.
// Returns (0, false) if the key is missing or the value isn't a uint —
// callers should treat that as an unauthenticated/unknown context rather
// than logging "%!d(<nil>)" into an email subject.
func UserIDFromContext(ctx context.Context) (uint, bool) {
	v, ok := ctx.Value(userIDKey).(uint)
	return v, ok
}

// UserIDStringFromContext returns the requesting user's ID as a decimal
// string for log/email fields. Falls back to "unknown" when the value
// is missing instead of leaking a Sprintf-of-nil ("%!d(<nil>)").
func UserIDStringFromContext(ctx context.Context) string {
	if uid, ok := UserIDFromContext(ctx); ok {
		return fmt.Sprintf("%d", uid)
	}
	return "unknown"
}

// WithThreadID returns a context that carries the numeric thread row
// ID (workagent_thread.id, not the UUID). Used by the production
// tool SDK bridge so tool Handlers can stamp chat_message.metadata
// against the right thread without an extra plumbed argument.
func WithThreadID(ctx context.Context, threadID uint) context.Context {
	return context.WithValue(ctx, threadIDKey, threadID)
}

// ThreadIDFromContext extracts the numeric thread ID from ctx.
// Returns (0, false) when the key is missing — callers treat that
// as "untrackable" and skip per-thread persistence rather than
// stamping into a zero-row.
func ThreadIDFromContext(ctx context.Context) (uint, bool) {
	v, ok := ctx.Value(threadIDKey).(uint)
	return v, ok
}

// WithProjectID returns a context that carries the thread's bound
// project id (Plan-A Phase A3). Set by setupAgentContext when the
// turn starts; consumed by project-aware production tools
// (lookup_asset and future helpers) so they auto-scope to the
// thread's project without the agent having to repeat the id on
// every call.
//
// Zero (no project bound) is a valid stamp — the production tools
// treat it as "no project scope," same as if the ctx value was
// missing. Stamping zero is fine; it just means "this thread
// isn't project-bound, don't auto-scope."
func WithProjectID(ctx context.Context, projectID uint) context.Context {
	return context.WithValue(ctx, projectIDKey, projectID)
}

// ProjectIDFromContext extracts the thread's bound project id
// from ctx. Returns (0, false) when the key is missing or the
// value isn't a uint — callers fall back to "no project scope,"
// matching the projectID=0 semantic across the repo layer's
// project-aware helpers.
func ProjectIDFromContext(ctx context.Context) (uint, bool) {
	v, ok := ctx.Value(projectIDKey).(uint)
	return v, ok
}

package workagent

import "testing"

// stubSurface is a minimal AgentSurface used by registry tests so we
// don't depend on a real surface implementation (which would create
// import cycles or pull half the world into this package's tests).
type stubSurface struct {
	tool string
	mode string
	typ  string
}

func (s stubSurface) Tool() string                  { return s.tool }
func (s stubSurface) AgentMode() string             { return s.mode }
func (s stubSurface) AgentType() string             { return s.typ }
func (s stubSurface) IdempotencyHeaderName() string { return "X-Stub-Request-Id" }
func (s stubSurface) IdempotencyKeyPrefix() string  { return "" }

func TestSurfaceRegistry_RegisterAndLookup(t *testing.T) {
	// Pin the basic round-trip: register under AgentMode key, look up
	// returns the same instance (interface-equal since stubSurface is
	// a value type, identity comparison would fail; check methods).
	const mode = "registry-test-mode-1"
	stub := stubSurface{tool: "tool-1", mode: mode, typ: "type-1"}
	RegisterSurface(stub)

	got := LookupSurface(mode)
	if got == nil {
		t.Fatal("LookupSurface returned nil after Register")
	}
	if got.Tool() != "tool-1" {
		t.Errorf("Tool() = %q, want %q", got.Tool(), "tool-1")
	}
	if got.AgentMode() != mode {
		t.Errorf("AgentMode() = %q, want %q", got.AgentMode(), mode)
	}
	if got.AgentType() != "type-1" {
		t.Errorf("AgentType() = %q, want %q", got.AgentType(), "type-1")
	}
}

func TestSurfaceRegistry_LookupMissingReturnsNil(t *testing.T) {
	// Pin the nil contract: callers MUST nil-check the return so an
	// unregistered mode doesn't panic-on-method-call. Use a mode
	// string that no production surface would ever register.
	got := LookupSurface("__definitely_not_registered__")
	if got != nil {
		t.Errorf("LookupSurface for unknown mode = %v, want nil", got)
	}
}

func TestSurfaceRegistry_RegisterOverwrites(t *testing.T) {
	// Re-registering under the same mode silently overwrites — the
	// production callers never do this, but tests use it to swap in
	// mocks. Pin the contract so a future "panic on dup" change is a
	// deliberate decision rather than a silent regression.
	const mode = "registry-test-mode-2"
	first := stubSurface{tool: "first", mode: mode, typ: "type-2"}
	second := stubSurface{tool: "second", mode: mode, typ: "type-2"}

	RegisterSurface(first)
	RegisterSurface(second)

	got := LookupSurface(mode)
	if got == nil {
		t.Fatal("LookupSurface returned nil after re-register")
	}
	if got.Tool() != "second" {
		t.Errorf("Tool() = %q, want %q (re-register should overwrite)", got.Tool(), "second")
	}
}

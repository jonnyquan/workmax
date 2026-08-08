package skills

import (
	"testing"
)

// isolatePerSkillCaches makes a cache-observation test independent from both
// earlier tests and earlier -count iterations. identityCache is the outer
// cache: when it contains name, Loader.Build intentionally never reaches
// readSkillBody, so clearing skillBodyCache alone cannot create a cold Build.
//
// Preserve and restore the individual entries instead of replacing either
// process-wide map. That keeps the helper safe for the rest of the test
// process and avoids discarding unrelated cached skills.
func isolatePerSkillCaches(t *testing.T, name string) {
	t.Helper()

	identityCacheMu.Lock()
	skillBodyCacheMu.Lock()
	identity, hadIdentity := identityCache[name]
	body, hadBody := skillBodyCache[name]
	delete(identityCache, name)
	delete(skillBodyCache, name)
	skillBodyCacheMu.Unlock()
	identityCacheMu.Unlock()

	t.Cleanup(func() {
		identityCacheMu.Lock()
		skillBodyCacheMu.Lock()
		if hadIdentity {
			identityCache[name] = identity
		} else {
			delete(identityCache, name)
		}
		if hadBody {
			skillBodyCache[name] = body
		} else {
			delete(skillBodyCache, name)
		}
		skillBodyCacheMu.Unlock()
		identityCacheMu.Unlock()
	})
}

// loader_cache_test.go — G1 (2026-05-17). Pins that the per-skill
// caches added to buildIdentity / readSkillBody actually short-
// circuit on the second call. The benefit only shows up when
// Build is called multiple times for the same skill in the same
// process — pre-G1 each call did 5 embed.FS reads + string
// concatenation; post-G1 it's a map lookup.
//
// We can't assert "no file read" directly without rewriting the
// loader to take an instrumented FS, but we CAN assert "the cache
// map has an entry" — which is the load-bearing invariant. If the
// cache stops populating, every Build does the full work again.

// TestBuildIdentity_PopulatesCacheOnFirstCall confirms that a
// fresh Build inserts an entry into identityCache. Future Build
// calls for the same skill should hit that entry.
func TestBuildIdentity_PopulatesCacheOnFirstCall(t *testing.T) {
	// Fresh registry — its own loader, but identityCache is package-
	// global so we don't get isolation. Use a skill name unlikely
	// to be touched by other tests in the same package.
	const skill = "ppt"

	// Drop and later restore this skill's dependent cache entries so the test
	// observes a real absent → present transition without leaking state.
	isolatePerSkillCaches(t, skill)

	r := NewRegistry(nil)
	if _, err := r.loader.Build(skill, BuildContext{}); err != nil {
		t.Fatalf("first build: %v", err)
	}

	identityCacheMu.Lock()
	_, present := identityCache[skill]
	identityCacheMu.Unlock()
	if !present {
		t.Errorf("identityCache[%q] should be populated after first Build", skill)
	}
}

// TestReadSkillBody_PopulatesCacheOnFirstCall — same shape for
// the skillBodyCache. SKILL.md embed.FS reads are the bigger
// per-call cost (each file is several KB); pinning the cache
// is what guarantees subsequent Build calls don't re-read.
func TestReadSkillBody_PopulatesCacheOnFirstCall(t *testing.T) {
	const skill = "character"

	// A cold body-cache test also needs a cold outer identity cache; otherwise
	// Build can short-circuit before readSkillBody is called.
	isolatePerSkillCaches(t, skill)

	r := NewRegistry(nil)
	if _, err := r.loader.Build(skill, BuildContext{}); err != nil {
		t.Fatalf("first build: %v", err)
	}

	skillBodyCacheMu.Lock()
	body, present := skillBodyCache[skill]
	skillBodyCacheMu.Unlock()
	if !present {
		t.Errorf("skillBodyCache[%q] should be populated after first Build", skill)
	}
	if body == "" {
		t.Errorf("cached body for %q should not be empty", skill)
	}
}

// TestBuild_RepeatedCallsReturnSameIdentity — second Build for
// the same skill returns the cached identity (same bytes). This
// pins the "deterministic across calls" contract that downstream
// SDK prompt-caching relies on.
func TestBuild_RepeatedCallsReturnSameIdentity(t *testing.T) {
	r := NewRegistry(nil)
	first, err := r.loader.Build("flashCard", BuildContext{})
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	second, err := r.loader.Build("flashCard", BuildContext{})
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if first.SystemPrompt != second.SystemPrompt {
		t.Errorf("repeated Build for same skill+ctx should produce byte-identical SystemPrompt")
	}
}

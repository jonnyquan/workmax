// Package detectors implements the M5 pre-emit / post-emit checklist
// gate's pluggable detector interface.
//
// A Detector is a deterministic, fast (typically <100ms) check on a
// generated artifact. The gate runs all enabled detectors against an
// emitted artifact and aggregates pass/fail/skipped results into a
// block-or-allow decision.
//
// Design contract:
//
//   - Detectors are STATELESS. The interface returns a fresh result
//     per call; concurrent calls against the same instance are safe.
//   - Detectors are deterministic. Same input → same output. The
//     gate caches results by (artifact-hash, detector-name) and a
//     non-deterministic detector would poison the cache.
//   - Detectors don't make network calls or LLM invocations. The
//     point of the gate's first stage is to be cheap; expensive
//     critique work happens in M3 critique sub-agent (Sprint B).
//   - Failures degrade to "skipped" with a reason, never crash. A
//     gate that throws is worse than a gate that didn't run — it
//     blocks legitimate output.
//
// New detectors register themselves into the package-level Registry
// at init() time (or via a build-tag-gated init for optional ones).
// The gate (PR-9, separate file) reads the registry by name from
// each skill's references/checklist.md.
package detectors

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Status enumerates a detector's verdict on the artifact it inspected.
type Status string

const (
	// StatusPass — detector ran cleanly, no issues found. Most common
	// outcome for healthy output.
	StatusPass Status = "pass"

	// StatusFail — detector ran cleanly and found at least one issue.
	// The Issues slice carries human-readable descriptions; the gate
	// folds these into the block reason returned to the caller.
	StatusFail Status = "fail"

	// StatusSkipped — detector decided not to run for this artifact
	// (missing prerequisite, e.g. brand-spec.md absent or no images
	// to compare). Skipped is NOT a failure: the gate treats it as
	// "no opinion" and lets other detectors decide.
	StatusSkipped Status = "skipped"
)

// Input is the slice of state every detector receives. Kept narrow on
// purpose — detectors shouldn't need DB access or thread metadata to
// do their job. Adding fields here means every detector implementation
// has to acknowledge them; resist scope creep.
type Input struct {
	// SkillName is the agent_mode the artifact came from. Detectors
	// can use this to short-circuit (e.g. character_anchor_consistency
	// returns Skipped if SkillName != "character").
	SkillName string

	// Artifact carries the generated output. Detectors that scan
	// text use Artifact.Text; detectors that need files (pptx, png,
	// mp4) read from Artifact.Files. Both may be empty if the agent
	// produced neither (rare but possible — gate should still run).
	Artifact Artifact

	// ThreadDir is the agent's working directory for this turn.
	// Detectors can stat / read brand-spec.md, character-spec.md,
	// or other thread-local context here. Empty when the gate runs
	// outside a real thread (testing).
	ThreadDir string

	// Locale carries the user's preferred language. Most detectors
	// don't need this, but regex_scanner uses it to pick the right
	// language-specific dictionary (the honest_data scanner has
	// different "fabricated metric" patterns per language).
	Locale string
}

// Artifact wraps the generated output the detector inspects. Two
// fields cover the cases we care about: text-only output (markdown,
// HTML, JSON) and file-based output (pptx, image, mp4).
//
// A detector that wants the textual content of a file must read it
// itself — the gate doesn't pre-load file bytes (most detectors only
// need the path to stat-and-skip, and pre-loading every artifact
// would balloon memory for image / video output).
type Artifact struct {
	// Text is the raw output the agent emitted as message content.
	// May be empty if the agent only produced files this turn.
	Text string

	// Files lists absolute paths inside ThreadDir that the agent
	// wrote during this turn. Format-specific detectors (e.g. a
	// future contrast_analyzer that opens the .pptx) use this list.
	Files []string
}

// Result is the detector's verdict + supporting evidence. The gate
// folds this into the block / warn / pass decision.
type Result struct {
	// Status is the verdict (pass / fail / skipped). Always set.
	Status Status

	// Issues is a list of human-readable problem descriptions.
	// Empty for pass / skipped. For fail, each entry is one
	// distinct issue (the gate may surface them all in the user-
	// facing block reason; a checklist that aggregates 100 issues
	// is a sign the detector should be tightened, not the gate).
	Issues []string

	// Trace is optional structured context for debugging. Lands in
	// the structured log line so an operator can see WHY a detector
	// fired without re-running it. Don't put PII here.
	Trace map[string]any
}

// Pass returns a fresh pass Result. Convenience constructor.
func Pass() Result { return Result{Status: StatusPass} }

// Fail returns a fail Result with the supplied issues.
func Fail(issues ...string) Result {
	return Result{Status: StatusFail, Issues: append([]string(nil), issues...)}
}

// Skipped returns a skipped Result with a single reason.
func Skipped(reason string) Result {
	return Result{Status: StatusSkipped, Issues: []string{reason}}
}

// Detector is the interface every check implements.
type Detector interface {
	// Name returns the registry-stable identifier. Must match the
	// detector name in skill checklist.md files. Conventionally
	// lower_snake_case (e.g. "honest_data", "brand_spec_grep").
	Name() string

	// Run executes the check. Implementations must:
	//   - Honor ctx cancellation (return Skipped + reason on cancel).
	//   - Complete in <100ms for the artifact sizes we ship today.
	//   - Be safe under concurrent invocation (stateless).
	//
	// A detector that returns an error has misbehaved — the registry
	// translates errors into Skipped results so the gate doesn't
	// crash, but the operator should treat a non-nil error here as
	// a bug to fix.
	Run(ctx context.Context, in Input) (Result, error)
}

// Registry is a name → Detector map with goroutine-safe access. The
// package-level Default() registry is populated by init() functions
// in each detector's source file, so the dispatcher (PR-9) can ask
// the registry for a detector by name without hard-coding the list.
//
// A thread-safe map (sync.Map) would also work, but RWMutex + a plain
// map is simpler to reason about for a registry that's read-heavy
// and write-only-at-startup.
type Registry struct {
	mu sync.RWMutex
	m  map[string]Detector
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{m: make(map[string]Detector)}
}

// Register adds a detector to the registry. Panics if a detector
// with the same name is already registered — duplicate registration
// is a build-time bug (two init() functions claiming the same name),
// which we want loud at startup, not silently overriding.
func (r *Registry) Register(d Detector) {
	if d == nil {
		panic("detectors: Register called with nil Detector")
	}
	name := d.Name()
	if name == "" {
		panic("detectors: Register called with empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.m[name]; exists {
		panic(fmt.Sprintf("detectors: duplicate registration for %q", name))
	}
	r.m[name] = d
}

// Get returns the detector with the supplied name. The second value
// is false if no detector by that name is registered — gate code
// should treat a missing detector as a checklist authoring error
// (the skill's checklist.md references a name no detector implements)
// and surface it loudly.
func (r *Registry) Get(name string) (Detector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.m[name]
	return d, ok
}

// Names returns the registered detector names in alphabetical order.
// Useful for /debug endpoints, CI smoke tests, and the startup log
// line that surfaces "what's plugged in today".
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.m))
	for k := range r.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// defaultRegistry is the package-level singleton. Detector source
// files register themselves here in their init(). Tests that need
// an isolated registry should construct their own via NewRegistry().
var defaultRegistry = NewRegistry()

// Default returns the package-level singleton. Detector init
// functions register against this.
func Default() *Registry { return defaultRegistry }

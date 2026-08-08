package skills

import (
	"fmt"
	"strings"
	"sync"
)

// SkillBundle is the runtime-ready output of Loader.Build. It carries
// the fully-rendered system prompt the SDK needs, plus metadata the
// rest of work-agent might consume (RequiredInputs for clarifying
// questions, PostScripts for OnDone hooks).
type SkillBundle struct {
	Name           string
	Version        string
	SystemPrompt   string
	RequiredInputs []InputRequirement
	PostScripts    []ScriptDescriptor
	Artifacts      *ArtifactMetadata

	// QuestionForm is the M1 turn-1 discovery form schema, if the
	// skill declares one. Nil when the skill omits the
	// `question_form` block in skill.yaml — runtime falls through
	// to the SDK normally in that case. See manifest.go QuestionForm
	// for the schema and the dispatcher for the runtime contract.
	QuestionForm *QuestionForm

	// DirectionsFallback is the M2 visual directions picker
	// config. Nil when the skill omits the `directions_fallback`
	// block. See manifest.go DirectionsFallback + v2.4 §M2.
	DirectionsFallback *DirectionsFallback

	// SideFiles is the DS-2 / DS-5 pre-flight payload — the contents
	// of the skill's `assets/` directory, ready to be wrapped into
	// the SystemAdditions <skill-side-files> XML block before SDK
	// invocation. Populated by LoadSideFiles (separate entry point)
	// rather than during Build, because side-file injection is an
	// opt-in feature gated by config.WorkAgentFeatures.
	// SkillPreflightEnabled. Build's path stays free of asset I/O
	// for callers that don't need preflight.
	SideFiles []SideFile
}

// SideFile is one (path, contents) pair from a skill's assets/ tree.
// Path is the filename relative to assets/ (e.g. "template-shell.md");
// the gate / preflight composer wraps it in
// <asset path="template-shell.md">...</asset>.
//
// Limited to assets/* by design — references/ has its own injection
// path through the identity build (see buildIdentity), and the
// scripts/ tree is reserved for post-generation validators.
type SideFile struct {
	Path     string
	Contents string
}

// BuildContext is what the runtime knows at the moment a skill is
// being bundled for a turn. Mirrors the legacy
// SystemPromptRegistry.GetSystemPrompt(mode, hasFiles, fileContext)
// signature so the call sites translate one-to-one.
//
// SystemAdditions is the Sprint-A onwards opt-in for injecting the
// _shared / discovery / design-system / skill-side-files / checklist-
// digest layers into the framework's tail. Empty string preserves
// pre-Sprint-A behaviour byte-for-byte (the renderer's TrimRight
// strips the placeholder cleanly). Callers compose the value via
// prompts.SystemAdditionsComposer before calling Build.
type BuildContext struct {
	HasFiles        bool
	FileContext     string
	SystemAdditions string
}

// Loader bundles skill assets into a ready-to-send SkillBundle. Stateless
// past construction; safe to share between goroutines (the embed.FS
// reads + the cached legacy-template lookups are read-only).
type Loader struct {
	legacy *legacyTemplates // resolves Legacy.InheritsIdentity / InheritsOutput
}

// NewLoader returns a Loader wired to the legacy template store. Pass
// nil legacyFallback if the caller doesn't want to support the
// migration shim (e.g. tests that only exercise fully-migrated
// skills). Production wires the real legacy reader so unmigrated
// modes keep working byte-identically.
func NewLoader(legacyFallback LegacyTemplateProvider) *Loader {
	if legacyFallback == nil {
		legacyFallback = noopLegacyProvider{}
	}
	return &Loader{
		legacy: &legacyTemplates{provider: legacyFallback},
	}
}

// Build resolves the skill named `name` and produces a bundle for the
// current request.
//
// Dispatch:
//
//  1. Try skills/<name>/skill.yaml. If present + Manifest.Legacy is
//     unset → render from the skill's own SKILL.md + references.
//  2. If the manifest has a Legacy block → synthesize the body from
//     legacy templates.go (identity_X + output_format_X), then wrap
//     in the framework template the same way the old loader did.
//  3. If no skill.yaml exists at all → return an error. All
//     production modes have graduated to explicit official bundles;
//     unknown names should not silently synthesize a prompt.
//
// Cases (1) and (2) inject the four `_shared` references when the
// manifest declares them. Registry.GetSystemPromptWithAdditions owns
// the user-facing fallback to ppt for misconfigured/unknown modes.
func (l *Loader) Build(name string, ctx BuildContext) (*SkillBundle, error) {
	manifest, manifestErr := l.tryReadManifest(name)
	if manifestErr != nil {
		return nil, manifestErr
	}

	if manifest == nil {
		return nil, fmt.Errorf("skill %q: skill.yaml missing", name)
	}

	// Case 1 or 2: manifest exists. Build the ModeIdentity slot
	// (= horizontal _shared overlay + skill body) and the
	// OutputFormat slot, then wrap.
	identity, err := l.buildIdentity(name, manifest)
	if err != nil {
		return nil, err
	}
	output, err := l.buildOutputFormat(name, manifest)
	if err != nil {
		return nil, err
	}

	body, err := l.legacy.renderFrameworkWithSlots(identity, output, ctx)
	if err != nil {
		return nil, err
	}

	return &SkillBundle{
		Name:               manifest.Name,
		Version:            manifest.Version,
		SystemPrompt:       body,
		RequiredInputs:     manifest.RequiredInputs,
		PostScripts:        manifest.Scripts,
		Artifacts:          manifest.Artifacts,
		QuestionForm:       manifest.QuestionForm,
		DirectionsFallback: manifest.DirectionsFallback,
	}, nil
}

// manifestCache memoizes parsed manifests across Build calls. embed.FS
// is immutable for the process lifetime, so a single read+parse per
// skill is enough.
var (
	manifestCache   = map[string]*Manifest{}
	manifestCacheMu sync.Mutex
)

func (l *Loader) tryReadManifest(name string) (*Manifest, error) {
	manifestCacheMu.Lock()
	defer manifestCacheMu.Unlock()
	if cached, ok := manifestCache[name]; ok {
		return cached, nil
	}

	data, err := skillsFS.ReadFile(name + "/skill.yaml")
	if err != nil {
		// Treat any read error (most commonly "file does not exist")
		// as "no manifest" rather than a hard failure. Build turns
		// nil into a clear unknown-skill error.
		manifestCache[name] = nil
		return nil, nil
	}
	m, err := parseManifest(data)
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", name, err)
	}
	if err := validateManifestForSkillDir(name, m); err != nil {
		return nil, fmt.Errorf("skill %q: skill.yaml: %w", name, err)
	}
	manifestCache[name] = m
	return m, nil
}

// identityCache memoizes buildIdentity's full rendered output per
// skill name. Result is a deterministic function of:
//   - the manifest (parsed once, cached at manifestCache)
//   - the SKILL.md body (now also cached at skillBodyCache below)
//   - the four _shared references (cached individually at sharedCache)
//   - the local references (cached at localCache)
//   - the required-inputs checklist (pure function of manifest)
//
// All inputs are process-stable (embed.FS is immutable), so the
// result is too. G1 (2026-05-17) — pre-cache, every turn redid 4
// shared-ref string concatenations + 1 SKILL.md ReadFile + 1
// renderRequiredInputs render. Five Build calls per turn made that
// 5× wasted work. Caching here drops the cost to one-time-per-skill.
var (
	identityCache   = map[string]string{}
	identityCacheMu sync.Mutex
)

// buildIdentity assembles the ModeIdentity slot. Order matters:
//   - horizontal _shared overlays come first (general posture rules
//     that the model should read before mode-specific behavior)
//   - then the skill's own body (SKILL.md or legacy-inherited
//     identity_<X>.md for pre-graduation manifests)
//   - then any local references the skill declares
//   - then the required-inputs checklist (last so the model sees
//     it right before user content)
//
// Each block is separated by a blank-line + `---` so the model sees
// a clean section boundary instead of one long wall of text.
//
// Process-cached per-skill (see identityCache above). The cache is
// keyed by skill name; embed.FS immutability means a single render
// suffices for the process lifetime.
func (l *Loader) buildIdentity(name string, m *Manifest) (string, error) {
	identityCacheMu.Lock()
	if cached, ok := identityCache[name]; ok {
		identityCacheMu.Unlock()
		return cached, nil
	}
	identityCacheMu.Unlock()

	var blocks []string

	for _, ref := range m.References.Shared {
		body, err := readSharedReference(ref)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, body)
	}

	body, err := l.readSkillBody(name, m)
	if err != nil {
		return "", err
	}
	blocks = append(blocks, body)

	for _, ref := range m.References.Local {
		local, err := readLocalReference(name, ref)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, local)
	}

	if checklist := renderRequiredInputs(m.RequiredInputs); checklist != "" {
		blocks = append(blocks, checklist)
	}

	rendered := strings.Join(blocks, "\n\n---\n\n")

	identityCacheMu.Lock()
	identityCache[name] = rendered
	identityCacheMu.Unlock()
	return rendered, nil
}

// renderRequiredInputs turns the manifest's RequiredInputs into a
// markdown checklist the model reads as part of its system prompt.
// Returns "" when the slice is empty so the caller can append
// unconditionally without producing trailing horizontal rules.
//
// We deliberately render this as a "checklist + ask if missing"
// instruction rather than a hard precondition the Go code enforces:
// keeping the reflection in the prompt is the Skill-first contract
// (behavior lives in markdown, Go is plumbing). A future iteration
// can layer Go-side validation on top, but starting at the prompt
// level lets the agent handle ambiguity (e.g. "the user uploaded a
// brand kit so brand_assets is implicitly satisfied") without
// hard-coding NLP heuristics on the server.
func renderRequiredInputs(reqs []InputRequirement) string {
	if len(reqs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Required Inputs Checklist\n\n")
	b.WriteString("Before generating, verify the user has provided each item below. If any are missing — and the gate condition (when applicable) is met — STOP and ask the user, **once, in a single batched message**. Do not guess substitutes; do not generate partial output then ask. Don't ask about items the user has already supplied (in the message, in the structured metadata, or via attached files).\n\n")
	for _, req := range reqs {
		if req.When != "" {
			fmt.Fprintf(&b, "- **%s** (when `%s`)\n", req.Kind, req.When)
		} else {
			fmt.Fprintf(&b, "- **%s**\n", req.Kind)
		}
	}
	b.WriteString("\nIf the checklist is satisfied, proceed without prompting.")
	return b.String()
}

// skillBodyCache memoizes readSkillBody output per skill name.
// Embed.FS is immutable for the process lifetime so the cache key
// is just `name`; legacy-fallback bodies route through l.legacy
// (which has its own caching) so this cache only covers the
// embed.FS read path. G1 (2026-05-17) — pre-cache, this read fired
// 5× per turn (once per Build call).
var (
	skillBodyCache   = map[string]string{}
	skillBodyCacheMu sync.Mutex
)

// readSkillBody returns the skill's main body. Resolution order:
//
//  1. Legacy inherits — when the manifest's Legacy block points
//     at identity_<X>.md, that file is the body. Used by the 24
//     non-graduated modes during migration.
//  2. Embedded official SKILL.md — fully-native skills' canonical
//     content, baked into the binary at build time.
//
// The embed.FS read path is process-cached (see skillBodyCache
// above). The legacy path is not cached here because l.legacy
// (LegacyTemplateProvider) owns its own caching.
func (l *Loader) readSkillBody(name string, m *Manifest) (string, error) {
	if m.Legacy != nil && m.Legacy.InheritsIdentity != "" {
		// Strip the "identity_" prefix because the legacy provider
		// keys by mode name, not file basename.
		mode := strings.TrimPrefix(m.Legacy.InheritsIdentity, "identity_")
		return l.legacy.identity(mode), nil
	}

	skillBodyCacheMu.Lock()
	if cached, ok := skillBodyCache[name]; ok {
		skillBodyCacheMu.Unlock()
		return cached, nil
	}
	skillBodyCacheMu.Unlock()

	data, err := skillsFS.ReadFile(name + "/SKILL.md")
	if err != nil {
		return "", fmt.Errorf("skill %q: SKILL.md missing and no legacy fallback: %w", name, err)
	}
	body := string(data)

	skillBodyCacheMu.Lock()
	skillBodyCache[name] = body
	skillBodyCacheMu.Unlock()
	return body, nil
}

// buildOutputFormat resolves the OutputFormat slot. Fully-graduated
// skills (skill.yaml without a Legacy block) declare their own output
// rules inline in SKILL.md — the framework template's
// {{.OutputFormat}} placeholder then renders empty, which the
// surrounding `---` separators tolerate cleanly. Legacy-inheriting
// skills (none left after Stage D except the removed writer mode)
// still pull from prompts/templates/output_format_<X>.md.
func (l *Loader) buildOutputFormat(name string, m *Manifest) (string, error) {
	if m.Legacy != nil && m.Legacy.InheritsOutput != "" {
		mode := strings.TrimPrefix(m.Legacy.InheritsOutput, "output_format_")
		return l.legacy.outputFormat(mode), nil
	}
	// SKILL.md is the source of truth for output guidance on
	// graduated skills; nothing to inject at this slot.
	return "", nil
}

// Reference readers — small, separate, cached individually so a
// missing _shared file panics fast at the bad skill, not at every
// Build call.
var (
	sharedCache   = map[string]string{}
	sharedCacheMu sync.Mutex

	localCache   = map[string]string{}
	localCacheMu sync.Mutex
)

func readSharedReference(name string) (string, error) {
	sharedCacheMu.Lock()
	defer sharedCacheMu.Unlock()
	if cached, ok := sharedCache[name]; ok {
		return cached, nil
	}
	data, err := skillsFS.ReadFile("_shared/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("shared reference %q: %w", name, err)
	}
	sharedCache[name] = string(data)
	return sharedCache[name], nil
}

func readLocalReference(skill, name string) (string, error) {
	key := skill + "/" + name
	localCacheMu.Lock()
	defer localCacheMu.Unlock()
	if cached, ok := localCache[key]; ok {
		return cached, nil
	}
	data, err := skillsFS.ReadFile(skill + "/references/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("local reference %q in skill %q: %w", name, skill, err)
	}
	localCache[key] = string(data)
	return localCache[key], nil
}

// ReadScript returns the raw bytes of a script file embedded inside a
// skill bundle. relativePath is relative to the skill directory
// (e.g. "scripts/validate-pptx.py"). The caller is responsible for
// extracting these bytes to a tmpfile if they need to exec the
// script — embed.FS files cannot be executed directly. Used by the
// workagent runtime's skill_validator to materialise embedded
// validators on first use.
//
// Returning the bytes (not a path) keeps the file system layout an
// implementation detail of this package — callers can't grow a
// dependency on "skills/X/Y exists at path P".
func ReadScript(skillName, relativePath string) ([]byte, error) {
	return skillsFS.ReadFile(skillName + "/" + relativePath)
}

// ReadChecklist returns the embedded references/checklist.md bytes
// for the named skill, or an os.ErrNotExist-wrapping error when the
// skill hasn't authored one. Wraps the embed.FS read so callers
// outside this package don't have to know the on-disk layout (the
// embed root differs between dev / packaged binary). Sprint-A's
// checklist gate dispatcher takes this path.
//
// Returns (nil, nil) when the file genuinely doesn't exist (treated
// as "no checklist authored"). All other errors propagate so callers
// can distinguish "missing" from "corrupt embed".
func ReadChecklist(skillName string) ([]byte, error) {
	data, err := skillsFS.ReadFile(skillName + "/references/checklist.md")
	if err != nil {
		// embed.FS returns *fs.PathError with ErrNotExist for
		// missing files — caller wants this as a clean nil/nil
		// signal.
		return nil, err
	}
	return data, nil
}

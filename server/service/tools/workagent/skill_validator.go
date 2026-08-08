package workagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"server/globals"
	"server/service/tools/workagent/skills"
)

// SkillValidationResult is the parsed shape of an external script's
// stdout. Mirrors the JSON the Python validator emits — keep the
// fields aligned with skills/<skill>/scripts/<*>.py output schemas.
//
// Status values:
//   - "ok"      : nothing wrong worth reporting
//   - "warning" : non-blocking issues (placeholder text, empty slide)
//   - "error"   : structural failure (corrupt zip, 0 slides, missing
//                 required openxml entry)
//   - "skipped" : runner couldn't execute (python missing / timeout
//                 / etc); not the validator's verdict, but ours
type SkillValidationResult struct {
	Name   string                 `json:"name,omitempty"`
	Status string                 `json:"status"`
	Issues []string               `json:"issues"`
	Extra  map[string]interface{} `json:"extra,omitempty"`
}

// ValidatorInput is the slice of state the workagent runtime hands
// to a skill's post-generation validators. Kept narrow on purpose —
// validators should not need DB access or thread metadata; they
// look at file system output.
type ValidatorInput struct {
	AgentMode   string
	OutputFiles []string // absolute paths the SDK wrote during this turn
	ThreadDir   string
}

// validatorTimeout caps an individual script's wall-clock cost so a
// hung Python interpreter or a huge .pptx that takes forever to parse
// can't lengthen the user-visible done event by minutes. 10s is
// generous for a pptx structural check (typically <100ms) but tight
// enough to bound the worst case.
const validatorTimeout = 10 * time.Second

// validatorOutputLimit caps the bytes we accept from a single script's
// stdout/stderr. A pathological validator (or a deliberate hostile
// script that survives the safety scanner) could write GB to stdout
// and OOM the parent. 1 MiB is way more than any structured JSON
// status payload needs (typical: <1 KiB) but small enough that two
// streams ≈ 2 MiB worst-case allocation per concurrent validator.
const validatorOutputLimit = 1 << 20

// validatorTargetFileLimit caps the size of the OUTPUT file we hand
// off to a validator. A misbehaving upstream that writes a 5 GB
// "pptx" would otherwise have python load and parse the whole thing
// before the 10s timeout fires — burning CPU and possibly OOM-ing
// the box. 100 MiB is generous (no real PPT/PDF/HTML output we ship
// approaches 10 MiB) but tight enough to stop a runaway. Large files
// emit a "skipped" result with a clear reason instead of locking up.
const validatorTargetFileLimit int64 = 100 << 20

// extractedScripts caches the tmpfile path each (skill, scriptPath)
// pair has been extracted to. embed.FS bytes are immutable for the
// process lifetime, so a single extraction per (skill, script) pair
// is enough; subsequent calls return the cached tmpfile.
var extractedScripts sync.Map // map[string]string  key="skill/relpath" → tmpfile path

// RunSkillPostGenerationScripts iterates the skill bundle's
// post_generation scripts, extracts each one to tmp on first use,
// runs it as a python3 subprocess against the most relevant output
// file, and returns the parsed results. Failures (script crashed /
// python missing / timeout) yield a "skipped" result rather than
// surfacing as turn-failing errors — the user already streamed the
// answer; validation is a nice-to-have, not the critical path.
func RunSkillPostGenerationScripts(ctx context.Context, bundle *skills.SkillBundle, in ValidatorInput) []SkillValidationResult {
	if bundle == nil || len(bundle.PostScripts) == 0 {
		return nil
	}

	var results []SkillValidationResult
	for _, descriptor := range bundle.PostScripts {
		if descriptor.When != "post_generation" {
			continue
		}
		if descriptor.Path == "" {
			// `Builtin` field is reserved for a future variant
			// that wires Go-implemented validators directly. Until
			// then, skipping with a marker keeps the manifest
			// surface honest.
			globals.Warn(fmt.Sprintf("[SkillValidator] skipping %q: no path declared and builtin validators not yet supported", descriptor.Name))
			continue
		}

		result := runOneScript(ctx, bundle.Name, descriptor, in)
		results = append(results, result)
	}
	return results
}

// runOneScript executes a single script descriptor. Always returns
// a SkillValidationResult — error conditions become "skipped" or
// "error" status rather than Go errors, because the caller's job is
// to decorate the done event payload, not to abort the turn.
func runOneScript(ctx context.Context, skillName string, descriptor skills.ScriptDescriptor, in ValidatorInput) SkillValidationResult {
	resultName := descriptor.Name

	target := pickRelevantOutput(in.OutputFiles, descriptor.Name, in.ThreadDir)
	if target == "" {
		return SkillValidationResult{
			Name:   resultName,
			Status: "skipped",
			Issues: []string{"no relevant output file for this validator"},
		}
	}

	// Refuse oversized targets up front. A python validator parsing
	// a 5 GB "pptx" would burn the validatorTimeout, possibly OOM,
	// and (worst case) leave the host loaded. Size check is cheap
	// (one stat) and saves the worst-case path.
	if info, err := os.Stat(target); err == nil && info.Size() > validatorTargetFileLimit {
		return SkillValidationResult{
			Name:   resultName,
			Status: "skipped",
			Issues: []string{fmt.Sprintf("target file exceeds %d bytes (%d) — refusing to validate", validatorTargetFileLimit, info.Size())},
		}
	}

	scriptBytes, err := skills.ReadScript(skillName, descriptor.Path)
	if err != nil {
		globals.Warn(fmt.Sprintf("[SkillValidator] read script %q from %s: %v", descriptor.Path, skillName, err))
		return SkillValidationResult{
			Name:   resultName,
			Status: "skipped",
			Issues: []string{"script not embedded in skill bundle"},
		}
	}

	scriptPath, err := extractScriptToTmp(skillName, descriptor.Path, scriptBytes)
	if err != nil {
		globals.Warn(fmt.Sprintf("[SkillValidator] extract %q: %v", descriptor.Name, err))
		return SkillValidationResult{
			Name:   resultName,
			Status: "skipped",
			Issues: []string{"failed to extract script to disk"},
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, validatorTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "python3", scriptPath, target)
	// Minimal env: don't leak OPENAI_API_KEY, DB credentials, AWS_*
	// or any other parent-process secret into a script that we
	// consider semi-trusted at best. Python needs PATH to find its
	// stdlib resolver; HOME for ~/.local/lib pip installs;
	// LANG/LC_* for utf-8 stdout encoding; TMPDIR for tempfile
	// fallback. Anything else is gravy the validator doesn't need.
	cmd.Env = minimalSubprocessEnv()
	// Setpgid: put the child in its own process group so we can
	// SIGKILL the whole group on timeout. Without this, a python
	// script that fork()s a subprocess would orphan grandchildren
	// when CommandContext kills python itself — they'd leak as
	// zombies / chew CPU until the host reaped them.
	configureProcessGroup(cmd)
	stdout := &cappedBuffer{limit: validatorOutputLimit}
	stderr := &cappedBuffer{limit: validatorOutputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Defer the process-group cleanup so it fires even when cmd.Run
	// SUCCEEDS. Go's Wait inside Run reaps only the immediate python
	// child; if the script forked grandchildren (subprocess.Popen,
	// multiprocessing pool, &c.) and then exited cleanly, those
	// grandchildren stay alive in our pgid forever. killProcessGroup
	// is documented as safe-on-already-dead — pgid lookup ESRCH is
	// silently discarded. Previously this only ran on the error
	// branch below.
	defer killProcessGroup(cmd)

	if err := cmd.Run(); err != nil {
		// python3 missing on the host — common in dev / on a fresh
		// production machine before someone runs `pip install
		// python-pptx`. Skip silently with a marker so the runtime
		// doesn't spam errors but the operator can spot it.
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
			return SkillValidationResult{
				Name:   resultName,
				Status: "skipped",
				Issues: []string{"python3 not installed on host"},
			}
		}
		if runCtx.Err() == context.DeadlineExceeded {
			return SkillValidationResult{
				Name:   resultName,
				Status: "skipped",
				Issues: []string{fmt.Sprintf("validator timeout after %s", validatorTimeout)},
			}
		}
		stderrTrim := strings.TrimSpace(stderr.String())
		globals.Warn(fmt.Sprintf("[SkillValidator] %s exited non-zero: %v (stderr: %s)", descriptor.Name, err, stderrTrim))
		// Surface a tail of stderr in issues so an operator reading
		// the done event in production logs (or a power user in the
		// frontend) can see WHY the script failed without grepping
		// server logs. Truncate aggressively — issues[] flows out
		// over SSE and lands in browser memory; a multi-KB stack
		// trace per turn is wasteful.
		issues := []string{fmt.Sprintf("validator process error: %v", err)}
		if stderrTrim != "" {
			issues = append(issues, "stderr: "+truncateForIssues(stderrTrim, 512))
		}
		return SkillValidationResult{
			Name:   resultName,
			Status: "skipped",
			Issues: issues,
		}
	}

	var raw struct {
		Status     string   `json:"status"`
		Issues     []string `json:"issues"`
		SlideCount int      `json:"slide_count,omitempty"`
		ChecksSkip string   `json:"checks_skipped,omitempty"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		globals.Warn(fmt.Sprintf("[SkillValidator] %s output not JSON: %v (stdout: %q)", descriptor.Name, err, stdout.String()))
		return SkillValidationResult{
			Name:   resultName,
			Status: "skipped",
			Issues: []string{"validator output not JSON"},
		}
	}

	// Roll the well-known optional fields into Extra so the
	// done-event consumer can read them without each validator
	// inventing a different shape.
	extra := map[string]interface{}{}
	if raw.SlideCount > 0 {
		extra["slide_count"] = raw.SlideCount
	}
	if raw.ChecksSkip != "" {
		extra["checks_skipped"] = raw.ChecksSkip
	}

	return SkillValidationResult{
		Name:   resultName,
		Status: raw.Status,
		Issues: raw.Issues,
		Extra:  extra,
	}
}

// pickRelevantOutput chooses which generated file the validator should
// run against. Today's logic is heuristic-light: validate-pptx wants
// the .pptx; future validators (HTML render check, MP4 sanity) will
// pick by extension. Keeping the matching by validator name lets
// each script declare its file selector implicitly via its name.
//
// Critical: the returned path MUST live inside threadDir. A future
// tool call or pipeline bug that injects a path outside the thread's
// own outputs/ directory would otherwise let the validator read e.g.
// /etc/shadow — the script returns the file's existence + zip-parse
// error string in `issues[]`, which lands on the user's done event.
// pathInsideThreadDir + EvalSymlinks closes that hole.
//
// Unknown validator names return "" (not files[0]) — every validator
// must declare its file kind via its name. Permissive default lets
// a misnamed validator scan the wrong artefact.
func pickRelevantOutput(files []string, validatorName, threadDir string) string {
	var candidate string
	switch {
	case strings.Contains(validatorName, "pptx"):
		candidate = findByExt(files, ".pptx")
	case strings.Contains(validatorName, "pdf"):
		candidate = findByExt(files, ".pdf")
	case strings.Contains(validatorName, "html"):
		candidate = findByExt(files, ".html")
	default:
		return ""
	}
	if candidate == "" {
		return ""
	}
	if threadDir != "" && !pathInsideThreadDir(candidate, threadDir) {
		return ""
	}
	return candidate
}

func findByExt(files []string, ext string) string {
	for _, p := range files {
		if strings.HasSuffix(strings.ToLower(p), ext) {
			return p
		}
	}
	return ""
}

// pathInsideThreadDir reports whether `candidate` resolves (after
// symlink evaluation) to a location inside `threadDir`. Both paths
// are absolute-cleaned + EvalSymlinks'd before the prefix check so
// a hostile symlink under outputs/ pointing at /etc can't sneak
// through, and so /var → /private/var-style mac quirks don't false-
// negative.
//
// Failures (file missing / permissions / etc.) are treated as
// "not inside" — refuse rather than risk the false positive.
func pathInsideThreadDir(candidate, threadDir string) bool {
	absCandidate, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return false
	}
	absThread, err := filepath.Abs(filepath.Clean(threadDir))
	if err != nil {
		return false
	}
	if eval, err := filepath.EvalSymlinks(absCandidate); err == nil {
		absCandidate = eval
	}
	if eval, err := filepath.EvalSymlinks(absThread); err == nil {
		absThread = eval
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(absCandidate+sep, absThread+sep) || absCandidate == absThread
}

// extractScriptToTmp materialises an embedded script file at a stable
// tmpfile path so exec.Command can run it. Caches by SHA-256 of the
// script bytes so a redeploy with new script contents lands at a new
// path (old cached entry stays harmless on disk; OS tmp cleanup
// reaps it eventually).
//
// Path format: $TMPDIR/workmax-skills/<skill>-<hash>-<basename>
//
// Hardening (against multi-tenant boxes / shared CI runners):
//   - tmpDir created with mode 0o700 so other users can't list it
//     and pre-create files at predictable paths
//   - file opened with O_EXCL|O_NOFOLLOW so we don't follow a
//     symlink someone planted at our path; on EEXIST we re-validate
//     ownership/mode before reusing
//   - cache hit re-checks ownership + mode before trusting the
//     path; a file that's been rewritten by another user since we
//     cached it gets re-extracted to a fresh path
//
// Without these, a local attacker on a shared host could symlink-
// hijack the path and either hijack our writes or feed our exec a
// malicious Python file under our UID. embed.FS bytes are public
// (the binary's bytes are public), and so is the hash — the path
// is fully derivable.
func extractScriptToTmp(skillName, relativePath string, content []byte) (string, error) {
	cacheKey := skillName + "/" + relativePath
	if cached, ok := extractedScripts.Load(cacheKey); ok {
		path := cached.(string)
		// Verify the cached file still belongs to us and has
		// the expected restrictive mode. If a $TMPDIR cleanup
		// yanked it OR another user replaced it in our window,
		// fall through to re-extract. os.Lstat + the inode
		// check below stops a symlink replacement from going
		// undetected.
		if isSafeOwnedScript(path) {
			return path, nil
		}
	}

	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:8])

	tmpDir := filepath.Join(os.TempDir(), "workmax-skills")
	// 0o700 — only us. World-readable tmpDir made every cached
	// script path enumerable by any local user.
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", tmpDir, err)
	}
	// MkdirAll skips chmod if the dir already exists with a
	// looser mode. Re-tighten explicitly so an existing 0o755
	// dir from earlier code still gets locked down.
	if err := os.Chmod(tmpDir, 0o700); err != nil {
		// Non-fatal: the file mode below is the second line
		// of defense. Log via the caller's warn if available.
		_ = err
	}

	base := filepath.Base(relativePath)
	tmpPath := filepath.Join(tmpDir, fmt.Sprintf("%s-%s-%s", skillName, hash, base))

	// O_EXCL refuses to follow a pre-existing symlink and
	// refuses to overwrite a regular file we don't expect.
	// O_NOFOLLOW is the belt-and-suspenders for the symlink
	// case (also covered by O_EXCL but explicit is safer).
	// On EEXIST: probe ownership/mode. If it's ours and looks
	// right, reuse; otherwise os.Remove + retry once.
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL | syscall.O_NOFOLLOW
	f, err := os.OpenFile(tmpPath, flags, 0o600)
	if err != nil {
		if !os.IsExist(err) {
			return "", fmt.Errorf("open %s: %w", tmpPath, err)
		}
		// Path exists. If it's a hijack attempt, refuse to
		// trust it; remove + retry. If it's a benign leftover
		// from a previous extract we lost the cache for, the
		// retry produces a fresh write under our control.
		if rmErr := os.Remove(tmpPath); rmErr != nil {
			return "", fmt.Errorf("remove stale tmp %s: %w", tmpPath, rmErr)
		}
		f, err = os.OpenFile(tmpPath, flags, 0o600)
		if err != nil {
			return "", fmt.Errorf("re-open %s: %w", tmpPath, err)
		}
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", tmpPath, err)
	}

	extractedScripts.Store(cacheKey, tmpPath)
	return tmpPath, nil
}

// isSafeOwnedScript reports whether path looks like a file we
// extracted ourselves: a regular file (not a symlink), owned by
// the current process's effective UID, with mode 0o600. Used by
// the cache hit path to detect mid-process tampering.
//
// The UID check matters when $TMPDIR cleanup reaped our cached
// file and another user created a file at the same predictable
// path before we re-verified. tmpDir's 0o700 owner-only mode is
// the first line of defense; this stat-uid check is the second.
//
// On Linux/macOS the syscall.Stat_t payload powers the UID check
// (see skill_validator_unix.go). The build is non-windows-only,
// so the strict path is the only path.
func isSafeOwnedScript(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	// Reject symlinks outright — they're the primary attack
	// vector this hardening exists for.
	if info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if !info.Mode().IsRegular() {
		return false
	}
	// Mode bits should be exactly 0o600. Looser permissions
	// suggest the file isn't ours.
	if info.Mode().Perm() != 0o600 {
		return false
	}
	// UID guarantee promised by the docstring above. Without
	// this, a 0o600 file owned by another user passes mode and
	// regular-file checks even though it's never going to be
	// what we wrote. Defense-in-depth on top of tmpDir 0o700.
	if !isOwnedByCurrentEUID(info) {
		return false
	}
	return true
}

// cappedBuffer is a bytes.Buffer that silently drops writes once the
// content exceeds limit. We discard overflow rather than returning
// an error from Write so cmd.Run()'s output-handling code path
// doesn't change behaviour: the script still completes, the parent
// just refuses to mirror more than `limit` bytes per stream.
//
// Read paths (Bytes/String/Len) read whatever has been retained.
type cappedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c == nil {
		return len(p), nil
	}
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		c.dropped = true
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		c.dropped = true
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte  { return c.buf.Bytes() }
func (c *cappedBuffer) String() string { return c.buf.String() }
func (c *cappedBuffer) Len() int       { return c.buf.Len() }

// minimalSubprocessEnv returns the smallest env that still lets a
// stdlib python3 invocation work. Anything not on the allowlist
// (DB creds, OPENAI_API_KEY, AWS_*, GCS_*, etc.) is excluded so a
// validator that the safety scanner missed can't exfiltrate them
// via stdout / a side channel.
//
// Allowlist rationale:
//   - PATH        : exec.LookPath ran already, but python's own
//                   subprocess machinery still consults PATH for
//                   stdlib helpers (uname, locale-gen on some
//                   distros). Strip it and you get cryptic crashes.
//   - HOME        : pip's user-site lookup, ~/.python_history etc.
//                   The validator should not write here, but pip
//                   crashes outright if HOME is unset.
//   - LANG, LC_*  : utf-8 stdout. Without these python emits
//                   ascii-with-replacement on some distros and
//                   the JSON we parse comes back mangled.
//   - TMPDIR      : python's tempfile module honours it; some
//                   sandboxed runners point it elsewhere.
//   - PYTHONPATH  : honor an operator-set search path (rare; lets
//                   ops inject python-pptx from a non-default
//                   location without a global pip install).
//   - PYTHONUNBUFFERED, PYTHONIOENCODING : stdio-only knobs that
//                   make stdout deterministic; safe to forward.
func minimalSubprocessEnv() []string {
	allowed := []string{
		"PATH",
		"HOME",
		"LANG",
		"TMPDIR",
		"PYTHONPATH",
		"PYTHONUNBUFFERED",
		"PYTHONIOENCODING",
	}
	out := make([]string, 0, len(allowed)+4)
	for _, key := range allowed {
		if val, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+val)
		}
	}
	// LC_* is a family — forward all of them so locale settings
	// stay coherent (mixing LANG=en_US.UTF-8 with LC_CTYPE=C
	// causes UnicodeEncodeError on some hosts).
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}

// truncateForIssues clips a string at max bytes for safe inclusion in
// issues[]. Picks a rune boundary so we don't slice mid-utf8, and
// appends a "…(truncated)" marker so the consumer knows there's more.
func truncateForIssues(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && cut < len(s) {
		// Walk back to a rune boundary.
		if (s[cut] & 0xC0) != 0x80 {
			break
		}
		cut--
	}
	return s[:cut] + "…(truncated)"
}

// AttachValidationResultsToResult enriches the done event's result
// payload with a "validation" field so the frontend can render
// inline status chips. Mirrors the existing
// attachGeneratedFilesToResult / attachRemainingCreditsToResult
// pattern. Best-effort — a JSON marshal failure leaves the result
// untouched rather than poisoning a successful turn.
func AttachValidationResultsToResult(result json.RawMessage, results []SkillValidationResult) json.RawMessage {
	if len(results) == 0 {
		return result
	}

	payload := map[string]interface{}{}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &payload); err != nil {
			// Result is not a JSON object — could be a string error
			// payload (ResultMessage.result is typed as object|string
			// in the SDK), null, an array, etc. Wrapping it as
			// `{"validation": ...}` would silently drop the original
			// content the SDK shipped to the user. Validators are
			// decoration; the real result wins. Log + return original.
			globals.Warn(fmt.Sprintf("[SkillValidator] result is not a JSON object, skipping validation enrichment to preserve original: %v", err))
			return result
		}
	}

	// Group by validator name so consumers can index without iterating.
	byName := make(map[string]map[string]interface{}, len(results))
	for _, r := range results {
		entry := map[string]interface{}{
			"status": r.Status,
			"issues": r.Issues,
		}
		for k, v := range r.Extra {
			entry[k] = v
		}
		byName[r.Name] = entry
	}
	payload["validation"] = byName

	data, err := json.Marshal(payload)
	if err != nil {
		return result
	}
	return data
}

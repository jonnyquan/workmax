package workagent

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"server/service/tools/workagent/skills"
)

// extractScriptToTmp must produce a stable path for repeated calls
// with the same content (no spurious re-extraction every turn) and
// must re-extract when the cached file vanishes (a $TMPDIR cleanup
// can yank it mid-process).
func TestExtractScriptToTmp_CachesAndRecoversFromTmpCleanup(t *testing.T) {
	resetExtractedScripts()
	const skillName = "ppt"
	const relPath = "scripts/test-script.py"
	content := []byte("#!/usr/bin/env python3\nprint('hello')\n")

	first, err := extractScriptToTmp(skillName, relPath, content)
	if err != nil {
		t.Fatalf("first extract: %v", err)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("first extract: file not on disk: %v", err)
	}

	second, err := extractScriptToTmp(skillName, relPath, content)
	if err != nil {
		t.Fatalf("second extract: %v", err)
	}
	if first != second {
		t.Errorf("cache miss on identical content: first=%s second=%s", first, second)
	}

	// Simulate $TMPDIR cleanup: the cached path's file is gone.
	// Next call must transparently re-extract instead of returning
	// a stale path.
	if err := os.Remove(first); err != nil {
		t.Fatalf("remove cached tmp: %v", err)
	}
	third, err := extractScriptToTmp(skillName, relPath, content)
	if err != nil {
		t.Fatalf("post-cleanup extract: %v", err)
	}
	if _, err := os.Stat(third); err != nil {
		t.Errorf("post-cleanup extract: file not on disk: %v", err)
	}
}

// pickRelevantOutput dispatches by validator-name substring rather
// than by extension on every call, because the output set may
// contain multiple files (e.g. .pptx + .pdf preview); the validator's
// name is the cheapest signal for which file it's interested in.
//
// Sweep covers happy paths + the security tightening (path
// confinement to threadDir, no permissive fallback for unknown
// validator names).
func TestPickRelevantOutput_SelectsByValidatorName(t *testing.T) {
	dir := t.TempDir()
	pptxPath := filepath.Join(dir, "deck.pptx")
	pdfPath := filepath.Join(dir, "preview.pdf")
	htmlPath := filepath.Join(dir, "report.html")
	pngPath := filepath.Join(dir, "preview.png")
	for _, p := range []string{pptxPath, pdfPath, htmlPath, pngPath} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed file %s: %v", p, err)
		}
	}
	files := []string{pngPath, pptxPath, pdfPath, htmlPath}

	cases := []struct {
		name string
		want string
	}{
		{"validate-pptx", pptxPath},
		{"validate-pdf", pdfPath},
		{"validate-html", htmlPath},
		// Security tightening: unknown validator names must
		// return "" (no permissive first-file fallback).
		{"validate-unknown", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickRelevantOutput(files, tc.name, dir)
			if got != tc.want {
				t.Errorf("pickRelevantOutput(%s) = %s; want %s", tc.name, got, tc.want)
			}
		})
	}
}

// Security regression: a path injected into OutputFiles that lives
// OUTSIDE threadDir must be rejected. Without this, a future tool
// call writing /etc/shadow into the file list would have the
// validator open it and surface its file-existence + zip-error
// string in `issues[]` to the user (and via SSE to anyone watching
// the done event).
func TestPickRelevantOutput_RejectsPathOutsideThreadDir(t *testing.T) {
	threadDir := t.TempDir()
	otherDir := t.TempDir()

	insideOK := filepath.Join(threadDir, "deck.pptx")
	if err := os.WriteFile(insideOK, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed inside: %v", err)
	}
	outsideEvil := filepath.Join(otherDir, "evil.pptx")
	if err := os.WriteFile(outsideEvil, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed outside: %v", err)
	}

	// File order matters here — the .pptx that's OUTSIDE the
	// thread sits FIRST in the slice. Without confinement the
	// runner would happily pick it. Confinement should reject
	// the outside path and... currently still doesn't reach the
	// inside one (single-pick logic, first-match). Pin that
	// behaviour: returning "" here is safer than silently
	// jumping to the legitimate file because the wrong-file
	// case might mean the upstream pipeline is misconfigured.
	got := pickRelevantOutput([]string{outsideEvil}, "validate-pptx", threadDir)
	if got != "" {
		t.Errorf("pickRelevantOutput should reject outside-thread path; got %q", got)
	}

	// Sanity: a clean inside-thread file still works.
	got = pickRelevantOutput([]string{insideOK}, "validate-pptx", threadDir)
	if got != insideOK {
		t.Errorf("pickRelevantOutput rejected legitimate inside-thread path; got %q want %q", got, insideOK)
	}
}

// pathInsideThreadDir handles symlink trickery: a candidate that
// resolves OUTSIDE the thread (via a planted symlink) must be
// rejected, even if the literal path string starts with threadDir.
func TestPathInsideThreadDir_DefendsAgainstSymlinkEscape(t *testing.T) {
	threadDir := t.TempDir()
	outsideTarget := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outsideTarget, []byte("secret"), 0o600); err != nil {
		t.Fatalf("seed outside target: %v", err)
	}

	// Symlink lives inside threadDir but resolves outside.
	symlink := filepath.Join(threadDir, "leak.pptx")
	if err := os.Symlink(outsideTarget, symlink); err != nil {
		t.Skipf("os.Symlink not supported in this env: %v", err)
	}

	if pathInsideThreadDir(symlink, threadDir) {
		t.Errorf("symlink escape was accepted as inside threadDir")
	}
}

// runOneScript: when the validator runs python3 successfully against
// a known PPTX the stdout JSON gets parsed into the result struct.
// Skips the test if python3 isn't on PATH (developer-machine reality).
func TestRunOneScript_ValidPPTX_ParsesJSONOutput(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; skipping integration test")
	}

	// Synthesize a minimal PPTX: a zip with the openxml signature
	// entry + one slide. python-pptx isn't required for the
	// structural-only path the script supports.
	threadDir := t.TempDir()
	pptxPath := writeMinimalPPTX(t, threadDir)
	defer os.Remove(pptxPath)

	resetExtractedScripts()

	// Build a real ppt skill bundle so we exercise the actual
	// embed.FS read path. The bundle's PostScripts comes pre-
	// populated from skill.yaml.
	bundle, err := skills.NewLoader(legacyPromptsBridge{}).Build("ppt", skills.BuildContext{})
	if err != nil {
		t.Fatalf("build ppt skill: %v", err)
	}
	if len(bundle.PostScripts) == 0 {
		t.Fatal("ppt skill has no PostScripts; manifest dropped scripts: section?")
	}

	results := RunSkillPostGenerationScripts(context.Background(), bundle, ValidatorInput{
		AgentMode:   "ppt",
		OutputFiles: []string{pptxPath},
		ThreadDir:   threadDir,
	})
	if len(results) == 0 {
		t.Fatal("expected at least one validator result")
	}
	got := results[0]
	if got.Name != "validate-pptx" {
		t.Errorf("result name = %q; want validate-pptx", got.Name)
	}
	// The synthetic PPTX has 1 slide and no python-pptx (in CI / on
	// dev). The script returns ok with checks_skipped marker.
	if got.Status != "ok" && got.Status != "warning" {
		t.Errorf("status = %q; expected ok/warning for valid pptx, got issues=%v", got.Status, got.Issues)
	}
}

// runOneScript on a corrupted file should produce status=error.
// The script handles BadZipFile internally and emits structured
// JSON; the runner just relays it.
func TestRunOneScript_CorruptedFile_ReturnsError(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; skipping integration test")
	}

	// Garbage bytes named .pptx — fails the zipfile.ZipFile() open.
	dir := t.TempDir()
	pptxPath := filepath.Join(dir, "corrupted.pptx")
	if err := os.WriteFile(pptxPath, []byte("this is not a zip"), 0o644); err != nil {
		t.Fatalf("write corrupted: %v", err)
	}

	resetExtractedScripts()
	bundle, err := skills.NewLoader(legacyPromptsBridge{}).Build("ppt", skills.BuildContext{})
	if err != nil {
		t.Fatalf("build ppt skill: %v", err)
	}

	results := RunSkillPostGenerationScripts(context.Background(), bundle, ValidatorInput{
		AgentMode:   "ppt",
		OutputFiles: []string{pptxPath},
		ThreadDir:   dir,
	})
	if len(results) == 0 {
		t.Fatal("expected at least one validator result")
	}
	if results[0].Status != "error" {
		t.Errorf("status = %q; want error for corrupted file. issues=%v", results[0].Status, results[0].Issues)
	}
}

// AttachValidationResultsToResult must be a no-op when the result
// slice is empty (don't pollute done events with empty validation
// objects when no validators ran).
func TestAttachValidationResultsToResult_NoopOnEmpty(t *testing.T) {
	original := json.RawMessage(`{"foo":"bar"}`)
	got := AttachValidationResultsToResult(original, nil)
	if string(got) != string(original) {
		t.Errorf("expected no-op; got %s", string(got))
	}
}

// AttachValidationResultsToResult merges a validation map keyed by
// validator name into the result payload, preserving the existing
// fields. Mirrors the contract of attachGeneratedFilesToResult /
// attachRemainingCreditsToResult.
func TestAttachValidationResultsToResult_MergesIntoExistingPayload(t *testing.T) {
	original := json.RawMessage(`{"foo":"bar","generated_files":[]}`)
	results := []SkillValidationResult{
		{Name: "validate-pptx", Status: "warning", Issues: []string{"slide 3 placeholder"}},
	}
	got := AttachValidationResultsToResult(original, results)

	var parsed map[string]interface{}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if parsed["foo"] != "bar" {
		t.Errorf("preexisting field lost: foo=%v", parsed["foo"])
	}
	validation, ok := parsed["validation"].(map[string]interface{})
	if !ok {
		t.Fatalf("validation field missing or wrong shape: %v", parsed["validation"])
	}
	pptx, ok := validation["validate-pptx"].(map[string]interface{})
	if !ok {
		t.Fatalf("validate-pptx entry missing")
	}
	if pptx["status"] != "warning" {
		t.Errorf("status = %v; want warning", pptx["status"])
	}
}

// runOneScript must refuse to invoke the python validator when the
// target file exceeds validatorTargetFileLimit. A real test would
// need a 100 MiB+ file which is wasteful; instead, override the
// limit via a test-only knob would be cleaner — but the symbol's
// const so we exercise the runner with a deliberately oversized
// target and assert the skipped reason. Use a sparse file (truncate)
// so we don't actually allocate 100 MiB on disk.
func TestRunOneScript_RefusesOversizedTarget(t *testing.T) {
	threadDir := t.TempDir()
	pptxPath := filepath.Join(threadDir, "huge.pptx")
	f, err := os.Create(pptxPath)
	if err != nil {
		t.Fatalf("create huge: %v", err)
	}
	// Sparse file, validatorTargetFileLimit + 1 bytes. os.Stat
	// reports the logical size which is what the size check
	// reads.
	if err := f.Truncate(validatorTargetFileLimit + 1); err != nil {
		f.Close()
		t.Fatalf("truncate huge: %v", err)
	}
	f.Close()

	resetExtractedScripts()
	bundle, err := skills.NewLoader(legacyPromptsBridge{}).Build("ppt", skills.BuildContext{})
	if err != nil {
		t.Fatalf("build ppt skill: %v", err)
	}

	results := RunSkillPostGenerationScripts(context.Background(), bundle, ValidatorInput{
		AgentMode:   "ppt",
		OutputFiles: []string{pptxPath},
		ThreadDir:   threadDir,
	})
	if len(results) == 0 {
		t.Fatal("expected one skipped result for oversized target")
	}
	if results[0].Status != "skipped" {
		t.Errorf("status = %q; want skipped for oversized target", results[0].Status)
	}
	joined := strings.Join(results[0].Issues, " ")
	if !strings.Contains(joined, "exceeds") {
		t.Errorf("issue text should mention size limit; got %q", joined)
	}
}

// truncateForIssues must clip to max bytes, append a truncated
// marker, and never split a utf-8 rune mid-byte (which would corrupt
// the JSON the issues[] field gets serialised into).
func TestTruncateForIssues(t *testing.T) {
	if got := truncateForIssues("short", 100); got != "short" {
		t.Errorf("under-limit string mutated: %q", got)
	}
	long := strings.Repeat("a", 600)
	got := truncateForIssues(long, 512)
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Errorf("missing truncation marker: %q", got[len(got)-30:])
	}
	if len(got) > 512+len("…(truncated)") {
		t.Errorf("truncated longer than max+marker: %d", len(got))
	}

	// Multi-byte utf-8: cutting mid-rune would yield invalid utf-8.
	// Build a string of CJK characters (3 bytes each) past the limit.
	cjk := strings.Repeat("中", 200) // 600 bytes
	out := truncateForIssues(cjk, 100)
	if !utf8.ValidString(out) {
		t.Errorf("truncation produced invalid utf-8: %q", out)
	}
}

// cappedBuffer must accept all writes (n == len(p)) so cmd.Run()
// doesn't see EIO from the script's stdout pipe, but stop retaining
// bytes past `limit`. A pathological validator that prints GB to
// stdout otherwise OOMs the parent.
func TestCappedBuffer_RetainsAtMostLimitBytes(t *testing.T) {
	cb := &cappedBuffer{limit: 10}
	n, err := cb.Write([]byte("0123456789ABCDEF")) // 16 bytes, limit 10
	if err != nil {
		t.Fatalf("Write should never error: %v", err)
	}
	if n != 16 {
		t.Errorf("Write returned %d; want 16 (must always claim full p length)", n)
	}
	if cb.Len() != 10 {
		t.Errorf("retained = %d bytes; want 10 (limit)", cb.Len())
	}
	if got := cb.String(); got != "0123456789" {
		t.Errorf("retained content = %q; want first 10 bytes", got)
	}
	if !cb.dropped {
		t.Errorf("dropped flag not set after overflow")
	}

	// Subsequent writes after limit reached: still claim len(p),
	// retain nothing more.
	n2, _ := cb.Write([]byte("more"))
	if n2 != 4 {
		t.Errorf("post-limit Write returned %d; want 4", n2)
	}
	if cb.Len() != 10 {
		t.Errorf("post-limit retained = %d; want still 10", cb.Len())
	}
}

// minimalSubprocessEnv must include PATH (Python needs it) and must
// exclude secret-shaped vars like OPENAI_API_KEY / DB_PASSWORD that
// the parent process inherits. This is the security guarantee a
// validator can't exfiltrate them via stdout / a side channel.
func TestMinimalSubprocessEnv_StripsSecretsKeepsAllowlist(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-secret-leak-me-please")
	t.Setenv("DB_PASSWORD", "hunter2")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "AKIA-FAKE")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_CTYPE", "en_US.UTF-8")

	env := minimalSubprocessEnv()
	joined := strings.Join(env, "\n")

	for _, secret := range []string{"OPENAI_API_KEY", "DB_PASSWORD", "AWS_SECRET_ACCESS_KEY"} {
		if strings.Contains(joined, secret+"=") {
			t.Errorf("subprocess env leaked %s", secret)
		}
	}

	// Allowlist sanity: PATH and LANG must survive (python needs
	// them); LC_CTYPE must survive (LC_* family is forwarded).
	for _, key := range []string{"PATH=", "LANG=", "LC_CTYPE="} {
		if !strings.Contains(joined, key) {
			t.Errorf("allowlist drop: %s missing from minimal env", key)
		}
	}
}

// resetExtractedScripts clears the package-level cache so each test
// observes fresh extraction behaviour. The sync.Map has no Range +
// Delete sugar so we walk it explicitly.
func resetExtractedScripts() {
	extractedScripts.Range(func(k, _ interface{}) bool {
		extractedScripts.Delete(k)
		return true
	})
}

// writeMinimalPPTX synthesises the tiniest zip the script's
// structural checks accept: ppt/presentation.xml + one slide xml.
// Lives in test code so we don't ship sample PPTX bytes in the repo.
//
// dir lets the caller control placement so the same value can be
// reused as ThreadDir, satisfying the path-confinement check in
// pickRelevantOutput.
func writeMinimalPPTX(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "minimal.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create pptx: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for _, entry := range []struct{ name, body string }{
		{"ppt/presentation.xml", "<?xml version=\"1.0\"?><p:presentation/>"},
		{"ppt/slides/slide1.xml", "<?xml version=\"1.0\"?><p:sld/>"},
	} {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return path
}

// Sanity: pickRelevantOutput on an empty file slice returns "". The
// runner uses this to short-circuit to a "no relevant output"
// skipped result.
func TestPickRelevantOutput_EmptyFilesReturnsBlank(t *testing.T) {
	if got := pickRelevantOutput(nil, "validate-pptx", t.TempDir()); got != "" {
		t.Errorf("empty files -> %q; want \"\"", got)
	}
}

// extractScriptToTmp must produce a file with strict mode 0o600 so
// the cache hit path's isSafeOwnedScript guard (which rejects
// looser permissions) can detect tampering. Without this, a later
// chmod-up-to-write attack on the tmp file would go undetected.
func TestExtractScriptToTmp_FileWrittenWithRestrictiveMode(t *testing.T) {
	resetExtractedScripts()
	path, err := extractScriptToTmp("ppt", "scripts/test.py", []byte("print('x')"))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("tmp file mode = %v; want 0o600", info.Mode().Perm())
	}
}

// isSafeOwnedScript must reject symlinks even when their target
// would otherwise pass the regular-file + mode check. This is the
// security guarantee that the cache-hit path can't be turned into
// a "exec the attacker's file" primitive.
func TestIsSafeOwnedScript_RejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.py")
	if err := os.WriteFile(real, []byte("ok"), 0o600); err != nil {
		t.Fatalf("seed real: %v", err)
	}
	link := filepath.Join(dir, "link.py")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if isSafeOwnedScript(link) {
		t.Error("isSafeOwnedScript accepted a symlink — primary attack vector")
	}
	// Sanity: the real file passes.
	if !isSafeOwnedScript(real) {
		t.Error("isSafeOwnedScript rejected a properly-owned regular file")
	}
}

// extractScriptToTmp's cache-hit path must re-validate ownership.
// If the cached file's mode has been chmod'd up to 0o644 (someone
// else replaced it), we must NOT reuse the cached path — re-extract
// to a fresh write under O_EXCL+O_NOFOLLOW.
func TestExtractScriptToTmp_RejectsCachedFileWithLooseMode(t *testing.T) {
	resetExtractedScripts()
	path1, err := extractScriptToTmp("ppt", "scripts/test.py", []byte("print('x')"))
	if err != nil {
		t.Fatalf("first extract: %v", err)
	}
	t.Cleanup(func() { os.Remove(path1) })

	// Loosen the file's mode — simulates either an attacker
	// replacing the file with a world-readable one or another
	// tool chmod'ing it.
	if err := os.Chmod(path1, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	// Cache says path1; isSafeOwnedScript should reject the
	// loose mode and force re-extraction.
	path2, err := extractScriptToTmp("ppt", "scripts/test.py", []byte("print('x')"))
	if err != nil {
		t.Fatalf("second extract after chmod: %v", err)
	}
	t.Cleanup(func() { os.Remove(path2) })

	// path2 should be a freshly-written file with 0o600 (the
	// path may equal path1 because the basename + hash are
	// stable; what matters is the mode is back to strict).
	info, err := os.Stat(path2)
	if err != nil {
		t.Fatalf("stat path2: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("re-extracted file mode = %v; want 0o600 (cache-hit guard didn't trigger)", info.Mode().Perm())
	}
}

// Regression: extractScriptToTmp's hijack-recovery path (the EEXIST
// branch that does Remove + retry) had no direct test. A local
// attacker on a shared host who plants a symlink at the predictable
// tmpPath BEFORE we extract should not get our exec to follow it
// (O_EXCL+O_NOFOLLOW already refuses the open) AND must not block
// extraction altogether — the recovery path removes the planted
// symlink and writes a fresh 0o600 file under our control.
func TestExtractScriptToTmp_RecoversFromPlantedSymlink(t *testing.T) {
	resetExtractedScripts()
	const skillName = "ppt"
	const relPath = "scripts/hijack-target.py"
	content := []byte("#!/usr/bin/env python3\nprint('legit')\n")

	// Reproduce the path extractScriptToTmp will compute. The hash is
	// the first 8 bytes of sha256(content), hex-encoded — same recipe
	// as the production helper at skill_validator.go:377-378.
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:8])
	tmpDir := filepath.Join(os.TempDir(), "workmax-skills")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatalf("mkdir tmpDir: %v", err)
	}
	tmpPath := filepath.Join(tmpDir, fmt.Sprintf("%s-%s-%s", skillName, hash, filepath.Base(relPath)))

	// Plant a symlink pointing at an arbitrary file (we don't even
	// need it to exist — O_NOFOLLOW refuses to follow regardless of
	// target validity).
	hijackTarget := filepath.Join(t.TempDir(), "decoy.py")
	if err := os.WriteFile(hijackTarget, []byte("attacker"), 0o600); err != nil {
		t.Fatalf("write decoy: %v", err)
	}
	_ = os.Remove(tmpPath) // make sure path is empty before symlink
	if err := os.Symlink(hijackTarget, tmpPath); err != nil {
		t.Skipf("symlink unsupported on this filesystem: %v", err)
	}

	// Extract should detect the planted file (O_EXCL fires), remove
	// it, and produce our content under O_EXCL+O_NOFOLLOW on the
	// retry. Cleanup the result regardless of outcome.
	got, err := extractScriptToTmp(skillName, relPath, content)
	t.Cleanup(func() { os.Remove(tmpPath) })
	if err != nil {
		t.Fatalf("expected hijack recovery to succeed, got: %v", err)
	}
	if got != tmpPath {
		t.Errorf("expected recovered path to match predicted: got=%s want=%s", got, tmpPath)
	}

	// Verify it's a regular file (not the planted symlink) with 0o600
	// and OUR content. The Lstat catches a "still a symlink" outcome
	// that a plain Stat would silently follow.
	info, err := os.Lstat(got)
	if err != nil {
		t.Fatalf("lstat result: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("recovered path is still a symlink — hijack not removed")
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("recovered file mode = %v; want 0o600", info.Mode().Perm())
	}
	got1, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read recovered: %v", err)
	}
	if string(got1) != string(content) {
		t.Errorf("recovered content mismatch: got %q, want %q", string(got1), string(content))
	}
}

// Regression: a non-symlink hijacker — e.g. another process that
// won the race to create a regular file at the predicted path —
// is also handled by the EEXIST recovery branch. The retry must
// produce OUR content, not the squatter's.
func TestExtractScriptToTmp_RecoversFromForeignRegularFile(t *testing.T) {
	resetExtractedScripts()
	const skillName = "ppt"
	const relPath = "scripts/squatter-target.py"
	content := []byte("#!/usr/bin/env python3\nprint('legit')\n")

	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:8])
	tmpDir := filepath.Join(os.TempDir(), "workmax-skills")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatalf("mkdir tmpDir: %v", err)
	}
	tmpPath := filepath.Join(tmpDir, fmt.Sprintf("%s-%s-%s", skillName, hash, filepath.Base(relPath)))

	// Pre-plant a regular file with attacker-controlled content. The
	// O_EXCL refusal triggers the recovery branch the same way as the
	// symlink case.
	_ = os.Remove(tmpPath)
	if err := os.WriteFile(tmpPath, []byte("attacker payload"), 0o644); err != nil {
		t.Fatalf("plant regular squatter: %v", err)
	}

	got, err := extractScriptToTmp(skillName, relPath, content)
	t.Cleanup(func() { os.Remove(tmpPath) })
	if err != nil {
		t.Fatalf("expected squatter recovery to succeed, got: %v", err)
	}

	got1, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read recovered: %v", err)
	}
	if string(got1) == "attacker payload" {
		t.Error("recovery did not overwrite the squatter file — would have exec'd attacker code")
	}
	if string(got1) != string(content) {
		t.Errorf("recovered content mismatch: got %q, want %q", string(got1), string(content))
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat recovered: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("recovered mode = %v; want 0o600", info.Mode().Perm())
	}
}

// Regression: even with python3 missing the runner produces a
// structured "skipped" result rather than panicking. We can't easily
// remove python3 from PATH inside a test, so we stub via PATH.
func TestRunOneScript_PythonMissing_ReturnsSkipped(t *testing.T) {
	// Drop PATH so python3 lookup fails. Restore at the end so
	// other tests in the package still find python3.
	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	os.Setenv("PATH", "")

	threadDir := t.TempDir()
	pptxPath := writeMinimalPPTX(t, threadDir)
	resetExtractedScripts()

	bundle, err := skills.NewLoader(legacyPromptsBridge{}).Build("ppt", skills.BuildContext{})
	if err != nil {
		t.Fatalf("build ppt skill: %v", err)
	}

	results := RunSkillPostGenerationScripts(context.Background(), bundle, ValidatorInput{
		AgentMode:   "ppt",
		OutputFiles: []string{pptxPath},
		ThreadDir:   threadDir,
	})
	if len(results) == 0 {
		t.Fatal("expected one result even when python3 missing")
	}
	if results[0].Status != "skipped" {
		t.Errorf("status = %q; want skipped when python3 missing. issues=%v", results[0].Status, results[0].Issues)
	}
	// Issue text should mention the cause for ops debugging.
	joined := strings.Join(results[0].Issues, " ")
	if !strings.Contains(joined, "python3") {
		t.Errorf("expected python3-related issue text; got %q", joined)
	}
}

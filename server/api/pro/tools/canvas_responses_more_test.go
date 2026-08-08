package tools

// canvas_responses_test.go pins a handful of the Contains-based arms
// of deriveCanvasErrorCode. Over a dozen substring branches are
// defined in the source but only 8 are exercised; the remainder ship
// untested despite each mapping a distinct user-facing error code.
//
// A regression here is silent by construction: the default arm returns
// CANVAS_OPERATION_FAILED, so a branch that stops matching (typo in
// the substring, reorder that lets a broader branch win first, or
// accidental case-drift after strings.ToLower) still yields a
// syntactically valid code — the frontend just groups the error into
// the wrong taxonomy bucket and localizes it wrong.
//
// These tests pin every untested Contains branch as its own case, plus
// a "default-bucket" guard to catch the case where a specific branch
// was deleted entirely.

import (
	"errors"
	"net/http"
	"testing"

	canvasService "server/service/tools/canvas"
)

// respondCanvasSchemaErrorIfApplicable is the shared helper write-path
// handlers use to surface CANVAS_SCHEMA_INVALID instead of letting a
// SchemaError fall through deriveCanvasErrorCode and degrade to the
// generic CANVAS_OPERATION_FAILED bucket. Pin the contract here so a
// future refactor that breaks the IsSchemaError detection can't pass
// the tests by also changing the FE classifier.
func TestRespondCanvasSchemaErrorIfApplicable_HandlesSchemaError(t *testing.T) {
	w, c := newRecorderCtx()

	// Trigger a real SchemaError via NormalizeCanvasDocumentForStorage —
	// matches what the production write paths see.
	_, err := canvasService.NormalizeCanvasDocumentForStorage(map[string]any{
		"viewMode": "this-mode-does-not-exist",
	})
	if err == nil {
		t.Fatal("expected NormalizeCanvasDocumentForStorage to reject unknown viewMode")
	}

	if !respondCanvasSchemaErrorIfApplicable(c, err) {
		t.Fatal("helper must return true for a SchemaError")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (legacy envelope)", w.Code)
	}
	body := decodeJSON(t, w)
	if body["errorCode"] != canvasService.ErrCodeSchemaInvalid {
		t.Errorf("top-level errorCode = %#v, want %q", body["errorCode"], canvasService.ErrCodeSchemaInvalid)
	}
	data := body["data"].(map[string]any)
	if data["errorCode"] != canvasService.ErrCodeSchemaInvalid {
		t.Errorf("data.errorCode = %#v, want %q", data["errorCode"], canvasService.ErrCodeSchemaInvalid)
	}
}

func TestRespondCanvasSchemaErrorIfApplicable_PassesThroughOtherErrors(t *testing.T) {
	// A non-schema error must NOT be claimed by this helper — the caller
	// then falls through to its own switch / respondCanvasError path.
	w, c := newRecorderCtx()
	if respondCanvasSchemaErrorIfApplicable(c, errors.New("totally unrelated")) {
		t.Fatal("helper must return false for non-SchemaError")
	}
	if w.Body.Len() != 0 {
		t.Errorf("helper must not write a response for non-SchemaError, body=%q", w.Body.String())
	}
}

func TestRespondCanvasSchemaErrorIfApplicable_NilErrorIsNoop(t *testing.T) {
	w, c := newRecorderCtx()
	if respondCanvasSchemaErrorIfApplicable(c, nil) {
		t.Fatal("helper must return false for nil error")
	}
	if w.Body.Len() != 0 {
		t.Errorf("helper must not write a response for nil error, body=%q", w.Body.String())
	}
}

// matchCanvasSentinel pins the (sentinel error → stable code, message)
// table. Adding a new sentinel without also updating this test means
// the frontend's classifier never learns about the new code — the call
// would silently fall through to CANVAS_OPERATION_FAILED.
//
// Pin every case in one table so the contract is auditable in one
// place and so a delete-by-mistake of any case fails loud.
func TestMatchCanvasSentinel_KnownErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
		wantMsg  string
	}{
		{"project not found", canvasService.ErrCanvasProjectNotFound, "PROJECT_NOT_FOUND", "Project not found"},
		{"version not found", canvasService.ErrCanvasVersionNotFound, "VERSION_NOT_FOUND", "Version not found"},
		{"version read-only", canvasService.ErrCanvasVersionReadOnly, "VERSION_READ_ONLY", "Version is read-only"},
		{"version conflict", canvasService.ErrCanvasVersionConflict, "CANVAS_VERSION_CONFLICT", "Canvas document changed in another session; reload before saving."},
		{"invalid version", canvasService.ErrCanvasInvalidVersion, "INVALID_VERSION", "Invalid version"},
		{"project no versions", canvasService.ErrCanvasProjectNoVersions, "PROJECT_NO_VERSIONS", "Project has no versions"},
		{"asset project not found", canvasService.ErrCanvasAssetProjectNotFound, "PROJECT_NOT_FOUND", "Project not found"},
		{"asset too large", canvasService.ErrCanvasAssetTooLarge, "FILE_TOO_LARGE", "File too large"},
		{"asset unsupported type", canvasService.ErrCanvasAssetUnsupportedType, "INVALID_FILE_TYPE", "Invalid file type"},
		{"asset empty file", canvasService.ErrCanvasAssetEmptyFile, "UPLOAD_FILE_READ_FAILED", "Failed to read uploaded file"},
		{"invalid video url", canvasService.ErrCanvasInvalidVideoURL, "VIDEO_URL_INVALID", "videoUrl is invalid"},
		{"asset invalid input", canvasService.ErrCanvasAssetInvalidInput, canvasAIErrorInvalidReq, "Invalid asset input"},
		{"title required", canvasService.ErrCanvasTitleRequired, "TITLE_REQUIRED", "Title cannot be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg, ok := matchCanvasSentinel(tc.err)
			if !ok {
				t.Fatalf("matchCanvasSentinel(%v) ok=false", tc.err)
			}
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if msg != tc.wantMsg {
				t.Errorf("message = %q, want %q", msg, tc.wantMsg)
			}
		})
	}
}

func TestMatchCanvasSentinel_NilAndUnknown(t *testing.T) {
	if _, _, ok := matchCanvasSentinel(nil); ok {
		t.Error("nil error must not match any sentinel")
	}
	if _, _, ok := matchCanvasSentinel(errors.New("totally unrelated")); ok {
		t.Error("unrelated error must not match any sentinel")
	}
}

func TestRespondCanvasErrorFromError_PrefersSentinelOverFallback(t *testing.T) {
	// When err matches a sentinel, the response uses the sentinel's
	// (code, message) pair — NOT the caller-provided fallback.
	w, c := newRecorderCtx()
	respondCanvasErrorFromError(c, canvasService.ErrCanvasProjectNotFound, "Get project failed")

	body := decodeJSON(t, w)
	if body["errorCode"] != "PROJECT_NOT_FOUND" {
		t.Errorf("errorCode = %#v, want PROJECT_NOT_FOUND", body["errorCode"])
	}
	if body["message"] != "Project not found" {
		t.Errorf("message = %#v, want %q", body["message"], "Project not found")
	}
}

func TestRespondCanvasErrorFromError_FallsBackToMessageDerived(t *testing.T) {
	// Unknown errors fall through to deriveCanvasErrorCode over the
	// caller's "<fallback>: <err>" envelope. Verify the substring path
	// still classifies — important because most legacy callers will
	// migrate gradually.
	w, c := newRecorderCtx()
	respondCanvasErrorFromError(c, errors.New("rate limited"), "failed to optimize prompt")

	body := decodeJSON(t, w)
	if body["errorCode"] != "OPTIMIZE_PROMPT_FAILED" {
		t.Errorf("errorCode = %#v, want OPTIMIZE_PROMPT_FAILED (derived from fallback envelope)", body["errorCode"])
	}
}

func TestDeriveCanvasErrorCode_UntestedSubstringBranches(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    string
	}{
		// Upload pipeline — each failure point gets its own code so the
		// UI can differentiate "user's browser didn't send a file" from
		// "server couldn't write to disk" etc.
		{"file too large", "request entity file too large", "FILE_TOO_LARGE"},
		{"invalid file type", "upload rejected: invalid file type for this endpoint", "INVALID_FILE_TYPE"},
		{"failed to get uploaded file", "failed to get uploaded file from form", "UPLOAD_FILE_MISSING"},
		{"failed to save file", "failed to save file to disk", "SAVE_FILE_FAILED"},
		{"failed to create upload directory", "failed to create upload directory: permission denied", "UPLOAD_DIR_CREATE_FAILED"},
		{"failed to open uploaded file", "failed to open uploaded file stream", "UPLOAD_FILE_OPEN_FAILED"},
		{"failed to read uploaded file", "failed to read uploaded file bytes", "UPLOAD_FILE_READ_FAILED"},

		// CRUD failures — each verb ("get"/"create"/"update"/"delete")
		// × resource gets its own code; a regression that collapsed any
		// pair to the default would break the admin dashboard's
		// per-operation error charts.
		{"get projects failed", "get projects failed: timeout", "LIST_PROJECTS_FAILED"},
		{"get versions failed", "get versions failed: io", "LIST_VERSIONS_FAILED"},
		{"get version failed", "get version failed: 404 upstream", "GET_VERSION_FAILED"},
		{"get assets failed", "get assets failed: db closed", "LIST_ASSETS_FAILED"},
		{"create version failed", "wrapped: create version failed: conflict", "CREATE_VERSION_FAILED"},
		{"create asset failed", "create asset failed: quota exceeded", "CREATE_ASSET_FAILED"},
		{"update project failed", "update project failed: stale version", "UPDATE_PROJECT_FAILED"},
		{"delete project failed", "delete project failed: foreign key", "DELETE_PROJECT_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveCanvasErrorCode(tc.message); got != tc.want {
				t.Errorf("deriveCanvasErrorCode(%q) = %q, want %q", tc.message, got, tc.want)
			}
		})
	}
}

func TestDeriveCanvasErrorCode_OrderSensitiveBranches(t *testing.T) {
	// The switch is ordered — broader Contains patterns placed above a
	// narrower one would silently swallow the narrower match. These
	// cases pin the expected resolution for the handful of substrings
	// that overlap: "get version failed" is a strict substring of
	// "get versions failed" (plural "s"), and both sit in the switch.
	// Pin the resolution so a reorder that moved "get version failed"
	// above "get versions failed" would be caught (the plural would
	// wrongly map to GET_VERSION_FAILED because "get version failed"
	// is a prefix of "get versions failed").
	//
	// (The current source order puts "get versions failed" first, so
	// the plural message lands on LIST_VERSIONS_FAILED — the intended
	// behaviour. Pin it.)
	if got := deriveCanvasErrorCode("get versions failed: boom"); got != "LIST_VERSIONS_FAILED" {
		t.Errorf("plural 'get versions failed' must map to LIST_VERSIONS_FAILED, got %q", got)
	}
	// Confirm the narrower singular still resolves correctly.
	if got := deriveCanvasErrorCode("get version failed: boom"); got != "GET_VERSION_FAILED" {
		t.Errorf("singular 'get version failed' must map to GET_VERSION_FAILED, got %q", got)
	}

	// "create version failed" is a strict substring of nothing else,
	// but pin it alongside for completeness — same shape regression.
	if got := deriveCanvasErrorCode("create version failed"); got != "CREATE_VERSION_FAILED" {
		t.Errorf("'create version failed' must map to CREATE_VERSION_FAILED, got %q", got)
	}
}

func TestDeriveCanvasErrorCode_CaseInsensitiveViaLowercaseNormalization(t *testing.T) {
	// deriveCanvasErrorCode ToLowers the input before matching, so
	// mixed-case handler messages still route correctly. A regression
	// that dropped the strings.ToLower call would pass all existing
	// lowercase-fixture tests but break whenever a handler emits
	// "Failed to Save File" (camelCase / title case style) — and many
	// handler wrappers do exactly that when concatenating fmt.Errorf
	// strings. Pin the normalization.
	cases := []struct {
		message string
		want    string
	}{
		{"Failed To Save File: disk full", "SAVE_FILE_FAILED"},
		{"FILE TOO LARGE", "FILE_TOO_LARGE"},
		{"Invalid File Type: pdf not accepted", "INVALID_FILE_TYPE"},
	}
	for _, tc := range cases {
		if got := deriveCanvasErrorCode(tc.message); got != tc.want {
			t.Errorf("deriveCanvasErrorCode(%q) = %q, want %q", tc.message, got, tc.want)
		}
	}
}

func TestDeriveCanvasErrorCode_EmptyAndWhitespaceMessages(t *testing.T) {
	// Empty and whitespace-only messages were tested for the empty
	// case already; pin whitespace-only too — a regression that only
	// guarded `== ""` would let "   " slip past TrimSpace and land in
	// the substring checks where it technically "contains" nothing
	// but is also not empty, producing undefined-ish behavior. The
	// current source does ToLower+TrimSpace, so the default arm wins.
	if got := deriveCanvasErrorCode("   "); got != "CANVAS_OPERATION_FAILED" {
		t.Errorf("whitespace-only message should land on default, got %q", got)
	}
	if got := deriveCanvasErrorCode("\t\n"); got != "CANVAS_OPERATION_FAILED" {
		t.Errorf("tab/newline-only message should land on default, got %q", got)
	}
}

// ─── Switch ordering + exact-vs-contains semantics ─────────────────────────
//
// Pin the resolution for overlapping patterns so a well-meaning reorder
// of the switch (e.g. "alphabetise cases") can't silently reclassify.

func TestDeriveCanvasErrorCode_PrefixBeatsLaterSubstring(t *testing.T) {
	// HasPrefix("invalid request") fires BEFORE any later substring
	// branch. "Invalid request: …" must always resolve to INVALID_REQUEST
	// regardless of what follows the colon. A refactor that reordered
	// cases by alphabet would break this.
	if got := deriveCanvasErrorCode("Invalid request: upload asset failed"); got != canvasAIErrorInvalidReq {
		t.Errorf("prefix-should-win: got %q, want %q", got, canvasAIErrorInvalidReq)
	}
}

func TestDeriveCanvasErrorCode_EarlierContainsWinsOverLater(t *testing.T) {
	// "file too large" appears earlier than "upload asset failed" in the
	// switch. A message carrying both must route to FILE_TOO_LARGE —
	// pin the first-match-wins ordering so a reorder can't flip it.
	msg := "upload asset failed: file too large (25MB)"
	if got := deriveCanvasErrorCode(msg); got != "FILE_TOO_LARGE" {
		t.Errorf("earlier-branch-wins: got %q, want FILE_TOO_LARGE", got)
	}
}

func TestDeriveCanvasErrorCode_ExactCompareIsStrictNotContains(t *testing.T) {
	// "project not found" uses `==`, not Contains. A message that merely
	// contains it falls through to the default. Pin the strict-equal
	// semantics — widening to Contains would reclassify unrelated
	// messages and is a semantic change, not a cleanup.
	if got := deriveCanvasErrorCode("Project not found in database"); got != "CANVAS_OPERATION_FAILED" {
		t.Errorf("strict-exact: 'project not found' contained but not equal → default, got %q", got)
	}
	if got := deriveCanvasErrorCode("version not found on disk"); got != "CANVAS_OPERATION_FAILED" {
		t.Errorf("strict-exact: 'version not found' contained but not equal → default, got %q", got)
	}
}

// ─── Envelope contract: legacy (respondCanvasErrorWithCode) ────────────────

func TestRespondCanvasErrorWithCode_NonAuthCodeOmitsReloadKey(t *testing.T) {
	// reload is gated on errorCode == UNAUTHORIZED. A non-auth code
	// (e.g. INVALID_VERSION) must NOT set data.reload. Frontends inspect
	// `reload` by key presence, so `reload: false` would still register.
	w, c := newRecorderCtx()
	respondCanvasErrorWithCode(c, "invalid version", "INVALID_VERSION")

	body := decodeJSON(t, w)
	data := body["data"].(map[string]any)
	if _, present := data["reload"]; present {
		t.Errorf("data.reload should be absent for non-auth code, got %#v", data["reload"])
	}
	if data["errorCode"] != "INVALID_VERSION" {
		t.Errorf("data.errorCode = %#v, want INVALID_VERSION", data["errorCode"])
	}
	if body["errorCode"] != "INVALID_VERSION" {
		t.Errorf("top-level errorCode = %#v, want INVALID_VERSION", body["errorCode"])
	}
}

func TestRespondCanvasError_UnknownMessageFallbackAtBothPositions(t *testing.T) {
	// An unrecognised message routes through deriveCanvasErrorCode's
	// default arm (CANVAS_OPERATION_FAILED). Pin that the fallback
	// reaches BOTH the top-level and data.errorCode slots — the
	// frontend classifier reads from either, so a single-position
	// fallback would look inconsistent across readers.
	w, c := newRecorderCtx()
	respondCanvasError(c, "something weird happened that nobody mapped")

	body := decodeJSON(t, w)
	if body["errorCode"] != "CANVAS_OPERATION_FAILED" {
		t.Errorf("top-level errorCode = %#v, want CANVAS_OPERATION_FAILED", body["errorCode"])
	}
	data := body["data"].(map[string]any)
	if data["errorCode"] != "CANVAS_OPERATION_FAILED" {
		t.Errorf("data.errorCode = %#v, want CANVAS_OPERATION_FAILED", data["errorCode"])
	}
}

// ─── Envelope contract: chat path (respondCanvasChatHTTPError) ─────────────

func TestRespondCanvasChatHTTPError_ReloadFalseOmitsKey(t *testing.T) {
	// Frontends check `"reload" in data`, not `data.reload === true`,
	// so the key must be absent when reload=false — not set to false.
	w, c := newRecorderCtx()
	respondCanvasChatHTTPError(c, http.StatusBadRequest, "nope", "BAD", false)

	body := decodeJSON(t, w)
	data := body["data"].(map[string]any)
	if _, present := data["reload"]; present {
		t.Errorf("data.reload should be omitted when reload=false, got %#v", data["reload"])
	}
	if data["success"] != false {
		t.Errorf("data.success = %#v, want false", data["success"])
	}
}

func TestRespondCanvasChatHTTPError_BlankErrorCodeOmittedAtBothPositions(t *testing.T) {
	// Parallel to the legacy envelope's blank-code omit — chat path
	// must match so the frontend classifier never sees empty strings
	// in either the top-level or data.errorCode slot.
	w, c := newRecorderCtx()
	respondCanvasChatHTTPError(c, http.StatusInternalServerError, "boom", "   ", false)

	body := decodeJSON(t, w)
	if _, present := body["errorCode"]; present {
		t.Errorf("top-level errorCode should be omitted when blank, got %#v", body["errorCode"])
	}
	data := body["data"].(map[string]any)
	if _, present := data["errorCode"]; present {
		t.Errorf("data.errorCode should be omitted when blank, got %#v", data["errorCode"])
	}
}

func TestRespondCanvasChatHTTPError_ReloadIndependentFromStatus(t *testing.T) {
	// The caller drives reload, not the HTTP status. A 500 with
	// reload=true must still emit data.reload=true — an upstream
	// provider can invalidate a session during a non-auth request and
	// the handler should be able to force a re-login even on 500.
	w, c := newRecorderCtx()
	respondCanvasChatHTTPError(c, http.StatusInternalServerError, "session kaput", "SESSION_GONE", true)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	body := decodeJSON(t, w)
	data := body["data"].(map[string]any)
	if data["reload"] != true {
		t.Errorf("data.reload = %#v, want true (reload tracks flag, not status)", data["reload"])
	}
}

func TestRespondCanvasChatHTTPError_ErrorFieldMirrorsMessageVerbatim(t *testing.T) {
	// error.{message,userMessage} both mirror the input verbatim. The
	// frontend's chat UI reads these directly — a refactor that
	// localised / rewrote either side would silently break the thread.
	w, c := newRecorderCtx()
	respondCanvasChatHTTPError(c, http.StatusBadRequest, "Invalid request format", "BAD", false)

	body := decodeJSON(t, w)
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error field missing or wrong type: %#v", body["error"])
	}
	if errObj["message"] != "Invalid request format" {
		t.Errorf("error.message = %#v, want verbatim", errObj["message"])
	}
	if errObj["userMessage"] != "Invalid request format" {
		t.Errorf("error.userMessage = %#v, want verbatim", errObj["userMessage"])
	}
}

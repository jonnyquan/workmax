package common

import (
	"errors"
	"strings"
	"testing"
)

// errors.go defines the ToolError type and the handful of classifier
// predicates (IsValidationError / IsProcessingError / IsSystemError /
// IsQuotaError) that HTTP handlers use to decide response codes. If a
// new error code is added to the constants block but NOT added to the
// right classifier, it quietly degrades to "500 internal server error"
// in production — the classifier membership table is load-bearing.
// Equally, the error envelope format "[CODE] message" is relied on by
// logging and analytics; pin the exact shape.

func TestToolError_FormatsBracketedCodeThenMessage(t *testing.T) {
	err := NewToolError("X_CODE", "something failed")
	got := err.Error()
	want := "[X_CODE] something failed"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestNewToolError_OmitsDetailsWhenNotPassed(t *testing.T) {
	// The "omitempty" tag on Details + absence of a passed map means
	// JSON payloads don't serialise an empty `"details": {}` blob.
	// Pin nil-ness here (observable via reflection via the JSON layer).
	err := NewToolError("X", "m")
	if err.Details != nil {
		t.Errorf("Details should be nil when not provided, got %v", err.Details)
	}
}

func TestNewToolError_TakesFirstDetailsMapWhenMultipleProvided(t *testing.T) {
	// Variadic details: only the first map is stored; extras are dropped.
	// This is quirky — a caller passing two maps might expect a merge.
	// Pin the "first wins" behaviour so the contract is visible.
	first := map[string]interface{}{"k": "first"}
	second := map[string]interface{}{"k": "second"}
	err := NewToolError("X", "m", first, second)
	if err.Details["k"] != "first" {
		t.Errorf("expected first map to win, got %v", err.Details["k"])
	}
}

func TestErrInvalidUserIDError_IncludesUserIDInDetails(t *testing.T) {
	err := ErrInvalidUserIDError("abc")
	if err.Code != ErrInvalidUserID {
		t.Errorf("code = %q", err.Code)
	}
	if err.Details["user_id"] != "abc" {
		t.Errorf("details user_id = %v", err.Details["user_id"])
	}
}

func TestErrTextTooLongError_EmbedsMaxLengthInMessageAndDetails(t *testing.T) {
	err := ErrTextTooLongError(500)
	if !strings.Contains(err.Message, "500") {
		t.Errorf("message should contain max length: %q", err.Message)
	}
	if err.Details["max_length"] != 500 {
		t.Errorf("max_length detail = %v", err.Details["max_length"])
	}
}

func TestErrDatabaseErrorFunc_CapturesOperationAndUnderlyingError(t *testing.T) {
	underlying := errors.New("connection refused")
	err := ErrDatabaseErrorFunc("save_user", underlying)
	if err.Details["operation"] != "save_user" {
		t.Errorf("operation detail = %v", err.Details["operation"])
	}
	// The underlying error is stored as a STRING (result of .Error()),
	// not as the error interface — this matters because it means the
	// details map is safe to JSON-encode.
	if err.Details["error"] != "connection refused" {
		t.Errorf("error detail = %v", err.Details["error"])
	}
}

func TestErrInvalidLanguageError_IncludesValidLanguageListForClientHint(t *testing.T) {
	err := ErrInvalidLanguageError("xx", []string{"en", "zh"})
	if err.Details["language"] != "xx" {
		t.Errorf("language detail = %v", err.Details["language"])
	}
	// The API gives the client the full list so the UI can render a
	// picker — leaking the valid values here is intentional.
	got, ok := err.Details["valid_languages"].([]string)
	if !ok || len(got) != 2 || got[0] != "en" || got[1] != "zh" {
		t.Errorf("valid_languages detail = %v", err.Details["valid_languages"])
	}
}

func TestErrNotFoundError_UsesResourceNameInMessageAndKeepsIDType(t *testing.T) {
	err := ErrNotFoundError("user", 42)
	if !strings.Contains(err.Message, "user") {
		t.Errorf("message should contain resource name: %q", err.Message)
	}
	// id is interface{} — an int passed in stays as int (not stringified).
	// Downstream analytics count on this to distinguish numeric from
	// string identifiers.
	if err.Details["id"] != 42 {
		t.Errorf("id detail = %v (type %T)", err.Details["id"], err.Details["id"])
	}
}

func TestIsValidationError_ReturnsTrueForEachValidationCode(t *testing.T) {
	// Every validation code in the constants block must be reachable
	// through IsValidationError — missing one means the handler would
	// return 500 instead of 400 for that error class.
	codes := []string{
		ErrInvalidUserID, ErrTextEmpty, ErrTextTooLong, ErrInvalidURL,
		ErrInvalidDate, ErrInvalidYear, ErrInvalidLanguage, ErrInvalidStyle,
		ErrInvalidSourceType, ErrEmptyTitle, ErrEmptyAuthor, ErrTitleTooLong,
		ErrAuthorTooLong, ErrSourceDataTooLarge, ErrMissingRequiredField,
	}
	for _, code := range codes {
		if !IsValidationError(NewToolError(code, "msg")) {
			t.Errorf("IsValidationError(%q) = false, want true", code)
		}
	}
}

func TestIsValidationError_ReturnsFalseForOtherCategories(t *testing.T) {
	// Negative cases: processing/system/quota codes must NOT leak into
	// the validation bucket.
	for _, code := range []string{
		ErrProcessingTimeout, ErrGenerationFailed, ErrServiceUnavailable,
		ErrQuotaExceeded, ErrDatabaseError, ErrNotFound, ErrTimeout,
	} {
		if IsValidationError(NewToolError(code, "m")) {
			t.Errorf("IsValidationError(%q) = true, expected false", code)
		}
	}

	// Non-ToolError types flow through as false — callers mix stdlib
	// errors.New() into the same error channel and we must not
	// misclassify them.
	if IsValidationError(errors.New("plain stdlib error")) {
		t.Errorf("plain stdlib error should not match any classifier")
	}
}

func TestIsProcessingError_CoversProcessingAndTimeoutCodes(t *testing.T) {
	// Note the quirky fact: ErrTimeout is classified as PROCESSING, not
	// as its own bucket. Pin this so a future split into "transient vs
	// processing" is an explicit, visible change.
	for _, code := range []string{
		ErrProcessingTimeout, ErrGenerationFailed, ErrServiceUnavailable, ErrTimeout,
	} {
		if !IsProcessingError(NewToolError(code, "m")) {
			t.Errorf("IsProcessingError(%q) = false, want true", code)
		}
	}
	if IsProcessingError(NewToolError(ErrQuotaExceeded, "m")) {
		t.Errorf("quota should not be processing")
	}
}

func TestIsSystemError_OnlyDatabaseErrorQualifies(t *testing.T) {
	// System-error is intentionally narrow — database-only. If someone
	// widens it to include everything that's not user-caused, external
	// monitoring thresholds would shift silently.
	if !IsSystemError(NewToolError(ErrDatabaseError, "m")) {
		t.Errorf("database should be system")
	}
	for _, code := range []string{ErrNotFound, ErrTimeout, ErrQuotaExceeded} {
		if IsSystemError(NewToolError(code, "m")) {
			t.Errorf("IsSystemError(%q) = true, want false", code)
		}
	}
}

func TestIsQuotaError_IsolatesQuotaExceeded(t *testing.T) {
	if !IsQuotaError(NewToolError(ErrQuotaExceeded, "m")) {
		t.Errorf("quota_exceeded should match")
	}
	// No other code matches — not even ErrServiceUnavailable despite
	// the surface similarity.
	for _, code := range []string{ErrServiceUnavailable, ErrDatabaseError, ErrTextEmpty} {
		if IsQuotaError(NewToolError(code, "m")) {
			t.Errorf("IsQuotaError(%q) = true, want false", code)
		}
	}
	if IsQuotaError(errors.New("plain")) {
		t.Errorf("plain error must not match")
	}
}

func TestErrorClassifiers_BucketsAreDisjoint(t *testing.T) {
	// A single error must fall into AT MOST one bucket. A widening of
	// IsValidationError that accidentally pulled in a processing code
	// would break rate-limit accounting and HTTP status routing.
	type row struct {
		code     string
		validate bool
		process  bool
		system   bool
		quota    bool
	}
	for _, r := range []row{
		{ErrInvalidUserID, true, false, false, false},
		{ErrGenerationFailed, false, true, false, false},
		{ErrDatabaseError, false, false, true, false},
		{ErrQuotaExceeded, false, false, false, true},
	} {
		e := NewToolError(r.code, "m")
		if IsValidationError(e) != r.validate {
			t.Errorf("%s IsValidationError mismatch", r.code)
		}
		if IsProcessingError(e) != r.process {
			t.Errorf("%s IsProcessingError mismatch", r.code)
		}
		if IsSystemError(e) != r.system {
			t.Errorf("%s IsSystemError mismatch", r.code)
		}
		if IsQuotaError(e) != r.quota {
			t.Errorf("%s IsQuotaError mismatch", r.code)
		}
	}
}

package middleware

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// ErrorHandler is the Server half of the error contract consumed by Desktop.
// The type → severity / recoverable / HTTP status mappings are what the
// renderer branches on for toast routing, retry logic, and re-login
// prompts. Keyword ordering in classifyError is also load-bearing:
// "connection refused" routes to DatabaseError here (not NetworkError),
// because the database branch runs first — a future reshuffle would
// silently re-classify every database outage as a network blip.

func TestClassifyError_KeywordBranches(t *testing.T) {
	h := DefaultErrorHandler()

	cases := []struct {
		name string
		msg  string
		want ErrorType
	}{
		// First-match ordering: database branch runs before every other
		// keyword, so "connection refused" lands here rather than the
		// network category (cf. utils/retry.go which classifies it as
		// network_error — that's a DIFFERENT classifier with different
		// semantics and callers should not conflate them).
		{"database keyword", "database is down", DatabaseError},
		{"sql keyword", "sql: no rows in result set", DatabaseError},
		{"connection refused routes to DatabaseError first", "connection refused", DatabaseError},

		// "auth" is a loose substring match — "unauthorized" ALSO hits
		// auth, but the unauthorized branch runs first so it wins.
		{"unauthorized → AuthError", "unauthorized access", AuthError},
		{"bare auth keyword → AuthError", "auth failed", AuthError},

		{"permission keyword", "permission denied", PermissionError},
		{"forbidden keyword", "forbidden resource", PermissionError},

		{"validation keyword", "validation failed", ValidationError},
		{"invalid keyword", "invalid email", ValidationError},
		{"required keyword", "field is required", ValidationError},

		// File sub-branches: file + too-large → FileTooLarge;
		// file + type → InvalidFileType; file alone → FileError.
		{"file too large", "file is too large", FileTooLarge},
		{"file type", "unsupported file type", InvalidFileType},
		{"bare file", "file read error", FileError},

		{"not found keyword", "record not found", ResourceNotFound},
		{"conflict keyword", "conflict detected", ResourceConflict},
		{"duplicate keyword", "duplicate entry", ResourceConflict},

		{"timeout keyword", "operation timeout", TimeoutError},

		// Fallthrough: anything unmatched lands on InternalError — this
		// is the fail-safe that prevents silent UNKNOWN leakage.
		{"fallthrough to InternalError", "completely new kind of error", InternalError},

		// Case-insensitive: errMsg is lower-cased before matching.
		{"mixed case still matches", "DATABASE ERROR", DatabaseError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := h.classifyError(errors.New(tc.msg))
			if got != tc.want {
				t.Fatalf("classifyError(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// Severity mapping pins the Desktop renderer's public wire contract
// (LOW→INFO, MEDIUM→WARNING, HIGH→ERROR, CRITICAL→CRITICAL). If a
// backend bump silently changed AuthError from HIGH to MEDIUM, the renderer
// would downgrade every login-required toast
// to a "warning" style.
func TestGetErrorSeverity(t *testing.T) {
	h := DefaultErrorHandler()
	cases := []struct {
		in   ErrorType
		want ErrorSeverity
	}{
		{InternalError, SeverityCritical},
		{DatabaseError, SeverityCritical},
		{ConfigurationError, SeverityCritical},
		{AuthError, SeverityHigh},
		{PermissionError, SeverityHigh},
		{QuotaExceeded, SeverityHigh},
		{BusinessError, SeverityMedium},
		{ResourceNotFound, SeverityMedium},
		{ResourceConflict, SeverityMedium},
		{FileError, SeverityMedium},
		{ValidationError, SeverityLow},
		{RequiredFieldError, SeverityLow},
		{FormatError, SeverityLow},
		{TimeoutError, SeverityLow},
		// Unknown types fall through to Medium — neither silently
		// critical (which would page on-call) nor silently ignored.
		{UnknownError, SeverityMedium},
	}
	for _, tc := range cases {
		if got := h.getErrorSeverity(tc.in); got != tc.want {
			t.Errorf("getErrorSeverity(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Recoverable flag drives Desktop's retryable decision via the
// structured BackendUserFriendlyError envelope. PermissionError and
// QuotaExceeded being NON-recoverable is the contract that prevents
// the UI from offering a retry button when the user has genuinely
// been denied access or run out of quota.
func TestIsRecoverable(t *testing.T) {
	h := DefaultErrorHandler()
	recoverable := []ErrorType{
		NetworkError, TimeoutError, ConnectionError,
		InternalError, DatabaseError, ExternalServiceError,
		AuthError, TokenExpiredError, InvalidCredentials,
		ValidationError, RequiredFieldError, FormatError, ConstraintError,
		ResourceConflict, RateLimitError,
	}
	nonRecoverable := []ErrorType{
		PermissionError, ResourceNotFound, QuotaExceeded,
		FileCorrupted, ConfigurationError,
	}
	for _, et := range recoverable {
		if !h.isRecoverable(et) {
			t.Errorf("isRecoverable(%q) = false, want true", et)
		}
	}
	for _, et := range nonRecoverable {
		if h.isRecoverable(et) {
			t.Errorf("isRecoverable(%q) = true, want false", et)
		}
	}
	// Default must be recoverable — safer to let the user retry an
	// unknown error than to dead-end them.
	if !h.isRecoverable(UnknownError) {
		t.Errorf("isRecoverable(UnknownError) = false, want true (default branch)")
	}
}

// GetHTTPStatus is the status the Desktop error adapter switches on.
// 401 → AuthError branch, 403 → PermissionError,
// 404 → ResourceNotFound, 409 → ResourceConflict, 422 → Validation
// family, 429 → RateLimit, 500 → Internal/Database/External/Config.
// A silent reshuffle here would cross-wire every client-side branch.
func TestGetHTTPStatus(t *testing.T) {
	h := DefaultErrorHandler()
	cases := []struct {
		in   ErrorType
		want int
	}{
		{AuthError, http.StatusUnauthorized},
		{TokenExpiredError, http.StatusUnauthorized},
		{InvalidCredentials, http.StatusUnauthorized},
		{PermissionError, http.StatusForbidden},
		{ResourceNotFound, http.StatusNotFound},
		{ResourceConflict, http.StatusConflict},
		{ValidationError, http.StatusUnprocessableEntity},
		{RequiredFieldError, http.StatusUnprocessableEntity},
		{FormatError, http.StatusUnprocessableEntity},
		{ConstraintError, http.StatusUnprocessableEntity},
		{RateLimitError, http.StatusTooManyRequests},
		{InternalError, http.StatusInternalServerError},
		{DatabaseError, http.StatusInternalServerError},
		{ExternalServiceError, http.StatusInternalServerError},
		{ConfigurationError, http.StatusInternalServerError},
		// Unclassified → 400, not 500. Keeping unknown errors on the
		// client-error side avoids spurious 5xx alerts in uptime monitors.
		{UnknownError, http.StatusBadRequest},
		{BusinessError, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if got := h.GetHTTPStatus(tc.in); got != tc.want {
			t.Errorf("GetHTTPStatus(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// CorrelationID must be a proper RFC-4122 v4 UUID, not a raw timestamp
// or counter — downstream log aggregation joins on this shape. Version
// nibble must be 4 and variant nibble must be 8/9/a/b.
func TestGenerateCorrelationID_FormatAndUniqueness(t *testing.T) {
	h := DefaultErrorHandler()
	uuidV4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	seen := make(map[string]struct{}, 32)
	for i := 0; i < 32; i++ {
		id := h.generateCorrelationID()
		if !uuidV4.MatchString(id) {
			t.Fatalf("id %q not RFC-4122 v4", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("collision: %q returned twice in 32 calls", id)
		}
		seen[id] = struct{}{}
	}
}

// generateErrorCode must preserve the type tag and include a time
// component so identical-type errors produced in the same request
// stay distinguishable in logs. The underscore-stripping keeps the
// code compact: ERR-DATABASEERROR-1712345678 not ERR-DATABASE_ERROR-...
func TestGenerateErrorCode_Format(t *testing.T) {
	h := DefaultErrorHandler()
	code := h.generateErrorCode(DatabaseError)
	if !strings.HasPrefix(code, "ERR-DATABASEERROR-") {
		t.Fatalf("code %q missing ERR-<type>- prefix with underscores stripped", code)
	}
	// Validator: an underscore surviving means ReplaceAll drifted.
	if strings.Contains(code, "_") {
		t.Fatalf("code %q still contains underscore; ReplaceAll path broken", code)
	}
}

// NewBusinessError and NewValidationError are the constructors most
// call sites use. Their defaults (Recoverable flag, Severity) flow
// straight through to the Desktop envelope and must not drift.
func TestNewBusinessError_Defaults(t *testing.T) {
	biz := NewBusinessError(QuotaExceeded, "over limit", "您的额度已用完")
	if biz.ErrorType != QuotaExceeded {
		t.Fatalf("ErrorType = %q, want %q", biz.ErrorType, QuotaExceeded)
	}
	if biz.UserMsg != "您的额度已用完" {
		t.Fatalf("UserMsg not preserved: %q", biz.UserMsg)
	}
	if biz.ErrorSeverity != SeverityMedium {
		t.Fatalf("default Severity = %q, want %q", biz.ErrorSeverity, SeverityMedium)
	}
	if biz.Recoverable {
		t.Fatalf("NewBusinessError must default Recoverable=false — caller opts in explicitly")
	}
	if !strings.HasPrefix(biz.ErrorCode, "BIZ-QUOTAEXCEEDED-") {
		t.Fatalf("ErrorCode %q missing BIZ-<type>- prefix", biz.ErrorCode)
	}
	// Error() surface must return the internal message, not UserMsg —
	// logs read Error(), users read UserMessage; mixing these leaks i18n
	// strings into operator-facing logs.
	if biz.Error() != "over limit" {
		t.Fatalf("Error() = %q, want internal message, not user message", biz.Error())
	}
}

func TestNewValidationError_Defaults(t *testing.T) {
	details := []ValidationErrorDetail{
		{Field: "email", Message: "required", Constraint: "not_empty"},
	}
	v := NewValidationError("bad input", details)
	if v.ErrorType != ValidationError {
		t.Fatalf("ErrorType = %q, want %q", v.ErrorType, ValidationError)
	}
	if !v.Recoverable {
		t.Fatalf("NewValidationError must default Recoverable=true — users can fix input")
	}
	if v.ErrorSeverity != SeverityLow {
		t.Fatalf("default Severity = %q, want %q", v.ErrorSeverity, SeverityLow)
	}
	if len(v.ValidationErrors) != 1 || v.ValidationErrors[0].Field != "email" {
		t.Fatalf("ValidationErrors not preserved: %+v", v.ValidationErrors)
	}
}

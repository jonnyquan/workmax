package common

import "fmt"

// ToolError 统一的工具错误类型
type ToolError struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
}

func (e *ToolError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// 创建新的工具错误
func NewToolError(code, message string, details ...map[string]interface{}) *ToolError {
	err := &ToolError{
		Code:    code,
		Message: message,
	}

	if len(details) > 0 {
		err.Details = details[0]
	}

	return err
}

// 通用错误代码常量
const (
	// 输入验证错误
	ErrInvalidUserID        = "INVALID_USER_ID"
	ErrTextEmpty            = "TEXT_EMPTY"
	ErrTextTooLong          = "TEXT_TOO_LONG"
	ErrInvalidURL           = "INVALID_URL"
	ErrInvalidDate          = "INVALID_DATE"
	ErrInvalidYear          = "INVALID_YEAR"
	ErrInvalidLanguage      = "INVALID_LANGUAGE"
	ErrInvalidStyle         = "INVALID_STYLE"
	ErrInvalidSourceType    = "INVALID_SOURCE_TYPE"
	ErrEmptyTitle           = "EMPTY_TITLE"
	ErrEmptyAuthor          = "EMPTY_AUTHOR"
	ErrTitleTooLong         = "TITLE_TOO_LONG"
	ErrAuthorTooLong        = "AUTHOR_TOO_LONG"
	ErrSourceDataTooLarge   = "SOURCE_DATA_TOO_LARGE"
	ErrMissingRequiredField = "MISSING_REQUIRED_FIELD"

	// 处理错误
	ErrProcessingTimeout  = "PROCESSING_TIMEOUT"
	ErrGenerationFailed   = "GENERATION_FAILED"
	ErrServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrQuotaExceeded      = "QUOTA_EXCEEDED"

	// 系统错误
	ErrDatabaseError = "DATABASE_ERROR"
	ErrNotFound      = "NOT_FOUND"
	ErrTimeout       = "TIMEOUT"
)

// 通用错误创建函数
func ErrInvalidUserIDError(userID string) *ToolError {
	return NewToolError(
		ErrInvalidUserID,
		"Invalid user ID format",
		map[string]interface{}{
			"user_id": userID,
		},
	)
}

func ErrTextEmptyError() *ToolError {
	return NewToolError(
		ErrTextEmpty,
		"Text content is required",
	)
}

func ErrTextTooLongError(maxLength int) *ToolError {
	return NewToolError(
		ErrTextTooLong,
		fmt.Sprintf("Text content exceeds maximum length of %d characters", maxLength),
		map[string]interface{}{
			"max_length": maxLength,
		},
	)
}

func ErrInvalidURLError(url string) *ToolError {
	return NewToolError(
		ErrInvalidURL,
		fmt.Sprintf("Invalid URL format: %s", url),
		map[string]interface{}{
			"url": url,
		},
	)
}

func ErrInvalidLanguageError(lang string, validLangs []string) *ToolError {
	return NewToolError(
		ErrInvalidLanguage,
		fmt.Sprintf("Invalid language: %s", lang),
		map[string]interface{}{
			"language":        lang,
			"valid_languages": validLangs,
		},
	)
}

func ErrDatabaseErrorFunc(operation string, err error) *ToolError {
	return NewToolError(
		ErrDatabaseError,
		fmt.Sprintf("Database operation failed: %s", operation),
		map[string]interface{}{
			"operation": operation,
			"error":     err.Error(),
		},
	)
}

func ErrNotFoundError(resource string, id interface{}) *ToolError {
	return NewToolError(
		ErrNotFound,
		fmt.Sprintf("%s not found", resource),
		map[string]interface{}{
			"resource": resource,
			"id":       id,
		},
	)
}

func ErrTimeoutError() *ToolError {
	return NewToolError(
		ErrTimeout,
		"Operation timed out",
	)
}

func ErrQuotaExceededError(quotaType string, limit int) *ToolError {
	return NewToolError(
		ErrQuotaExceeded,
		fmt.Sprintf("%s quota exceeded", quotaType),
		map[string]interface{}{
			"quota_type": quotaType,
			"limit":      limit,
		},
	)
}

func ErrGenerationFailedError(reason string) *ToolError {
	return NewToolError(
		ErrGenerationFailed,
		fmt.Sprintf("Generation failed: %s", reason),
		map[string]interface{}{
			"reason": reason,
		},
	)
}

func ErrServiceUnavailableError(service string) *ToolError {
	return NewToolError(
		ErrServiceUnavailable,
		fmt.Sprintf("%s service is temporarily unavailable", service),
		map[string]interface{}{
			"service": service,
		},
	)
}

// 错误检查辅助函数
func IsValidationError(err error) bool {
	if toolErr, ok := err.(*ToolError); ok {
		switch toolErr.Code {
		case ErrInvalidUserID, ErrTextEmpty, ErrTextTooLong, ErrInvalidURL,
			ErrInvalidDate, ErrInvalidYear, ErrInvalidLanguage, ErrInvalidStyle,
			ErrInvalidSourceType, ErrEmptyTitle, ErrEmptyAuthor, ErrTitleTooLong,
			ErrAuthorTooLong, ErrSourceDataTooLarge, ErrMissingRequiredField:
			return true
		}
	}
	return false
}

func IsProcessingError(err error) bool {
	if toolErr, ok := err.(*ToolError); ok {
		switch toolErr.Code {
		case ErrProcessingTimeout, ErrGenerationFailed, ErrServiceUnavailable, ErrTimeout:
			return true
		}
	}
	return false
}

func IsSystemError(err error) bool {
	if toolErr, ok := err.(*ToolError); ok {
		return toolErr.Code == ErrDatabaseError
	}
	return false
}

func IsQuotaError(err error) bool {
	if toolErr, ok := err.(*ToolError); ok {
		return toolErr.Code == ErrQuotaExceeded
	}
	return false
}

package middleware

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ErrorType 错误类型枚举
type ErrorType string

const (
	// 网络错误
	NetworkError    ErrorType = "NETWORK_ERROR"
	TimeoutError    ErrorType = "TIMEOUT_ERROR"
	ConnectionError ErrorType = "CONNECTION_ERROR"

	// 认证错误
	AuthError          ErrorType = "AUTH_ERROR"
	PermissionError    ErrorType = "PERMISSION_ERROR"
	TokenExpiredError  ErrorType = "TOKEN_EXPIRED"
	InvalidCredentials ErrorType = "INVALID_CREDENTIALS"

	// 验证错误
	ValidationError    ErrorType = "VALIDATION_ERROR"
	RequiredFieldError ErrorType = "REQUIRED_FIELD_ERROR"
	FormatError        ErrorType = "FORMAT_ERROR"
	ConstraintError    ErrorType = "CONSTRAINT_ERROR"

	// 业务错误
	BusinessError    ErrorType = "BUSINESS_ERROR"
	ResourceNotFound ErrorType = "RESOURCE_NOT_FOUND"
	ResourceConflict ErrorType = "RESOURCE_CONFLICT"
	QuotaExceeded    ErrorType = "QUOTA_EXCEEDED"

	// 文件错误
	FileError       ErrorType = "FILE_ERROR"
	FileTooLarge    ErrorType = "FILE_TOO_LARGE"
	InvalidFileType ErrorType = "INVALID_FILE_TYPE"
	FileCorrupted   ErrorType = "FILE_CORRUPTED"

	// 系统错误
	InternalError        ErrorType = "INTERNAL_ERROR"
	DatabaseError        ErrorType = "DATABASE_ERROR"
	ExternalServiceError ErrorType = "EXTERNAL_SERVICE_ERROR"
	ConfigurationError   ErrorType = "CONFIGURATION_ERROR"

	// 速率限制
	RateLimitError ErrorType = "RATE_LIMIT_ERROR"

	// 未知错误
	UnknownError ErrorType = "UNKNOWN_ERROR"
)

// ErrorSeverity 错误严重性
type ErrorSeverity string

const (
	SeverityLow      ErrorSeverity = "LOW"
	SeverityMedium   ErrorSeverity = "MEDIUM"
	SeverityHigh     ErrorSeverity = "HIGH"
	SeverityCritical ErrorSeverity = "CRITICAL"
)

// ErrorAction 错误操作
type ErrorAction struct {
	Type       string `json:"type"`
	Label      string `json:"label"`
	Handler    string `json:"handler,omitempty"`
	URL        string `json:"url,omitempty"`
	Delay      int    `json:"delay,omitempty"`
	MaxRetries int    `json:"maxRetries,omitempty"`
}

// ErrorContext 错误上下文
type ErrorContext struct {
	UserID    string            `json:"userId,omitempty"`
	SessionID string            `json:"sessionId,omitempty"`
	RequestID string            `json:"requestId,omitempty"`
	Timestamp string            `json:"timestamp"`
	UserAgent string            `json:"userAgent,omitempty"`
	IP        string            `json:"ip,omitempty"`
	Path      string            `json:"path,omitempty"`
	Method    string            `json:"method,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      interface{}       `json:"body,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	Query     map[string]string `json:"query,omitempty"`
}

// UserFriendlyError 用户友好错误
type UserFriendlyError struct {
	Type          ErrorType     `json:"type"`
	Code          string        `json:"code"`
	Message       string        `json:"message"`
	UserMessage   string        `json:"userMessage"`
	Severity      ErrorSeverity `json:"severity"`
	Recoverable   bool          `json:"recoverable"`
	Actions       []ErrorAction `json:"actions"`
	Context       *ErrorContext `json:"context,omitempty"`
	Details       interface{}   `json:"details,omitempty"`
	Timestamp     string        `json:"timestamp"`
	CorrelationID string        `json:"correlationId,omitempty"`
	Stack         string        `json:"stack,omitempty"`
}

// CustomError 自定义错误接口
type CustomError interface {
	error
	GetType() ErrorType
	GetCode() string
	GetUserMessage() string
	GetSeverity() ErrorSeverity
	IsRecoverable() bool
	GetDetails() interface{}
}

// BusinessException 业务异常
type BusinessException struct {
	ErrorType     ErrorType     `json:"type"`
	ErrorCode     string        `json:"code"`
	ErrorMessage  string        `json:"message"`
	UserMsg       string        `json:"userMessage"`
	ErrorSeverity ErrorSeverity `json:"severity"`
	Recoverable   bool          `json:"recoverable"`
	ErrorDetails  interface{}   `json:"details,omitempty"`
}

func (e *BusinessException) Error() string {
	return e.ErrorMessage
}

func (e *BusinessException) GetType() ErrorType {
	return e.ErrorType
}

func (e *BusinessException) GetCode() string {
	return e.ErrorCode
}

func (e *BusinessException) GetUserMessage() string {
	return e.UserMsg
}

func (e *BusinessException) GetSeverity() ErrorSeverity {
	return e.ErrorSeverity
}

func (e *BusinessException) IsRecoverable() bool {
	return e.Recoverable
}

func (e *BusinessException) GetDetails() interface{} {
	return e.ErrorDetails
}

// ValidationException 验证异常
type ValidationException struct {
	*BusinessException
	ValidationErrors []ValidationErrorDetail `json:"validationErrors"`
}

// ValidationErrorDetail 验证错误详情
type ValidationErrorDetail struct {
	Field      string      `json:"field"`
	Message    string      `json:"message"`
	Value      interface{} `json:"value,omitempty"`
	Constraint string      `json:"constraint,omitempty"`
}

// ErrorHandlerConfig 错误处理配置
type ErrorHandlerConfig struct {
	EnableLogging     bool
	EnableReporting   bool
	ShowStackTrace    bool
	LogLevel          string
	ReportingEndpoint string
	FallbackMessage   string
}

// ErrorHandler 错误处理器
type ErrorHandler struct {
	config ErrorHandlerConfig
}

// NewErrorHandler 创建错误处理器
func NewErrorHandler(config ErrorHandlerConfig) *ErrorHandler {
	return &ErrorHandler{
		config: config,
	}
}

// DefaultErrorHandler 默认错误处理器
func DefaultErrorHandler() *ErrorHandler {
	return NewErrorHandler(ErrorHandlerConfig{
		EnableLogging:   true,
		EnableReporting: true,
		ShowStackTrace:  false,
		LogLevel:        "error",
		FallbackMessage: "服务器内部错误，请稍后重试",
	})
}

// ErrorHandlerMiddleware 错误处理中间件
func ErrorHandlerMiddleware(handler *ErrorHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				// 处理panic
				err := fmt.Errorf("panic recovered: %v", r)
				userFriendlyError := handler.HandleError(err, c)

				// 记录panic堆栈
				if handler.config.ShowStackTrace {
					log.Printf("Panic: %v\nStack: %s", r, debug.Stack())
				}

				c.JSON(http.StatusInternalServerError, gin.H{
					"error": userFriendlyError,
				})
				c.Abort()
			}
		}()

		c.Next()

		// 处理中间件和处理程序中的错误
		if len(c.Errors) > 0 {
			err := c.Errors.Last()
			userFriendlyError := handler.HandleError(err.Err, c)

			status := handler.GetHTTPStatus(userFriendlyError.Type)
			c.JSON(status, gin.H{
				"error": userFriendlyError,
			})
		}
	}
}

// HandleError 处理错误
func (h *ErrorHandler) HandleError(err error, c *gin.Context) *UserFriendlyError {
	// 构建错误上下文
	context := h.buildErrorContext(c)

	// 如果是自定义错误，直接使用
	if customErr, ok := err.(CustomError); ok {
		return &UserFriendlyError{
			Type:          customErr.GetType(),
			Code:          customErr.GetCode(),
			Message:       customErr.Error(),
			UserMessage:   customErr.GetUserMessage(),
			Severity:      customErr.GetSeverity(),
			Recoverable:   customErr.IsRecoverable(),
			Actions:       h.getErrorActions(customErr.GetType()),
			Context:       context,
			Details:       customErr.GetDetails(),
			Timestamp:     time.Now().Format(time.RFC3339),
			CorrelationID: h.generateCorrelationID(),
		}
	}

	// 分类错误
	errorType := h.classifyError(err)
	severity := h.getErrorSeverity(errorType)
	recoverable := h.isRecoverable(errorType)

	userFriendlyError := &UserFriendlyError{
		Type:          errorType,
		Code:          h.generateErrorCode(errorType),
		Message:       err.Error(),
		UserMessage:   h.getUserMessage(errorType),
		Severity:      severity,
		Recoverable:   recoverable,
		Actions:       h.getErrorActions(errorType),
		Context:       context,
		Timestamp:     time.Now().Format(time.RFC3339),
		CorrelationID: h.generateCorrelationID(),
	}

	// 记录错误
	if h.config.EnableLogging {
		h.logError(userFriendlyError)
	}

	// 报告错误
	if h.config.EnableReporting {
		h.reportError(userFriendlyError)
	}

	return userFriendlyError
}

// 分类错误类型
func (h *ErrorHandler) classifyError(err error) ErrorType {
	errMsg := strings.ToLower(err.Error())

	// 数据库错误
	if strings.Contains(errMsg, "database") ||
		strings.Contains(errMsg, "sql") ||
		strings.Contains(errMsg, "connection refused") {
		return DatabaseError
	}

	// 认证错误
	if strings.Contains(errMsg, "unauthorized") ||
		strings.Contains(errMsg, "auth") {
		return AuthError
	}

	// 权限错误
	if strings.Contains(errMsg, "permission") ||
		strings.Contains(errMsg, "forbidden") {
		return PermissionError
	}

	// 验证错误
	if strings.Contains(errMsg, "validation") ||
		strings.Contains(errMsg, "invalid") ||
		strings.Contains(errMsg, "required") {
		return ValidationError
	}

	// 文件错误
	if strings.Contains(errMsg, "file") {
		if strings.Contains(errMsg, "too large") {
			return FileTooLarge
		}
		if strings.Contains(errMsg, "type") {
			return InvalidFileType
		}
		return FileError
	}

	// 资源不存在
	if strings.Contains(errMsg, "not found") {
		return ResourceNotFound
	}

	// 冲突
	if strings.Contains(errMsg, "conflict") ||
		strings.Contains(errMsg, "duplicate") {
		return ResourceConflict
	}

	// 超时
	if strings.Contains(errMsg, "timeout") {
		return TimeoutError
	}

	// 默认为内部错误
	return InternalError
}

// 获取错误严重性
func (h *ErrorHandler) getErrorSeverity(errorType ErrorType) ErrorSeverity {
	switch errorType {
	case InternalError, DatabaseError, ConfigurationError:
		return SeverityCritical
	case AuthError, PermissionError, QuotaExceeded:
		return SeverityHigh
	case BusinessError, ResourceNotFound, ResourceConflict, FileError:
		return SeverityMedium
	case ValidationError, RequiredFieldError, FormatError, TimeoutError:
		return SeverityLow
	default:
		return SeverityMedium
	}
}

// 判断是否可恢复
func (h *ErrorHandler) isRecoverable(errorType ErrorType) bool {
	switch errorType {
	case NetworkError, TimeoutError, ConnectionError, InternalError, DatabaseError, ExternalServiceError:
		return true
	case AuthError, TokenExpiredError, InvalidCredentials:
		return true
	case ValidationError, RequiredFieldError, FormatError, ConstraintError:
		return true
	case ResourceConflict, RateLimitError:
		return true
	case PermissionError, ResourceNotFound, QuotaExceeded, FileCorrupted, ConfigurationError:
		return false
	default:
		return true
	}
}

// 获取用户消息
func (h *ErrorHandler) getUserMessage(errorType ErrorType) string {
	switch errorType {
	case NetworkError:
		return "网络连接失败，请检查您的网络连接"
	case TimeoutError:
		return "请求超时，请稍后重试"
	case ConnectionError:
		return "连接失败，请检查网络设置"
	case AuthError:
		return "身份验证失败，请重新登录"
	case PermissionError:
		return "权限不足，无法执行此操作"
	case TokenExpiredError:
		return "登录已过期，请重新登录"
	case InvalidCredentials:
		return "用户名或密码错误"
	case ValidationError:
		return "输入信息有误，请检查并重试"
	case RequiredFieldError:
		return "必填字段不能为空"
	case FormatError:
		return "格式不正确，请重新输入"
	case ConstraintError:
		return "输入不符合要求"
	case BusinessError:
		return "操作违反了业务规则"
	case ResourceNotFound:
		return "请求的资源不存在"
	case ResourceConflict:
		return "资源冲突，请稍后重试"
	case QuotaExceeded:
		return "已超出使用限额"
	case FileError:
		return "文件操作失败"
	case FileTooLarge:
		return "文件过大，请选择较小的文件"
	case InvalidFileType:
		return "不支持的文件类型"
	case FileCorrupted:
		return "文件已损坏，无法处理"
	case InternalError:
		return "服务器内部错误，请稍后重试"
	case DatabaseError:
		return "数据库错误，请稍后重试"
	case ExternalServiceError:
		return "外部服务暂时不可用"
	case ConfigurationError:
		return "系统配置错误"
	case RateLimitError:
		return "请求过于频繁，请稍后重试"
	default:
		return h.config.FallbackMessage
	}
}

// 获取错误操作
func (h *ErrorHandler) getErrorActions(errorType ErrorType) []ErrorAction {
	switch errorType {
	case NetworkError, TimeoutError, ConnectionError:
		return []ErrorAction{
			{Type: "retry", Label: "重试", MaxRetries: 3},
			{Type: "dismiss", Label: "忽略"},
		}
	case AuthError, TokenExpiredError:
		return []ErrorAction{
			{Type: "login", Label: "重新登录", URL: "/login"},
			{Type: "dismiss", Label: "取消"},
		}
	case PermissionError:
		return []ErrorAction{
			{Type: "contact_support", Label: "联系支持"},
			{Type: "dismiss", Label: "确定"},
		}
	case ValidationError, RequiredFieldError, FormatError:
		return []ErrorAction{
			{Type: "dismiss", Label: "确定"},
		}
	case ResourceConflict:
		return []ErrorAction{
			{Type: "retry", Label: "重试"},
			{Type: "dismiss", Label: "取消"},
		}
	case RateLimitError:
		return []ErrorAction{
			{Type: "retry", Label: "稍后重试", Delay: 60000},
			{Type: "dismiss", Label: "确定"},
		}
	case InternalError, DatabaseError:
		return []ErrorAction{
			{Type: "retry", Label: "重试"},
			{Type: "contact_support", Label: "联系支持"},
		}
	default:
		return []ErrorAction{
			{Type: "dismiss", Label: "确定"},
		}
	}
}

// 获取HTTP状态码
func (h *ErrorHandler) GetHTTPStatus(errorType ErrorType) int {
	switch errorType {
	case AuthError, TokenExpiredError, InvalidCredentials:
		return http.StatusUnauthorized
	case PermissionError:
		return http.StatusForbidden
	case ResourceNotFound:
		return http.StatusNotFound
	case ResourceConflict:
		return http.StatusConflict
	case ValidationError, RequiredFieldError, FormatError, ConstraintError:
		return http.StatusUnprocessableEntity
	case RateLimitError:
		return http.StatusTooManyRequests
	case InternalError, DatabaseError, ExternalServiceError, ConfigurationError:
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}

// 构建错误上下文
func (h *ErrorHandler) buildErrorContext(c *gin.Context) *ErrorContext {
	context := &ErrorContext{
		Timestamp: time.Now().Format(time.RFC3339),
		UserAgent: c.GetHeader("User-Agent"),
		IP:        c.ClientIP(),
		Path:      c.Request.URL.Path,
		Method:    c.Request.Method,
		Headers:   make(map[string]string),
		Params:    make(map[string]string),
		Query:     make(map[string]string),
	}

	// 添加头部信息
	for key, values := range c.Request.Header {
		if len(values) > 0 {
			context.Headers[key] = values[0]
		}
	}

	// 添加路径参数
	for _, param := range c.Params {
		context.Params[param.Key] = param.Value
	}

	// 添加查询参数
	for key, values := range c.Request.URL.Query() {
		if len(values) > 0 {
			context.Query[key] = values[0]
		}
	}

	// 添加用户信息
	if userID, exists := c.Get("user_id"); exists {
		context.UserID = fmt.Sprintf("%v", userID)
	}

	if sessionID, exists := c.Get("session_id"); exists {
		context.SessionID = fmt.Sprintf("%v", sessionID)
	}

	if requestID, exists := c.Get("request_id"); exists {
		context.RequestID = fmt.Sprintf("%v", requestID)
	}

	return context
}

// 记录错误
func (h *ErrorHandler) logError(err *UserFriendlyError) {
	logData, _ := json.Marshal(err)
	log.Printf("[ERROR] %s: %s", err.Code, string(logData))
}

// 报告错误
func (h *ErrorHandler) reportError(err *UserFriendlyError) {
	// 这里可以发送到外部监控系统
	// 例如：Sentry, Datadog, 等
	// 目前只是记录日志
	log.Printf("[REPORT] Error reported: %s", err.Code)
}

// 生成关联ID (UUID v4)
func (h *ErrorHandler) generateCorrelationID() string {
	b := make([]byte, 16)
	rand.Read(b)
	// Set version (4) and variant (RFC 4122)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// 生成错误代码
func (h *ErrorHandler) generateErrorCode(errorType ErrorType) string {
	timestamp := time.Now().Unix()
	return fmt.Sprintf("ERR-%s-%d", strings.ReplaceAll(string(errorType), "_", ""), timestamp)
}

// 便捷函数：创建业务异常
func NewBusinessError(errorType ErrorType, message string, userMessage string) *BusinessException {
	return &BusinessException{
		ErrorType:     errorType,
		ErrorCode:     fmt.Sprintf("BIZ-%s-%d", strings.ReplaceAll(string(errorType), "_", ""), time.Now().Unix()),
		ErrorMessage:  message,
		UserMsg:       userMessage,
		ErrorSeverity: SeverityMedium,
		Recoverable:   false,
	}
}

// 便捷函数：创建验证异常
func NewValidationError(message string, errors []ValidationErrorDetail) *ValidationException {
	return &ValidationException{
		BusinessException: &BusinessException{
			ErrorType:     ValidationError,
			ErrorCode:     fmt.Sprintf("VAL-%d", time.Now().Unix()),
			ErrorMessage:  message,
			UserMsg:       "输入信息有误，请检查并重试",
			ErrorSeverity: SeverityLow,
			Recoverable:   true,
		},
		ValidationErrors: errors,
	}
}

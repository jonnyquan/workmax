package model

import (
	"server/globals"
	"time"
)

// SystemLog 系统日志表 - 记录外部服务接口异常等
type SystemLog struct {
	globals.GraMODEL
	LogType      string    `json:"logType" gorm:"column:log_type;type:varchar(50);not null;comment:日志类型(api_error, service_error, external_api, etc.)"`
	Service      string    `json:"service" gorm:"column:service;type:varchar(100);not null;comment:服务名称"`
	Method       string    `json:"method" gorm:"column:method;type:varchar(20);comment:HTTP方法"`
	Endpoint     string    `json:"endpoint" gorm:"column:endpoint;type:varchar(500);comment:请求端点"`
	RequestBody  string    `json:"requestBody" gorm:"column:request_body;type:text;comment:请求内容"`
	ResponseBody string    `json:"responseBody" gorm:"column:response_body;type:text;comment:响应内容"`
	ErrorMessage string    `json:"errorMessage" gorm:"column:error_message;type:text;comment:错误信息"`
	StatusCode   int       `json:"statusCode" gorm:"column:status_code;comment:HTTP状态码"`
	Duration     int64     `json:"duration" gorm:"column:duration;comment:请求耗时(毫秒)"`
	UserID       uint      `json:"userId" gorm:"column:user_id;comment:用户ID"`
	IPAddress    string    `json:"ipAddress" gorm:"column:ip_address;type:varchar(45);comment:请求IP"`
	UserAgent    string    `json:"userAgent" gorm:"column:user_agent;type:varchar(500);comment:用户代理"`
	Level        string    `json:"level" gorm:"column:level;type:varchar(20);default:'INFO';comment:日志级别(DEBUG, INFO, WARN, ERROR, FATAL)"`
	RequestID    string    `json:"requestId" gorm:"column:request_id;type:varchar(100);comment:请求ID"`
	TraceID      string    `json:"traceId" gorm:"column:trace_id;type:varchar(100);comment:链路追踪ID"`
	ExtraData    string    `json:"extraData" gorm:"column:extra_data;type:text;comment:额外数据(JSON格式)"`
	OccurredAt   time.Time `json:"occurredAt" gorm:"column:occurred_at;type:datetime;not null;default:CURRENT_TIMESTAMP;comment:发生时间"`
}

func (SystemLog) TableName() string {
	return "w_system_log"
}

// 日志类型常量
const (
	LOG_TYPE_API_ERROR     = "api_error"     // API接口错误
	LOG_TYPE_SERVICE_ERROR = "service_error" // 服务错误
	LOG_TYPE_EXTERNAL_API  = "external_api"  // 外部API调用
	LOG_TYPE_DATABASE      = "database"      // 数据库操作
	LOG_TYPE_AUTH          = "auth"          // 认证相关
	LOG_TYPE_SYSTEM        = "system"        // 系统相关
	LOG_TYPE_BUSINESS      = "business"      // 业务逻辑
)

// 日志级别常量
const (
	LOG_LEVEL_DEBUG = "DEBUG"
	LOG_LEVEL_INFO  = "INFO"
	LOG_LEVEL_WARN  = "WARN"
	LOG_LEVEL_ERROR = "ERROR"
	LOG_LEVEL_FATAL = "FATAL"
)

// CreateSystemLog 创建系统日志的便捷方法
func CreateSystemLog(logType, service, level string) *SystemLog {
	return &SystemLog{
		LogType:    logType,
		Service:    service,
		Level:      level,
		OccurredAt: time.Now(),
	}
}

// WithRequest 设置请求相关信息
func (s *SystemLog) WithRequest(method, endpoint, body string) *SystemLog {
	s.Method = method
	s.Endpoint = endpoint
	s.RequestBody = body
	return s
}

// WithResponse 设置响应相关信息
func (s *SystemLog) WithResponse(statusCode int, body string) *SystemLog {
	s.StatusCode = statusCode
	s.ResponseBody = body
	return s
}

// WithError 设置错误信息
func (s *SystemLog) WithError(err error) *SystemLog {
	if err != nil {
		s.ErrorMessage = err.Error()
	}
	return s
}

// WithUser 设置用户相关信息
func (s *SystemLog) WithUser(userID uint, ip, userAgent string) *SystemLog {
	s.UserID = userID
	s.IPAddress = ip
	s.UserAgent = userAgent
	return s
}

// WithTrace 设置追踪相关信息
func (s *SystemLog) WithTrace(requestID, traceID string) *SystemLog {
	s.RequestID = requestID
	s.TraceID = traceID
	return s
}

// WithDuration 设置请求耗时
func (s *SystemLog) WithDuration(duration time.Duration) *SystemLog {
	s.Duration = duration.Milliseconds()
	return s
}

// WithExtra 设置额外数据
func (s *SystemLog) WithExtra(data string) *SystemLog {
	s.ExtraData = data
	return s
}

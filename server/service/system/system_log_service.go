package system

import (
	"context"
	"encoding/json"
	"fmt"
	"server/globals"
	"server/model"
	"time"

	"gorm.io/gorm"
)

type SystemLogService struct {
	db *gorm.DB
}

func NewSystemLogService() *SystemLogService {
	return &SystemLogService{
		db: globals.GraDBs["system"],
	}
}

// LogAPIRequest 记录API请求日志
func (s *SystemLogService) LogAPIRequest(ctx context.Context, req *APILogRequest) error {
	log := &model.SystemLog{
		LogType:      model.LOG_TYPE_EXTERNAL_API,
		Service:      req.Service,
		Method:       req.Method,
		Endpoint:     req.Endpoint,
		RequestBody:  req.RequestBody,
		ResponseBody: req.ResponseBody,
		StatusCode:   req.StatusCode,
		Duration:     req.Duration,
		UserID:       req.UserID,
		IPAddress:    req.IPAddress,
		UserAgent:    req.UserAgent,
		Level:        req.Level,
		RequestID:    req.RequestID,
		TraceID:      req.TraceID,
		OccurredAt:   time.Now(),
	}

	if req.ErrorMessage != "" {
		log.ErrorMessage = req.ErrorMessage
		if log.Level == "" {
			log.Level = model.LOG_LEVEL_ERROR
		}
	} else if log.Level == "" {
		log.Level = model.LOG_LEVEL_INFO
	}

	// 设置额外数据
	if req.ExtraData != nil {
		extraBytes, err := json.Marshal(req.ExtraData)
		if err == nil {
			log.ExtraData = string(extraBytes)
		}
	}

	return s.db.WithContext(ctx).Create(log).Error
}

// LogServiceError 记录服务错误日志
func (s *SystemLogService) LogServiceError(ctx context.Context, service string, err error, extra map[string]interface{}) error {
	log := &model.SystemLog{
		LogType:      model.LOG_TYPE_SERVICE_ERROR,
		Service:      service,
		ErrorMessage: err.Error(),
		Level:        model.LOG_LEVEL_ERROR,
		OccurredAt:   time.Now(),
	}

	if extra != nil {
		extraBytes, jsonErr := json.Marshal(extra)
		if jsonErr == nil {
			log.ExtraData = string(extraBytes)
		}
	}

	return s.db.WithContext(ctx).Create(log).Error
}

// LogExternalAPI 记录外部API调用日志
func (s *SystemLogService) LogExternalAPI(ctx context.Context, service, method, endpoint string, statusCode int, duration time.Duration, err error) error {
	log := &model.SystemLog{
		LogType:    model.LOG_TYPE_EXTERNAL_API,
		Service:    service,
		Method:     method,
		Endpoint:   endpoint,
		StatusCode: statusCode,
		Duration:   duration.Milliseconds(),
		Level:      model.LOG_LEVEL_INFO,
		OccurredAt: time.Now(),
	}

	if err != nil {
		log.ErrorMessage = err.Error()
		log.Level = model.LOG_LEVEL_ERROR
	}

	return s.db.WithContext(ctx).Create(log).Error
}

// QueryLogs 查询日志
func (s *SystemLogService) QueryLogs(ctx context.Context, req *LogQueryRequest) ([]*model.SystemLog, int64, error) {
	query := s.db.WithContext(ctx).Model(&model.SystemLog{})

	// 添加查询条件
	if req.LogType != "" {
		query = query.Where("log_type = ?", req.LogType)
	}
	if req.Service != "" {
		query = query.Where("service = ?", req.Service)
	}
	if req.Level != "" {
		query = query.Where("level = ?", req.Level)
	}
	if req.UserID > 0 {
		query = query.Where("user_id = ?", req.UserID)
	}
	if !req.StartTime.IsZero() {
		query = query.Where("occurred_at >= ?", req.StartTime)
	}
	if !req.EndTime.IsZero() {
		query = query.Where("occurred_at <= ?", req.EndTime)
	}
	if req.StatusCode > 0 {
		query = query.Where("status_code = ?", req.StatusCode)
	}
	if req.ErrorOnly {
		query = query.Where("error_message IS NOT NULL AND error_message != ''")
	}

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页和排序
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 20
	}
	if req.PageSize > 1000 {
		req.PageSize = 1000
	}

	offset := (req.Page - 1) * req.PageSize
	orderBy := "occurred_at DESC"
	if req.OrderBy != "" {
		orderBy = req.OrderBy
	}

	var logs []*model.SystemLog
	err := query.Order(orderBy).Offset(offset).Limit(req.PageSize).Find(&logs).Error

	return logs, total, err
}

// GetLogStats 获取日志统计
func (s *SystemLogService) GetLogStats(ctx context.Context, startTime, endTime time.Time) (*LogStats, error) {
	stats := &LogStats{}

	// 基础统计查询
	baseQuery := s.db.WithContext(ctx).Model(&model.SystemLog{}).
		Where("occurred_at >= ? AND occurred_at <= ?", startTime, endTime)

	// 总日志数
	if err := baseQuery.Count(&stats.TotalLogs).Error; err != nil {
		return nil, err
	}

	// 错误日志数
	if err := baseQuery.Where("level = ?", model.LOG_LEVEL_ERROR).Count(&stats.ErrorLogs).Error; err != nil {
		return nil, err
	}

	// 按服务分组统计
	type ServiceCount struct {
		Service string `json:"service"`
		Count   int64  `json:"count"`
	}
	var serviceCounts []ServiceCount
	if err := baseQuery.Select("service, COUNT(*) as count").
		Group("service").
		Find(&serviceCounts).Error; err != nil {
		return nil, err
	}
	stats.ServiceStats = make(map[string]int64)
	for _, sc := range serviceCounts {
		stats.ServiceStats[sc.Service] = sc.Count
	}

	// 按日志级别分组统计
	type LevelCount struct {
		Level string `json:"level"`
		Count int64  `json:"count"`
	}
	var levelCounts []LevelCount
	if err := baseQuery.Select("level, COUNT(*) as count").
		Group("level").
		Find(&levelCounts).Error; err != nil {
		return nil, err
	}
	stats.LevelStats = make(map[string]int64)
	for _, lc := range levelCounts {
		stats.LevelStats[lc.Level] = lc.Count
	}

	// 平均响应时间
	var avgDuration float64
	if err := s.db.WithContext(ctx).Model(&model.SystemLog{}).
		Where("occurred_at >= ? AND occurred_at <= ? AND duration > 0", startTime, endTime).
		Select("AVG(duration)").
		Scan(&avgDuration).Error; err != nil {
		return nil, err
	}
	stats.AvgDuration = avgDuration

	return stats, nil
}

// CleanOldLogs 清理旧日志
func (s *SystemLogService) CleanOldLogs(ctx context.Context, retentionDays int) error {
	cutoffTime := time.Now().AddDate(0, 0, -retentionDays)

	result := s.db.WithContext(ctx).
		Where("occurred_at < ?", cutoffTime).
		Delete(&model.SystemLog{})

	if result.Error != nil {
		return result.Error
	}

	fmt.Printf("Cleaned %d old log records before %v\n", result.RowsAffected, cutoffTime)
	return nil
}

// APILogRequest API日志请求结构
type APILogRequest struct {
	Service      string                 `json:"service"`
	Method       string                 `json:"method"`
	Endpoint     string                 `json:"endpoint"`
	RequestBody  string                 `json:"requestBody"`
	ResponseBody string                 `json:"responseBody"`
	ErrorMessage string                 `json:"errorMessage"`
	StatusCode   int                    `json:"statusCode"`
	Duration     int64                  `json:"duration"`
	UserID       uint                   `json:"userId"`
	IPAddress    string                 `json:"ipAddress"`
	UserAgent    string                 `json:"userAgent"`
	Level        string                 `json:"level"`
	RequestID    string                 `json:"requestId"`
	TraceID      string                 `json:"traceId"`
	ExtraData    map[string]interface{} `json:"extraData"`
}

// LogQueryRequest 日志查询请求
type LogQueryRequest struct {
	LogType    string    `json:"logType"`
	Service    string    `json:"service"`
	Level      string    `json:"level"`
	UserID     uint      `json:"userId"`
	StartTime  time.Time `json:"startTime"`
	EndTime    time.Time `json:"endTime"`
	StatusCode int       `json:"statusCode"`
	ErrorOnly  bool      `json:"errorOnly"`
	Page       int       `json:"page"`
	PageSize   int       `json:"pageSize"`
	OrderBy    string    `json:"orderBy"`
}

// LogStats 日志统计
type LogStats struct {
	TotalLogs    int64            `json:"totalLogs"`
	ErrorLogs    int64            `json:"errorLogs"`
	ServiceStats map[string]int64 `json:"serviceStats"`
	LevelStats   map[string]int64 `json:"levelStats"`
	AvgDuration  float64          `json:"avgDuration"`
}

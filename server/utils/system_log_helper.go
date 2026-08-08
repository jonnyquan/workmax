package utils

import (
	"context"
	"fmt"
	"server/globals"
	"server/model"
	systemService "server/service/system"
	"time"
)

// SystemLogHelper 系统日志助手工具
type SystemLogHelper struct {
	logService *systemService.SystemLogService
}

// NewSystemLogHelper 创建系统日志助手
func NewSystemLogHelper() *SystemLogHelper {
	return &SystemLogHelper{
		logService: systemService.NewSystemLogService(),
	}
}

// InitializeSystemLogTable 初始化系统日志表
func (h *SystemLogHelper) InitializeSystemLogTable() error {
	db := globals.GraDBs["system"]
	if db == nil {
		return fmt.Errorf("system database connection not available")
	}

	// 自动创建表结构
	if err := db.AutoMigrate(&model.SystemLog{}); err != nil {
		return fmt.Errorf("failed to migrate system_log table: %w", err)
	}

	// 检查表是否存在数据
	var count int64
	if err := db.Model(&model.SystemLog{}).Count(&count).Error; err != nil {
		return fmt.Errorf("failed to count system_log records: %w", err)
	}

	// 如果表为空，插入初始化记录
	if count == 0 {
		initLog := &model.SystemLog{
			LogType:      model.LOG_TYPE_SYSTEM,
			Service:      "system_log",
			Level:        model.LOG_LEVEL_INFO,
			ErrorMessage: "System log table initialized successfully",
			OccurredAt:   time.Now(),
		}

		if err := db.Create(initLog).Error; err != nil {
			return fmt.Errorf("failed to create initialization log: %w", err)
		}

		fmt.Println("System log table initialized successfully")
	} else {
		fmt.Printf("System log table already exists with %d records\n", count)
	}

	return nil
}

// LogExternalServiceError 记录外部服务错误的便捷方法
func LogExternalServiceError(ctx context.Context, service, endpoint string, err error, extraData map[string]interface{}) {
	helper := NewSystemLogHelper()

	logReq := &systemService.APILogRequest{
		Service:      service,
		Method:       "GET", // 默认GET，可根据实际情况修改
		Endpoint:     endpoint,
		ErrorMessage: err.Error(),
		Level:        model.LOG_LEVEL_ERROR,
		ExtraData:    extraData,
	}

	// 异步记录，避免阻塞主流程
	go func() {
		if logErr := helper.logService.LogAPIRequest(context.Background(), logReq); logErr != nil {
			fmt.Printf("Failed to log external service error: %v\n", logErr)
		}
	}()
}

// LogServiceError 记录服务错误的便捷方法
func LogServiceError(ctx context.Context, service string, err error, extra map[string]interface{}) {
	helper := NewSystemLogHelper()

	// 异步记录，避免阻塞主流程
	go func() {
		if logErr := helper.logService.LogServiceError(context.Background(), service, err, extra); logErr != nil {
			fmt.Printf("Failed to log service error: %v\n", logErr)
		}
	}()
}

// GetRecentErrors 获取最近的错误日志
func (h *SystemLogHelper) GetRecentErrors(ctx context.Context, hours int, limit int) ([]*model.SystemLog, error) {
	startTime := time.Now().Add(-time.Duration(hours) * time.Hour)

	req := &systemService.LogQueryRequest{
		Level:     model.LOG_LEVEL_ERROR,
		StartTime: startTime,
		ErrorOnly: true,
		Page:      1,
		PageSize:  limit,
		OrderBy:   "occurred_at DESC",
	}

	logs, _, err := h.logService.QueryLogs(ctx, req)
	return logs, err
}

// GetAPIStats 获取API调用统计
func (h *SystemLogHelper) GetAPIStats(ctx context.Context, hours int) (*systemService.LogStats, error) {
	startTime := time.Now().Add(-time.Duration(hours) * time.Hour)
	endTime := time.Now()

	return h.logService.GetLogStats(ctx, startTime, endTime)
}

// CleanOldLogs 清理旧日志
func (h *SystemLogHelper) CleanOldLogs(ctx context.Context, retentionDays int) error {
	return h.logService.CleanOldLogs(ctx, retentionDays)
}

// CheckDatabaseConnection 检查数据库连接并记录日志
func CheckDatabaseConnection() error {
	db := globals.GraDBs["system"]
	if db == nil {
		return fmt.Errorf("system database connection is nil")
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get database instance: %w", err)
	}

	if err := sqlDB.Ping(); err != nil {
		return fmt.Errorf("database ping failed: %w", err)
	}

	// 记录数据库连接成功日志
	LogServiceError(context.Background(), "database", nil, map[string]interface{}{
		"action": "connection_check",
		"status": "success",
	})

	return nil
}

// GetSystemLogService 获取系统日志服务实例
func GetSystemLogService() *systemService.SystemLogService {
	return systemService.NewSystemLogService()
}

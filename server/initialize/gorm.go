package initialize

import (
	"os"
	"server/globals"
	"server/model"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Gorm 初始化数据库并产生数据库全局变量
func Gorm() map[string]*gorm.DB {
	return GormMysql()
}

// RegisterDBTables 注册数据库表专用
func RegisterDBTables(db *gorm.DB) {
	db = globals.GraDBs["system"]
	err := db.AutoMigrate(
		// 系统模块表
		model.User{},
		// JwtBlacklist表已移除 - 使用无状态JWT

		// 生成任务表
		// model.GenerationTask{},
		// model.GenerationRecord{},
		// model.GeneratorProvider{},

		// Canvas 任务绑定（项目 ↔ 生成任务）
		model.CanvasTaskBinding{},

		// Canvas 镜头（§8.3 ShotLink 的持久化行）
		model.Shot{},

		// Image Prompt Builder 配置表
		model.ImagePromptConfig{},

		// Video Prompt Builder 配置表
		model.VideoPromptConfig{},

		// 用户资产账本表
		model.UserAssetLedger{},

		// 通知表
		model.Notification{},

		// Prompt 业务表
		model.Prompt{},
		model.PromptCategory{},
		model.PromptTag{},

		// Twitter多账号模块表 (合并后的单表)
		//twitter.TwitterAccount{},
	)
	if err != nil {
		globals.GraLog.Error("register table failed", zap.Error(err))
		os.Exit(0)
	}
	globals.GraLog.Info("register table success")
}

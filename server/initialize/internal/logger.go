package internal

import (
	"fmt"
	"server/globals"

	"gorm.io/gorm/logger"
)

type writer struct {
	logger.Writer
	dbName string
}

// NewWriter writer 构造函数
func NewWriter(w logger.Writer, dbName string) *writer {
	return &writer{Writer: w, dbName: dbName}
}

// Printf 格式化打印日志
func (w *writer) Printf(message string, data ...interface{}) {
	var logZap bool

	// 根据数据库名称判断是否开启zap日志库 用于记录sql语句 如需记录其他数据库请在此处添加
	switch w.dbName {
	case "GormMysqlSystem":
		logZap = globals.GraConf.GormMysqlSystem.LogZap
	default:
		logZap = false
	}
	if logZap {
		globals.GraLog.Info(fmt.Sprintf(message+"\n", data...))
	} else {
		w.Writer.Printf(message, data...)
	}
}

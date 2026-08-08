package internal

import (
	"log"
	"os"
	"server/globals"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

type DBBASE interface {
	GetLogMode() string
}

var Gorm = new(_gorm)

type _gorm struct{}

// Config gorm 自定义配置
func (g *_gorm) Config(dbName string, prefix string, singular bool) *gorm.Config {
	config := &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			TablePrefix:   prefix,
			SingularTable: singular,
		},
		DisableForeignKeyConstraintWhenMigrating: true,
	}
	_default := logger.New(NewWriter(log.New(os.Stdout, "\r\n", log.LstdFlags), dbName), logger.Config{
		SlowThreshold:        200 * time.Millisecond,
		LogLevel:             logger.Warn,
		Colorful:             true,
		ParameterizedQueries: true, // never interpolate passwords, OAuth codes, or capability values into SQL logs
	})
	var logMode string
	switch dbName {
	case "system":
		logMode = globals.GraConf.GormMysqlSystem.LogMode
	default:
		logMode = "info"
	}

	switch logMode {
	case "silent", "Silent":
		config.Logger = _default.LogMode(logger.Silent)
	case "error", "Error":
		config.Logger = _default.LogMode(logger.Error)
	case "warn", "Warn":
		config.Logger = _default.LogMode(logger.Warn)
	case "info", "Info":
		config.Logger = _default.LogMode(logger.Info)
	default:
		config.Logger = _default.LogMode(logger.Info)
	}
	return config
}

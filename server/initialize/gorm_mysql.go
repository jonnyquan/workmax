package initialize

import (
	"server/config"
	"server/globals"
	"server/initialize/internal"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// GormMysql initializes multiple Mysql databases and returns a map of them
func GormMysql() map[string]*gorm.DB {
	dbs := make(map[string]*gorm.DB)

	// Initialize GormMysqlSystem 初始化数据库 其它数据库可以按照这个格式添加
	m := globals.GraConf.GormMysqlSystem
	if m.Dbname != "" {
		db := initializeDB(m)
		if db != nil {
			dbs["system"] = db
		}
	}

	return dbs
}

// initializeDB initializes a Mysql database with the given config
func initializeDB(m config.GormMysql) *gorm.DB {
	mysqlConfig := mysql.Config{
		DSN:                       m.Dsn(), // DSN data source name
		DefaultStringSize:         191,     // string 类型字段的默认长度
		SkipInitializeWithVersion: false,   // 根据版本自动配置
	}
	db, err := gorm.Open(mysql.New(mysqlConfig), internal.Gorm.Config(m.Dbname, m.Prefix, m.Singular))
	if err != nil {
		globals.GraLog.Error("Failed to connect to database: " + err.Error())
		panic("Failed to connect to database: " + err.Error()) // This will help identify configuration issues immediately
	}
	db.InstanceSet("gorm:table_options", "ENGINE="+m.Engine)
	sqlDB, err := db.DB()
	if err != nil {
		globals.GraLog.Error("Failed to get database instance: " + err.Error())
		panic("Failed to get database instance: " + err.Error())
	}
	sqlDB.SetMaxIdleConns(m.MaxIdleConns)
	sqlDB.SetMaxOpenConns(m.MaxOpenConns)
	return db
}

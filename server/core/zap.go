package core

import (
	"fmt"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"os"
	"server/core/internal"
	"server/globals"
	"server/utils"
)

// Zap 获取 zap.Logger
// Author [wangrui19970405](https://github.com/wangrui19970405)
func Zap() (logger *zap.Logger) {
	if ok, _ := utils.PathExists(globals.GraConf.Zap.Director); !ok { // 判断是否有Director文件夹
		fmt.Printf("create %v directory\n", globals.GraConf.Zap.Director)
		_ = os.Mkdir(globals.GraConf.Zap.Director, os.ModePerm)
	}

	cores := internal.Zap.GetZapCores()
	logger = zap.New(zapcore.NewTee(cores...))

	if globals.GraConf.Zap.ShowLine {
		logger = logger.WithOptions(zap.AddCaller())
	}
	return logger
}

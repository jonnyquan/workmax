package internal

import (
	"os"
	"path"
	"server/globals"
	"time"

	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"go.uber.org/zap/zapcore"
)

var FileRotatelogs = new(fileRotatelogs)

type fileRotatelogs struct{}

// GetWriteSyncer 获取 zapcore.WriteSyncer
// Author [wangrui19970405](https://github.com/wangrui19970405)
func (r *fileRotatelogs) GetWriteSyncer(level string) (zapcore.WriteSyncer, error) {
	fileWriter, err := rotatelogs.New(
		path.Join(globals.GraConf.Zap.Director, "%Y-%m-%d", level+".log"),
		rotatelogs.WithClock(rotatelogs.Local),
		rotatelogs.WithMaxAge(time.Duration(globals.GraConf.Zap.MaxAge)*24*time.Hour), // 日志留存时间
		rotatelogs.WithRotationTime(time.Hour*24),
	)
	if globals.GraConf.Zap.LogInConsole {
		return zapcore.NewMultiWriteSyncer(zapcore.AddSync(os.Stdout), zapcore.AddSync(fileWriter)), err
	}
	return zapcore.AddSync(fileWriter), err
}

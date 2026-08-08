package core

import (
	"fmt"
	tools "server/api/pro/tools"
	"server/globals"
	"server/initialize"
	"time"
)

type server interface {
	ListenAndServe() error
}

func RunAppServer() {

	// JWT黑名单功能已移除 - 使用无状态JWT

	Router := initialize.Routers()
	// /uploads/* uses an auth-gated handler instead of the bare Static
	// route — required because canvas-asset bytes under
	// /uploads/canvas/uid/{uid}/... would otherwise be world-readable.
	// See server/api/pro/tools/canvas_asset_serve.go.
	Router.GET("/uploads/*filepath", tools.ServeUploadedFile)
	Router.HEAD("/uploads/*filepath", tools.ServeUploadedFile)

	address := fmt.Sprintf(":%s", globals.GraConf.System.Addr)
	s := initServer(address, Router)
	// 保证文本顺序输出
	// In order to ensure that the text order output can be deleted
	time.Sleep(10 * time.Microsecond)

	fmt.Printf("WorkMax服务启动成功，监听端口：%s\n", address)
	globals.GraLog.Info(fmt.Sprintf("WorkMax server starting on %s", address))

	// ListenAndServe 是阻塞的，只有在出错时才会返回
	if err := s.ListenAndServe(); err != nil {
		globals.GraLog.Error(fmt.Sprintf("Server failed to start: %v", err))
	}
}

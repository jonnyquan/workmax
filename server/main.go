package main

import (
	"os"
	"os/signal"
	"server/core"
	"server/globals"
	"server/initialize"
	toolsService "server/service/tools"
	toolsCommon "server/service/tools/common"
	workagentService "server/service/tools/workagent"
	"syscall"
)

// @title                       Gin-React-Admin Swagger API接口文档
// @version                     v1.0.0
// @description                 使用gin+react进行极速开发的全栈开发基础平台
// @securityDefinitions.apikey  ApiKeyAuth
// @in                          header
// @name                        Authorization
// @BasePath
func main() {
	globals.GraVp = core.Viper() // 初始化Viper
	initialize.OtherInit()       // 初始化其他
	globals.GraLog = core.Zap()  // 初始化zap日志库

	globals.GraDBs = initialize.Gorm()    // 初始化数据库
	globals.GraRedis = initialize.Redis() // 初始化Redis

	/*for _, db := range global.GraDBs {
		if db != nil {
			initialize.RegisterDBTables(db) // 初始化表
			sqlDB, _ := db.DB()
			defer sqlDB.Close()
		}
	}
	*/
	//source.InitSystemDB() // 初始化系统用户

	// 初始化 Agent Client Manager (singleton)
	initialize.InitAgentClient()

	// 初始化图片生成器
	initialize.InitGenerator()
	toolsService.InitVideoPricingCatalogCache()
	toolsService.InitToolPricingCatalogCache()
	if err := workagentService.StartDefaultArtifactStaticRenderRunner(); err != nil {
		globals.Warn("[Initialize] WorkAgent artifact static render runner disabled: " + err.Error())
	}
	if err := workagentService.StartDefaultArtifactMotionRenderRunner(); err != nil {
		globals.Warn("[Initialize] WorkAgent artifact motion render runner disabled: " + err.Error())
	}

	// Register before synchronous cron initialization. A SIGTERM received while
	// the graph is being built stays buffered and is handled as soon as its
	// fully initialized runtime is available.
	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, os.Interrupt, syscall.SIGTERM)

	// 初始化定时任务（使用新框架）。
	cronRuntime := initialize.CronInitV2()

	// 设置优雅关闭
	setupGracefulShutdown(shutdownSignals, cronRuntime.Stop)

	core.RunAppServer()
}

// 设置优雅关闭 - 确保WorkerPool正确清理
//
// Order matters: stop cron first so no new background database pass begins,
// then drain the WorkAgent SSE manager + account pool BEFORE the generic
// worker pool. In-flight Agent requests post stat-update callbacks into the
// account pool's queue, and the SSE manager closes still-streaming connections
// so the SDK iterator gets a clean ctx.Done rather than a TCP RST mid-frame.
type gracefulShutdownHooks struct {
	cleanup []func()
	log     func(string)
	exit    func(int)
}

func setupGracefulShutdown(signals <-chan os.Signal, stopCron func()) {
	runGracefulShutdown(signals, gracefulShutdownHooks{
		cleanup: []func(){
			// Stop cron first so it cannot begin new background database work
			// while the remaining worker pools are draining.
			stopCron,
			func() { workagentService.GetGlobalSSEManager().Shutdown() },
			func() { workagentService.GetAgentAccountPool().Shutdown() },
			func() { workagentService.GetAgentErrorNotifier().Shutdown() },
			workagentService.ShutdownDefaultArtifactStaticRenderRunner,
			workagentService.ShutdownDefaultArtifactMotionRenderRunner,
			toolsService.ShutdownGlobalTaskQueue,
			toolsCommon.ShutdownGlobalWorkerPool,
		},
		log:  func(message string) { globals.GraLog.Info(message) },
		exit: os.Exit,
	})
}

// runGracefulShutdown is separated from signal registration and os.Exit so
// shutdown ordering and wait semantics can be tested without terminating the
// test process.
func runGracefulShutdown(signals <-chan os.Signal, hooks gracefulShutdownHooks) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-signals
		if hooks.log != nil {
			hooks.log("Received shutdown signal, cleaning up...")
		}
		for _, cleanup := range hooks.cleanup {
			if cleanup != nil {
				cleanup()
			}
		}
		if hooks.log != nil {
			hooks.log("All services shut down, cleanup completed, exiting...")
		}
		if hooks.exit != nil {
			hooks.exit(0)
		}
	}()
	return done
}

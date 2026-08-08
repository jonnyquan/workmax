package initialize

import (
	"fmt"
	"server/globals"
	toolsService "server/service/tools"
)

// InitGenerator 初始化图片生成器
func InitGenerator() {
	db := globals.GraDBs["system"]
	if db == nil {
		globals.Error("Failed to initialize generator: system database is nil")
		return
	}

	// Provider 配置不再缓存：每次路由都会去 w_generator_provider 重查，
	// DB 改 enabled / api_key / priority 等会立即生效，无需 reload。

	globals.Info("Generator initialized successfully")

	cfg := globals.GraConf.Generator.TaskQueue
	workers := cfg.Workers
	if workers <= 0 {
		workers = 3
	}
	maxQueueSize := cfg.MaxQueueSize
	if maxQueueSize <= 0 {
		maxQueueSize = 100
	}

	taskQueue := toolsService.NewTaskQueue(workers, maxQueueSize)
	taskQueue.Start()
	toolsService.SetGlobalTaskQueue(taskQueue)
	globals.Info("Task queue started with " + fmt.Sprint(workers) + " workers")
}

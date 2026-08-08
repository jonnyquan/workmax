package common

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"server/globals"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
)

// 全局工作池管理器
var (
	globalPool     *WorkerPool
	globalPoolOnce sync.Once
)

// WorkerPool 管理异步任务的工作池
type WorkerPool struct {
	taskChan chan func()
	workers  int
	wg       sync.WaitGroup
	quit     chan struct{}
	once     sync.Once

	redisClient   *redis.Client
	queueKey      string
	processingKey string

	handlersMu sync.RWMutex
	handlers   map[string]func(context.Context, []byte) error
}

// WorkerPoolConfig 工作池配置
type WorkerPoolConfig struct {
	MaxWorkers      int
	WorkerQueueSize int
}

// GetDefaultWorkerPoolConfig 获取默认工作池配置
func GetDefaultWorkerPoolConfig() WorkerPoolConfig {
	return WorkerPoolConfig{
		MaxWorkers:      10,
		WorkerQueueSize: 100,
	}
}

// 获取全局工作池实例
func GetGlobalWorkerPool() *WorkerPool {
	globalPoolOnce.Do(func() {
		config := GetDefaultWorkerPoolConfig()
		globalPool = NewWorkerPool(config.MaxWorkers, config.WorkerQueueSize)
		if globals.GraRedis != nil {
			if rc := globals.GraRedis["system"]; rc != nil {
				globalPool.EnableRedisPersistence(rc, "tools:workerpool")
			}
		}
		globalPool.Start()
	})
	return globalPool
}

// 创建新的工作池
func NewWorkerPool(workers, queueSize int) *WorkerPool {
	return &WorkerPool{
		taskChan: make(chan func(), queueSize),
		workers:  workers,
		quit:     make(chan struct{}),
		handlers: map[string]func(context.Context, []byte) error{},
	}
}

// 启动工作池
func (wp *WorkerPool) Start() {
	globals.GraLog.Info("Starting tools worker pool", zap.Int("workers", wp.workers))

	wp.recoverRedisInFlight()

	for i := 0; i < wp.workers; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}
}

// 工作协程
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()

	globals.GraLog.Debug("Tools worker started", zap.Int("worker_id", id))

	for {
		select {
		case <-wp.quit:
			globals.GraLog.Debug("Tools worker shutting down", zap.Int("worker_id", id))
			return
		default:
		}

		select {
		case task := <-wp.taskChan:
			globals.GraLog.Debug("Tools worker processing task", zap.Int("worker_id", id))
			func() {
				defer func() {
					if r := recover(); r != nil {
						globals.GraLog.Error("Tools worker panic",
							zap.Int("worker_id", id),
							zap.Any("panic", r))
					}
				}()
				task()
			}()
			continue
		default:
		}

		if wp.redisClient == nil || wp.queueKey == "" || wp.processingKey == "" {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		item, err := wp.redisClient.BRPopLPush(ctx, wp.queueKey, wp.processingKey, 1*time.Second).Result()
		cancel()
		if err != nil {
			if err == redis.Nil {
				continue
			}
			globals.GraLog.Warn("Tools worker redis pop failed", zap.Int("worker_id", id), zap.Error(err))
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if item == "" {
			continue
		}

		if err := wp.processRedisTask(item); err != nil {
			globals.GraLog.Warn("Tools worker redis task failed", zap.Int("worker_id", id), zap.Error(err))
			continue
		}

		ctxAck, cancelAck := context.WithTimeout(context.Background(), 2*time.Second)
		_ = wp.redisClient.LRem(ctxAck, wp.processingKey, 1, item).Err()
		cancelAck()
	}
}

// 提交任务到工作池
func (wp *WorkerPool) SubmitTask(task func()) bool {
	select {
	case wp.taskChan <- task:
		globals.GraLog.Debug("Task submitted to tools worker pool")
		return true
	default:
		globals.GraLog.Warn("Tools worker pool task queue full, task dropped")
		return false
	}
}

// 带超时的任务提交
func (wp *WorkerPool) SubmitTaskWithTimeout(task func(), timeout time.Duration) bool {
	select {
	case wp.taskChan <- task:
		globals.GraLog.Debug("Task submitted to tools worker pool")
		return true
	case <-time.After(timeout):
		globals.GraLog.Warn("Tools worker pool task submission timed out")
		return false
	}
}

type persistentTaskEnvelope struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

func (wp *WorkerPool) EnableRedisPersistence(client *redis.Client, keyPrefix string) {
	if client == nil {
		return
	}
	prefix := keyPrefix
	if prefix == "" {
		prefix = "tools:workerpool"
	}
	wp.redisClient = client
	wp.queueKey = prefix + ":queue"
	wp.processingKey = prefix + ":processing"
}

func (wp *WorkerPool) RegisterPersistentTaskHandler(taskType string, handler func(context.Context, []byte) error) {
	if taskType == "" || handler == nil {
		return
	}
	wp.handlersMu.Lock()
	wp.handlers[taskType] = handler
	wp.handlersMu.Unlock()
}

func (wp *WorkerPool) SubmitPersistentTask(taskType string, payload []byte) bool {
	if wp.redisClient == nil || wp.queueKey == "" {
		globals.GraLog.Warn("Tools worker pool persistent queue not enabled")
		return false
	}
	env := persistentTaskEnvelope{
		Type:    taskType,
		Payload: base64.StdEncoding.EncodeToString(payload),
	}
	b, err := json.Marshal(env)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := wp.redisClient.LPush(ctx, wp.queueKey, string(b)).Err(); err != nil {
		globals.GraLog.Warn("Tools worker pool enqueue persistent task failed", zap.Error(err))
		return false
	}
	return true
}

func (wp *WorkerPool) processRedisTask(item string) error {
	var env persistentTaskEnvelope
	if err := json.Unmarshal([]byte(item), &env); err != nil {
		return err
	}
	if env.Type == "" {
		return fmt.Errorf("missing task type")
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return err
	}

	wp.handlersMu.RLock()
	handler := wp.handlers[env.Type]
	wp.handlersMu.RUnlock()
	if handler == nil {
		return fmt.Errorf("no handler for task type: %s", env.Type)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return handler(ctx, payload)
}

func (wp *WorkerPool) recoverRedisInFlight() {
	if wp.redisClient == nil || wp.queueKey == "" || wp.processingKey == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for {
		item, err := wp.redisClient.RPopLPush(ctx, wp.processingKey, wp.queueKey).Result()
		if err != nil {
			if err == redis.Nil {
				return
			}
			globals.GraLog.Warn("Tools worker pool recover in-flight tasks failed", zap.Error(err))
			return
		}
		if item == "" {
			return
		}
	}
}

// 获取工作池状态
func (wp *WorkerPool) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"workers":         wp.workers,
		"queue_size":      cap(wp.taskChan),
		"pending_tasks":   len(wp.taskChan),
		"queue_usage_pct": float64(len(wp.taskChan)) / float64(cap(wp.taskChan)) * 100,
	}
}

// 关闭工作池
func (wp *WorkerPool) Shutdown() {
	wp.once.Do(func() {
		globals.GraLog.Info("Shutting down tools worker pool")
		close(wp.quit)
		wp.wg.Wait()
		globals.GraLog.Info("Tools worker pool shut down complete")
	})
}

// 关闭全局工作池
func ShutdownGlobalWorkerPool() {
	if globalPool != nil {
		globalPool.Shutdown()
	}
}

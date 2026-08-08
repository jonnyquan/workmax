package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"server/globals"
	"server/model"
	accountsvc "server/service/account"
	assetLedgerService "server/service/assetledger"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 全局任务队列实例
var (
	globalTaskQueue     *TaskQueue
	globalTaskQueueOnce sync.Once
)

// SetGlobalTaskQueue 设置全局任务队列
func SetGlobalTaskQueue(q *TaskQueue) {
	globalTaskQueueOnce.Do(func() {
		globalTaskQueue = q
	})
}

// GetGlobalTaskQueue 获取全局任务队列
func GetGlobalTaskQueue() *TaskQueue {
	return globalTaskQueue
}

// ShutdownGlobalTaskQueue 关闭全局任务队列
func ShutdownGlobalTaskQueue() {
	if globalTaskQueue != nil {
		globalTaskQueue.Stop()
		globals.Info("[TaskQueue] Global task queue shut down")
	}
}

// TaskQueue 任务队列服务
type TaskQueue struct {
	mu            sync.RWMutex
	queue         chan *model.GenerationTask
	workers       int
	maxQueueSize  int
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	processor     *TaskProcessor
	activeCancels map[string]context.CancelFunc
}

// TaskProcessor 任务处理器
type TaskProcessor struct {
	generatorService *GeneratorService
}

// TaskStatusUpdate 任务状态更新
type TaskStatusUpdate struct {
	TaskID   string
	Status   model.TaskStatus
	Progress int
	ErrorMsg string
}

// NewTaskQueue 创建任务队列
func NewTaskQueue(workers, maxQueueSize int) *TaskQueue {
	ctx, cancel := context.WithCancel(context.Background())
	return &TaskQueue{
		queue:         make(chan *model.GenerationTask, maxQueueSize),
		workers:       workers,
		maxQueueSize:  maxQueueSize,
		ctx:           ctx,
		cancel:        cancel,
		processor:     &TaskProcessor{generatorService: &GeneratorService{}},
		activeCancels: make(map[string]context.CancelFunc),
	}
}

// Start 启动任务队列
func (q *TaskQueue) Start() {
	globals.Info("[TaskQueue] Starting task queue with " + fmt.Sprint(q.workers) + " workers")
	for i := 0; i < q.workers; i++ {
		q.wg.Add(1)
		go q.worker(i)
	}

	// 启动恢复未完成任务的任务
	go q.recoverPendingTasks()
}

// Stop 停止任务队列
func (q *TaskQueue) Stop() {
	globals.Info("[TaskQueue] Stopping task queue")
	q.cancel()
	q.wg.Wait()
	globals.Info("[TaskQueue] Task queue stopped")
}

// worker 工作协程
func (q *TaskQueue) worker(id int) {
	defer q.wg.Done()
	for {
		select {
		case <-q.ctx.Done():
			globals.Info(fmt.Sprintf("[TaskQueue] Worker %d shutting down", id))
			return
		case task := <-q.queue:
			if task == nil {
				return
			}
			// Run each task inside its own panic boundary so an
			// unrecovered panic in one task does not kill this worker
			// goroutine and bleed worker slots until process restart.
			// processTask owns the inner recover that flips the task to
			// Failed; this outer one is a belt-and-braces guard for the
			// case where processTask's own deferred recover itself
			// panics (e.g. DB unreachable so the Failed update panics).
			func() {
				defer func() {
					if r := recover(); r != nil {
						globals.Error(fmt.Sprintf("[TaskQueue-%d] Worker recovered from panic on task %s: %v\n%s", id, task.TaskID, r, debug.Stack()))
					}
				}()
				q.processTask(task, id)
			}()
		}
	}
}

// getTaskTimeout 获取任务超时时间（从配置读取，默认5分钟）
func getTaskTimeout() time.Duration {
	cfg := globals.GraConf.Generator.TaskQueue
	if cfg.TaskTimeout > 0 {
		return time.Duration(cfg.TaskTimeout) * time.Second
	}
	return 5 * time.Minute // 默认值
}

func isVideoTask(task *model.GenerationTask) bool {
	if task == nil {
		return false
	}
	if strings.TrimSpace(task.ToolID) == model.TOOL_VIDEO_GENERATOR {
		return true
	}
	if task.RequestData == nil {
		return false
	}
	if mediaType, ok := task.RequestData["mediaType"].(string); ok && strings.TrimSpace(mediaType) == model.MediaTypeVideo {
		return true
	}
	if paramsRaw, ok := task.RequestData["params"]; ok {
		switch params := paramsRaw.(type) {
		case map[string]interface{}:
			if mediaType, ok := params["mediaType"].(string); ok && strings.TrimSpace(mediaType) == model.MediaTypeVideo {
				return true
			}
		case model.JSONMap:
			if mediaType, ok := params["mediaType"].(string); ok && strings.TrimSpace(mediaType) == model.MediaTypeVideo {
				return true
			}
		}
	}
	return false
}

// GetTaskTimeoutForTask 获取任务超时时间，视频任务默认至少 20 分钟。
func GetTaskTimeoutForTask(task *model.GenerationTask) time.Duration {
	timeout := getTaskTimeout()
	if !isVideoTask(task) {
		return timeout
	}
	const minVideoTimeout = 20 * time.Minute
	if timeout < minVideoTimeout {
		return minVideoTimeout
	}
	return timeout
}

// getMaxRetries 获取最大重试次数（从配置读取，默认2次）
func getMaxRetries() int {
	cfg := globals.GraConf.Generator.TaskQueue
	if cfg.MaxRetries >= 0 {
		return cfg.MaxRetries
	}
	return 2 // 默认值
}

// processTask 处理单个任务
func (q *TaskQueue) processTask(task *model.GenerationTask, workerID int) {
	globals.Info(fmt.Sprintf("[TaskQueue-%d] Processing task %s", workerID, task.TaskID))

	// Belt-and-braces against any panic between the Pending→Processing
	// flip below and the explicit Failed/Completed updates further down.
	// Without this, a panic anywhere in the call chain (provider HTTP
	// client, image decode, DB op, …) leaves the row stuck at
	// "processing" until the recover_threshold cron fires (1h by
	// default), so every long-poll handler watching this task spins
	// uselessly until each client hits its own timeout. Flipping to
	// Failed here publishes a TaskEvent that wakes those handlers
	// immediately and refunds the user's credits via the
	// updateTaskStatus refund path.
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		globals.Error(fmt.Sprintf("[TaskQueue-%d] Panic processing task %s: %v\n%s", workerID, task.TaskID, r, debug.Stack()))
		q.updateTaskStatus(task.TaskID, model.TaskStatusFailed, 0, fmt.Sprintf("Internal server error: %v", r))
		q.updateTaskCompleteTime(task.TaskID, time.Now())
		// Re-panic so the worker's outer recover can log a second
		// trace at worker-level granularity (helps when reading logs
		// to distinguish "task crashed but worker survived" from
		// "worker itself died").
		panic(r)
	}()

	now := time.Now()
	res := globals.GraDBs["system"].
		Model(&model.GenerationTask{}).
		Where("task_id = ? AND status = ?", task.TaskID, model.TaskStatusPending).
		Updates(map[string]interface{}{
			"status":     model.TaskStatusProcessing,
			"progress":   0,
			"started_at": now,
		})
	if res.Error == nil && res.RowsAffected > 0 {
		// Wake any long-poll handlers watching this task — Pending →
		// Processing is a user-visible transition.
		PublishTaskEvent(task.TaskID)
	}
	if res.Error != nil {
		globals.Error(fmt.Sprintf("[TaskQueue-%d] Failed to start task %s: %v", workerID, task.TaskID, res.Error))
		return
	}
	if res.RowsAffected == 0 {
		globals.Info(fmt.Sprintf("[TaskQueue-%d] Skipping task %s (not pending)", workerID, task.TaskID))
		return
	}

	// 创建带超时的上下文
	processCtx, cancel := context.WithTimeout(q.ctx, GetTaskTimeoutForTask(task))
	defer cancel()
	q.registerActiveCancel(task.TaskID, cancel)
	defer q.unregisterActiveCancel(task.TaskID)

	// 处理任务（带重试）
	var result *GenerateImageResponse
	var err error
	maxRetries := getMaxRetries()

	for retry := 0; retry <= maxRetries; retry++ {
		if retry > 0 {
			globals.Info(fmt.Sprintf("[TaskQueue-%d] Retrying task %s (attempt %d/%d)", workerID, task.TaskID, retry, maxRetries))
		}

		result, err = q.processor.Process(processCtx, task)
		if err == nil {
			break
		}

		// 检查是否是临时错误（可重试）
		if !q.isRetryableError(err) || retry >= maxRetries {
			break
		}

		// 等待一段时间后重试
		select {
		case <-time.After(time.Duration(retry) * time.Second):
		case <-q.ctx.Done():
			globals.Info(fmt.Sprintf("[TaskQueue-%d] Task %s cancelled during retry wait", workerID, task.TaskID))
			return
		}
	}

	if err != nil {
		if q.isTaskCancelled(task.TaskID) || errors.Is(err, context.Canceled) {
			globals.Info(fmt.Sprintf("[TaskQueue-%d] Task %s cancelled", workerID, task.TaskID))
			return
		}
		globals.Error(fmt.Sprintf("[TaskQueue-%d] Task %s failed: %v", workerID, task.TaskID, err))

		// 检查是否是超时错误
		if errors.Is(err, context.DeadlineExceeded) {
			q.updateTaskStatus(task.TaskID, model.TaskStatusFailed, 0, "Task timed out")
		} else {
			q.updateTaskStatus(task.TaskID, model.TaskStatusFailed, 0, err.Error())
		}
		q.updateTaskCompleteTime(task.TaskID, time.Now())
		return
	}

	// 更新为完成状态
	progress := 100
	if result != nil && result.Success {
		if q.isTaskCancelled(task.TaskID) {
			globals.Info(fmt.Sprintf("[TaskQueue-%d] Skip completing cancelled task %s", workerID, task.TaskID))
			return
		}
		q.updateTaskWithResult(task.TaskID, result, progress, time.Now())
		globals.Info(fmt.Sprintf("[TaskQueue-%d] Task %s completed successfully", workerID, task.TaskID))
	} else {
		errorMsg := "Generation failed"
		if result != nil {
			errorMsg = result.Error
		}
		q.updateTaskStatus(task.TaskID, model.TaskStatusFailed, 0, errorMsg)
		q.updateTaskCompleteTime(task.TaskID, time.Now())
	}
}

func (q *TaskQueue) registerActiveCancel(taskID string, cancel context.CancelFunc) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.activeCancels[taskID] = cancel
}

func (q *TaskQueue) unregisterActiveCancel(taskID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.activeCancels, taskID)
}

func (q *TaskQueue) CancelActiveTask(taskID string) bool {
	q.mu.RLock()
	cancel := q.activeCancels[taskID]
	q.mu.RUnlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (q *TaskQueue) isTaskCancelled(taskID string) bool {
	var status model.TaskStatus
	if err := globals.GraDBs["system"].Model(&model.GenerationTask{}).Select("status").Where("task_id = ?", taskID).Scan(&status).Error; err != nil {
		return false
	}
	return status == model.TaskStatusCancelled
}

// isRetryableError 判断错误是否可重试
func (q *TaskQueue) isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()

	// 网络错误通常可以重试
	retryableErrors := []string{
		"connection refused",
		"timeout",
		"deadline exceeded",
		"temporary failure",
		"rate limit",
		"service unavailable",
	}

	for _, retryable := range retryableErrors {
		if strings.Contains(strings.ToLower(errStr), retryable) {
			return true
		}
	}

	return false
}

// Enqueue 入队一个新任务
func (q *TaskQueue) Enqueue(task *model.GenerationTask) error {
	if task == nil {
		return fmt.Errorf("task is nil")
	}
	return q.EnqueueMany([]*model.GenerationTask{task})
}

// EnqueueMany 批量入队任务（原子检查容量）
func (q *TaskQueue) EnqueueMany(tasks []*model.GenerationTask) error {
	if len(tasks) == 0 {
		return fmt.Errorf("no tasks to enqueue")
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(tasks) > cap(q.queue)-len(q.queue) {
		return fmt.Errorf("task queue is full")
	}

	for _, task := range tasks {
		if task == nil {
			return fmt.Errorf("task is nil")
		}
		q.queue <- task
		globals.Info(fmt.Sprintf("[TaskQueue] Task %s enqueued, queue size: %d", task.TaskID, len(q.queue)))
	}
	return nil
}

// QueueLen 返回当前队列长度
func (q *TaskQueue) QueueLen() int {
	return len(q.queue)
}

// recoverPendingTasks 恢复未完成的任务
func (q *TaskQueue) recoverPendingTasks() {
	// 等待队列启动
	time.Sleep(2 * time.Second)

	// 从配置读取恢复参数
	cfg := globals.GraConf.Generator.TaskQueue
	recoverThreshold := cfg.RecoverThreshold
	if recoverThreshold <= 0 {
		recoverThreshold = 60 // 默认1小时
	}
	recoverLimit := cfg.RecoverLimit
	if recoverLimit <= 0 {
		recoverLimit = 100 // 默认最多恢复100个任务
	}
	expireHours := cfg.ExpireHours
	if expireHours <= 0 {
		expireHours = 24 // 默认24小时后过期
	}

	// 只恢复最近指定时间内的任务，避免恢复过期任务
	cutoff := time.Now().Add(-time.Duration(recoverThreshold) * time.Minute)

	var tasks []model.GenerationTask
	err := globals.GraDBs["system"].
		Where("status = ? AND created_at > ?", model.TaskStatusPending, cutoff).
		Order("created_at ASC").
		Limit(recoverLimit).
		Find(&tasks).Error

	if err != nil {
		globals.Error("[TaskQueue] Failed to recover pending tasks: " + err.Error())
		return
	}

	if len(tasks) > 0 {
		globals.Info(fmt.Sprintf("[TaskQueue] Recovering %d pending tasks (created after %s)", len(tasks), cutoff.Format(time.RFC3339)))

		recovered := 0
		for i := range tasks {
			taskCopy := tasks[i]
			select {
			case q.queue <- &taskCopy:
				globals.Info(fmt.Sprintf("[TaskQueue] Recovered task %s (created at %s)", taskCopy.TaskID, taskCopy.CreatedAt.Format(time.RFC3339)))
				recovered++
			case <-q.ctx.Done():
				globals.Info("[TaskQueue] Recovery cancelled by shutdown")
				return
			case <-time.After(5 * time.Second):
				globals.Error(fmt.Sprintf("[TaskQueue] Timeout recovering task %s, queue may be full", taskCopy.TaskID))
			}
		}

		globals.Info(fmt.Sprintf("[TaskQueue] Successfully recovered %d/%d tasks", recovered, len(tasks)))
	}

	// 标记过期任务为失败
	expiredCutoff := time.Now().Add(-time.Duration(expireHours) * time.Hour)
	expiredMsg := fmt.Sprintf("Task expired (not processed within %d hours; marked failed after service restart)", expireHours)
	var expiredTasks []model.GenerationTask
	if err := globals.GraDBs["system"].Where("status = ? AND created_at <= ?", model.TaskStatusPending, expiredCutoff).Find(&expiredTasks).Error; err != nil {
		globals.Error("[TaskQueue] Failed to load expired tasks: " + err.Error())
	}
	result := globals.GraDBs["system"].
		Model(&model.GenerationTask{}).
		Where("status = ? AND created_at <= ?", model.TaskStatusPending, expiredCutoff).
		Updates(map[string]interface{}{
			"status":       model.TaskStatusFailed,
			"error_msg":    expiredMsg,
			"completed_at": time.Now(),
		})

	if result.Error != nil {
		globals.Error("[TaskQueue] Failed to mark expired tasks: " + result.Error.Error())
	} else if result.RowsAffected > 0 {
		globals.Info(fmt.Sprintf("[TaskQueue] Marked %d expired tasks as failed", result.RowsAffected))
		for _, task := range expiredTasks {
			q.updateUsageRecordFailed(task.TaskID, expiredMsg)
			if err := refundCreditsForTask(task.TaskID); err != nil {
				globals.Error(fmt.Sprintf("[TaskQueue] Failed to refund expired task %s: %v", task.TaskID, err))
			}
			// Wake any long-poll handlers still watching the expired
			// task so they observe the terminal state immediately.
			PublishTaskEvent(task.TaskID)
		}
	}
}

// updateTaskStatus 更新任务状态
//
// Terminal transitions (Failed, Completed) are guarded by `status IN
// (Pending, Processing)` so a concurrent CancelTask cannot be silently
// clobbered by a worker that already finished processing. RowsAffected==0
// means someone else (e.g. CancelTask) already settled the row — we skip
// the post-update accounting hooks to keep the result idempotent and to
// avoid charging or refunding twice.
func (q *TaskQueue) updateTaskStatus(taskID string, status model.TaskStatus, progress int, errorMsg string) {
	updates := map[string]interface{}{
		"status":   status,
		"progress": progress,
	}
	if errorMsg != "" {
		updates["error_msg"] = errorMsg
	}

	query := globals.GraDBs["system"].Model(&model.GenerationTask{}).Where("task_id = ?", taskID)
	if isTerminalTaskStatus(status) {
		query = query.Where("status IN ?", []model.TaskStatus{model.TaskStatusPending, model.TaskStatusProcessing})
	}
	res := query.Updates(updates)
	if res.Error != nil {
		globals.Error(fmt.Sprintf("[TaskQueue] Failed to update task status: %v", res.Error))
		return
	}
	if isTerminalTaskStatus(status) && res.RowsAffected == 0 {
		globals.Info(fmt.Sprintf("[TaskQueue] Skip terminal transition to %s for %s: row already settled", status.String(), taskID))
		return
	}
	// Wake long-poll handlers — covers all the worker's status
	// transitions (Processing → Completed / Failed / Cancelled).
	PublishTaskEvent(taskID)

	if status == model.TaskStatusFailed {
		q.updateUsageRecordFailed(taskID, errorMsg)
		if err := refundCreditsForTask(taskID); err != nil {
			globals.Error(fmt.Sprintf("[TaskQueue] Failed to refund failed task %s: %v", taskID, err))
		}
	}
	if status == model.TaskStatusFailed || status == model.TaskStatusCancelled {
		q.tryMergeSplitBatch(taskID)
	}
}

// isTerminalTaskStatus reports whether a status represents a settled row —
// terminal writes need extra guards (status + accounting) to stay idempotent
// against concurrent cancel / webhook retries.
func isTerminalTaskStatus(s model.TaskStatus) bool {
	return s == model.TaskStatusCompleted || s == model.TaskStatusFailed || s == model.TaskStatusCancelled
}

// updateTaskStartTime 更新任务开始时间
func (q *TaskQueue) updateTaskStartTime(taskID string, startTime time.Time) {
	if err := globals.GraDBs["system"].Model(&model.GenerationTask{}).Where("task_id = ?", taskID).Update("started_at", startTime).Error; err != nil {
		globals.Error(fmt.Sprintf("[TaskQueue] Failed to update task start time: %v", err))
	}
}

func getTaskCreditCost(task *model.GenerationTask) int {
	if task == nil {
		return 0
	}
	if task.CreditsUsed > 0 {
		return task.CreditsUsed
	}

	modelType := task.Model
	if task.RequestData != nil {
		if m, ok := task.RequestData["model"].(string); ok && m != "" {
			modelType = m
		}
	}

	return GetCreditCostByToolID(CreditCostParams{
		ToolID: task.ToolID,
		Model:  modelType,
		Params: task.RequestData,
	})
}

func isTaskRefunded(task *model.GenerationTask) bool {
	if task == nil || task.ResultData == nil {
		return false
	}
	if v, ok := task.ResultData["refunded"].(bool); ok && v {
		return true
	}
	return false
}

func refundCreditsForTask(taskID string) error {
	return globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		var task model.GenerationTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id = ?", taskID).First(&task).Error; err != nil {
			return err
		}
		if task.Status != model.TaskStatusFailed {
			return nil
		}
		if isTaskRefunded(&task) {
			return nil
		}

		creditCost := getTaskCreditCost(&task)
		if creditCost <= 0 {
			return nil
		}

		svc := accountsvc.NewCreditReservationService()
		reservation, err := svc.FindForSettlement(tx, task.UID, task.TaskID)
		if err != nil {
			return err
		}
		if reservation != nil {
			if err := svc.Release(tx, reservation.Id); err != nil {
				return err
			}
		}

		resultData := task.ResultData
		if resultData == nil {
			resultData = model.JSONMap{}
		}
		resultData["refunded"] = true
		resultData["refundCredits"] = creditCost

		if err := tx.Model(&model.GenerationTask{}).Where("id = ?", task.Id).Updates(map[string]interface{}{
			"result_data":  resultData,
			"credits_used": 0,
		}).Error; err != nil {
			return err
		}

		return nil
	})
}

// updateTaskCompleteTime 更新任务完成时间
func (q *TaskQueue) updateTaskCompleteTime(taskID string, completeTime time.Time) {
	if err := globals.GraDBs["system"].Model(&model.GenerationTask{}).Where("task_id = ?", taskID).Update("completed_at", completeTime).Error; err != nil {
		globals.Error(fmt.Sprintf("[TaskQueue] Failed to update task complete time: %v", err))
	}
}

// updateTaskWithResult 更新任务结果
func (q *TaskQueue) updateTaskWithResult(taskID string, result *GenerateImageResponse, progress int, completeTime time.Time) {
	resultData := model.JSONMap{
		"success":       result.Success,
		"imageUrls":     result.ImageURLs,
		"videoUrls":     result.VideoURLs,
		"thumbnailUrl":  result.ThumbnailURL,
		"providerJobId": result.ProviderJobID,
		"taskId":        result.TaskID,
		"creditsUsed":   result.CreditsUsed,
		"duration":      result.Duration,
	}
	if result.ResultMetadata != nil {
		resultData["resultMetadata"] = result.ResultMetadata
	}

	if len(result.ImageURLs) > 0 {
		resultData["imageUrls"] = result.ImageURLs
	}
	if len(result.VideoURLs) > 0 {
		resultData["videoUrls"] = result.VideoURLs
	}
	if strings.TrimSpace(result.ThumbnailURL) != "" {
		resultData["thumbnailUrl"] = result.ThumbnailURL
	}
	if strings.TrimSpace(result.ProviderJobID) != "" {
		resultData["providerJobId"] = result.ProviderJobID
	}
	resultJSON, _ := json.Marshal(resultData)

	updates := map[string]interface{}{
		"status":       model.TaskStatusCompleted,
		"progress":     progress,
		"result_data":  string(resultJSON),
		"credits_used": result.CreditsUsed,
		"duration_ms":  int(result.Duration),
		"completed_at": completeTime,
	}

	// Guard against overwriting a row that was just cancelled (or settled by
	// a duplicate completion path). If the row is no longer Pending/Processing
	// we skip the finalize + usage record so credits aren't charged against
	// a task the user already refunded.
	res := globals.GraDBs["system"].
		Model(&model.GenerationTask{}).
		Where("task_id = ? AND status IN ?", taskID, []model.TaskStatus{model.TaskStatusPending, model.TaskStatusProcessing}).
		Updates(updates)
	if res.Error != nil {
		globals.Error(fmt.Sprintf("[TaskQueue] Failed to update task result: %v", res.Error))
		return
	}
	if res.RowsAffected == 0 {
		globals.Info(fmt.Sprintf("[TaskQueue] Skip completing task %s: row already settled (cancelled or completed elsewhere)", taskID))
		return
	}
	// Wake long-poll handlers — terminal Completed transition with
	// the new result_data (urls, recordId, etc.) is what they're
	// waiting for.
	PublishTaskEvent(taskID)
	finalizeTaskReservation(taskID)
	q.updateUsageRecordSuccess(taskID, result)
	q.tryMergeSplitBatch(taskID)
}

// finalizeTaskReservation marks the task's reservation as finalized. The used
// amount equals the reserved amount — generator billing doesn't refund a diff
// (pricing is decided up front). The task owner is locked before the
// reservation, matching the sweeper's global Owner -> Reservation order.
func finalizeTaskReservation(taskID string) {
	err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		var task model.GenerationTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ?", taskID).First(&task).Error; err != nil {
			return err
		}
		svc := accountsvc.NewCreditReservationService()
		reservation, err := svc.FindForSettlement(tx, task.UID, task.TaskID)
		if err != nil {
			return err
		}
		if reservation == nil {
			// Legacy tasks may predate reservations. New tasks always have one;
			// the admission transaction enforces that invariant.
			return nil
		}
		return svc.Finalize(tx, reservation.Id, reservation.Reserved)
	})
	if err != nil {
		globals.Error(fmt.Sprintf("[TaskQueue] Failed to finalize reservation for task %s: %v", taskID, err))
	}
	if errors.Is(err, accountsvc.ErrReservationTTLExpired) || errors.Is(err, accountsvc.ErrReservationExpired) {
		globals.Warn(fmt.Sprintf("[TaskQueue] Task %s succeeded but reservation was already swept (expired) — credits leaked. Consider tightening TTL.", taskID))
	}
}

func (q *TaskQueue) updateTaskRuntimeMeta(taskID string, updates map[string]interface{}) {
	if strings.TrimSpace(taskID) == "" || len(updates) == 0 {
		return
	}
	var task model.GenerationTask
	if err := globals.GraDBs["system"].Select("id", "result_data").Where("task_id = ?", taskID).First(&task).Error; err != nil {
		return
	}
	resultData := task.ResultData
	if resultData == nil {
		resultData = model.JSONMap{}
	}
	for key, value := range updates {
		if value == nil {
			continue
		}
		resultData[key] = value
	}
	if err := globals.GraDBs["system"].Model(&model.GenerationTask{}).Where("id = ?", task.Id).Update("result_data", resultData).Error; err != nil {
		globals.Warn(fmt.Sprintf("[TaskQueue] Failed to update task runtime meta: %v", err))
	}
}

func (q *TaskQueue) updateUsageRecordFailed(_ string, _ string) {
	// usage_record 只记录成功消耗，失败任务不落库
	return
}

func (q *TaskQueue) updateUsageRecordSuccess(taskID string, result *GenerateImageResponse) {
	if result == nil {
		return
	}
	var task model.GenerationTask
	if err := globals.GraDBs["system"].Select("id", "uid", "tool_id", "record_id").Where("task_id = ?", taskID).First(&task).Error; err != nil {
		return
	}
	durationSeconds := 0
	if result != nil && result.Duration > 0 {
		durationSeconds = int(result.Duration / 1000)
	}
	_ = CreateUsageRecordTx(globals.GraDBs["system"], task.UID, task.ToolID, task.RecordID, result.CreditsUsed, model.STATUS_SUCCESS, durationSeconds, &UsageRecordMeta{
		ResultMetadata: result.ResultMetadata,
	})
}

// Process 处理任务
func (p *TaskProcessor) Process(ctx context.Context, task *model.GenerationTask) (*GenerateImageResponse, error) {
	// 解析请求数据
	var requestData model.TaskRequestData
	if task.RequestData != nil {
		requestDataBytes, _ := json.Marshal(task.RequestData)
		if err := json.Unmarshal(requestDataBytes, &requestData); err != nil {
			return nil, fmt.Errorf("failed to parse request data: %w", err)
		}
	} else {
		// 兼容旧格式：RequestData 为 nil 时，只使用 task.Model
		requestData = model.TaskRequestData{
			Model: task.Model,
		}
	}
	if strings.TrimSpace(requestData.Model) == "" {
		requestData.Model = task.Model
	}

	// 构建生成请求
	genReq := &GenerateImageRequest{
		UID:                   uint(task.UID),
		ToolID:                task.ToolID,
		Model:                 requestData.Model,
		Prompt:                requestData.Prompt,
		NegativePrompt:        requestData.NegativePrompt,
		AspectRatio:           requestData.AspectRatio,
		StylePreset:           requestData.StylePreset,
		Resolution:            requestData.Resolution,
		NumberOfImages:        requestData.NumberOfImages,
		Steps:                 requestData.Steps,
		CFGScale:              requestData.CFGScale,
		Sampler:               requestData.Sampler,
		Seed:                  requestData.Seed,
		Upscale:               requestData.Upscale,
		ReferenceImages:       requestData.ReferenceImages,
		ReferenceVideos:       requestData.ReferenceVideos,
		ReferenceAudios:       requestData.ReferenceAudios,
		RawRequestData:        task.RequestData,
		CreditCost:            task.CreditsUsed,
		SkipRecord:            requestData.SkipRecord,
		AssetBindings:         requestData.AssetBindings,
		Origin:                requestData.Origin,
		LineageParentRecordID: requestData.LineageParentRecordID,
	}
	if genReq.Origin == "" && task.RequestData != nil {
		if raw, ok := task.RequestData["origin"].(string); ok {
			genReq.Origin = strings.TrimSpace(raw)
		}
	}
	// JSONMap unmarshal can drop pointer-to-uint when the JSON went
	// through encoding/decoding cycles; fall back to the raw map so a
	// late-arriving lineage parent never gets dropped on the floor.
	if genReq.LineageParentRecordID == nil && task.RequestData != nil {
		if raw, ok := task.RequestData["lineageParentRecordId"]; ok {
			switch v := raw.(type) {
			case float64:
				if v > 0 {
					id := uint(v)
					genReq.LineageParentRecordID = &id
				}
			case int:
				if v > 0 {
					id := uint(v)
					genReq.LineageParentRecordID = &id
				}
			case uint:
				if v > 0 {
					genReq.LineageParentRecordID = &v
				}
			}
		}
	}

	// 转换参考图片格式
	if genReq.ReferenceImages == nil && task.RequestData != nil {
		if refImagesRaw, ok := task.RequestData["referenceImages"]; ok {
			if refImagesArray, ok := refImagesRaw.([]interface{}); ok {
				for _, item := range refImagesArray {
					if itemMap, ok := item.(map[string]interface{}); ok {
						genReq.ReferenceImages = append(genReq.ReferenceImages, model.ReferenceImageParam{
							ID:     getStringFromMap(itemMap, "id"),
							URL:    getStringFromMap(itemMap, "url"),
							Weight: getFloatFromMap(itemMap, "weight"),
						})
					}
				}
			}
		}
	}
	if genReq.ReferenceVideos == nil && task.RequestData != nil {
		genReq.ReferenceVideos = extractReferenceMediaFromMap(task.RequestData["referenceVideos"])
	}
	if genReq.ReferenceAudios == nil && task.RequestData != nil {
		genReq.ReferenceAudios = extractReferenceMediaFromMap(task.RequestData["referenceAudios"])
	}
	if genReq.NumberOfImages == 0 && task.RequestData != nil {
		if v, ok := task.RequestData["numberOfImages"].(float64); ok {
			genReq.NumberOfImages = int(v)
		}
	}
	if genReq.Resolution == "" && task.RequestData != nil {
		if v, ok := task.RequestData["resolution"].(string); ok {
			genReq.Resolution = v
		}
	}
	if !genReq.SkipRecord && task.RequestData != nil {
		if v, ok := task.RequestData["skipRecord"].(bool); ok {
			genReq.SkipRecord = v
		}
	}
	if task.RequestData != nil {
		if v, ok := task.RequestData["loraSlug"].(string); ok {
			genReq.Lora = strings.TrimSpace(v)
		}
	}

	modelCandidates := buildTaskModelCandidates(genReq.Model, task.RequestData)
	var (
		result      *GenerateImageResponse
		err         error
		lastErr     error
		lastFailure *GenerateImageResponse
	)
	for idx, candidateModel := range modelCandidates {
		attemptReq := *genReq
		attemptReq.Model = candidateModel
		attemptReq.RawRequestData = cloneRequestDataWithModel(task.RequestData, candidateModel)
		attemptReq.TaskID = task.TaskID
		attemptReq.OnProgress = func(progress int, meta map[string]interface{}) {
			if progress > 0 {
				p.updateProgress(task.TaskID, progress)
			}
			if len(meta) > 0 {
				if queue := GetGlobalTaskQueue(); queue != nil {
					queue.updateTaskRuntimeMeta(task.TaskID, meta)
				}
			}
		}
		attemptReq.OnProviderJob = func(jobID string, meta map[string]interface{}) {
			updates := map[string]interface{}{}
			if strings.TrimSpace(jobID) != "" {
				updates["providerJobId"] = jobID
			}
			for key, value := range meta {
				updates[key] = value
			}
			if len(updates) > 0 {
				if queue := GetGlobalTaskQueue(); queue != nil {
					queue.updateTaskRuntimeMeta(task.TaskID, updates)
				}
			}
		}

		result, err = p.generatorService.GenerateImage(ctx, &attemptReq)
		if err != nil {
			lastErr = err
			globals.Warn(fmt.Sprintf("[TaskQueue] Task %s model attempt %s failed with error: %v", task.TaskID, candidateModel, err))
			continue
		}
		if result != nil && result.Success {
			if idx > 0 {
				globals.Info(fmt.Sprintf("[TaskQueue] Task %s succeeded via fallback model %s (primary: %s)", task.TaskID, candidateModel, genReq.Model))
			}
			break
		}

		if result != nil {
			lastFailure = result
			globals.Warn(fmt.Sprintf("[TaskQueue] Task %s model attempt %s returned unsuccessful result: %s", task.TaskID, candidateModel, result.Error))
		}
	}
	if result == nil || !result.Success {
		if lastFailure != nil {
			result = lastFailure
		}
		if lastErr != nil {
			return nil, lastErr
		}
		if result == nil {
			return nil, fmt.Errorf("generation failed with no provider result")
		}
	}

	// 仅对未上报细粒度进度的任务补充模拟进度，避免覆盖工具自定义进度。
	if task.ToolID != model.TOOL_IMAGE_VECTORIZER {
		p.updateProgress(task.TaskID, 50)
		p.updateProgress(task.TaskID, 90)
	}

	return result, nil
}

func buildTaskModelCandidates(primary string, requestData model.JSONMap) []string {
	seen := map[string]struct{}{}
	candidates := make([]string, 0, 4)
	appendIfValid := func(raw string) {
		modelID := strings.TrimSpace(raw)
		if modelID == "" {
			return
		}
		if _, exists := seen[modelID]; exists {
			return
		}
		seen[modelID] = struct{}{}
		candidates = append(candidates, modelID)
	}

	appendIfValid(primary)
	if requestData != nil {
		if raw, ok := requestData["modelCandidates"]; ok {
			switch v := raw.(type) {
			case []string:
				for _, modelID := range v {
					appendIfValid(modelID)
				}
			case []interface{}:
				for _, item := range v {
					if modelID, ok := item.(string); ok {
						appendIfValid(modelID)
					}
				}
			}
		}
	}
	if len(candidates) == 0 {
		return []string{model.NANO_BANANA_2}
	}
	return candidates
}

func cloneRequestDataWithModel(source model.JSONMap, modelID string) model.JSONMap {
	if source == nil {
		return nil
	}
	cloned := make(model.JSONMap, len(source))
	for k, v := range source {
		cloned[k] = v
	}
	cloned["model"] = modelID
	return cloned
}

// updateProgress 更新任务进度
func (p *TaskProcessor) updateProgress(taskID string, progress int) {
	globals.GraDBs["system"].Model(&model.GenerationTask{}).Where("task_id = ?", taskID).Update("progress", progress)
}

// getStringFromMap 从 map 中安全获取字符串
func getStringFromMap(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// extractReferenceMediaFromMap 从 JSON 反序列化的任意值中抽取视频/音频参考
func extractReferenceMediaFromMap(raw interface{}) []model.ReferenceMediaParam {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]model.ReferenceMediaParam, 0, len(arr))
	for _, item := range arr {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		url := strings.TrimSpace(getStringFromMap(itemMap, "url"))
		if url == "" {
			continue
		}
		out = append(out, model.ReferenceMediaParam{
			ID:       getStringFromMap(itemMap, "id"),
			URL:      url,
			MimeType: getStringFromMap(itemMap, "mimeType"),
			FileName: getStringFromMap(itemMap, "fileName"),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// getFloatFromMap 从 map 中安全获取 float64
func getFloatFromMap(m map[string]interface{}, key string) float32 {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case float32:
			return val
		case float64:
			return float32(val)
		case int:
			return float32(val)
		}
	}
	return 1.0
}

// CreateTask 创建新任务
func CreateTask(uid int, toolID, modelID string, requestData model.JSONMap, creditsUsed int, meta *UsageRecordMeta) (*model.GenerationTask, error) {
	return CreateTaskTx(globals.GraDBs["system"], uid, toolID, modelID, requestData, creditsUsed, meta)
}

// CreateTaskTx 在事务中创建新任务
func CreateTaskTx(tx *gorm.DB, uid int, toolID, modelID string, requestData model.JSONMap, creditsUsed int, meta *UsageRecordMeta) (*model.GenerationTask, error) {
	task := &model.GenerationTask{
		TaskID:      generateTaskID(),
		UID:         uid,
		ToolID:      toolID,
		Model:       modelID,
		Status:      model.TaskStatusPending,
		Progress:    0,
		RequestData: requestData,
		CreditsUsed: creditsUsed,
	}

	if err := tx.Create(task).Error; err != nil {
		return nil, err
	}

	return task, nil
}

// generateTaskID 生成任务 ID (使用 UUID 避免碰撞)
func generateTaskID() string {
	return fmt.Sprintf("task_%s", uuid.New().String())
}

// GetTaskByID 根据 TaskID 获取任务
func GetTaskByID(taskID string) (*model.GenerationTask, error) {
	var task model.GenerationTask
	err := globals.GraDBs["system"].Where("task_id = ?", taskID).First(&task).Error
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// GetTasksByUID 获取用户的任务列表
func GetTasksByUID(uid int, page, limit int) ([]model.GenerationTask, int64, error) {
	var tasks []model.GenerationTask
	var total int64

	db := globals.GraDBs["system"].Model(&model.GenerationTask{}).Where("uid = ?", uid)

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * limit
	if err := db.Order("created_at DESC").Offset(offset).Limit(limit).Find(&tasks).Error; err != nil {
		return nil, 0, err
	}

	return tasks, total, nil
}

func GetRecentTasksByUID(uid int, limit int) ([]model.GenerationTask, error) {
	var tasks []model.GenerationTask

	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	err := globals.GraDBs["system"].
		Where("uid = ?", uid).
		Order("created_at DESC").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

func GetRecentTasksByUIDAndTool(uid int, toolIDs []string, limit int) ([]model.GenerationTask, error) {
	var tasks []model.GenerationTask

	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	db := globals.GraDBs["system"].Where("uid = ?", uid)
	if len(toolIDs) > 0 {
		db = db.Where("tool_id IN ?", toolIDs)
	}

	err := db.
		Order("created_at DESC").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

func GetRecentTasksByUIDAndToolPrefix(uid int, toolIDPrefix string, limit int) ([]model.GenerationTask, error) {
	var tasks []model.GenerationTask

	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	db := globals.GraDBs["system"].Where("uid = ?", uid)
	if strings.TrimSpace(toolIDPrefix) != "" {
		db = db.Where("tool_id LIKE ?", strings.TrimSpace(toolIDPrefix)+"%")
	}

	err := db.
		Order("created_at DESC").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

func getIdempotencyKeyFromRequestData(requestData model.JSONMap) string {
	if requestData == nil {
		return ""
	}
	raw, ok := requestData["idempotencyKey"]
	if !ok {
		return ""
	}
	key, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(key)
}

// GetRecentTasksByIdempotencyKey 根据 idempotency key 查找近期同请求任务，用于防重复扣费。
func GetRecentTasksByIdempotencyKey(uid int, idempotencyKey string, toolIDs []string, window time.Duration) ([]model.GenerationTask, error) {
	normalizedKey := strings.TrimSpace(idempotencyKey)
	if normalizedKey == "" {
		return nil, nil
	}

	if window <= 0 {
		window = 2 * time.Minute
	}
	cutoff := time.Now().Add(-window)

	var candidates []model.GenerationTask
	db := globals.GraDBs["system"].
		Where("uid = ? AND status IN (?)", uid, []int{
			int(model.TaskStatusPending),
			int(model.TaskStatusProcessing),
			int(model.TaskStatusCompleted),
		}).
		Where("created_at >= ?", cutoff)
	if len(toolIDs) > 0 {
		db = db.Where("tool_id IN ?", toolIDs)
	}
	if err := db.Order("created_at DESC").Limit(200).Find(&candidates).Error; err != nil {
		return nil, err
	}

	matched := make([]model.GenerationTask, 0, len(candidates))
	for _, task := range candidates {
		if getIdempotencyKeyFromRequestData(task.RequestData) == normalizedKey {
			matched = append(matched, task)
		}
	}

	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].Id < matched[j].Id
		}
		return matched[i].CreatedAt.Before(matched[j].CreatedAt)
	})

	return matched, nil
}

// GetActiveTasksByUID 获取用户进行中的任务
func GetActiveTasksByUID(uid int) ([]model.GenerationTask, error) {
	var tasks []model.GenerationTask
	err := globals.GraDBs["system"].
		Where("uid = ? AND status IN (?)", uid, []int{int(model.TaskStatusPending), int(model.TaskStatusProcessing)}).
		Order("created_at ASC").
		Find(&tasks).Error
	return tasks, err
}

func GetActiveTasksByUIDAndTool(uid int, toolIDs []string) ([]model.GenerationTask, error) {
	var tasks []model.GenerationTask
	db := globals.GraDBs["system"].
		Where("uid = ? AND status IN (?)", uid, []int{int(model.TaskStatusPending), int(model.TaskStatusProcessing)})
	if len(toolIDs) > 0 {
		db = db.Where("tool_id IN ?", toolIDs)
	}
	err := db.
		Order("created_at ASC").
		Find(&tasks).Error
	return tasks, err
}

func GetActiveTasksByUIDAndToolPrefix(uid int, toolIDPrefix string) ([]model.GenerationTask, error) {
	var tasks []model.GenerationTask
	db := globals.GraDBs["system"].
		Where("uid = ? AND status IN (?)", uid, []int{int(model.TaskStatusPending), int(model.TaskStatusProcessing)})
	if strings.TrimSpace(toolIDPrefix) != "" {
		db = db.Where("tool_id LIKE ?", strings.TrimSpace(toolIDPrefix)+"%")
	}
	err := db.
		Order("created_at ASC").
		Find(&tasks).Error
	return tasks, err
}

// UpdateTaskProgress 更新任务进度
//
// Each successful write fires a TaskEventBus publish so long-poll
// handlers waiting on this task surface the new progress value
// immediately instead of waiting for a 500ms re-tick. This is the
// hottest mutation site during normal generation — the bus shaves
// most of the steady-state DB-poll load.
func UpdateTaskProgress(taskID string, progress int, errorMsg string) error {
	updates := map[string]interface{}{
		"progress": progress,
	}
	if errorMsg != "" {
		updates["error_msg"] = errorMsg
		updates["status"] = model.TaskStatusFailed
	}
	if err := globals.GraDBs["system"].Model(&model.GenerationTask{}).Where("task_id = ?", taskID).Updates(updates).Error; err != nil {
		return err
	}
	PublishTaskEvent(taskID)
	return nil
}

// CancelTask 取消任务（只能取消 pending 状态的任务）
func CancelTask(taskID string, uid int) error {
	var task model.GenerationTask
	err := globals.GraDBs["system"].Where("task_id = ? AND uid = ?", taskID, uid).First(&task).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("task not found")
		}
		return err
	}

	if task.Status != model.TaskStatusPending {
		return fmt.Errorf("task cannot be cancelled, current status: %s", task.Status)
	}

	if err := globals.GraDBs["system"].Model(&task).Updates(map[string]interface{}{
		"status":       model.TaskStatusCancelled,
		"error_msg":    "Cancelled by user",
		"completed_at": time.Now(),
	}).Error; err != nil {
		return err
	}
	// User-initiated cancel is the most latency-sensitive transition
	// from a UX standpoint — wake any long-poll handlers immediately.
	PublishTaskEvent(taskID)
	return nil
}

// GetActiveTaskCount 获取指定用户的活跃任务数
func GetActiveTaskCount(uid int) (int64, error) {
	var count int64
	err := globals.GraDBs["system"].
		Model(&model.GenerationTask{}).
		Where("uid = ? AND status IN (?)", uid, []int{int(model.TaskStatusPending), int(model.TaskStatusProcessing)}).
		Count(&count).Error
	return count, err
}

// CleanupOldTasks 清理旧任务（保留最近 N 天）
func CleanupOldTasks(days int) error {
	cutoff := time.Now().AddDate(0, 0, -days)
	db := globals.GraDBs["system"]
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	query := tx.Model(&model.GenerationTask{}).
		Where("created_at < ? AND status IN (?)", cutoff, []int{int(model.TaskStatusCompleted), int(model.TaskStatusFailed)})

	var taskIDs []string
	if err := query.Pluck("task_id", &taskIDs).Error; err != nil {
		tx.Rollback()
		return err
	}

	if err := (&GenerationObjectService{}).markTaskObjectsOrphanWithDB(tx, taskIDs); err != nil {
		tx.Rollback()
		return err
	}

	result := tx.Where("created_at < ? AND status IN (?)", cutoff, []int{int(model.TaskStatusCompleted), int(model.TaskStatusFailed)}).
		Delete(&model.GenerationTask{})
	if result.Error != nil {
		tx.Rollback()
		return result.Error
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	globals.Info(fmt.Sprintf("[TaskQueue] Cleaned up %d old tasks", result.RowsAffected))
	return nil
}

// ParseImageURLs 从结果数据解析图片 URLs
func ParseImageURLs(resultData model.JSONMap) []string {
	if resultData == nil {
		return nil
	}
	urlsRaw, ok := resultData["imageUrls"]
	if !ok || urlsRaw == nil {
		return nil
	}

	imageURLs := make([]string, 0)
	appendURL := func(value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		imageURLs = append(imageURLs, value)
	}

	switch v := urlsRaw.(type) {
	case []string:
		for _, u := range v {
			appendURL(u)
		}
	case []interface{}:
		for _, u := range v {
			if urlStr, ok := u.(string); ok {
				appendURL(urlStr)
			}
		}
	case string:
		raw := strings.TrimSpace(v)
		if raw == "" {
			return nil
		}
		var parsed []string
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			for _, u := range parsed {
				appendURL(u)
			}
		} else {
			appendURL(raw)
		}
	}

	if len(imageURLs) == 0 {
		return nil
	}
	return imageURLs
}

// ParseVideoURLs 从结果数据解析视频 URLs
func ParseVideoURLs(resultData model.JSONMap) []string {
	if resultData == nil {
		return nil
	}
	urlsRaw, ok := resultData["videoUrls"]
	if !ok || urlsRaw == nil {
		return nil
	}

	videoURLs := make([]string, 0)
	appendURL := func(value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		videoURLs = append(videoURLs, value)
	}

	switch v := urlsRaw.(type) {
	case []string:
		for _, u := range v {
			appendURL(u)
		}
	case []interface{}:
		for _, u := range v {
			if urlStr, ok := u.(string); ok {
				appendURL(urlStr)
			}
		}
	case string:
		raw := strings.TrimSpace(v)
		if raw == "" {
			return nil
		}
		var parsed []string
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			for _, u := range parsed {
				appendURL(u)
			}
		} else {
			appendURL(raw)
		}
	}

	if len(videoURLs) == 0 {
		return nil
	}
	return videoURLs
}

func ParseThumbnailURL(resultData model.JSONMap) string {
	if resultData == nil {
		return ""
	}
	if v, ok := resultData["thumbnailUrl"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func ParseOutputURLs(resultData model.JSONMap) []string {
	output := make([]string, 0)
	output = append(output, ParseImageURLs(resultData)...)
	output = append(output, ParseVideoURLs(resultData)...)
	if len(output) == 0 {
		return nil
	}
	return output
}

func extractSplitBatchMeta(requestData model.JSONMap) (string, int, int) {
	if requestData == nil {
		return "", 0, 0
	}

	batchID, _ := requestData["splitBatchId"].(string)
	if strings.TrimSpace(batchID) == "" {
		return "", 0, 0
	}

	batchSize := 0
	switch v := requestData["splitBatchSize"].(type) {
	case int:
		batchSize = v
	case int64:
		batchSize = int(v)
	case float64:
		batchSize = int(v)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			batchSize = parsed
		}
	}

	batchIndex := 0
	switch v := requestData["splitBatchIndex"].(type) {
	case int:
		batchIndex = v
	case int64:
		batchIndex = int(v)
	case float64:
		batchIndex = int(v)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			batchIndex = parsed
		}
	}

	return batchID, batchSize, batchIndex
}

func parseJSONStringMap(raw string) map[string]interface{} {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]interface{}{}
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil && parsed != nil {
		return parsed
	}

	var wrapped string
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil {
		wrapped = strings.TrimSpace(wrapped)
		if wrapped != "" {
			if err := json.Unmarshal([]byte(wrapped), &parsed); err == nil && parsed != nil {
				return parsed
			}
		}
	}

	return map[string]interface{}{}
}

func parseJSONStringSlice(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	var parsed []string
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
		return parsed
	}

	var wrapped string
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil {
		wrapped = strings.TrimSpace(wrapped)
		if wrapped != "" {
			if err := json.Unmarshal([]byte(wrapped), &parsed); err == nil {
				return parsed
			}
		}
	}

	return nil
}

func appendUniqueURLStrings(existing []string, values ...string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		seen[item] = struct{}{}
	}
	for _, item := range values {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		existing = append(existing, item)
		seen[item] = struct{}{}
	}
	return existing
}

func appendMetadataListItem(existing interface{}, item map[string]interface{}) []map[string]interface{} {
	items := make([]map[string]interface{}, 0)
	switch value := existing.(type) {
	case []interface{}:
		for _, raw := range value {
			if entry, ok := raw.(map[string]interface{}); ok {
				items = append(items, entry)
			}
		}
	case []map[string]interface{}:
		items = append(items, value...)
	}

	taskID, _ := item["taskId"].(string)
	if strings.TrimSpace(taskID) != "" {
		for idx, existingItem := range items {
			existingTaskID, _ := existingItem["taskId"].(string)
			if existingTaskID == taskID {
				items[idx] = item
				return items
			}
		}
	}

	items = append(items, item)
	return items
}

func buildMergedRecord(taskList []model.GenerationTask, batchID string) *model.GenerationRecord {
	if len(taskList) == 0 {
		return nil
	}

	first := taskList[0]
	requestData := first.RequestData
	imageURLs := make([]string, 0, len(taskList))
	taskIDs := make([]string, 0, len(taskList))
	totalDuration := 0
	totalCredits := 0
	successCount := 0
	failedCount := 0

	sort.SliceStable(taskList, func(i, j int) bool {
		_, _, left := extractSplitBatchMeta(taskList[i].RequestData)
		_, _, right := extractSplitBatchMeta(taskList[j].RequestData)
		if left == right {
			return taskList[i].Id < taskList[j].Id
		}
		return left < right
	})

	for _, task := range taskList {
		if urls := ParseOutputURLs(task.ResultData); len(urls) > 0 {
			imageURLs = append(imageURLs, urls...)
		}
		taskIDs = append(taskIDs, task.TaskID)
		totalDuration += task.DurationMs
		totalCredits += task.CreditsUsed
		switch task.Status {
		case model.TaskStatusCompleted:
			successCount++
		case model.TaskStatusFailed, model.TaskStatusCancelled:
			failedCount++
		}
	}
	if len(imageURLs) == 0 {
		return nil
	}

	prompt, _ := requestData["prompt"].(string)
	negativePrompt, _ := requestData["negativePrompt"].(string)
	aspectRatio, _ := requestData["aspectRatio"].(string)
	stylePreset, _ := requestData["stylePreset"].(string)

	paramsMap := map[string]interface{}{}
	switch nested := requestData["params"].(type) {
	case map[string]interface{}:
		for key, value := range nested {
			paramsMap[key] = value
		}
	case model.JSONMap:
		for key, value := range nested {
			paramsMap[key] = value
		}
	}
	for key, value := range requestData {
		switch key {
		case "model", "prompt", "negativePrompt", "aspectRatio", "stylePreset", "referenceImages", "skipRecord", "splitBatchId", "splitBatchSize", "splitBatchIndex", "params", "idempotencyKey", "origin":
			continue
		default:
			paramsMap[key] = value
		}
	}
	origin, _ := requestData["origin"].(string)
	origin = strings.TrimSpace(origin)
	_, batchSize, _ := extractSplitBatchMeta(requestData)
	if batchSize > 0 {
		paramsMap["numberOfImages"] = batchSize
	}
	if FeatureTypeForToolID(first.ToolID) == model.TOOL_AVATAR_STUDIO {
		requestedCount := batchSize
		if requestedCount <= 0 {
			requestedCount = len(taskList)
		}
		unitCreditCost := 0
		if requestedCount > 0 {
			unitCreditCost = totalCredits / requestedCount
		}
		if unitCreditCost <= 0 && totalCredits > 0 {
			unitCreditCost = totalCredits
		}
		if unitCreditCost <= 0 {
			unitCreditCost = 1
		}
		paramsMap["billingSnapshot"] = map[string]interface{}{
			"unitCreditCost":      unitCreditCost,
			"requestedCount":      requestedCount,
			"reservedCredits":     totalCredits,
			"successCount":        requestedCount,
			"failedCount":         0,
			"finalChargedCredits": totalCredits,
			"refundCredits":       0,
			"billingStatus":       "settled",
		}
		if inputSnapshot, ok := paramsMap["inputSnapshot"].(map[string]interface{}); ok {
			inputSnapshot["numberOfImages"] = requestedCount
			paramsMap["inputSnapshot"] = inputSnapshot
		}
		if inputSnapshot, ok := paramsMap["inputSnapshot"].(model.JSONMap); ok {
			inputSnapshot["numberOfImages"] = requestedCount
			paramsMap["inputSnapshot"] = inputSnapshot
		}
	}
	paramsJSON, _ := json.Marshal(paramsMap)
	if len(paramsJSON) == 0 {
		paramsJSON = []byte("{}")
	}

	inputJSON := []byte("{}")
	if ref, ok := requestData["referenceImages"]; ok {
		if buf, err := json.Marshal(map[string]interface{}{"referenceImages": ref}); err == nil && len(buf) > 0 {
			inputJSON = buf
		}
	}

	imagesJSON, _ := json.Marshal(imageURLs)
	if len(imagesJSON) == 0 {
		imagesJSON = []byte("[]")
	}

	metadataJSON, _ := json.Marshal(map[string]interface{}{
		"merged":       true,
		"batchId":      batchID,
		"sourceTask":   taskIDs,
		"successCount": successCount,
		"failedCount":  failedCount,
		"status": func() string {
			if failedCount == 0 {
				return "completed"
			}
			return "partial"
		}(),
	})
	if len(metadataJSON) == 0 {
		metadataJSON = []byte("{}")
	}

	return &model.GenerationRecord{
		UID:            first.UID,
		ToolID:         first.ToolID,
		Model:          first.Model,
		Prompt:         prompt,
		NegativePrompt: negativePrompt,
		StylePreset:    stylePreset,
		AspectRatio:    aspectRatio,
		Params:         string(paramsJSON),
		InputFiles:     string(inputJSON),
		ResultImages:   string(imagesJSON),
		ResultMetadata: string(metadataJSON),
		Status:         1,
		DurationMs:     totalDuration,
		CreditsUsed:    totalCredits,
		BatchID:        batchID,
		Origin:         origin,
	}
}

func (q *TaskQueue) tryMergeSplitBatch(taskID string) {
	var sourceTask model.GenerationTask
	if err := globals.GraDBs["system"].
		Select("id", "uid", "task_id", "request_data", "status").
		Where("task_id = ?", taskID).
		First(&sourceTask).Error; err != nil {
		return
	}

	batchID, batchSize, _ := extractSplitBatchMeta(sourceTask.RequestData)
	if batchID == "" {
		return
	}
	if batchSize <= 1 {
		return
	}

	pattern := fmt.Sprintf("%%%s%%", batchID)
	// Collect taskIDs for post-commit event publish. Populated inside
	// the tx; published only on successful commit so rollback paths
	// don't wake handlers with stale state.
	var mergedTaskIDs []string
	err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		var rawTasks []model.GenerationTask
		if err := tx.
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("uid = ? AND request_data LIKE ?", sourceTask.UID, pattern).
			Order("id ASC").
			Find(&rawTasks).Error; err != nil {
			return err
		}

		batchTasks := make([]model.GenerationTask, 0, len(rawTasks))
		for _, task := range rawTasks {
			id, _, _ := extractSplitBatchMeta(task.RequestData)
			if id == batchID {
				batchTasks = append(batchTasks, task)
			}
		}
		if len(batchTasks) < batchSize {
			return nil
		}
		for _, task := range batchTasks {
			if !task.IsFinal() {
				return nil
			}
		}
		for _, task := range batchTasks {
			if task.RecordID > 0 {
				return nil
			}
		}

		record := buildMergedRecord(batchTasks, batchID)
		if record == nil {
			return nil
		}
		if err := tx.Create(record).Error; err != nil {
			return err
		}
		if err := assetLedgerService.New().SyncGenerationInputsWithDB(tx, record); err != nil {
			globals.Warn(fmt.Sprintf("[TaskQueue] Failed to sync generation input ledger for merged record %d: %v", record.Id, err))
		}
		for _, task := range batchTasks {
			resultData := task.ResultData
			if resultData == nil {
				resultData = model.JSONMap{}
			}
			resultData["mergedRecordId"] = record.Id
			if err := tx.Model(&model.GenerationTask{}).
				Where("id = ?", task.Id).
				Updates(map[string]interface{}{
					"record_id":   record.Id,
					"result_data": resultData,
				}).Error; err != nil {
				return err
			}
			mergedTaskIDs = append(mergedTaskIDs, task.TaskID)
		}
		return nil
	})
	if err != nil {
		globals.Warn(fmt.Sprintf("[TaskQueue] Failed to merge split batch %s: %v", batchID, err))
		return
	}
	// Wake any long-poll handlers waiting on tasks in the batch — the
	// merge wrote a fresh record_id that the response payload depends
	// on, so subscribers should observe the new value immediately.
	for _, taskID := range mergedTaskIDs {
		PublishTaskEvent(taskID)
	}
}

// GetImageURLsFromTask 从任务获取图片 URLs
func GetImageURLsFromTask(task *model.GenerationTask) []string {
	return ParseOutputURLs(task.ResultData)
}

// TaskToResponseDTO 转换任务为响应 DTO.
func TaskToResponseDTO(task *model.GenerationTask) map[string]interface{} {
	return TaskToResponseDTOWithContext(context.Background(), task)
}

// TaskToResponseDTOWithContext converts a task into the API DTO while allowing
// request cancellation to stop object URL resolution work.
func TaskToResponseDTOWithContext(ctx context.Context, task *model.GenerationTask) map[string]interface{} {
	if ctx == nil {
		ctx = context.Background()
	}
	(&GenerationObjectService{}).ResolveTaskDownloadURLs(ctx, task)

	response := map[string]interface{}{
		"taskId":      task.TaskID,
		"recordId":    task.RecordID,
		"status":      task.Status.String(),
		"progress":    task.Progress,
		"createdAt":   task.CreatedAt,
		"startedAt":   task.StartedAt,
		"completedAt": task.CompletedAt,
		"toolId":      task.ToolID,
		"model":       task.Model,
		"creditsUsed": task.CreditsUsed,
		"durationMs":  task.DurationMs,
		"requestData": task.RequestData,
	}

	if task.ResultData != nil {
		providerStatus := ""
		if raw, ok := task.ResultData["providerStatus"].(string); ok {
			providerStatus = strings.TrimSpace(raw)
		}
		if providerStatus == "" {
			if raw, ok := task.ResultData["status"].(string); ok {
				providerStatus = strings.TrimSpace(raw)
			}
		}
		if providerStatus != "" {
			response["providerStatus"] = providerStatus
		}
		if raw, ok := task.ResultData["providerJobId"].(string); ok {
			if providerJobID := strings.TrimSpace(raw); providerJobID != "" {
				response["providerJobId"] = providerJobID
			}
		}
	}

	if task.Status == model.TaskStatusCompleted && task.ResultData != nil {
		if urls := ParseImageURLs(task.ResultData); len(urls) > 0 {
			response["imageUrls"] = urls
		}
		if urls := ParseVideoURLs(task.ResultData); len(urls) > 0 {
			response["videoUrls"] = urls
		}
		if urls := ParseOutputURLs(task.ResultData); len(urls) > 0 {
			response["outputUrls"] = urls
		}
		if thumbnailURL := ParseThumbnailURL(task.ResultData); thumbnailURL != "" {
			response["thumbnailUrl"] = thumbnailURL
		}
		if metadata, ok := task.ResultData["resultMetadata"]; ok && metadata != nil {
			response["resultMetadata"] = metadata
		}
	}

	if task.Status == model.TaskStatusFailed && task.ErrorMsg != "" {
		response["error"] = task.ErrorMsg
		response["errorCode"] = ClassifyTaskErrorCode(task.ErrorMsg)
	}
	if task.Status == model.TaskStatusCancelled {
		response["errorCode"] = ClassifyTaskErrorCode(task.ErrorMsg)
	}

	return response
}

// GetToolIDFromModelStr 从模型字符串获取工具 ID
func GetToolIDFromModelStr(modelStr string) string {
	normalized := strings.ToLower(strings.TrimSpace(modelStr))
	if normalized == "" {
		return model.TOOL_IMAGE_GENERATOR
	}
	if toolID := model.GetToolIDFromModel(normalized); toolID != normalized {
		return toolID
	}
	switch {
	case strings.Contains(normalized, "avatar"):
		return model.TOOL_AVATAR_STUDIO
	case strings.Contains(normalized, "upscaler"):
		return model.TOOL_IMAGE_UPSCALER
	case strings.Contains(normalized, "remover"):
		return model.TOOL_BACKGROUND_REMOVER
	case strings.Contains(normalized, "vectorizer"):
		return model.TOOL_IMAGE_VECTORIZER
	case strings.Contains(normalized, "video"):
		return model.TOOL_VIDEO_GENERATOR
	case strings.Contains(normalized, "pro"):
		return model.TOOL_IMAGE_GENERATOR
	default:
		return model.TOOL_IMAGE_GENERATOR
	}
}

func isImageGeneratorToolID(toolID string) bool {
	switch toolID {
	case model.TOOL_IMAGE_GENERATOR, model.NANO_BANANA, model.NANO_BANANA_2, model.NANO_BANANA_PRO:
		return true
	default:
		return false
	}
}

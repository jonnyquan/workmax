package initialize

import (
	"sync"

	"server/api/callback"
	"server/globals"
	"server/scheduler"

	"github.com/robfig/cron"
)

// CronRuntime owns the stop signal and completion barrier for one initialized
// cron graph. Stop is safe to call repeatedly or concurrently and returns only
// after every stoppable scheduler in that graph has exited.
type CronRuntime struct {
	stopOnce sync.Once
	stopChan chan struct{}
	doneChan chan struct{}
}

func newCronRuntime() *CronRuntime {
	return &CronRuntime{
		stopChan: make(chan struct{}),
		doneChan: make(chan struct{}),
	}
}

func (runtime *CronRuntime) Stop() {
	if runtime == nil || runtime.stopChan == nil || runtime.doneChan == nil {
		return
	}
	runtime.stopOnce.Do(func() { close(runtime.stopChan) })
	<-runtime.doneChan
}

// CronInitV2 initializes the cron graph with the new framework. The runtime
// handle is returned only after its shutdown waiter is installed, so callers
// never observe a partially published stop channel or scheduler graph.
func CronInitV2() *CronRuntime {
	runtime := newCronRuntime()
	if !globals.GraConf.System.Cron.Enable {
		close(runtime.doneChan)
		return runtime
	}

	globals.Info("Initializing cron services with new framework support...")
	c := cron.New()

	// 启动邮件自动化定时任务
	go scheduler.StartEmailAutomationScheduler()
	globals.Info("Email automation scheduler started")

	// 启动标签统计定时任务（每天凌晨3点更新）
	var tagStatsScheduler *scheduler.TagStatsScheduler
	if globals.GraConf.System.Cron.TagStats {
		tagStatsScheduler = scheduler.NewTagStatsScheduler()
		tagStatsScheduler.Start()
		globals.Info("Tag stats scheduler started")
	}

	// 启动生成对象清理定时任务（每小时检查一次 orphan 对象）
	var generationObjectCleanupScheduler *scheduler.GenerationObjectCleanupScheduler
	if globals.GraConf.System.Cron.GenerationObjectCleanup {
		generationObjectCleanupScheduler = scheduler.NewGenerationObjectCleanupScheduler()
		generationObjectCleanupScheduler.Start()
		globals.Info("Generation object cleanup scheduler started")
	}

	// 启动积分预留过期清理（每 5 分钟扫一次 reserved+expired 行，
	// 退积分 + 标记 expired，防止 handler 崩溃后积分被永久卡住）
	var creditReservationSweeper *scheduler.CreditReservationSweeper
	if globals.GraConf.System.Cron.CreditReservationSweeper {
		creditReservationSweeper = scheduler.NewCreditReservationSweeper()
		creditReservationSweeper.Start()
		globals.Info("Credit reservation sweeper started")
	}

	// Reconcile signature-verified provider events that were durably
	// accepted but could not finish in the inline webhook request. This is
	// opt-in while the durable commerce inbox is rolled out.
	var commerceEventReconciler *scheduler.CommerceProviderEventReconciler
	if globals.GraConf.System.Cron.CommerceEventReconciler {
		var err error
		commerceEventReconciler, err = scheduler.NewCommerceProviderEventReconciler(
			globals.GraDBs["system"],
			callback.NewStripeProviderEventProcessor(),
		)
		if err != nil {
			// Constructor errors can describe connection internals. Keep the
			// startup log payload-free and leave the optional worker disabled.
			globals.Error("Commerce provider event reconciler is unavailable")
			commerceEventReconciler = nil
		} else {
			commerceEventReconciler.Start()
			globals.Info("Commerce provider event reconciler started")
		}
	}

	// 启动 tombstone 90 天 GC（P1.A.5c）。每小时一轮，分批删除
	// w_workagent_tombstone 中 deleted_at < now-90d 的行。
	// 不加配置开关：tombstone 表无界增长本身就是一个安全问题
	// (持续累积会拖慢 sync endpoint 的 query)，没有"我不想跑 GC"
	// 的合理场景。其他 sweeper 是因为功能可关才有 enable 开关。
	tombstoneSweeper := scheduler.NewTombstoneSweeper()
	tombstoneSweeper.Start()
	globals.Info("Tombstone sweeper started")

	// 订阅会员月度 credits 补齐。读 quota 必须保持纯读；订单/webhook 发放
	// 是主路径，这个 scheduler 负责漏发/跨周期兜底。
	subscriptionCreditsScheduler := scheduler.NewSubscriptionCreditsScheduler()
	subscriptionCreditsScheduler.Start()
	globals.Info("Subscription credits scheduler started")

	c.Start()
	globals.Info("Cron services initialized successfully with new framework support")

	go func() {
		defer close(runtime.doneChan)
		<-runtime.stopChan

		c.Stop()
		globals.Info("Cron scheduler stopped")

		if tagStatsScheduler != nil {
			tagStatsScheduler.Stop()
			globals.Info("Tag stats scheduler stopped")
		}
		if generationObjectCleanupScheduler != nil {
			generationObjectCleanupScheduler.Stop()
			globals.Info("Generation object cleanup scheduler stopped")
		}
		if creditReservationSweeper != nil {
			creditReservationSweeper.Stop()
			globals.Info("Credit reservation sweeper stopped")
		}
		if commerceEventReconciler != nil {
			commerceEventReconciler.Stop()
			globals.Info("Commerce provider event reconciler stopped")
		}
		if tombstoneSweeper != nil {
			tombstoneSweeper.Stop()
			globals.Info("Tombstone sweeper stopped")
		}
		if subscriptionCreditsScheduler != nil {
			subscriptionCreditsScheduler.Stop()
			globals.Info("Subscription credits scheduler stopped")
		}
	}()

	return runtime
}

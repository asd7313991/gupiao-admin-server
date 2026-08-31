package cron

import (
	"time"

	"github.com/go-co-op/gocron/v2"
	"go.uber.org/zap"
)

var scheduler gocron.Scheduler

// InitCronJobs 初始化所有定时任务
func InitCronJobs() {
	var err error
	location, locationErr := time.LoadLocation("Asia/Shanghai")
	if locationErr != nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	// 所有交易相关任务统一按北京时间调度。
	scheduler, err = gocron.NewScheduler(gocron.WithLocation(location))
	if err != nil {
		zap.L().Error("创建定时任务调度器失败", zap.Error(err))
		return
	}

	// 初始化用户缓存定时任务
	InitUserCacheJob()
	InitStockSyncJob()
	InitNewsCollectJob()
	InitLimitOrderMatchJob()
	InitManagementFeeJob()
	InitMarginCallJob()
	// 启动调度器
	scheduler.Start()

	zap.L().Info("定时任务调度器已启动")
}

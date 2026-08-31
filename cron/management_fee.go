package cron

import (
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"go.uber.org/zap"

	mobile "api-server/api/app/v1/open/mobile"
)

var managementFeeLock sync.Mutex

func InitManagementFeeJob() {
	job, err := scheduler.NewJob(
		gocron.CronJob("0 0 * * *", false),
		gocron.NewTask(runManagementFee),
	)
	if err != nil {
		zap.L().Error("创建每日持仓管理费任务失败", zap.Error(err))
		return
	}
	zap.L().Info("每日持仓管理费任务已创建", zap.String("jobID", job.ID().String()), zap.String("schedule", "00:00 Asia/Shanghai"))
}

func runManagementFee() {
	if !managementFeeLock.TryLock() {
		return
	}
	defer managementFeeLock.Unlock()
	processed, forced, err := mobile.ProcessDailyManagementFees(time.Now())
	if err != nil {
		zap.L().Error("每日持仓管理费处理失败", zap.Error(err))
		return
	}
	zap.L().Info("每日持仓管理费处理完成", zap.Int("processed", processed), zap.Int("forcedClose", forced))
}

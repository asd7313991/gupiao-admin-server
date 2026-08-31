package cron

import (
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"go.uber.org/zap"

	mobile "api-server/api/app/v1/open/mobile"
)

var marginCallLock sync.Mutex

func InitMarginCallJob() {
	job, err := scheduler.NewJob(
		gocron.DurationJob(time.Minute),
		gocron.NewTask(runMarginCalls),
	)
	if err != nil {
		zap.L().Error("创建风险补仓检查任务失败", zap.Error(err))
		return
	}
	zap.L().Info("风险补仓检查任务已创建，每分钟执行一次", zap.String("jobID", job.ID().String()))
}

func runMarginCalls() {
	if !marginCallLock.TryLock() {
		return
	}
	defer marginCallLock.Unlock()
	supplements, forced, err := mobile.ProcessMarginCalls(time.Now())
	if err != nil {
		zap.L().Error("风险补仓检查失败", zap.Error(err))
		return
	}
	if supplements > 0 || forced > 0 {
		zap.L().Info("风险补仓处理完成", zap.Int("supplements", supplements), zap.Int("forcedClose", forced))
	}
}

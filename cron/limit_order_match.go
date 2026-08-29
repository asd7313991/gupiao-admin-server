package cron

import (
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"go.uber.org/zap"

	mobile "api-server/api/app/v1/open/mobile"
)

var limitOrderMatchLock sync.Mutex

func InitLimitOrderMatchJob() {
	job, err := scheduler.NewJob(
		gocron.DurationJob(time.Minute),
		gocron.NewTask(runLimitOrderMatch),
	)
	if err != nil {
		zap.L().Error("创建限价委托撮合任务失败", zap.Error(err))
		return
	}
	zap.L().Info("限价委托撮合任务已创建", zap.String("jobID", job.ID().String()))
}

func runLimitOrderMatch() {
	if !limitOrderMatchLock.TryLock() {
		return
	}
	defer limitOrderMatchLock.Unlock()

	matched, err := mobile.MatchPendingLimitOrders()
	if err != nil {
		zap.L().Warn("限价委托撮合失败", zap.Error(err))
		return
	}
	if matched > 0 {
		zap.L().Info("限价委托撮合完成", zap.Int("matched", matched))
	}
}

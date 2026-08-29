package cron

import (
	"context"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"go.uber.org/zap"

	"api-server/config"
	"api-server/db/pgdb"
	newsdomain "api-server/domain/news"
)

var newsCollectLock sync.Mutex

func InitNewsCollectJob() {
	if !config.NewsCollectionEnabled {
		zap.L().Info("新闻采集任务已禁用")
		return
	}

	cronExpr := config.NewsCollectionCron
	if cronExpr == "" {
		cronExpr = "*/10 * * * *"
	}

	job, err := scheduler.NewJob(
		gocron.CronJob(cronExpr, false),
		gocron.NewTask(runNewsCollect),
	)
	if err != nil {
		zap.L().Error("创建新闻采集任务失败", zap.Error(err))
		return
	}

	zap.L().Info("新闻采集任务已创建", zap.String("cron", cronExpr), zap.String("jobID", job.ID().String()))
}

func runNewsCollect() {
	if !newsCollectLock.TryLock() {
		return
	}
	defer newsCollectLock.Unlock()

	if !config.NewsCollectionEnabled {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.NewsRequestTimeoutMS)*time.Millisecond)
	defer cancel()

	service := newsdomain.NewCollectionService(pgdb.GetClient())
	stats, err := service.CollectEnabledSources(ctx, "cron")
	if err != nil {
		zap.L().Warn("新闻采集任务执行失败", zap.Error(err))
		return
	}
	zap.L().Info("新闻采集任务执行完成",
		zap.Int("fetched", stats.FetchedCount),
		zap.Int("inserted", stats.InsertedCount),
		zap.Int("updated", stats.UpdatedCount),
		zap.Int("duplicate", stats.DuplicateCount),
		zap.Int("failed", stats.FailedCount),
	)
}

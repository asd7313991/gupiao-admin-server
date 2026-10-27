package cron

import (
	"context"
	"net/http"
	"sync"
	"time"

	"api-server/db/pgdb"
	"api-server/util/exchangerate"
	"github.com/go-co-op/gocron/v2"
	"go.uber.org/zap"
)

var exchangeRateSync sync.Mutex

func InitExchangeRateJob() {
	if _, err := scheduler.NewJob(gocron.DurationJob(5*time.Minute), gocron.NewTask(runExchangeRateSync),
		gocron.WithStartAt(gocron.WithStartImmediately())); err != nil {
		zap.L().Error("创建每日港币汇率同步任务失败", zap.Error(err))
	}
}

func runExchangeRateSync() {
	if !exchangeRateSync.TryLock() {
		return
	}
	defer exchangeRateSync.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Now()
	row, err := exchangerate.Fetch(ctx, &http.Client{Timeout: 12 * time.Second}, exchangerate.Endpoint, now)
	if err == nil {
		err = exchangerate.Save(pgdb.GetClient().WithContext(ctx), row, time.Now())
	}
	if err != nil {
		zap.L().Warn("港币每日汇率同步失败，未达到应公布日期时暂停新兑换", zap.Error(err))
		return
	}
	zap.L().Info("港币每日汇率同步完成", zap.String("rate_date", row.RateDate), zap.Float64("rate", row.Rate))
}

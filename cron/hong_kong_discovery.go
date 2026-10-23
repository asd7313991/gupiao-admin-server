package cron

import (
	"errors"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"go.uber.org/zap"

	stock "api-server/api/app/v1/private/admin/platform/stock"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

var hongKongDiscovery struct {
	sync.Mutex
	attempt time.Time
	success time.Time
}

func InitHongKongDiscoveryJob() {
	if _, err := scheduler.NewJob(gocron.DurationJob(time.Minute), gocron.NewTask(runHongKongDiscovery)); err != nil {
		zap.L().Error("创建港股名单自动补齐任务失败", zap.Error(err))
	}
}

func hongKongDiscoveryDue(now, attempt, success time.Time) bool {
	return (success.IsZero() || now.Sub(success) >= 24*time.Hour) &&
		(attempt.IsZero() || now.Sub(attempt) >= 10*time.Minute)
}

func runHongKongDiscovery() {
	if !hongKongDiscovery.TryLock() {
		return
	}
	defer hongKongDiscovery.Unlock()
	if !hongKongDiscoveryDue(time.Now(), hongKongDiscovery.attempt, hongKongDiscovery.success) {
		return
	}
	var row system.AppSystemSetting
	if err := pgdb.GetClient().First(&row).Error; err != nil {
		zap.L().Warn("读取港股名单同步配置失败", zap.Error(err))
		return
	}
	settings, err := parseStockSyncSettings(row.Config)
	if err != nil {
		zap.L().Warn("港股名单同步配置无效", zap.Error(err))
		return
	}
	if !settings.StockSync.Enabled {
		return
	}
	err = stock.SyncHongKongData()
	if errors.Is(err, stock.ErrHongKongSyncRunning) {
		return
	}
	hongKongDiscovery.attempt = time.Now()
	if err != nil {
		zap.L().Warn("港股名单自动补齐失败，10分钟后重试", zap.Error(err))
		return
	}
	hongKongDiscovery.success = time.Now()
	zap.L().Info("港股名单自动补齐完成，24小时后再次检查")
}

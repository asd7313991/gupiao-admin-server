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

var hongKongQuoteLock sync.Mutex
var lastHongKongQuoteAttempt time.Time

func InitHongKongQuoteJob() {
	if _, err := scheduler.NewJob(gocron.DurationJob(10*time.Second), gocron.NewTask(runHongKongQuotesIfDue)); err != nil {
		zap.L().Error("创建港股报价自动更新任务失败", zap.Error(err))
	}
}

func runHongKongQuotesIfDue() {
	if !hongKongQuoteLock.TryLock() {
		return
	}
	defer hongKongQuoteLock.Unlock()
	var row system.AppSystemSetting
	if err := pgdb.GetClient().First(&row).Error; err != nil {
		zap.L().Warn("读取港股自动同步配置失败", zap.Error(err))
		return
	}
	settings, err := parseStockSyncSettings(row.Config)
	if err != nil {
		zap.L().Warn("港股自动同步配置无效", zap.Error(err))
		return
	}
	if !settings.StockSync.Enabled {
		return
	}
	interval := hongKongQuoteInterval(time.Now(), settings.StockSync)
	if time.Since(lastHongKongQuoteAttempt) < time.Duration(interval)*time.Second {
		return
	}
	lastHongKongQuoteAttempt = time.Now()
	count, err := stock.RefreshHongKongQuotes(settings.StockSync.MaxSyncRows)
	if errors.Is(err, stock.ErrHongKongSyncRunning) {
		return
	}
	if err != nil {
		zap.L().Warn("港股报价自动更新失败", zap.Error(err))
		return
	}
	zap.L().Info("港股报价自动更新完成", zap.Int("count", count), zap.Int("interval_seconds", interval))
}

func hongKongQuoteInterval(now time.Time, settings stockSyncConfig) int {
	interval := settings.OffHoursIntervalSecs
	if isTradingTime(now, "09:30:00", "12:00:00", "13:00:00", "16:00:00") {
		interval = settings.TradingIntervalSecs
	}
	if interval <= 0 {
		return 600
	}
	return interval
}

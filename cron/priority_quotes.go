package cron

import (
	"time"

	"github.com/go-co-op/gocron/v2"
	"go.uber.org/zap"

	stock "api-server/api/app/v1/private/admin/platform/stock"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

func InitPriorityQuoteJob() {
	if _, err := scheduler.NewJob(gocron.DurationJob(5*time.Second), gocron.NewTask(runPriorityQuotes)); err != nil {
		zap.L().Error("创建重点行情高频任务失败", zap.Error(err))
	}
}

func priorityQuoteInterval(now time.Time, market string, settings systemSettings) int {
	trading := isTradingTime(now, settings.Trade.MorningStart, settings.Trade.MorningEnd, settings.Trade.AfternoonStart, settings.Trade.AfternoonEnd)
	if market == "HK" {
		trading = isTradingTime(now, "09:30:00", "12:00:00", "13:00:00", "16:00:00")
	}
	if trading {
		return 5
	}
	return 60
}

func runPriorityQuotes() {
	var row system.AppSystemSetting
	if err := pgdb.GetClient().First(&row).Error; err != nil {
		stock.PriorityConfigError(err)
		return
	}
	settings, err := parseStockSyncSettings(row.Config)
	if err != nil {
		stock.PriorityConfigError(err)
		return
	}
	stock.SetPrioritySyncEnabled(settings.StockSync.Enabled)
	if !settings.StockSync.Enabled {
		return
	}
	for _, market := range []string{"A", "HK"} {
		// 任务内部按市场防重入、记录错误并退避，不影响全市场同步。
		_ = stock.RefreshPriorityQuotes(pgdb.GetClient(), market, time.Now(), priorityQuoteInterval(time.Now(), market, settings))
	}
}

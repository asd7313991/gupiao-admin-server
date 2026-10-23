package cron

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"go.uber.org/zap"

	stock "api-server/api/app/v1/private/admin/platform/stock"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type stockSyncConfig struct {
	Enabled              bool `json:"enabled"`
	TradingIntervalSecs  int  `json:"tradingIntervalSecs"`
	OffHoursIntervalSecs int  `json:"offHoursIntervalSecs"`
	MaxSyncRows          int  `json:"maxSyncRows"`
}

type systemSettings struct {
	Trade struct {
		MorningStart   string `json:"morningStart"`
		MorningEnd     string `json:"morningEnd"`
		AfternoonStart string `json:"afternoonStart"`
		AfternoonEnd   string `json:"afternoonEnd"`
	} `json:"trade"`
	StockSync stockSyncConfig `json:"stockSync"`
}

var stockSyncLock sync.Mutex
var lastStockSync time.Time

func InitStockSyncJob() {
	job, err := scheduler.NewJob(
		gocron.DurationJob(10*time.Second),
		gocron.NewTask(runStockSyncIfDue),
	)
	if err != nil {
		zap.L().Error("创建证券行情同步任务失败", zap.Error(err))
		return
	}
	zap.L().Info("证券行情同步任务已创建，每10秒检查一次配置", zap.String("jobID", job.ID().String()))
}

func runStockSyncIfDue() {
	if !stockSyncLock.TryLock() {
		return
	}
	defer stockSyncLock.Unlock()

	var row system.AppSystemSetting
	if err := pgdb.GetClient().First(&row).Error; err != nil {
		zap.L().Warn("读取证券同步配置失败", zap.Error(err))
		return
	}
	settings, err := parseStockSyncSettings(row.Config)
	if err != nil {
		zap.L().Warn("证券同步配置无效", zap.Error(err))
		return
	}
	if !settings.StockSync.Enabled {
		return
	}

	interval := settings.StockSync.OffHoursIntervalSecs
	if isTradingTime(time.Now(), settings.Trade.MorningStart, settings.Trade.MorningEnd, settings.Trade.AfternoonStart, settings.Trade.AfternoonEnd) {
		interval = settings.StockSync.TradingIntervalSecs
	}
	if interval <= 0 {
		interval = 600
	}
	if !lastStockSync.IsZero() && time.Since(lastStockSync) < time.Duration(interval)*time.Second {
		return
	}

	count, err := stock.SyncEastmoneyData(settings.StockSync.MaxSyncRows)
	if err != nil {
		zap.L().Warn("证券行情同步失败", zap.Error(err))
		return
	}
	lastStockSync = time.Now()
	zap.L().Info("证券行情同步完成", zap.Int("count", count), zap.Int("interval_seconds", interval))
}

func parseStockSyncSettings(raw string) (systemSettings, error) {
	settings := systemSettings{
		StockSync: stockSyncConfig{Enabled: true, TradingIntervalSecs: 60, OffHoursIntervalSecs: 600, MaxSyncRows: 10000},
	}
	settings.Trade.MorningStart, settings.Trade.MorningEnd = "09:30:00", "11:30:00"
	settings.Trade.AfternoonStart, settings.Trade.AfternoonEnd = "13:00:00", "15:00:00"
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return settings, err
	}
	if settings.StockSync.MaxSyncRows <= 0 {
		settings.StockSync.MaxSyncRows = 10000
	}
	for _, clock := range []string{settings.Trade.MorningStart, settings.Trade.MorningEnd, settings.Trade.AfternoonStart, settings.Trade.AfternoonEnd} {
		if _, err := time.Parse("15:04:05", clock); err != nil {
			return settings, fmt.Errorf("证券同步交易时间无效：%q", clock)
		}
	}
	return settings, nil
}

func isTradingTime(now time.Time, morningStart, morningEnd, afternoonStart, afternoonEnd string) bool {
	china := time.FixedZone("Asia/Shanghai", 8*60*60)
	current := now.In(china)
	if current.Weekday() == time.Saturday || current.Weekday() == time.Sunday {
		return false
	}
	clock := current.Format("15:04:05")
	return (morningStart <= clock && clock <= morningEnd) || (afternoonStart <= clock && clock <= afternoonEnd)
}

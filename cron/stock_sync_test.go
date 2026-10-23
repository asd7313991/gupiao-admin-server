package cron

import (
	"testing"
	"time"
)

func TestIsTradingTimeUsesAsiaShanghai(t *testing.T) {
	morningStart, morningEnd := "09:30:00", "11:30:00"
	afternoonStart, afternoonEnd := "13:00:00", "15:00:00"

	tradingTimeUTC := time.Date(2026, time.October, 16, 2, 0, 0, 0, time.UTC)
	if !isTradingTime(tradingTimeUTC, morningStart, morningEnd, afternoonStart, afternoonEnd) {
		t.Fatal("北京时间 10:00 应处于交易时段")
	}

	nonTradingTimeUTC := time.Date(2026, time.October, 16, 8, 0, 0, 0, time.UTC)
	if isTradingTime(nonTradingTimeUTC, morningStart, morningEnd, afternoonStart, afternoonEnd) {
		t.Fatal("北京时间 16:00 不应处于交易时段")
	}
}

func TestStockSyncLegacyDefaultsAndExplicitDisable(t *testing.T) {
	settings, err := parseStockSyncSettings(`{}`)
	if err != nil || !settings.StockSync.Enabled || settings.StockSync.TradingIntervalSecs != 60 {
		t.Fatalf("旧配置须保留默认采集：%+v，%v", settings, err)
	}
	now := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	if !isTradingTime(now, settings.Trade.MorningStart, settings.Trade.MorningEnd, settings.Trade.AfternoonStart, settings.Trade.AfternoonEnd) {
		t.Fatal("缺失交易时段不能错误采用盘外10分钟策略")
	}
	settings, err = parseStockSyncSettings(`{"stockSync":{"enabled":false}}`)
	if err != nil || settings.StockSync.Enabled {
		t.Fatal("显式关闭自动同步必须保留，不能被缺失的最大条数覆盖")
	}
	for _, raw := range []string{`bad`, `{"trade":{"morningStart":"invalid"}}`} {
		if _, err := parseStockSyncSettings(raw); err == nil {
			t.Fatal("无效同步配置必须明确报错")
		}
	}
}

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

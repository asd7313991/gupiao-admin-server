package cron

import (
	"testing"
	"time"
)

func TestHongKongQuoteIntervalUsesHongKongHours(t *testing.T) {
	settings := stockSyncConfig{TradingIntervalSecs: 60, OffHoursIntervalSecs: 600}
	for _, tc := range []struct {
		at   string
		want int
	}{
		{"2026-10-23 11:45:00", 60},
		{"2026-10-23 12:30:00", 600},
		{"2026-10-23 15:30:00", 60},
		{"2026-10-23 16:01:00", 600},
		{"2026-10-24 10:00:00", 600},
	} {
		now, err := time.ParseInLocation("2006-01-02 15:04:05", tc.at, time.FixedZone("Asia/Hong_Kong", 8*3600))
		if err != nil {
			t.Fatal(err)
		}
		if got := hongKongQuoteInterval(now, settings); got != tc.want {
			t.Fatalf("%s：间隔=%d，应为%d", tc.at, got, tc.want)
		}
	}
}

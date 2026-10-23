package cron

import (
	"testing"
	"time"
)

func TestPriorityQuoteIntervals(t *testing.T) {
	settings, err := parseStockSyncSettings("{}")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at    string
		a, hk int
	}{
		{"2026-10-23 10:00:00", 5, 5},
		{"2026-10-23 11:45:00", 60, 5},
		{"2026-10-23 12:30:00", 60, 60},
		{"2026-10-23 13:30:00", 5, 5},
		{"2026-10-23 15:30:00", 60, 5},
		{"2026-10-23 17:00:00", 60, 60},
		{"2026-10-24 10:00:00", 60, 60},
	} {
		now, err := time.ParseInLocation("2006-01-02 15:04:05", tc.at, time.FixedZone("UTC+8", 8*3600))
		if err != nil {
			t.Fatal(err)
		}
		if priorityQuoteInterval(now, "A", settings) != tc.a || priorityQuoteInterval(now, "HK", settings) != tc.hk {
			t.Fatalf("两市场盘中/午休/盘外间隔错误：%s", tc.at)
		}
	}
}

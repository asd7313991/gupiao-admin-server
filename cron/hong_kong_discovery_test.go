package cron

import (
	"testing"
	"time"
)

func TestHongKongDiscoverySchedule(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name             string
		attempt, success time.Time
		want             bool
	}{
		{"启动后首次检查", time.Time{}, time.Time{}, true},
		{"失败后不密集重试", now.Add(-9 * time.Minute), time.Time{}, false},
		{"失败十分钟后重试", now.Add(-10 * time.Minute), time.Time{}, true},
		{"成功后一天内不重复全量", now.Add(-time.Hour), now.Add(-23 * time.Hour), false},
		{"成功一天后重新补齐", now.Add(-24 * time.Hour), now.Add(-24 * time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hongKongDiscoveryDue(now, tc.attempt, tc.success); got != tc.want {
				t.Fatalf("调度结果=%v，期望%v", got, tc.want)
			}
		})
	}
}

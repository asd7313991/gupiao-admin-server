package mobile

import (
	"math"
	"testing"

	"api-server/db/pgdb/system"
)

func TestPositionPriceDropRateUsesStockPriceInsteadOfLeveragedProfit(t *testing.T) {
	tests := []struct {
		name  string
		price float64
		want  float64
	}{
		{name: "未下跌", price: 10, want: 0},
		{name: "上涨", price: 11, want: 0},
		{name: "未到十六点", price: 8.401, want: 15.99},
		{name: "正好十六点", price: 8.4, want: 16},
		{name: "下跌十八点二", price: 8.18, want: 18.2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			position := system.TradePosition{CostPrice: 10, CurrentPrice: test.price, Leverage: 5}
			got := positionPriceDropRate(position)
			if math.Abs(got-test.want) > 0.000001 {
				t.Fatalf("下跌比例=%v，期望=%v", got, test.want)
			}
		})
	}
}

func TestPendingMarginCallRange(t *testing.T) {
	tests := []struct {
		name           string
		drop           float64
		last           int
		wantFrom       int
		wantTo         int
		wantSupplement bool
	}{
		{name: "十五点九九不触发", drop: 15.99},
		{name: "十六点立即触发", drop: 16, wantFrom: 16, wantTo: 16, wantSupplement: true},
		{name: "跨越三档逐档补缴", drop: 18.2, wantFrom: 16, wantTo: 18, wantSupplement: true},
		{name: "已补档位不重复", drop: 18.9, last: 18},
		{name: "只补新跨越档位", drop: 19.1, last: 18, wantFrom: 19, wantTo: 19, wantSupplement: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			from, to, ok := pendingMarginCallRange(test.drop, 16, test.last)
			if from != test.wantFrom || to != test.wantTo || ok != test.wantSupplement {
				t.Fatalf("补仓档位=(%d,%d,%v)，期望=(%d,%d,%v)", from, to, ok, test.wantFrom, test.wantTo, test.wantSupplement)
			}
		})
	}
}

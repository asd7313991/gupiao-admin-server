package mobile

import (
	"math"
	"testing"
	"time"

	"api-server/db/pgdb/system"
)

func TestHongKongLimitMatchPrice(t *testing.T) {
	now := time.Date(2026, 10, 22, 11, 0, 0, 0, time.FixedZone("HK", 8*3600))
	base := hongKongQuote{Code: "02348", LastPrice: 0.5, LotSize: 4000, Currency: "HKD", Kind: "GP", QuoteAt: now.Add(-15 * time.Minute).Unix()}
	for _, tc := range []struct {
		name      string
		direction string
		price     float64
		bid, ask  []orderBookLevel
		age       time.Duration
		time      time.Time
		want      float64
		allDay    bool
	}{
		{name: "最新价达标但无盘口仍等待", direction: "买入", price: 1},
		{name: "零挂量不成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95}}},
		{name: "有效卖盘满足买入限价", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}, want: 0.95},
		{name: "有效买盘满足卖出限价", direction: "卖出", price: 0.9, bid: []orderBookLevel{{Price: 0.95, Volume: 100}}, want: 0.95},
		{name: "卖盘未达标", direction: "买入", price: 0.8, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}},
		{name: "买盘未达标", direction: "卖出", price: 1, bid: []orderBookLevel{{Price: 0.95, Volume: 100}}},
		{name: "过期盘口不成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}, age: 31 * time.Minute},
		{name: "超前盘口不成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}, age: -2 * time.Minute},
		{name: "午休不成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}, time: now.Add(90 * time.Minute)},
		{name: "收市测试不绕过", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}, time: now.Add(7 * time.Hour)},
		{name: "周末不成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}, time: now.AddDate(0, 0, 2)},
		{name: "交叉盘口不成交", direction: "买入", price: 1, bid: []orderBookLevel{{Price: 0.96, Volume: 100}}, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}},
		{name: "非数值盘口不成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: math.NaN(), Volume: 100}}},
		{name: "无限挂量不成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95, Volume: math.Inf(1)}}},
		{name: "仅有买盘不能买入", direction: "买入", price: 1, bid: []orderBookLevel{{Price: 0.95, Volume: 100}}},
		{name: "全天模式午休有效盘口成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}, time: now.Add(90 * time.Minute), allDay: true, want: 0.95},
		{name: "全天模式夜间有效买盘成交", direction: "卖出", price: 0.9, bid: []orderBookLevel{{Price: 0.95, Volume: 100}}, time: now.Add(12 * time.Hour), allDay: true, want: 0.95},
		{name: "全天模式周末有效盘口成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}, time: now.AddDate(0, 0, 2), allDay: true, want: 0.95},
		{name: "全天模式旧盘口不成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}, age: 31 * time.Minute, allDay: true},
		{name: "全天模式未来盘口不成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95, Volume: 100}}, age: -2 * time.Minute, allDay: true},
		{name: "全天模式零挂量不成交", direction: "买入", price: 1, ask: []orderBookLevel{{Price: 0.95}}, allDay: true},
		{name: "全天模式无盘口不成交", direction: "买入", price: 1, allDay: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, at := base, now
			if !tc.time.IsZero() {
				at = tc.time
			}
			q.QuoteAt = at.Add(-15 * time.Minute).Unix()
			if tc.age != 0 {
				q.QuoteAt = at.Add(-tc.age).Unix()
			}
			q.Bid, q.Ask = tc.bid, tc.ask
			order := system.LimitOrder{Symbol: "02348.HK", Direction: tc.direction, LimitPrice: tc.price}
			var settings mobileTradeSettings
			settings.Trade.AllDay = tc.allDay
			price, match := hongKongLimitMatchPrice(order, q, at, settings)
			if price != tc.want || match != (tc.want > 0) {
				t.Fatalf("price=%v match=%v want=%v", price, match, tc.want)
			}
		})
	}
}

func TestHongKongLimitInputAndDepth(t *testing.T) {
	input := hongKongLimitInput{limitOrderInput: limitOrderInput{Code: "02348", Direction: "买入", LimitPrice: 0.106, Quantity: 4000}, RequestID: "hk-limit-test-001"}
	if message := validateHongKongLimitInput(input); message != "" {
		t.Fatal(message)
	}
	for _, price := range []float64{0, -1, 0.0001, 0.1061, math.NaN(), math.Inf(1)} {
		input.LimitPrice = price
		if validateHongKongLimitInput(input) == "" {
			t.Fatalf("应拒绝价格 %v", price)
		}
	}
	for _, values := range [][2]string{{"0.95", "0"}, {"0", "100"}, {"NaN", "100"}, {"0.95", "Inf"}, {"0.95", "bad"}} {
		if level := parseHongKongDepthLevel(values[0], values[1]); level != (orderBookLevel{}) {
			t.Fatalf("应拒绝盘口 %+v", level)
		}
	}
	if level := parseHongKongDepthLevel("0.106", "4000"); level.Price != 0.106 || level.Volume != 4000 {
		t.Fatal(level)
	}
}
